package platform

import (
	"sync"
	"testing"
	"time"

	"restream_go/internal/fallback"
)

// Синтетика стейт-машини: конкурентність, P6 і лок-фрі-читання — те, чого
// golden-слід не покриває.

type stubNodes struct {
	mu       sync.Mutex
	calls    []string
	resuming bool
	failed   bool

	onPrepareBackup func()
}

func (s *stubNodes) call(name string) {
	s.mu.Lock()
	s.calls = append(s.calls, name)
	s.mu.Unlock()
}

func (s *stubNodes) taken() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *stubNodes) StartRelay()   { s.call("relay.start") }
func (s *stubNodes) StopRelay()    { s.call("relay.stop") }
func (s *stubNodes) StartOutput()  { s.call("out.start") }
func (s *stubNodes) StopOutput()   { s.call("out.stop") }
func (s *stubNodes) BounceOutput() { s.call("out.bounce") }

func (s *stubNodes) OutputAlive() bool { return true }
func (s *stubNodes) Failed() bool      { return s.failed }
func (s *stubNodes) SetFailed(v bool)  { s.failed = v }

func (s *stubNodes) SetActive(source string)        { s.call("set_active " + source) }
func (s *stubNodes) ResetTimeline()                 { s.call("reset_timeline") }
func (s *stubNodes) RequestSwitch()                 { s.call("request_switch") }
func (s *stubNodes) PendingSource() string          { return "" }
func (s *stubNodes) SecondsSinceRelayData() float64 { return 0 }

func (s *stubNodes) BackupStart(float64)   { s.call("backup.start") }
func (s *stubNodes) BackupStop()           { s.call("backup.stop") }
func (s *stubNodes) BackupRestart()        { s.call("backup.restart") }
func (s *stubNodes) HasReadySegment() bool { return true }

func (s *stubNodes) ResumeBegin()     { s.call("resume.begin"); s.resuming = true }
func (s *stubNodes) ResumeCancel()    { s.call("resume.cancel"); s.resuming = false }
func (s *stubNodes) IsResuming() bool { return s.resuming }

func (s *stubNodes) PrepareBackup() {
	s.call("prepare_backup")
	if s.onPrepareBackup != nil {
		s.onPrepareBackup()
	}
}

func (s *stubNodes) EnsureLadderBackup() { s.call("ensure_ladder") }

func (s *stubNodes) ApplyPreset() (fallback.LiveParams, bool) {
	s.call("apply_preset")
	return fallback.LiveParams{}, false
}

func (s *stubNodes) ResumePreparation(fallback.LiveParams, bool) { s.call("resume_preparation") }

func (s *stubNodes) SetAudio(int, int) { s.call("set_audio") }
func (s *stubNodes) SetAudioMap([]int) { s.call("set_audio_map") }

func (s *stubNodes) SetCredentials(string, string, string, string) bool {
	s.call("set_credentials")
	return true
}

func (s *stubNodes) Shutdown()            { s.call("shutdown") }
func (s *stubNodes) Snapshot() NodeStatus { return NodeStatus{} }

func stubMachine(t *testing.T, n nodes) *Machine {
	t.Helper()
	m := newMachine(plainSpec(), n, MachineOptions{
		Manager: ManagerDeps{IsDefaultSource: func() bool { return true }},
		Enabled: true, Gate: true,
		Clock: &virtualClock{}, Wall: func() float64 { return 0 },
	})
	t.Cleanup(m.Shutdown)
	return m
}

// TestMachineLateEventsAreNoOps — P6: після shutdown жодна подія не піднімає
// вузли (клон суворіший за еталон, див. нотатка).
func TestMachineLateEventsAreNoOps(t *testing.T) {
	n := &stubNodes{}
	m := stubMachine(t, n)
	m.OnSourceAvailable()
	m.settle()
	m.Shutdown()
	before := len(n.taken())

	m.OnSourceAvailable()
	m.OnSourceUnavailable()
	m.OnRelayStalled()
	m.OnRelayResumed()
	m.OnSwitchedToRelay(true)
	m.OnOutFlapping(true)
	m.OnLadderMinted()
	m.SetEnabled(false)
	m.SetGate(false)
	m.ApplyPreset()
	m.UpdateTracks(1, 1)
	m.Halt()
	m.settle()

	if got := n.taken(); len(got) != before {
		t.Errorf("після shutdown вузли рухались: %v", got[before:])
	}
	if !m.snap.Load().shut || m.StateValid() {
		t.Error("StateValid після shutdown має бути false")
	}
}

// TestMachineConcurrentEventsDuringShutdown — команди з чужих горутин у мить
// знесення не блокують і не панікують.
func TestMachineConcurrentEventsDuringShutdown(t *testing.T) {
	n := &stubNodes{}
	m := stubMachine(t, n)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				m.OnSourceAvailable()
				m.OnRelayStalled()
				m.OnOutFlapping(false)
				m.Status()
				_ = m.StateValid()
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)

	done := make(chan struct{})
	go func() { defer close(done); m.Shutdown() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown не завершився під конкурентними подіями")
	}
	close(stop)
	wg.Wait()
}

// TestMachineReadsNeverBlock — StateValid/State/Status читають атомарний знімок,
// тож не чекають на зайняту горутину-власника (вимога RS5).
func TestMachineReadsNeverBlock(t *testing.T) {
	release := make(chan struct{})
	n := &stubNodes{}
	n.onPrepareBackup = func() { <-release }
	m := stubMachine(t, n)

	m.OnSourceAvailable()
	waitFor(t, func() bool { return len(n.taken()) > 0 })

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			_ = m.StateValid()
			_ = m.State()
			_ = m.EffectiveEnabled()
			m.Status()
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("читання заблокувались на зайнятій горутині-власнику")
	}
	close(release)
}

// TestMachineQueueOverflowDrops — переповнення черги лічиться й не блокує
// відправника.
func TestMachineQueueOverflowDrops(t *testing.T) {
	release := make(chan struct{})
	n := &stubNodes{}
	n.onPrepareBackup = func() { <-release }
	m := stubMachine(t, n)

	m.OnSourceAvailable()
	waitFor(t, func() bool { return len(n.taken()) > 0 })

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < cmdQueue+64; i++ {
			m.OnRelayResumed()
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("post заблокувався на повній черзі")
	}
	if m.DroppedEvents() == 0 {
		t.Error("переповнення не зафіксовано")
	}
	close(release)
}

// TestMachineHaltReportsStateAtCall — Halt віддає стан на МОМЕНТ виклику.
func TestMachineHaltReportsStateAtCall(t *testing.T) {
	n := &stubNodes{}
	m := stubMachine(t, n)
	if m.Halt() {
		t.Error("OFFLINE-платформа не була активною")
	}
	m.OnSourceAvailable()
	m.settle()
	if !m.Halt() {
		t.Error("LIVE-платформа була активною")
	}
	m.settle()
	if m.State() != StateOffline {
		t.Errorf("після Halt стан %v", m.State())
	}
}

// Непридатна заглушка в момент переходу: замість FALLBACK -- знос із ефіру.
func TestMachineSwitchesOffWhenFallbackUnusable(t *testing.T) {
	const problem = "fallback loop video not found: backup.mp4"
	cases := []struct {
		name string
		drop func(m *Machine)
	}{
		{"source-unavailable", func(m *Machine) { m.OnSourceUnavailable() }},
		{"relay-stalled", func(m *Machine) { m.OnRelayStalled() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := &stubNodes{}
			var reported []string
			m := newMachine(plainSpec(), n, MachineOptions{
				Manager: ManagerDeps{
					IsDefaultSource:    func() bool { return true },
					FallbackProblem:    func() string { return problem },
					OnFallbackUnusable: func(p string) { reported = append(reported, p) },
				},
				Enabled: true, Gate: true,
				Clock: &virtualClock{}, Wall: func() float64 { return 0 },
			})
			t.Cleanup(m.Shutdown)

			m.OnSourceAvailable()
			m.settle()
			before := len(n.taken())
			tc.drop(m)
			m.settle()

			if m.State() != StateOffline {
				t.Errorf("стан %v, очікували OFFLINE", m.State())
			}
			if len(reported) != 1 || reported[0] != problem {
				t.Errorf("Manager отримав %q, очікували [%q]", reported, problem)
			}
			for _, call := range n.taken()[before:] {
				if call == "backup.start" {
					t.Error("гравець заглушки стартував попри непридатний пресет")
				}
			}
			if !hasCall(n.taken()[before:], "out.stop") {
				t.Error("вихід не погашено")
			}
		})
	}
}

func hasCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}

// TestMachineOverRealPipeline — повний цикл на СПРАВЖНІХ вузлах без мережі:
// колбеки підвʼязані, знос чистий.
func TestMachineOverRealPipeline(t *testing.T) {
	p := testPipeline(t, plainSpec(), 0)
	m := NewMachine(p, MachineOptions{
		Manager: ManagerDeps{IsDefaultSource: func() bool { return true }},
		Enabled: false, Gate: true,
	})
	if p.OnRelayStalled == nil || p.OnRelayResumed == nil || p.OnOutFlapping == nil ||
		p.OnSwitchedToRelay == nil || p.OnLadderMinted == nil || p.StateValid == nil {
		t.Fatal("конвеєр лишився без колбеків машини")
	}
	if p.StateValid() {
		t.Error("StateValid у OFFLINE має бути false")
	}

	m.OnSourceUnavailable()
	m.settle()
	if m.State() != StateOffline {
		t.Errorf("unavailable у OFFLINE змінив стан на %v", m.State())
	}
	if s := m.Status(); s.State != "OFFLINE" || s.Name != p.Spec.Name {
		t.Errorf("знімок %+v", s)
	}

	done := make(chan struct{})
	go func() { defer close(done); m.Shutdown() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown справжнього конвеєра завис")
	}
}
