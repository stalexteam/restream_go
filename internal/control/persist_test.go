package control

import (
	"bytes"
	"os"
	"testing"
)

// normalizeAll — ті самі чотири присвоєння normalize_*, що робить персист.
func normalizeAll(config *Dict) {
	config.Set("sources", NormalizeSources(config))
	config.Set("platform_groups", NormalizeGroups(config))
	config.Set("platforms", NormalizePlatforms(config))
	config.Set("fallback_presets", NormalizePresets(config))
}

func persistCaseMinimal() *Dict {
	return D(
		"listen_host", "0.0.0.0",
		"listen_port", int64(8790),
		"public_host", "example.com",
		"dashboard_token", "tok",
		"mediamtx_rtmp_host", "127.0.0.1",
		"mediamtx_rtmp_port", int64(1935),
		"mediamtx_srt_port", int64(8890),
		"obs_pass", "obspass",
		"internal_user", "internal",
		"internal_pass", "intpass",
		"fallback_presets", []any{},
		"sources", []any{},
		"platform_groups", []any{},
		"platforms", []any{},
		"offline_timeout_sec", int64(1800),
		"connect_timeout_ms", int64(5000),
		"read_timeout_ms", int64(500),
		"max_concurrent_transcodes", int64(1),
		"transcode_threads", int64(1),
		"icmp_ping", false,
		"obs_widget_show_bitrate", false,
	)
}

// Побайтовий формат персисту: порядок ключів = порядок вставки, indent=2,
// не-ASCII екранується, normalize_* застосовані.
func TestPersistBytes(t *testing.T) {
	config := D(
		"public_host", "example.com",
		"icmp_ping", false,
		"sources", []any{D("name", "Głowna", "type", "weird", "audio_tracks", "3")},
		"platform_groups", []any{D("id", "g1", "is_default", "yes"), D("id", "g1")},
		"platforms", []any{},
		"fallback_presets", []any{},
	)
	normalizeAll(config)
	want := `{
  "public_host": "example.com",
  "icmp_ping": false,
  "sources": [
    {
      "name": "G\u0142owna",
      "type": "rtmp",
      "audio_tracks": 1,
      "is_default": true,
      "vod_track": false,
      "enhanced_broadcasting": false,
      "video_tracks": 0
    }
  ],
  "platform_groups": [
    {
      "id": "g1",
      "is_default": true,
      "name": "g1",
      "enabled": true
    },
    {
      "id": "g1-2",
      "name": "g1",
      "enabled": true,
      "is_default": false
    }
  ],
  "platforms": [],
  "fallback_presets": [
    {
      "id": "default",
      "name": "Default",
      "is_default": true,
      "type": "sequence",
      "start_file": "",
      "loop_file": "backup.mp4",
      "end_file": "",
      "folder": "",
      "separator_file": ""
    }
  ]
}
`
	if got := string(Dumps(config)); got != want {
		t.Errorf("Dumps mismatch:\n got=%s\nwant=%s", got, want)
	}
}

// Persist кладе на диск ті самі байти, що Dumps (tmp + rename).
func TestPersistWritesFile(t *testing.T) {
	config := persistCaseMinimal()
	normalizeAll(config)
	path := t.TempDir() + "/config.json"
	if err := Persist(path, config); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted file: %v", err)
	}
	if !bytes.Equal(got, Dumps(config)) {
		t.Errorf("Persist mismatch:\n got=%s\nwant=%s", got, Dumps(config))
	}
}
