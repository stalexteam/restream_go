package settings

import (
	"os"
	"path/filepath"
	"testing"

	"restream_go/internal/control"
)

// Persist пише той самий байтовий вигляд, що control.Dumps: порядок ключів
// зберігається, не-ASCII екранується.
func TestPersistBytes(t *testing.T) {
	cases := map[string]*control.Dict{
		"settings_only": control.D(
			"connect_timeout_ms", int64(5000), "read_timeout_ms", int64(500),
			"offline_timeout_sec", int64(1800), "icmp_ping", true, "obs_widget_show_bitrate", false,
			"sources", []any{}, "platforms", []any{}, "platform_groups", []any{}, "fallback_presets", []any{},
		),
		"with_unicode_and_extra_fields": control.D(
			"connect_timeout_ms", int64(2500), "read_timeout_ms", int64(300),
			"offline_timeout_sec", int64(60), "icmp_ping", false, "obs_widget_show_bitrate", true,
			"dashboard_token", "tok \U0001F525", "listen_port", int64(8790),
			"sources", []any{control.D("name", "Głowna", "is_default", true)},
			"platforms", []any{}, "platform_groups", []any{}, "fallback_presets", []any{},
		),
	}
	for name, config := range cases {
		tmp := filepath.Join(t.TempDir(), "config.json")
		if err := Persist(tmp, config); err != nil {
			t.Fatalf("Persist[%s]: %v", name, err)
		}
		got, err := os.ReadFile(tmp)
		if err != nil {
			t.Fatalf("read persisted file: %v", err)
		}
		if want := control.Dumps(config); string(got) != string(want) {
			t.Errorf("Persist[%s] bytes differ:\ngot:  %q\nwant: %q", name, got, want)
		}
	}
}

// Не-ASCII і сурогатні пари екрануються, як у ensure_ascii.
func TestPersistEscapesNonASCII(t *testing.T) {
	config := control.D("token", "tok \U0001F525", "name", "Głowna")
	tmp := filepath.Join(t.TempDir(), "config.json")
	if err := Persist(tmp, config); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"token\": \"tok \\ud83d\\udd25\",\n  \"name\": \"G\\u0142owna\"\n}\n"
	if string(got) != want {
		t.Errorf("persisted bytes:\ngot:  %q\nwant: %q", got, want)
	}
}
