package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"restream_go/internal/control"
	"restream_go/internal/media"
)

const testToken = "tttttttttttttttttttt"

type harness struct {
	ts      *httptest.Server
	server  *Server
	manager *control.Manager
	store   *media.Store
	hub     *Hub
	base    string
	dash    string
}

// restart підміняє шов рестарту MediaMTX.
func (h *harness) restart(fn func() error) { h.server.d.RestartMediaMTX = fn }

func newHarness(t *testing.T) *harness {
	t.Helper()
	base := t.TempDir()
	for _, dir := range []string{"tmp", "media", "dashboard", "logs"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(base, "config.json")
	config := control.D(
		"public_host", "127.0.0.1",
		"listen_host", "127.0.0.1",
		"listen_port", int64(0),
		"dashboard_token", testToken,
		"obs_pass", "p",
		"internal_user", "i",
		"internal_pass", "p",
		"offline_timeout_sec", int64(60),
		"connect_timeout_ms", int64(2500),
		"read_timeout_ms", int64(300),
		"icmp_ping", false,
		"sources", []any{},
		"platforms", []any{},
		"platform_groups", []any{},
		"fallback_presets", []any{},
	)
	if err := control.Persist(configPath, config); err != nil {
		t.Fatal(err)
	}

	manager := control.New(config, control.Options{BaseDir: base, ConfigPath: configPath})
	store := media.NewStore(filepath.Join(base, "media"))
	// Дашборд у тестах — з диска: кожен кейс кладе свої байти в dash.
	dash := filepath.Join(base, "dashboard")
	// Хаб без фонової петлі: у тестах потрібні лише кадри у відповідь на дії.
	hub := newHub(ManagerSource{M: manager}, base)
	server := NewServer(Deps{
		Manager: manager, Media: store, Hub: hub, BaseDir: base,
		ConfigPath: configPath, Dashboard: os.DirFS(dash), Token: testToken,
	})
	ts := httptest.NewServer(server)
	t.Cleanup(func() {
		ts.Close()
		manager.Shutdown()
	})
	return &harness{ts: ts, server: server, manager: manager, store: store, hub: hub,
		base: base, dash: dash}
}

func (h *harness) get(t *testing.T, path string, header map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range header {
		req.Header.Set(name, value)
	}
	resp, err := h.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func (h *harness) post(t *testing.T, path string, body []byte) (*http.Response, []byte) {
	t.Helper()
	resp, err := h.ts.Client().Post(h.ts.URL+path, "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	return resp, got
}

func decodeBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("bad JSON body %q: %v", body, err)
	}
	return out
}

// TestTokenGateMatrix — токен-гейт на всіх захищених маршрутах.
func TestTokenGateMatrix(t *testing.T) {
	h := newHarness(t)
	writeFile(t, filepath.Join(h.dash, "index.html"), []byte("<html>dash</html>"))
	writeFile(t, filepath.Join(h.dash, "config.html"), []byte("<html>config</html>"))

	protected := []string{"/dashboard", "/config", "/files/raw", "/ws"}
	bad := []string{"", "?token=", "?token=wrong", "?token=__DASHBOARD_TOKEN__", "?token=" + testToken + "x"}
	for _, path := range protected {
		for _, query := range bad {
			resp, body := h.get(t, path+query, nil)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("GET %s%s = %d, want 401", path, query, resp.StatusCode)
			}
			if got := decodeBody(t, body)["error"]; got != "invalid token" {
				t.Errorf("GET %s%s error = %v", path, query, got)
			}
		}
	}
	resp, _ := h.get(t, "/dashboard?token="+testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("valid token = %d, want 200", resp.StatusCode)
	}
}

// TestEmptyTokenLocksEverything — порожній dashboard_token не відкриває дашборд.
func TestEmptyTokenLocksEverything(t *testing.T) {
	h := newHarness(t)
	h.ts.Close()
	server := NewServer(Deps{Manager: h.manager, Media: h.store, Hub: h.hub, BaseDir: h.base,
		ConfigPath: filepath.Join(h.base, "config.json"), Dashboard: os.DirFS(h.dash), Token: ""})
	h.ts = httptest.NewServer(server)
	defer h.ts.Close()
	for _, query := range []string{"", "?token=", "?token=anything"} {
		resp, _ := h.get(t, "/dashboard"+query, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("empty expected token: GET /dashboard%s = %d, want 401", query, resp.StatusCode)
		}
	}
}

// TestPagesAndStatic — сторінки з Referrer-Policy і статика без токена.
func TestPagesAndStatic(t *testing.T) {
	h := newHarness(t)
	writeFile(t, filepath.Join(h.dash, "index.html"), []byte("<html>dash</html>"))
	writeFile(t, filepath.Join(h.dash, "config.html"), []byte("<html>config</html>"))
	writeFile(t, filepath.Join(h.dash, "dashboard.js"), []byte("// js"))

	for path, want := range map[string]string{"/dashboard": "<html>dash</html>", "/config": "<html>config</html>"} {
		resp, body := h.get(t, path+"?token="+testToken, nil)
		if resp.StatusCode != http.StatusOK || string(body) != want {
			t.Errorf("GET %s = %d %q", path, resp.StatusCode, body)
		}
		if got := resp.Header.Get("Referrer-Policy"); got != "no-referrer" {
			t.Errorf("GET %s Referrer-Policy = %q", path, got)
		}
		if got := resp.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("GET %s Content-Type = %q", path, got)
		}
	}

	resp, body := h.get(t, "/dashboard.js", nil)
	if resp.StatusCode != http.StatusOK || string(body) != "// js" {
		t.Errorf("static asset = %d %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/javascript; charset=utf-8" {
		t.Errorf("static Content-Type = %q", got)
	}
	if got := resp.Header.Get("Referrer-Policy"); got != "" {
		t.Errorf("static must not carry Referrer-Policy, got %q", got)
	}

	resp, body = h.get(t, "/config.js", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing asset = %d, want 404", resp.StatusCode)
	}
	if got := decodeBody(t, body)["error"]; got != "not found" {
		t.Errorf("missing asset body = %v", got)
	}
	resp, _ = h.get(t, "/nope", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown route = %d, want 404", resp.StatusCode)
	}
}

// TestStatusRoute — /status без токена (лише localhost).
func TestStatusRoute(t *testing.T) {
	h := newHarness(t)
	resp, body := h.get(t, "/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /status = %d", resp.StatusCode)
	}
	snapshot := decodeBody(t, body)
	for _, key := range []string{"sources", "platforms", "groups", "session", "manual_halt", "obs_widget_show_bitrate"} {
		if _, ok := snapshot[key]; !ok {
			t.Errorf("/status missing %q", key)
		}
	}
	if _, ok := snapshot["components"]; ok {
		t.Error("/status must not carry hub-only components")
	}
}

// TestHookRoutes — хуки MediaMTX з ?path= і ліміт тіла.
func TestHookRoutes(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/hooks/available", "/hooks/unavailable"} {
		resp, body := h.post(t, path+"?path=live/unknown", []byte("dummy=1"))
		if resp.StatusCode != http.StatusOK {
			t.Errorf("POST %s = %d, want 200", path, resp.StatusCode)
		}
		if got := decodeBody(t, body)["ok"]; got != true {
			t.Errorf("POST %s body = %v", path, got)
		}
		resp, _ = h.post(t, path, nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("POST %s without path = %d, want 200", path, resp.StatusCode)
		}
	}
	resp, _ := h.post(t, "/hooks/bogus?path=live/x", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown hook = %d, want 404", resp.StatusCode)
	}
	resp, body := h.post(t, "/hooks/available?path=live/x", bytes.Repeat([]byte("x"), maxRequestBody+1))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized hook body = %d, want 413", resp.StatusCode)
	}
	if got := decodeBody(t, body)["error"]; got != "request body too large" {
		t.Errorf("oversized body error = %v", got)
	}
	resp, _ = h.post(t, "/hooks/available?path=live/x", bytes.Repeat([]byte("x"), maxRequestBody))
	if resp.StatusCode != http.StatusOK {
		t.Errorf("body at the limit = %d, want 200", resp.StatusCode)
	}
}

// TestRangeMatrix — Range `N-`/`-N`/416 і повна віддача без заголовка.
func TestRangeMatrix(t *testing.T) {
	h := newHarness(t)
	payload := []byte("0123456789abcdef")
	writeFile(t, filepath.Join(h.base, "media", "clip.mp4"), payload)
	size := len(payload)

	cases := []struct {
		header  string
		code    int
		body    string
		content string
	}{
		{"", 200, string(payload), ""},
		{"bytes=0-", 206, string(payload), fmt.Sprintf("bytes 0-%d/%d", size-1, size)},
		{"bytes=5-9", 206, "56789", fmt.Sprintf("bytes 5-9/%d", size)},
		{"bytes=-4", 206, "cdef", fmt.Sprintf("bytes %d-%d/%d", size-4, size-1, size)},
		{"bytes=10-999", 206, "abcdef", fmt.Sprintf("bytes 10-%d/%d", size-1, size)},
		{"bytes=999-", 206, "f", fmt.Sprintf("bytes %d-%d/%d", size-1, size-1, size)},
		{"bytes=99999999999999999999-", 206, "f", fmt.Sprintf("bytes %d-%d/%d", size-1, size-1, size)},
		{"bytes=-999", 206, string(payload), fmt.Sprintf("bytes 0-%d/%d", size-1, size)},
		{"bytes=0-0", 206, "0", fmt.Sprintf("bytes 0-0/%d", size)},
		{"bytes=-0", 416, "", fmt.Sprintf("bytes */%d", size)},
		{"bytes=9-2", 416, "", fmt.Sprintf("bytes */%d", size)},
		{"bytes=-", 206, string(payload), fmt.Sprintf("bytes 0-%d/%d", size-1, size)},
		{"items=1-2", 200, string(payload), ""},
		{"bytes=abc-", 200, string(payload), ""},
	}
	for _, c := range cases {
		header := map[string]string{}
		if c.header != "" {
			header["Range"] = c.header
		}
		resp, body := h.get(t, "/files/raw?token="+testToken+"&path=clip.mp4", header)
		if resp.StatusCode != c.code {
			t.Errorf("Range %q: status = %d, want %d", c.header, resp.StatusCode, c.code)
			continue
		}
		if string(body) != c.body {
			t.Errorf("Range %q: body = %q, want %q", c.header, body, c.body)
		}
		if got := resp.Header.Get("Content-Range"); got != c.content {
			t.Errorf("Range %q: Content-Range = %q, want %q", c.header, got, c.content)
		}
		if c.code != 416 {
			if got := resp.Header.Get("Content-Type"); got != "video/mp4" {
				t.Errorf("Range %q: Content-Type = %q", c.header, got)
			}
			if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
				t.Errorf("Range %q: Accept-Ranges = %q", c.header, got)
			}
		}
	}

	for _, path := range []string{"nope.mp4", "../../etc/passwd", ""} {
		resp, _ := h.get(t, "/files/raw?token="+testToken+"&path="+path, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("raw %q = %d, want 404", path, resp.StatusCode)
		}
	}
}

// TestUploadMatrix.py.
func TestUploadMatrix(t *testing.T) {
	fixture := filepath.Join("..", "probe", "testdata", "clip_a.mp4")
	if _, err := os.Stat(fixture); err != nil {
		t.Skip("internal/probe/testdata fixtures missing")
	}
	h := newHarness(t)
	payload, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(h.base, "media")

	resp, _ := h.post(t, "/files/upload?token=wrong&path=&name=x.mp4", payload)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad token = %d, want 401", resp.StatusCode)
	}

	resp, body := h.post(t, "/files/upload?token="+testToken+"&path=&name=uploaded.mp4", payload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid upload = %d: %s", resp.StatusCode, body)
	}
	reply := decodeBody(t, body)
	if reply["ok"] != true || reply["path"] != "uploaded.mp4" {
		t.Errorf("upload reply = %v", reply)
	}
	stored, err := os.ReadFile(filepath.Join(root, "uploaded.mp4"))
	if err != nil || !bytes.Equal(stored, payload) {
		t.Errorf("stored bytes differ (err=%v)", err)
	}

	checks := []struct {
		name  string
		query string
		body  []byte
		code  int
		text  string
	}{
		{"duplicate", "path=&name=uploaded.mp4", payload, 400, "already exists"},
		{"escaping folder", "path=../..&name=x.mp4", payload, 400, "target folder not found"},
		{"non-video extension", "path=&name=x.txt", payload, 400, "only video files are accepted"},
		{"empty upload", "path=&name=empty.mp4", nil, 400, "empty upload"},
		{"unreadable file", "path=&name=garbage.mp4", []byte("not a video at all"), 400, "unreadable"},
	}
	for _, c := range checks {
		resp, body := h.post(t, "/files/upload?token="+testToken+"&"+c.query, c.body)
		if resp.StatusCode != c.code {
			t.Errorf("%s = %d, want %d (%s)", c.name, resp.StatusCode, c.code, body)
			continue
		}
		if text, _ := decodeBody(t, body)["error"].(string); !strings.Contains(text, c.text) {
			t.Errorf("%s error = %q, want it to mention %q", c.name, text, c.text)
		}
	}

	silent, err := os.ReadFile(filepath.Join("..", "probe", "testdata", "video_only.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	resp, body = h.post(t, "/files/upload?token="+testToken+"&path=&name=silent.mp4", silent)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("silent upload = %d: %s", resp.StatusCode, body)
	}
	if text, _ := decodeBody(t, body)["error"].(string); !strings.Contains(text, "no audio track") {
		t.Errorf("silent upload error = %q", text)
	}
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".part") || entry.Name() == "silent.mp4" {
			t.Errorf("rejected upload left %q behind", entry.Name())
		}
	}

	if !h.store.AcquireUploadSlot() {
		t.Error("upload slot not released after the attempts")
	}
	h.store.ReleaseUploadSlot()
}

// TestUploadSlotBusy — другий одночасний аплоад відхиляється 409.
func TestUploadSlotBusy(t *testing.T) {
	h := newHarness(t)
	if !h.store.AcquireUploadSlot() {
		t.Fatal("could not take the slot")
	}
	defer h.store.ReleaseUploadSlot()
	resp, body := h.post(t, "/files/upload?token="+testToken+"&path=&name=x.mp4", []byte("data"))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("busy slot = %d, want 409", resp.StatusCode)
	}
	if got := decodeBody(t, body)["error"]; got != "another upload is already running" {
		t.Errorf("busy slot error = %v", got)
	}
}

// TestRejectedUploadDrainsBody — велике тіло з наперед відомою відмовою все
// одно дочитується, тож клієнт бачить причину, а не RST.
func TestRejectedUploadDrainsBody(t *testing.T) {
	h := newHarness(t)
	big := bytes.Repeat([]byte("x"), 3*1024*1024)
	resp, body := h.post(t, "/files/upload?token="+testToken+"&path=&name=notes.txt", big)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("rejected big upload = %d, want 400", resp.StatusCode)
	}
	if text, _ := decodeBody(t, body)["error"].(string); !strings.Contains(text, "only video files") {
		t.Errorf("rejected big upload error = %q", text)
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
