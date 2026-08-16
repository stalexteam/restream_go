package proc

import (
	"errors"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// fakeChild — процес, який живе рівно `life`; `deaf` ігнорує сигнали.
type fakeChild struct {
	life  time.Duration
	dead  chan struct{}
	once  sync.Once
	pidNo int
	deaf  bool
}

func newFakeChild(life time.Duration, pid int) *fakeChild {
	return &fakeChild{life: life, dead: make(chan struct{}), pidNo: pid}
}

func (f *fakeChild) finish() { f.once.Do(func() { close(f.dead) }) }

func (f *fakeChild) pid() int { return f.pidNo }

func (f *fakeChild) alive() bool {
	select {
	case <-f.dead:
		return false
	default:
		return true
	}
}

func (f *fakeChild) wait() {
	if f.life > 0 {
		timer := time.NewTimer(f.life)
		defer timer.Stop()
		select {
		case <-f.dead:
			return
		case <-timer.C:
		}
	}
	f.finish()
}

func (f *fakeChild) waitFor(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-f.dead:
		return true
	case <-timer.C:
		return false
	}
}

func (f *fakeChild) signal() {
	if !f.deaf {
		f.finish()
	}
}

func (f *fakeChild) terminate()        { f.signal() }
func (f *fakeChild) kill()             { f.signal() }
func (f *fakeChild) hasStdin() bool    { return false }
func (f *fakeChild) closeStdin()       { f.signal() }
func (f *fakeChild) started() *Started { return &Started{PID: f.pidNo} }
func (f *fakeChild) closePipes()       {}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Stop із хука on_flapping не дає супервізору чекати бекоф даремно.
func TestStopFromFlappingHookSkipsBackoff(t *testing.T) {
	var s *Supervisor
	var sleeps atomic.Int32
	stopped := make(chan struct{})

	s = NewSupervisor("hook", func() []string { return []string{"ffmpeg"} }, t.TempDir(), Options{
		OnFlapping: func(bool) {
			s.Stop(50 * time.Millisecond)
			close(stopped)
		},
	})
	s.spawn = func(spawnSpec) (child, error) { return newFakeChild(0, 4242), nil }
	s.sleep = func(time.Duration) { sleeps.Add(1) }
	s.Start()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("on_flapping never fired")
	}
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not exit after Stop from the hook")
	}
	if got := sleeps.Load(); got != 0 {
		t.Fatalf("backoff slept %d times after Stop from the hook", got)
	}
}

// Q51: збій запуску (немає бінаря) сигналить назовні й ретраїться, а не вбиває
// супервізорну горутину мовчки.
func TestSpawnFailureSignalsAndRetries(t *testing.T) {
	var attempts atomic.Int32
	flaps := make(chan bool, 8)
	exits := make(chan struct{}, 8)

	s := NewSupervisor("spawn-fail", func() []string { return []string{"ffmpeg"} }, t.TempDir(), Options{
		OnExit:     func() { exits <- struct{}{} },
		OnFlapping: func(neverSucceeded bool) { flaps <- neverSucceeded },
	})
	s.spawn = func(spawnSpec) (child, error) {
		if attempts.Add(1) <= 2 {
			return nil, errors.New("no such file or directory: ffmpeg")
		}
		return newFakeChild(time.Hour, 4242), nil
	}
	s.sleep = func(time.Duration) {}
	s.Start()
	defer s.Stop(50 * time.Millisecond)

	for attempt := 1; attempt <= 2; attempt++ {
		select {
		case never := <-flaps:
			if !never {
				t.Fatalf("attempt %d: on_flapping reported never_succeeded=false", attempt)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("attempt %d: on_flapping did not fire on a failed spawn", attempt)
		}
		select {
		case <-exits:
		case <-time.After(5 * time.Second):
			t.Fatalf("attempt %d: on_exit did not fire on a failed spawn", attempt)
		}
	}
	waitFor(t, "a start once spawn works again", s.IsRunning)
}

// args-провайдер читається заново перед КОЖНИМ перезапуском.
func TestArgsProviderReadEveryRestart(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	var n int

	s := NewSupervisor("args", func() []string {
		mu.Lock()
		defer mu.Unlock()
		n++
		arg := "run-" + string(rune('0'+n))
		seen = append(seen, arg)
		return []string{"ffmpeg", arg}
	}, t.TempDir(), Options{})
	s.spawn = func(spec spawnSpec) (child, error) { return newFakeChild(0, 1), nil }
	s.sleep = func(time.Duration) {}
	s.Start()
	waitFor(t, "three restarts", func() bool { return s.RestartCount() >= 3 })
	s.Stop(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 3 || seen[0] == seen[1] || seen[1] == seen[2] {
		t.Fatalf("args were not re-read per restart: %v", seen)
	}
}

// Повторний Start під час живого супервізора — no-op.
func TestDoubleStartIsNoop(t *testing.T) {
	var spawns atomic.Int32
	s := NewSupervisor("double", func() []string { return []string{"ffmpeg"} }, t.TempDir(), Options{})
	s.spawn = func(spawnSpec) (child, error) {
		spawns.Add(1)
		return newFakeChild(time.Hour, 7), nil
	}
	s.Start()
	waitFor(t, "first spawn", func() bool { return s.IsRunning() })
	s.Start()
	time.Sleep(50 * time.Millisecond)
	if got := spawns.Load(); got != 1 {
		t.Fatalf("%d spawns after a second Start, want 1", got)
	}
	s.Stop(50 * time.Millisecond)
}

// Q28/Q54: Start під час недожатого Stop лишає РІВНО один супервізор.
func TestStartAfterUnfinishedStopLeavesOneSupervisor(t *testing.T) {
	var spawns atomic.Int32
	release := make(chan struct{})
	s := NewSupervisor("stale", func() []string { return []string{"ffmpeg"} }, t.TempDir(), Options{})
	s.spawn = func(spawnSpec) (child, error) {
		if spawns.Add(1) == 1 {
			return newFakeChild(0, 1), nil // миттєва смерть -> бекоф
		}
		return newFakeChild(time.Hour, 2), nil
	}
	s.sleep = func(time.Duration) { <-release }

	s.Start()
	waitFor(t, "бекоф першої горутини", func() bool { return s.RestartCount() == 1 })

	// Процесу вже немає, тож Stop повертається, не дочекавшись горутини.
	s.Stop(50 * time.Millisecond)
	s.Start()
	waitFor(t, "запуск другої горутини", func() bool { return s.IsRunning() })
	close(release)

	time.Sleep(100 * time.Millisecond)
	if got := spawns.Load(); got != 2 {
		t.Fatalf("%d spawns: the stale supervisor kept restarting", got)
	}
	if pid, ok := s.PID(); !ok || pid != 2 {
		t.Fatalf("live pid %d (ok=%v), want the process of the new run", pid, ok)
	}
	s.Stop(50 * time.Millisecond)
}

// Витіснена горутина не шле on_exit: він детачив би sink нового запуску.
func TestSupersededRunDoesNotFireExit(t *testing.T) {
	var exits atomic.Int32
	var spawns atomic.Int32
	stubborn := &fakeChild{life: time.Hour, dead: make(chan struct{}), pidNo: 1, deaf: true}
	s := NewSupervisor("superseded", func() []string { return []string{"ffmpeg"} }, t.TempDir(),
		Options{OnExit: func() { exits.Add(1) }})
	s.spawn = func(spawnSpec) (child, error) {
		if spawns.Add(1) == 1 {
			return stubborn, nil
		}
		return newFakeChild(time.Hour, 2), nil
	}
	s.killWait, s.reapWait = 10*time.Millisecond, 10*time.Millisecond

	s.Start()
	waitFor(t, "перший процес", s.IsRunning)

	// Stop не дочекається горутини: процес не реагує на сигнали.
	go s.Stop(10 * time.Millisecond)
	waitFor(t, "знятий desired", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.desired
	})
	if got := exits.Load(); got != 1 {
		t.Fatalf("Stop fired on_exit %d time(s), want 1", got)
	}

	s.Start()
	waitFor(t, "новий процес", func() bool {
		pid, ok := s.PID()
		return ok && pid == 2
	})
	stubborn.finish() // стара горутина прокидається вже витісненою

	time.Sleep(100 * time.Millisecond)
	if got := exits.Load(); got != 1 {
		t.Fatalf("on_exit fired %d time(s): the superseded run detached the live sink", got)
	}
	if pid, ok := s.PID(); !ok || pid != 2 {
		t.Fatalf("live pid %d (ok=%v), want the new run", pid, ok)
	}

	// Двобічність: чинний запуск on_exit і далі шле.
	s.Stop(50 * time.Millisecond)
	if got := exits.Load(); got < 2 {
		t.Fatalf("on_exit count %d after stopping the live run, want it to fire", got)
	}
}

// Uptime рахується від старту поточного процесу і зникає разом із ним.
func TestUptimeTracksLiveProcess(t *testing.T) {
	s := NewSupervisor("uptime", func() []string { return []string{"ffmpeg"} }, t.TempDir(), Options{})
	ch := newFakeChild(time.Hour, 99)
	s.spawn = func(spawnSpec) (child, error) { return ch, nil }
	if got := s.UptimeSec(); got != 0 {
		t.Fatalf("uptime before Start = %v", got)
	}
	s.Start()
	waitFor(t, "process up", func() bool { return s.IsRunning() })
	time.Sleep(30 * time.Millisecond)
	if got := s.UptimeSec(); got <= 0 {
		t.Fatalf("uptime of a live process = %v", got)
	}
	if pid, ok := s.PID(); !ok || pid != 99 {
		t.Fatalf("PID = %d, %v", pid, ok)
	}
	s.Stop(50 * time.Millisecond)
	if got := s.UptimeSec(); got != 0 {
		t.Fatalf("uptime after Stop = %v", got)
	}
	if _, ok := s.PID(); ok {
		t.Fatal("PID is still reported after Stop")
	}
}

// Паніка в хуку не валить супервізора.
func TestHookPanicDoesNotKillSupervisor(t *testing.T) {
	var restarts atomic.Int32
	s := NewSupervisor("panic", func() []string { return []string{"ffmpeg"} }, t.TempDir(), Options{
		OnStart: func(*Started) { restarts.Add(1); panic("boom") },
	})
	s.spawn = func(spawnSpec) (child, error) { return newFakeChild(0, 5), nil }
	s.sleep = func(time.Duration) {}
	s.Start()
	waitFor(t, "supervisor survived the panic", func() bool { return restarts.Load() >= 3 })
	s.Stop(50 * time.Millisecond)
}

// ConfigureCmd не затирає вже виставлений SysProcAttr (пріоритет транскоду).
func TestConfigureCmdKeepsSysProcAttr(t *testing.T) {
	cmd := exec.Command("ffmpeg")
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	before := cmd.SysProcAttr
	ConfigureCmd(cmd)
	if cmd.SysProcAttr != before {
		t.Fatal("ConfigureCmd replaced an existing SysProcAttr")
	}
}
