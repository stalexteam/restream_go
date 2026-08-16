package mtx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Без портових ключів рендер лишається побайтово тим самим, що до девіації
// EL18: підстановки тотожні.
func TestRenderPortsDefaultsAreIdentity(t *testing.T) {
	for _, config := range []map[string]any{nil, {}, {"obs_pass": "x"}} {
		if got := renderPorts(TemplateText, config); got != TemplateText {
			t.Errorf("renderPorts(%v) changed the template", config)
		}
	}
}

func TestRenderPortsFromConfig(t *testing.T) {
	config := map[string]any{
		"mediamtx_rtmp_port": int64(1937),
		"mediamtx_srt_port":  int64(8891),
		"listen_port":        int64(8791),
	}
	got := renderPorts(TemplateText, config)
	want := map[string]string{
		"rtmpAddress: :1935": "rtmpAddress: :1937",
		"srtAddress: :8890":  "srtAddress: :8891",
	}
	base := strings.Split(TemplateText, "\n")
	lines := strings.Split(got, "\n")
	if len(base) != len(lines) {
		t.Fatalf("line count changed: %d -> %d", len(base), len(lines))
	}
	for i, before := range base {
		after := lines[i]
		expected := before
		if repl, ok := want[before]; ok {
			expected = repl
		} else if strings.Contains(before, "127.0.0.1:8790") {
			expected = strings.ReplaceAll(before, "127.0.0.1:8790", "127.0.0.1:8791")
		}
		if after != expected {
			t.Errorf("line %d: got %q, want %q", i+1, after, expected)
		}
	}
	if !strings.Contains(got, "runOnAvailable") || !strings.Contains(got, "127.0.0.1:8791/hooks/available") {
		t.Errorf("hook address was not rewritten")
	}
}

// Нецілі значення (як і в asPyInt: рядок, float, bool) падають на дефолти.
func TestRenderPortsRejectsNonInt(t *testing.T) {
	for _, bad := range []any{"1937", 1937.0, true, nil, []any{1937}} {
		config := map[string]any{
			"mediamtx_rtmp_port": bad, "mediamtx_srt_port": bad, "listen_port": bad,
		}
		if got := renderPorts(TemplateText, config); got != TemplateText {
			t.Errorf("value %#v (%T) was accepted as a port", bad, bad)
		}
	}
}

// Порти доїжджають до файлу через повний Render.
func TestRenderWritesConfiguredPorts(t *testing.T) {
	out := filepath.Join(t.TempDir(), "mediamtx.yml")
	config := map[string]any{
		"obs_pass": "o", "internal_pass": "i",
		"mediamtx_rtmp_port": 1937, "mediamtx_srt_port": 8891, "listen_port": 8791,
	}
	if err := Render(TemplateText, out, config); err != nil {
		t.Fatalf("Render: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"rtmpAddress: :1937", "srtAddress: :8891", "127.0.0.1:8791/hooks/unavailable"} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered file has no %q", want)
		}
	}
	for _, unwanted := range []string{":1935", ":8890", "127.0.0.1:8790"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("rendered file still carries %q", unwanted)
		}
	}
}

// Пароль із текстом іншого плейсхолдера підставляється один раз.
func TestPasswordsAreSubstitutedOnce(t *testing.T) {
	out := filepath.Join(t.TempDir(), "mediamtx.yml")
	config := map[string]any{"obs_pass": "__INTERNAL_PASS__", "internal_pass": "REALSECRET"}
	if err := Render(TemplateText, out, config); err != nil {
		t.Fatalf("Render: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Count(text, "REALSECRET") != 1 {
		t.Errorf("REALSECRET appears %d time(s), want 1", strings.Count(text, "REALSECRET"))
	}
	if !strings.Contains(text, "pass: __INTERNAL_PASS__") {
		t.Error("the obs password lost its literal value")
	}
}
