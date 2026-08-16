package control

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"restream_go/internal/probe"
	"restream_go/internal/wire/ts"
)

// Гейт fallback-пресета в enable_platform — власне рішення.

type gateStand struct {
	mgr  *Manager
	base string

	mu       sync.Mutex
	events   []string
	persists int
}

// newGateStand — база з media/, дефолтним пресетом і вимкненою платформою на
// пресеті `test`.
func newGateStand(t *testing.T, preset *Dict) *gateStand {
	t.Helper()
	base := t.TempDir()
	for _, dir := range []string{"media", "tmp"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stand := &gateStand{base: base}
	config := D(
		"fallback_presets", []any{
			D("id", "default", "name", "Default", "is_default", true,
				"type", "sequence", "loop_file", "backup.mp4"),
			preset,
		},
		"sources", []any{D("name", "Main", "is_default", true, "type", "rtmp",
			"live_path", "live/main", "audio_tracks", int64(1))},
		"platform_groups", []any{D("id", "default", "name", "Default",
			"is_default", true, "enabled", true)},
		"platforms", []any{D("name", "Twitch", "type", "rtmp", "enabled", false,
			"source", "Main", "backup_preset", "test")},
	)
	stand.mgr = New(config, Options{
		BaseDir: base,
		Probes: Probes{
			TSManifest:   func(string) (ts.Manifest, bool) { return ts.Manifest{}, false },
			TrackCounts:  func(string) (probe.TrackCounts, bool) { return probe.TrackCounts{Video: 1, Audio: 1}, true },
			StreamParams: func(string, int, int) (probe.StreamParams, bool) { return probe.StreamParams{}, false },
		},
		newRuntime: func(m *Manager, e *platformEntry) Runtime {
			return &stubRuntime{mgr: m, name: e.name}
		},
		// Валідація контракту синхронно -- інакше гейт старту ефіру гониться з тестом.
		spawn: func(fn func()) { fn() },
		persist: func(string, *Dict) error {
			stand.mu.Lock()
			defer stand.mu.Unlock()
			stand.persists++
			return nil
		},
	})
	stand.mgr.OnEvent = func(level, text string) {
		stand.mu.Lock()
		defer stand.mu.Unlock()
		stand.events = append(stand.events, level+" "+text)
	}
	return stand
}

func (s *gateStand) mkdirMedia(t *testing.T, rel string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(s.base, "media", rel), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (s *gateStand) writeMedia(t *testing.T, rel string) {
	t.Helper()
	path := filepath.Join(s.base, "media", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (s *gateStand) removeMedia(t *testing.T, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(s.base, "media", filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
}

func (s *gateStand) enabled() bool {
	s.mgr.mu.Lock()
	defer s.mgr.mu.Unlock()
	e, ok := s.mgr.platforms.Get("Twitch")
	return ok && e.enabled
}

func (s *gateStand) snapshot() ([]string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...), s.persists
}

func sequencePreset(loopFile string) *Dict {
	return D("id", "test", "name", "Test", "is_default", false,
		"type", "sequence", "loop_file", loopFile)
}

func folderPreset(folder string) *Dict {
	return D("id", "test", "name", "Test", "is_default", false,
		"type", "folder", "folder", folder)
}

func TestEnablePlatformRejectsInvalidFallback(t *testing.T) {
	cases := []struct {
		name   string
		preset *Dict
		setup  func(t *testing.T, s *gateStand)
		want   string
	}{
		{
			name:   "sequence-loop-empty",
			preset: sequencePreset(""),
			want:   "warning Twitch: its fallback preset has no loop video set -- configure it in Settings first",
		},
		{
			name:   "sequence-loop-blank",
			preset: sequencePreset("   "),
			want:   "warning Twitch: its fallback preset has no loop video set -- configure it in Settings first",
		},
		{
			name:   "sequence-loop-missing",
			preset: sequencePreset("gone.mp4"),
			want:   "warning Twitch: fallback loop video not found: gone.mp4",
		},
		{
			name:   "sequence-loop-is-a-directory",
			preset: sequencePreset("clips"),
			setup:  func(t *testing.T, s *gateStand) { s.mkdirMedia(t, "clips") },
			want:   "warning Twitch: fallback loop video not found: clips",
		},
		{
			name:   "sequence-loop-outside-media",
			preset: sequencePreset("../outside.mp4"),
			want:   "warning Twitch: fallback loop video not found: ../outside.mp4",
		},
		{
			name:   "folder-empty",
			preset: folderPreset(""),
			want:   "warning Twitch: fallback folder is not set -- configure it in Settings first",
		},
		{
			name:   "folder-missing",
			preset: folderPreset("clips"),
			want:   "warning Twitch: fallback folder not found: clips",
		},
		{
			name:   "folder-is-a-file",
			preset: folderPreset("clips"),
			setup:  func(t *testing.T, s *gateStand) { s.writeMedia(t, "clips") },
			want:   "warning Twitch: fallback folder not found: clips",
		},
		{
			name:   "folder-without-files",
			preset: folderPreset("clips"),
			setup:  func(t *testing.T, s *gateStand) { s.mkdirMedia(t, "clips") },
			want:   "warning Twitch: fallback folder has no video files: clips",
		},
		{
			name:   "folder-without-video-files",
			preset: folderPreset("clips"),
			setup: func(t *testing.T, s *gateStand) {
				s.writeMedia(t, "clips/notes.txt")
				s.writeMedia(t, "clips/cover.jpg")
				s.writeMedia(t, "clips/no-extension")
			},
			want: "warning Twitch: fallback folder has no video files: clips",
		},
		{
			name:   "folder-with-nested-video-only",
			preset: folderPreset("clips"),
			setup:  func(t *testing.T, s *gateStand) { s.writeMedia(t, "clips/deep/one.mp4") },
			want:   "warning Twitch: fallback folder has no video files: clips",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stand := newGateStand(t, tc.preset)
			if tc.setup != nil {
				tc.setup(t, stand)
			}
			_, persistsBefore := stand.snapshot()
			stand.mgr.EnablePlatform("Twitch")
			events, persists := stand.snapshot()

			if len(events) != 1 || events[0] != tc.want {
				t.Errorf("events = %q, want exactly [%q]", events, tc.want)
			}
			if stand.enabled() {
				t.Error("платформа увімкнулась попри невалідний пресет")
			}
			if persists != persistsBefore {
				t.Errorf("persist кликнули %d разів понад %d", persists-persistsBefore, persistsBefore)
			}
		})
	}
}

func TestEnablePlatformAcceptsValidFallback(t *testing.T) {
	cases := []struct {
		name   string
		preset *Dict
		setup  func(t *testing.T, s *gateStand)
	}{
		{
			name:   "sequence",
			preset: sequencePreset("loop.mp4"),
			setup:  func(t *testing.T, s *gateStand) { s.writeMedia(t, "loop.mp4") },
		},
		{
			name:   "sequence-with-spaces-around-the-path",
			preset: sequencePreset("  loop.mp4  "),
			setup:  func(t *testing.T, s *gateStand) { s.writeMedia(t, "loop.mp4") },
		},
		{
			name:   "folder",
			preset: folderPreset("clips"),
			setup: func(t *testing.T, s *gateStand) {
				s.writeMedia(t, "clips/notes.txt")
				s.writeMedia(t, "clips/one.MP4")
			},
		},
		{
			name:   "folder-nested-path",
			preset: folderPreset("clips/night"),
			setup:  func(t *testing.T, s *gateStand) { s.writeMedia(t, "clips/night/one.mkv") },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stand := newGateStand(t, tc.preset)
			tc.setup(t, stand)
			_, persistsBefore := stand.snapshot()
			stand.mgr.EnablePlatform("Twitch")
			events, persists := stand.snapshot()

			if len(events) != 0 {
				t.Errorf("несподівані події: %q", events)
			}
			if !stand.enabled() {
				t.Error("платформа не увімкнулась при валідному пресеті")
			}
			if persists <= persistsBefore {
				t.Error("успішний enable не персистнув конфіг")
			}
		})
	}
}

// Порожній backup_preset платформи резолвиться в дефолтний пресет — і гейт
// перевіряє саме його.
func TestEnablePlatformFallsBackToDefaultPreset(t *testing.T) {
	stand := newGateStand(t, sequencePreset("loop.mp4"))
	stand.writeMedia(t, "loop.mp4")
	stand.mgr.UpdatePlatform("Twitch", PlatformFields{BackupPreset: strPtr("")})
	stand.mgr.EnablePlatform("Twitch")

	events, _ := stand.snapshot()
	want := "warning Twitch: fallback loop video not found: backup.mp4"
	if len(events) != 1 || events[0] != want {
		t.Fatalf("events = %q, want [%q]", events, want)
	}
	if stand.enabled() {
		t.Error("платформа увімкнулась на невалідному дефолтному пресеті")
	}
}

// enabledWithLoop — платформа, ввімкнена з валідним loop.mp4; файл лишається на
// місці, поки тест його не прибере.
func enabledWithLoop(t *testing.T) *gateStand {
	t.Helper()
	stand := newGateStand(t, sequencePreset("loop.mp4"))
	stand.writeMedia(t, "loop.mp4")
	stand.mgr.EnablePlatform("Twitch")
	if !stand.enabled() {
		t.Fatal("платформа не увімкнулась на валідному пресеті")
	}
	return stand
}

const switchedOffWarning = "warning Twitch: fallback loop video not found: loop.mp4 -- the platform was switched off"

// Гейт на старті ефіру: заглушка зникла після enable -- галочка знімається ДО
// того, як бродкаст піде на платформу.
func TestBroadcastStartSwitchesOffInvalidFallback(t *testing.T) {
	stand := enabledWithLoop(t)
	stand.removeMedia(t, "loop.mp4")
	_, persistsBefore := stand.snapshot()

	stand.mgr.OnAvailable("live/main")

	events, persists := stand.snapshot()
	want := []string{"info Broadcast started", switchedOffWarning}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %q, want %q", events, want)
	}
	if stand.enabled() {
		t.Error("платформа лишилась увімкненою на старті ефіру")
	}
	if persists <= persistsBefore {
		t.Error("зняття галочки не персистнуло")
	}
}

func TestBroadcastStartKeepsValidFallback(t *testing.T) {
	stand := enabledWithLoop(t)
	stand.mgr.OnAvailable("live/main")

	events, _ := stand.snapshot()
	want := []string{"info Broadcast started"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %q, want %q", events, want)
	}
	if !stand.enabled() {
		t.Error("платформа з валідною заглушкою знялась з ефіру")
	}
}

// Груповий тумблер -- ще один шлях у ефір, тож гейт стоїть і там; вимкнення
// групи нічого не перевіряє.
func TestGroupToggleSwitchesOffInvalidFallback(t *testing.T) {
	stand := enabledWithLoop(t)
	stand.removeMedia(t, "loop.mp4")

	stand.mgr.SetGroupEnabled("default", false)
	if events, _ := stand.snapshot(); len(events) != 0 {
		t.Errorf("вимкнення групи емітнуло події гейта: %q", events)
	}
	stand.mgr.SetGroupEnabled("default", true)

	events, _ := stand.snapshot()
	if !reflect.DeepEqual(events, []string{switchedOffWarning}) {
		t.Fatalf("events = %q, want [%q]", events, switchedOffWarning)
	}
	if stand.enabled() {
		t.Error("груповий тумблер підняв платформу з непридатною заглушкою")
	}
}

// Платформа, перестворена під час активної публікації, підключається одразу --
// гейт стоїть і на цьому шляху.
func TestRecreateDuringPublicationSwitchesOffInvalidFallback(t *testing.T) {
	stand := enabledWithLoop(t)
	stand.mgr.OnAvailable("live/main")
	stand.removeMedia(t, "loop.mp4")

	video := 1
	stand.mgr.UpdatePlatform("Twitch", PlatformFields{Video: &video})

	events, _ := stand.snapshot()
	want := []string{"info Broadcast started", switchedOffWarning}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %q, want %q", events, want)
	}
	if stand.enabled() {
		t.Error("перестворена платформа лишилась увімкненою")
	}
}

// Перехід у backup із непридатною заглушкою: машина вже зняла платформу з
// ефіру, Manager знімає галочку.
func TestOnPlatformFallbackUnusableSwitchesOff(t *testing.T) {
	stand := enabledWithLoop(t)
	_, persistsBefore := stand.snapshot()

	stand.mgr.OnPlatformFallbackUnusable("Twitch", "fallback loop video not found: loop.mp4")

	events, persists := stand.snapshot()
	if !reflect.DeepEqual(events, []string{switchedOffWarning}) {
		t.Fatalf("events = %q, want [%q]", events, switchedOffWarning)
	}
	if stand.enabled() {
		t.Error("галочка лишилась після непридатної заглушки в ефірі")
	}
	if persists <= persistsBefore {
		t.Error("зняття галочки не персистнуло")
	}
}

// Вимкнена платформа гейт не чіпає -- повторного зняття й події бути не може.
func TestOnPlatformFallbackUnusableIgnoresDisabled(t *testing.T) {
	stand := newGateStand(t, sequencePreset("loop.mp4"))
	stand.mgr.OnPlatformFallbackUnusable("Twitch", "fallback loop video not found: loop.mp4")
	if events, _ := stand.snapshot(); len(events) != 0 {
		t.Errorf("вимкнена платформа емітнула подію: %q", events)
	}
}

func strPtr(s string) *string { return &s }
