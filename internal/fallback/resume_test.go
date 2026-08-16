package fallback

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// realSleeper — реальні паузи: синтетика ганяє гонки, а не слід рішень.
type realSleeper struct{}

func (realSleeper) Sleep(sec float64) { time.Sleep(time.Duration(sec * float64(time.Second))) }

type resumeProbe struct {
	mu       sync.Mutex
	switches []*float64
	reasons  chan string
	endReady bool
	valid    atomic.Bool
	playEnd  atomic.Int32
	rejects  atomic.Bool
	callback atomic.Value // func()

	keyframeAt float64 // виставляти ДО Begin; 0 лишає горутину чекати keyframe
}

// newResumeProbe — оркестратор на реальному годиннику; relay мовчить, тож
// фазування сидить в очікуванні keyframe, поки його не скасують.
func newResumeProbe(endReady bool) (*Resume, *resumeProbe) {
	probe := &resumeProbe{reasons: make(chan string, 8), endReady: endReady}
	probe.valid.Store(true)
	resume := NewResume(ResumeOptions{
		Name:            "synthetic",
		Sleeper:         realSleeper{},
		RelayKeyframeAt: func() float64 { return probe.keyframeAt },
		RelayGOPSec:     func() (float64, bool) { return 0, false },
		RequestSwitch: func(notBefore *float64) {
			probe.mu.Lock()
			probe.switches = append(probe.switches, notBefore)
			probe.mu.Unlock()
		},
		HasEndReady: func() bool { return probe.endReady },
		PlayEnd: func(onDone func()) bool {
			probe.playEnd.Add(1)
			if probe.rejects.Load() {
				return false
			}
			probe.callback.Store(onDone)
			return true
		},
		PlayerPhase:      func() string { return PhaseLoop },
		SegmentStartedAt: func() (float64, bool) { return 0, false },
		EndDuration:      func() (float64, bool) { return 2.0, true },
		StateValid:       func() bool { return probe.valid.Load() },
	})
	resume.doneHook = func(reason string) { probe.reasons <- reason }
	return resume, probe
}

func (p *resumeProbe) switchCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.switches)
}

func (p *resumeProbe) waitReason(t *testing.T) string {
	t.Helper()
	select {
	case reason := <-p.reasons:
		return reason
	case <-time.After(5 * time.Second):
		t.Fatal("the resume goroutine never reported")
		return ""
	}
}

// Q46: гравець аутро не взяв -- resume не має чекати OnBackupEnd вічно.
func TestResumeCutsImmediatelyWhenPlayEndRejected(t *testing.T) {
	resume, probe := newResumeProbe(true)
	probe.rejects.Store(true)
	probe.keyframeAt = 1e9 // keyframe уже бачили -- одразу до play_end
	resume.Begin()

	if reason := probe.waitReason(t); reason != outcomePlayEndRejected {
		t.Fatalf("горутина завершилась із %q", reason)
	}
	if probe.playEnd.Load() != 1 {
		t.Errorf("play_end кликнули %d разів", probe.playEnd.Load())
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if len(probe.switches) != 1 || probe.switches[0] != nil {
		t.Fatalf("switches = %v, очікували один негайний cut", probe.switches)
	}
}

// Cancel посеред очікування keyframe: горутина вмирає, перемикання не просить.
func TestResumeCancelStopsThePhasingGoroutine(t *testing.T) {
	resume, probe := newResumeProbe(true)
	resume.Begin()
	time.Sleep(120 * time.Millisecond)
	if !resume.IsResuming() {
		t.Fatal("Begin did not arm the resume")
	}
	resume.Cancel()
	if reason := probe.waitReason(t); reason != outcomeInvalidKeyframeWait {
		t.Fatalf("goroutine finished with %q", reason)
	}
	if probe.switchCount() != 0 {
		t.Fatalf("a cancelled resume still asked for %d switches", probe.switchCount())
	}
	if resume.IsResuming() {
		t.Fatal("IsResuming after Cancel")
	}
}

// Повторний Begin: gen росте, стара горутина мре, жива лишається одна.
func TestResumeSecondBeginKillsTheFirstGoroutine(t *testing.T) {
	resume, probe := newResumeProbe(true)
	resume.Begin()
	time.Sleep(120 * time.Millisecond)
	resume.Begin()
	if reason := probe.waitReason(t); reason != outcomeInvalidKeyframeWait {
		t.Fatalf("the stale goroutine finished with %q", reason)
	}
	resume.mu.Lock()
	gen := resume.gen
	resume.mu.Unlock()
	if gen != 2 {
		t.Fatalf("gen=%d after two Begin calls", gen)
	}
	resume.Cancel()
	if reason := probe.waitReason(t); reason != outcomeInvalidKeyframeWait {
		t.Fatalf("the live goroutine finished with %q", reason)
	}
	if probe.switchCount() != 0 {
		t.Fatalf("%d switches requested while the relay was silent", probe.switchCount())
	}
}

// Гонка Begin/Cancel: перемикання не сміє прорватись після скасування.
func TestResumeBeginCancelRace(t *testing.T) {
	resume, probe := newResumeProbe(true)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); resume.Begin() }()
		go func() { defer wg.Done(); resume.Cancel() }()
	}
	wg.Wait()
	resume.Cancel()
	deadline := time.Now().Add(5 * time.Second)
	drained := 0
	for time.Now().Before(deadline) {
		select {
		case <-probe.reasons:
			drained++
		case <-time.After(200 * time.Millisecond):
		}
		if drained == 50 {
			break
		}
	}
	if drained != 50 {
		t.Fatalf("%d of 50 goroutines reported", drained)
	}
	if probe.switchCount() != 0 {
		t.Fatalf("%d switches requested despite Cancel", probe.switchCount())
	}
}

// Без готового End Begin просить перемикання одразу і горутини не заводить.
func TestResumeBeginWithoutEndSwitchesImmediately(t *testing.T) {
	resume, probe := newResumeProbe(false)
	resume.Begin()
	if probe.switchCount() != 1 {
		t.Fatalf("%d switch requests, want 1", probe.switchCount())
	}
	probe.mu.Lock()
	notBefore := probe.switches[0]
	probe.mu.Unlock()
	if notBefore != nil {
		t.Fatalf("not_before=%v, want nil (cut on the first keyframe)", *notBefore)
	}
}

// OnBackupEnd після Cancel — no-op; на живому resume — перемикання без межі.
func TestResumeOnBackupEndGuard(t *testing.T) {
	resume, probe := newResumeProbe(false)
	resume.Cancel()
	resume.OnBackupEnd()
	if reason := probe.waitReason(t); reason != outcomeEndGuardFailed {
		t.Fatalf("OnBackupEnd after Cancel finished with %q", reason)
	}
	if probe.switchCount() != 0 {
		t.Fatalf("%d switches after a cancelled resume", probe.switchCount())
	}

	resume.Begin() // без End: одразу одне перемикання
	probe.valid.Store(false)
	resume.OnBackupEnd()
	if reason := probe.waitReason(t); reason != outcomeEndGuardFailed {
		t.Fatalf("OnBackupEnd with an invalid state finished with %q", reason)
	}
	probe.valid.Store(true)
	resume.OnBackupEnd()
	if reason := probe.waitReason(t); reason != outcomeEndSwitch {
		t.Fatalf("OnBackupEnd on a live resume finished with %q", reason)
	}
	if probe.switchCount() != 2 {
		t.Fatalf("%d switch requests, want 2 (Begin + OnBackupEnd)", probe.switchCount())
	}
}

// Гравцеві передається саме OnBackupEnd.
func TestResumePlayEndGetsOnBackupEnd(t *testing.T) {
	resume, probe := newResumeProbe(true)
	var keyframe atomic.Uint64
	resume.opts.RelayKeyframeAt = func() float64 { return math.Float64frombits(keyframe.Load()) }
	resume.opts.RelayGOPSec = func() (float64, bool) { return 0, false }
	resume.Begin()
	keyframe.Store(math.Float64bits(resume.clock.Now() + 1.0)) // «щойно» бачений keyframe live
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && probe.playEnd.Load() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if probe.playEnd.Load() != 1 {
		t.Fatal("play_end was never called")
	}
	callback, _ := probe.callback.Load().(func())
	if callback == nil {
		t.Fatal("play_end got a nil callback")
	}
	callback()
	if reason := probe.waitReason(t); reason != outcomeEndSwitch {
		t.Fatalf("the play_end callback finished with %q", reason)
	}
	if probe.switchCount() != 1 {
		t.Fatalf("%d switch requests from the callback", probe.switchCount())
	}
	resume.Cancel()
	probe.waitReason(t)
}

func TestPyModMatchesPythonSemantics(t *testing.T) {
	cases := []struct{ x, y, want float64 }{
		{-1.83, 2.0, 0.17000000000000015},
		{0.01, 2.0, 0.01},
		{-4.0, 2.0, 0.0},
		{5.0, 2.0, 1.0},
		{-0.5, 2.0, 1.5},
		{1.0, -2.0, -1.0},
	}
	for _, tc := range cases {
		if got := pyMod(tc.x, tc.y); math.Abs(got-tc.want) > 1e-12 {
			t.Fatalf("pyMod(%v, %v) = %v, want %v", tc.x, tc.y, got, tc.want)
		}
	}
	if got := math.Mod(-1.83, 2.0); got > 0 {
		t.Fatalf("math.Mod(-1.83, 2) = %v -- pyMod would be pointless", got)
	}
}
