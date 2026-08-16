package control

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"restream_go/internal/fallback"
	"restream_go/internal/platform"
	"restream_go/internal/probe"
	"restream_go/internal/wire/ts"
)

// --- матриця роутингу хуків ---

func TestSourceForPathMatrix(t *testing.T) {
	config := D("sources", []any{
		D("name", "EB", "is_default", true, "type", "rtmp",
			"enhanced_broadcasting", true, "live_path", "live/my_stream"),
		D("name", "Plain", "type", "rtmp", "live_path", "live/plain"),
	})
	mgr := newTestManager(t, config, nil)
	resolve := func(path string) string {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		if source := mgr.sourceForPathLocked(path); source != nil {
			return source.name
		}
		return ""
	}

	cases := []struct{ path, want string }{
		{"live/my_stream", "EB"},            // точний збіг
		{"live/plain", "Plain"},             // не-EB теж матчиться точно
		{"", ""},                            // порожній шлях
		{"live/nobody", ""},                 // невідомий
		{"live/plain_extra", ""},            // не-EB не має suffix-матчу
		{"live/v1_sec_1_2_my_stream", "EB"}, // переписаний EB-ключ
		{"live/a_x_my_stream", "EB"},        // будь-який непорожній префікс
		{"live/_my_stream", ""},             // Q62: порожній префікс
		{"my_stream", ""},                   // без "live/" не роутиться
	}
	for _, c := range cases {
		if got := resolve(c.path); got != c.want {
			t.Errorf("sourceForPath(%q) = %q, очікували %q", c.path, got, c.want)
		}
	}

	// Два EB-source-и, де один slug -- хвіст іншого: виграє НАЙДОВШИЙ.
	both := newTestManager(t, D("sources", []any{
		D("name", "Long", "is_default", true, "type", "rtmp",
			"enhanced_broadcasting", true, "live_path", "live/my_s"),
		D("name", "Short", "type", "rtmp",
			"enhanced_broadcasting", true, "live_path", "live/s"),
	}), nil)
	both.mu.Lock()
	source := both.sourceForPathLocked("live/v1_x_my_s")
	both.mu.Unlock()
	if source == nil || source.name != "Long" {
		t.Fatalf("longest slug must win, got %v", source)
	}
}

// --- синтетика конкурентності ---

// stubRuntime — платформа, що повторює контракт машини: Shutdown БЛОКУЄТЬСЯ
// до кінця колбека в Manager, а колбек іде з «горутини-власника» без локів.
type stubRuntime struct {
	mgr      *Manager
	name     string
	callback func()

	mu      sync.Mutex
	running bool
	done    chan struct{}
}

func (s *stubRuntime) fireCallback() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.done = make(chan struct{})
	done := s.done
	s.mu.Unlock()
	go func() {
		defer close(done)
		s.callback()
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()
}

func (s *stubRuntime) OnSourceAvailable()                               {}
func (s *stubRuntime) OnSourceUnavailable()                             {}
func (s *stubRuntime) SetEnabled(bool)                                  {}
func (s *stubRuntime) SetGate(bool)                                     {}
func (s *stubRuntime) SetGroup(string, bool)                            {}
func (s *stubRuntime) UpdateCredentials(string, string, string, string) {}
func (s *stubRuntime) UpdateTracks(int, int)                            {}
func (s *stubRuntime) UpdateAudioMap([]int)                             {}
func (s *stubRuntime) ApplyPreset(string, Segments)                     {}
func (s *stubRuntime) EnsureEBSession() bool                            { return false }
func (s *stubRuntime) WarmBackup(fallback.WarmEntry, <-chan struct{})   {}
func (s *stubRuntime) Halt() bool                                       { return false }
func (s *stubRuntime) GracefulStopIfFallback()                          {}
func (s *stubRuntime) State() platform.State                            { return platform.StateOffline }
func (s *stubRuntime) EffectiveEnabled() bool                           { return false }
func (s *stubRuntime) Failed() bool                                     { return false }
func (s *stubRuntime) URL() string                                      { return "" }
func (s *stubRuntime) SetRTT(int, bool)                                 {}
func (s *stubRuntime) Status() platform.Status                          { return platform.Status{Name: s.name} }
func (s *stubRuntime) BackupProgress() fallback.Progress                { return fallback.Progress{} }

// Shutdown — як у Machine.Shutdown: чекає горутину-власника.
func (s *stubRuntime) Shutdown() {
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}

// Знесення платформи під час її ж колбека в Manager не дедлочить: Manager не
// сміє тримати свій лок на блокуючому вході машини (PM5, рамка №12в).
func TestShutdownDuringCallbackDoesNotDeadlock(t *testing.T) {
	var stub *stubRuntime
	mgr := newTestManager(t, testConfig(), func(m *Manager, e *platformEntry) Runtime {
		stub = &stubRuntime{mgr: m, name: e.name}
		stub.callback = func() { m.OnPlatformGaveUp(e.name) }
		return stub
	})
	entered := make(chan struct{})
	mgr.OnEvent = func(string, string) { close(entered) }

	stub.fireCallback()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("колбек платформи не дійшов до Manager")
	}

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		mgr.RemovePlatform("Twitch")
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("RemovePlatform завис: Manager тримав лок на Shutdown платформи")
	}
}

// Конкурентні хуки під час shutdown: жодних паніки/дедлоку, усе завершується.
func TestConcurrentHooksDuringShutdown(t *testing.T) {
	mgr := newTestManager(t, testConfig(), func(m *Manager, e *platformEntry) Runtime {
		return &stubRuntime{mgr: m, name: e.name}
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				switch (i + j) % 6 {
				case 0:
					mgr.OnAvailable("live/main")
				case 1:
					mgr.OnUnavailable("live/main")
				case 2:
					mgr.Status()
				case 3:
					mgr.FallbackProgress()
				case 4:
					mgr.OnPlatformStalled("Twitch")
				default:
					mgr.OnMediaMTXConnectTimeout()
				}
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		mgr.Shutdown()
	}()
	waitGroup(t, &wg, 20*time.Second)
}

// Q63: після Shutdown хуки MediaMTX більше нічого не заводять.
func TestHooksAfterShutdownAreIgnored(t *testing.T) {
	newStand := func() (*Manager, *int) {
		probed := 0
		mgr := newRecreateStand(t, D("name", "Main", "is_default", true, "type", "rtmp",
			"live_path", "live/main", "audio_tracks", int64(1)))
		mgr.probes.TrackCounts = func(string) (probe.TrackCounts, bool) {
			probed++
			return probe.TrackCounts{Video: 1, Audio: 1}, true
		}
		return mgr, &probed
	}

	live, liveProbed := newStand()
	live.OnAvailable("live/main")
	live.OnUnavailable("live/main")
	if *liveProbed == 0 {
		t.Fatal("a live manager must probe the publication")
	}
	live.mu.Lock()
	armed := live.timeoutTimer != nil
	live.mu.Unlock()
	if !armed {
		t.Fatal("losing the default source while live must arm the session timer")
	}

	dead, deadProbed := newStand()
	dead.Shutdown()
	dead.OnAvailable("live/main")
	dead.OnUnavailable("live/main")
	if *deadProbed != 0 {
		t.Fatalf("probe ran %d time(s) after shutdown", *deadProbed)
	}
	dead.mu.Lock()
	source, _ := dead.sources.Get("Main")
	mutated := source.available || source.activePath != ""
	stillArmed := dead.timeoutTimer != nil
	dead.mu.Unlock()
	if mutated {
		t.Fatal("hook after shutdown mutated the source")
	}
	if stillArmed {
		t.Fatal("hook after shutdown armed the session timer")
	}
}

// Лок-фрі читання не чекають Manager.lock, навіть коли той зайнятий.
func TestLockFreeReadsDoNotBlock(t *testing.T) {
	mgr := newTestManager(t, testConfig(), func(m *Manager, e *platformEntry) Runtime {
		return &stubRuntime{mgr: m, name: e.name}
	})
	held := make(chan struct{})
	release := make(chan struct{})
	go func() {
		mgr.mu.Lock()
		close(held)
		<-release
		mgr.mu.Unlock()
	}()
	<-held
	deadline := time.Now().Add(time.Second)
	for i := 0; i < 10000; i++ {
		mgr.IsGracefulRecent()
		mgr.IsMainSessionLive()
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("лок-фрі читання впиралось у Manager.lock")
		}
	}
	close(release)
}

// Вікно детектора штатного стопу — строге `<` на 1.5с.
func TestIsGracefulRecentWindow(t *testing.T) {
	clock := &virtualClock{t: 1000}
	mgr := newTestManagerWithClock(t, testConfig(), clock)
	if mgr.IsGracefulRecent() {
		t.Fatal("без штатного стопу детектор мусить мовчати")
	}
	mgr.OnManualStop()
	for _, tc := range []struct {
		advance float64
		want    bool
	}{{0, true}, {1.4999, true}, {OracleWindowSec, false}, {2.0, false}} {
		clock.t = 1000 + tc.advance
		if got := mgr.IsGracefulRecent(); got != tc.want {
			t.Errorf("IsGracefulRecent через %.4fс = %v, очікували %v", tc.advance, got, tc.want)
		}
	}
}

// --- джейл сегментів ---

func TestResolveMediaPathJail(t *testing.T) {
	base := t.TempDir()
	backup := filepath.Join(base, "media")
	if err := os.MkdirAll(filepath.Join(backup, "clips"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "clips", "one.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside.mp4")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(backup, "link.mp4")); err != nil {
		t.Skipf("симлінки недоступні: %v", err)
	}
	cases := []struct {
		rel  string
		want string
	}{
		{"clips/one.mp4", filepath.Join(backup, "clips", "one.mp4")},
		{"./clips/one.mp4", filepath.Join(backup, "clips", "one.mp4")},
		{"/etc/passwd", ""},
		{"../outside.mp4", ""},
		{"clips/../../outside.mp4", ""},
		{"link.mp4", ""},
		{"", filepath.Clean(backup)},
	}
	for _, tc := range cases {
		got := resolveMediaPath(tc.rel, base)
		if got != tc.want {
			t.Errorf("resolveMediaPath(%q) = %q, очікували %q", tc.rel, got, tc.want)
		}
	}
}

// --- helpers ---

func testConfig() *Dict {
	raw := []byte(`{
		"sources": [{"name": "Main", "is_default": true, "type": "rtmp", "live_path": "live/main"}],
		"platforms": [{"name": "Twitch", "type": "rtmp", "enabled": true, "source": "Main"}]
	}`)
	config, err := Loads(raw)
	if err != nil {
		panic(err)
	}
	return config
}

func newTestManagerWithClock(t *testing.T, config *Dict, clock *virtualClock) *Manager {
	t.Helper()
	mgr := newTestManager(t, config, nil)
	mgr.clock = clock
	return mgr
}

func newTestManager(t *testing.T, config *Dict,
	newRuntime func(m *Manager, e *platformEntry) Runtime) *Manager {
	t.Helper()
	if newRuntime == nil {
		newRuntime = func(m *Manager, e *platformEntry) Runtime {
			return &stubRuntime{mgr: m, name: e.name}
		}
	}
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	return New(config, Options{
		BaseDir: base,
		Probes: Probes{
			TSManifest:   func(string) (ts.Manifest, bool) { return ts.Manifest{}, false },
			TrackCounts:  func(string) (probe.TrackCounts, bool) { return probe.TrackCounts{}, false },
			StreamParams: func(string, int, int) (probe.StreamParams, bool) { return probe.StreamParams{}, false },
		},
		newRuntime: newRuntime,
	})
}

func waitGroup(t *testing.T, wg *sync.WaitGroup, timeout time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatal("горутини не завершились у відведений час")
	}
}

var _ = json.Marshal
