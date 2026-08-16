package control

import (
	"os"
	"path/filepath"
	"testing"

	"restream_go/internal/probe"
	"restream_go/internal/wire/ts"
)

// Q60: recreate вирішується за НОРМАЛІЗОВАНИМ video, а не за сирим полем форми.

func newRecreateStand(t *testing.T, source *Dict) *Manager {
	t.Helper()
	base := t.TempDir()
	for _, dir := range []string{"media", "tmp"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	config := D(
		"fallback_presets", []any{D("id", "default", "name", "Default", "is_default", true,
			"type", "sequence", "loop_file", "backup.mp4")},
		"sources", []any{source},
		"platform_groups", []any{D("id", "default", "name", "Default", "is_default", true, "enabled", true)},
		"platforms", []any{D("name", "Twitch", "type", "rtmp", "enabled", false,
			"source", "Main", "video", int64(0))},
	)
	return New(config, Options{
		BaseDir: base,
		Probes: Probes{
			TSManifest:   func(string) (ts.Manifest, bool) { return ts.Manifest{}, false },
			TrackCounts:  func(string) (probe.TrackCounts, bool) { return probe.TrackCounts{Video: 1, Audio: 1}, true },
			StreamParams: func(string, int, int) (probe.StreamParams, bool) { return probe.StreamParams{}, false },
		},
		newRuntime: func(m *Manager, e *platformEntry) Runtime {
			return &stubRuntime{mgr: m, name: e.name}
		},
		spawn:   func(fn func()) { fn() },
		persist: func(string, *Dict) error { return nil },
	})
}

func platformEntryOf(t *testing.T, m *Manager, name string) *platformEntry {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.platforms.Get(name)
	if !ok {
		t.Fatalf("platform %s is gone", name)
	}
	return e
}

// Q58: VOD Track над однодоріжковим не-EB source відхиляється на Save і enable.
func TestVodTrackOverSingleTrackSourceIsRejected(t *testing.T) {
	mgr := newRecreateStand(t, D("name", "Main", "is_default", true, "type", "rtmp",
		"live_path", "live/main", "audio_tracks", int64(1)))
	var warnings []string
	mgr.OnEvent = func(level, text string) { warnings = append(warnings, level+" "+text) }

	vod := true
	mgr.UpdatePlatform("Twitch", PlatformFields{VODTrack: &vod})
	if e := platformEntryOf(t, mgr, "Twitch"); e.vodTrack || e.spec.Chimera {
		t.Fatal("VOD Track was applied over a single-track source")
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings %v, want exactly one rejection", warnings)
	}

	// Той самий гейт на enable -- повз форму.
	e := platformEntryOf(t, mgr, "Twitch")
	e.vodTrack = true
	e.spec = mgr.specForConfigLocked(D("name", "Twitch", "type", "rtmp", "vod_track", true,
		"source", "Main", "video", int64(0)))
	mgr.EnablePlatform("Twitch")
	if platformEntryOf(t, mgr, "Twitch").enabled {
		t.Fatal("a platform with an unbuildable chimera was enabled")
	}
	if len(warnings) != 2 {
		t.Fatalf("warnings %v, want a rejection from enable too", warnings)
	}
}

// Дві доріжки в rtmp-source дає лише його власний VOD Track (SourceTrackCount).
func TestVodTrackOverTwoTrackSourceIsAccepted(t *testing.T) {
	mgr := newRecreateStand(t, D("name", "Main", "is_default", true, "type", "rtmp",
		"live_path", "live/main", "vod_track", true))
	vod := true
	mgr.UpdatePlatform("Twitch", PlatformFields{VODTrack: &vod})
	if e := platformEntryOf(t, mgr, "Twitch"); !e.vodTrack || !e.spec.Chimera {
		t.Fatal("VOD Track was rejected over a two-track source")
	}
}

func TestSaveWithLadderVideoOverPlainSourceKeepsPlatform(t *testing.T) {
	mgr := newRecreateStand(t, D("name", "Main", "is_default", true, "type", "rtmp",
		"live_path", "live/main", "audio_tracks", int64(1)))
	before := platformEntryOf(t, mgr, "Twitch")

	video := -1
	for save := 1; save <= 2; save++ {
		mgr.UpdatePlatform("Twitch", PlatformFields{Video: &video})
		if after := platformEntryOf(t, mgr, "Twitch"); after != before {
			t.Fatalf("save %d recreated the platform: video -1 normalizes to the current 0", save)
		}
	}
	if got := before.video; got != 0 {
		t.Fatalf("video %d, want the normalized 0", got)
	}
}

func TestSaveWithLadderVideoOverEBSourceRecreatesPlatform(t *testing.T) {
	mgr := newRecreateStand(t, D("name", "Main", "is_default", true, "type", "rtmp",
		"live_path", "live/main", "audio_tracks", int64(2), "enhanced_broadcasting", true,
		"video_tracks", int64(2)))
	before := platformEntryOf(t, mgr, "Twitch")

	video := -1
	mgr.UpdatePlatform("Twitch", PlatformFields{Video: &video})
	after := platformEntryOf(t, mgr, "Twitch")
	if after == before {
		t.Fatal("switching an EB platform to the whole ladder must recreate it")
	}
	if after.video != -1 || !after.spec.EB {
		t.Fatalf("video %d eb %v, want -1 and the EB arm", after.video, after.spec.EB)
	}

	mgr.UpdatePlatform("Twitch", PlatformFields{Video: &video})
	if again := platformEntryOf(t, mgr, "Twitch"); again != after {
		t.Fatal("the second identical save recreated the platform again")
	}
}

// Q61: audio_map не існує для rtmp-платформи й не сміє чіпати її audio.
func TestAudioMapIsIgnoredForRtmpPlatform(t *testing.T) {
	mgr := newRecreateStand(t, D("name", "Main", "is_default", true, "type", "rtmp",
		"live_path", "live/main", "audio_tracks", int64(1)))
	audio := 2
	mgr.UpdatePlatform("Twitch", PlatformFields{Audio: &audio})

	audioMap := []any{int64(4), nil, nil, nil, nil, nil}
	mgr.UpdatePlatform("Twitch", PlatformFields{AudioMap: &audioMap})
	e := platformEntryOf(t, mgr, "Twitch")
	if e.audio != 2 {
		t.Fatalf("audio = %d, want the value set through the rtmp field", e.audio)
	}
	if _, ok := e.toConfig().Get("audio_map"); ok {
		t.Fatal("audio_map leaked into an rtmp platform config")
	}

	// srt-платформа мапу далі приймає.
	stype := "srt"
	mgr.UpdatePlatform("Twitch", PlatformFields{Type: &stype})
	mgr.UpdatePlatform("Twitch", PlatformFields{AudioMap: &audioMap})
	if e := platformEntryOf(t, mgr, "Twitch"); e.audio != 4 {
		t.Fatalf("srt audio = %d, want the first mapped track", e.audio)
	}
}
