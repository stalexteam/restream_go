package api

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type wsClient struct {
	conn net.Conn
	r    *bufio.Reader
}

func dialWS(t *testing.T, ts *httptest.Server, token string) *wsClient {
	t.Helper()
	parsed, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	request := "GET /ws?token=" + token + " HTTP/1.1\r\n" +
		"Host: " + parsed.Host + "\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("ws handshake: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("ws handshake status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != wsAccept("dGhlIHNhbXBsZSBub25jZQ==") {
		t.Fatalf("Sec-WebSocket-Accept = %q", got)
	}
	client := &wsClient{conn: conn, r: reader}
	t.Cleanup(func() { conn.Close() })
	return client
}

func (c *wsClient) send(t *testing.T, message map[string]any) {
	t.Helper()
	payload, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	frame := encodeFrame(opText, payload)
	// Клієнтські фрейми ОБОВ'ЯЗКОВО маскуються (RFC 6455).
	key := []byte{0x11, 0x22, 0x33, 0x44}
	body := append([]byte(nil), frame[2:]...)
	if len(payload) < 126 {
		frame = frame[:2]
	} else {
		frame, body = frame[:4], body[2:]
	}
	frame[1] |= 0x80
	masked := make([]byte, len(body))
	for i, b := range body {
		masked[i] = b ^ key[i%4]
	}
	if _, err := c.conn.Write(append(append(frame, key...), masked...)); err != nil {
		t.Fatal(err)
	}
}

func (c *wsClient) recv(t *testing.T) map[string]any {
	t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	opcode, payload, ok := decodeFrame(c.r)
	if !ok {
		t.Fatal("connection closed while waiting for a frame")
	}
	if opcode != opText {
		t.Fatalf("unexpected opcode %d", opcode)
	}
	var message map[string]any
	if err := json.Unmarshal(payload, &message); err != nil {
		t.Fatalf("bad frame %q: %v", payload, err)
	}
	return message
}

// expect — наступний кадр саме цього типу (снапшоти хаба пропускаються).
func (c *wsClient) expect(t *testing.T, want string) map[string]any {
	t.Helper()
	for {
		message := c.recv(t)
		kind, _ := message["type"].(string)
		if kind == "full" || kind == "delta" {
			continue
		}
		if kind != want {
			t.Fatalf("got %q message, want %q (%v)", kind, want, message)
		}
		return message
	}
}

// probeSilence — після команди без відповіді наступним кадром має бути pong.
func (c *wsClient) probeSilence(t *testing.T, marker float64) {
	t.Helper()
	c.send(t, map[string]any{"command": "ping", "t": marker})
	message := c.expect(t, "pong")
	if message["t"] != marker {
		t.Fatalf("unexpected reply before the ping probe: %v", message)
	}
}

// TestWSHandshakeAndPing — /ws піднімається і відповідає на ping.
func TestWSHandshakeAndPing(t *testing.T) {
	h := newHarness(t)
	client := dialWS(t, h.ts, testToken)
	full := client.recv(t)
	if full["type"] != "full" {
		t.Fatalf("first frame = %v, want full", full)
	}
	data, _ := full["data"].(map[string]any)
	for _, key := range []string{"sources", "platforms", "components", "obs_source_connected"} {
		if _, ok := data[key]; !ok {
			t.Errorf("full snapshot missing %q", key)
		}
	}
	client.send(t, map[string]any{"command": "ping", "t": 4242.0})
	if got := client.expect(t, "pong")["t"]; got != 4242.0 {
		t.Errorf("pong t = %v, want 4242", got)
	}
}

// TestCRUDReplyShapes — нюанси C3: які команди шлють *_result, а які лише
// повторний settings.
func TestCRUDReplyShapes(t *testing.T) {
	h := newHarness(t)
	client := dialWS(t, h.ts, testToken)
	client.expect2(t)

	client.send(t, map[string]any{"command": "add_source", "name": "obs", "type": "rtmp"})
	if result := client.expect(t, "source_result"); result["ok"] != true {
		t.Fatalf("add_source = %v", result)
	}
	settings := client.expect(t, "settings")
	groupID := firstGroupID(t, settings)

	client.send(t, map[string]any{"command": "add_source", "name": "obs", "type": "rtmp"})
	result := client.expect(t, "source_result")
	if result["ok"] != false {
		t.Errorf("duplicate source accepted: %v", result)
	}
	if errors, _ := result["errors"].(map[string]any); errors["name"] == nil {
		t.Errorf("duplicate source errors = %v", result["errors"])
	}

	client.send(t, map[string]any{"command": "add_platform", "name": "twitch", "type": "rtmp"})
	if result := client.expect(t, "platform_result"); result["ok"] != true {
		t.Fatalf("add_platform = %v", result)
	}
	client.expect(t, "settings")

	// C3: update_group / remove_group / remove_platform — БЕЗ *_result.
	client.send(t, map[string]any{"command": "update_group", "id": groupID, "name": "Main"})
	client.expect(t, "settings")
	client.send(t, map[string]any{"command": "remove_platform", "name": "twitch"})
	client.expect(t, "settings")

	// C3: remove_source — ШЛЕ source_result.
	client.send(t, map[string]any{"command": "remove_source", "name": "obs"})
	if result := client.expect(t, "source_result"); result["ok"] != true {
		t.Errorf("remove_source = %v", result)
	}
	client.expect(t, "settings")

	client.send(t, map[string]any{"command": "add_group", "name": "Second"})
	if result := client.expect(t, "group_result"); result["ok"] != true {
		t.Fatalf("add_group = %v", result)
	}
	settings = client.expect(t, "settings")
	newGroup := lastGroupID(t, settings)
	client.send(t, map[string]any{"command": "remove_group", "id": newGroup})
	client.expect(t, "settings")

	// enable/disable — без жодної відповіді (реконсиляція наступним snapshot).
	client.send(t, map[string]any{"command": "enable_platform", "name": "nope"})
	client.probeSilence(t, 1)
	client.send(t, map[string]any{"command": "disable_group", "id": groupID})
	client.probeSilence(t, 2)
}

// TestUnknownAndMalformedCommands — невідома команда й биті байти не рвуть
// з'єднання і не шлють відповіді.
func TestUnknownAndMalformedCommands(t *testing.T) {
	h := newHarness(t)
	client := dialWS(t, h.ts, testToken)
	client.expect2(t)

	client.send(t, map[string]any{"command": "does_not_exist"})
	client.probeSilence(t, 1)
	client.send(t, map[string]any{"name": "no command at all"})
	client.probeSilence(t, 2)

	frame := encodeFrame(opText, []byte("{not json"))
	key := []byte{1, 2, 3, 4}
	body := frame[2:]
	masked := make([]byte, len(body))
	for i, b := range body {
		masked[i] = b ^ key[i%4]
	}
	head := []byte{frame[0], frame[1] | 0x80}
	if _, err := client.conn.Write(append(append(head, key...), masked...)); err != nil {
		t.Fatal(err)
	}
	client.probeSilence(t, 3)
}

// TestFileManagerCommands — форми відповідей файлового менеджера.
func TestFileManagerCommands(t *testing.T) {
	h := newHarness(t)
	client := dialWS(t, h.ts, testToken)
	client.expect2(t)

	client.send(t, map[string]any{"command": "list_files", "path": ""})
	listing := client.expect(t, "files")
	data, _ := listing["data"].(map[string]any)
	if data == nil || data["path"] != "" {
		t.Fatalf("files data = %v", listing["data"])
	}
	if _, ok := data["entries"]; !ok {
		t.Errorf("files data missing entries: %v", data)
	}

	client.send(t, map[string]any{"command": "make_dir", "path": "", "name": "clips"})
	if result := client.expect(t, "files_result"); result["ok"] != true {
		t.Fatalf("make_dir = %v", result)
	}
	client.expect(t, "files")

	client.send(t, map[string]any{"command": "make_dir", "path": "", "name": ".."})
	result := client.expect(t, "files_result")
	if result["ok"] != false {
		t.Errorf("bad folder name accepted: %v", result)
	}
	if errors, _ := result["errors"].(map[string]any); errors["_"] == nil {
		t.Errorf("files_result errors = %v", result["errors"])
	}
	client.expect(t, "files")

	client.send(t, map[string]any{"command": "complete_path", "field": "loop_file", "prefix": "cl",
		"dirs_only": true})
	suggestions := client.expect(t, "path_suggestions")
	if suggestions["field"] != "loop_file" || suggestions["prefix"] != "cl" {
		t.Errorf("path_suggestions echo = %v", suggestions)
	}
	entries, _ := suggestions["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("suggestions entries = %v", suggestions["entries"])
	}
	if entry, _ := entries[0].(map[string]any); entry["name"] != "clips" {
		t.Errorf("suggestion = %v", entries[0])
	}
	status, _ := suggestions["status"].(map[string]any)
	if status == nil || status["exists"] != false {
		t.Errorf("status для неповного префікса = %v", suggestions["status"])
	}

	client.send(t, map[string]any{"command": "complete_path", "field": "folder", "prefix": "clips",
		"dirs_only": true})
	folder := client.expect(t, "path_suggestions")
	status, _ = folder["status"].(map[string]any)
	if status == nil || status["exists"] != true || status["is_dir"] != true {
		t.Fatalf("status наявної теки = %v", folder["status"])
	}
	// `clips` вище створена порожньою.
	if count, ok := status["video_count"].(float64); !ok || count != 0 {
		t.Errorf("video_count порожньої теки = %v", status["video_count"])
	}

	// Обидва написання відсутньої теки: без слеша й зі слешем.
	for _, prefix := range []string{"nosuchdir", "clipss/"} {
		client.send(t, map[string]any{"command": "complete_path", "field": "preset-folder",
			"prefix": prefix, "dirs_only": true})
		missing := client.expect(t, "path_suggestions")
		status, _ = missing["status"].(map[string]any)
		if status == nil || status["exists"] != false {
			t.Errorf("status для %q = %v", prefix, missing["status"])
		}
	}

	client.send(t, map[string]any{"command": "check_upload", "path": "", "name": "x.mp4", "size": 1024.0})
	check := client.expect(t, "upload_check")
	if check["ok"] != true || check["error"] != nil {
		t.Errorf("upload_check = %v", check)
	}
	client.send(t, map[string]any{"command": "check_upload", "path": "", "name": "x.txt", "size": 1024.0})
	check = client.expect(t, "upload_check")
	if check["ok"] != false {
		t.Errorf("upload_check for a non-video = %v", check)
	}
	if text, _ := check["error"].(string); !strings.Contains(text, "only video files") {
		t.Errorf("upload_check error = %v", check["error"])
	}

	client.send(t, map[string]any{"command": "rename_path", "path": "clips", "new_name": "intros"})
	if result := client.expect(t, "files_result"); result["ok"] != true {
		t.Errorf("rename = %v", result)
	}
	client.expect(t, "files")
	client.send(t, map[string]any{"command": "delete_path", "path": "intros"})
	if result := client.expect(t, "files_result"); result["ok"] != true {
		t.Errorf("delete = %v", result)
	}
	client.expect(t, "files")
}

// TestSaveSettings — валідація System-блоку і застосування.
func TestSaveSettings(t *testing.T) {
	h := newHarness(t)
	client := dialWS(t, h.ts, testToken)
	client.expect2(t)

	client.send(t, map[string]any{"command": "save_settings", "settings": "nope"})
	reply := client.expect(t, "settings_saved")
	if reply["ok"] != false {
		t.Fatalf("malformed payload accepted: %v", reply)
	}
	if errors, _ := reply["errors"].(map[string]any); errors["_"] != "malformed settings payload" {
		t.Errorf("malformed payload errors = %v", reply["errors"])
	}

	client.send(t, map[string]any{"command": "save_settings", "settings": map[string]any{
		"connect_timeout_ms": 100, "read_timeout_ms": 300, "offline_timeout_sec": 60}})
	reply = client.expect(t, "settings_saved")
	if reply["ok"] != false {
		t.Fatalf("below-minimum timeout accepted: %v", reply)
	}
	if errors, _ := reply["errors"].(map[string]any); errors["connect_timeout_ms"] == nil {
		t.Errorf("validation errors = %v", reply["errors"])
	}

	var restarts atomic.Int32
	h.restart(func() error { restarts.Add(1); return nil })
	client.send(t, map[string]any{"command": "save_settings", "settings": map[string]any{
		"connect_timeout_ms": 3000, "read_timeout_ms": 400, "offline_timeout_sec": 120,
		"icmp_ping": true}})
	if reply := client.expect(t, "settings_saved"); reply["ok"] != true {
		t.Fatalf("valid settings rejected: %v", reply)
	}
	if got, _ := h.manager.ConfigValue("connect_timeout_ms"); got != int64(3000) {
		t.Errorf("connect_timeout_ms = %v, want 3000", got)
	}
	if got, _ := h.manager.ConfigValue("icmp_ping"); got != true {
		t.Errorf("icmp_ping = %v, want true", got)
	}
	if restarts.Load() != 1 {
		t.Errorf("mediamtx restarts = %d, want 1 (timings changed)", restarts.Load())
	}

	client.send(t, map[string]any{"command": "save_settings", "settings": map[string]any{
		"connect_timeout_ms": 3000, "read_timeout_ms": 400, "offline_timeout_sec": 300}})
	if reply := client.expect(t, "settings_saved"); reply["ok"] != true {
		t.Fatalf("valid settings rejected: %v", reply)
	}
	if restarts.Load() != 1 {
		t.Errorf("mediamtx restarted without a timing change (%d)", restarts.Load())
	}
}

// TestObsSourceRegistration — індикатор Source і точковий stop_streaming.
func TestObsSourceRegistration(t *testing.T) {
	h := newHarness(t)
	client := dialWS(t, h.ts, testToken)
	client.expect2(t)

	client.send(t, map[string]any{"command": "register_source", "obs_id": "session-1"})
	client.probeSilence(t, 1)
	if h.hub.sourceCount.Load() != 1 {
		t.Errorf("source connections = %d, want 1", h.hub.sourceCount.Load())
	}

	// Латч халту вирішує Manager; тут перевіряється форма проводу.
	h.hub.PushControl("stop_streaming")
	if action := client.expect(t, "control"); action["action"] != "stop_streaming" {
		t.Errorf("control action = %v", action)
	}

	client.conn.Close()
	h.hub.Unregister(nil)
	if h.hub.sourceCount.Load() != 1 {
		t.Errorf("unregister of a foreign connection changed the indicator")
	}
}

// expect2 — перший кадр з'єднання завжди повний знімок.
func (c *wsClient) expect2(t *testing.T) {
	t.Helper()
	if message := c.recv(t); message["type"] != "full" {
		t.Fatalf("first frame = %v, want full", message)
	}
}

func firstGroupID(t *testing.T, settings map[string]any) string {
	t.Helper()
	groups := settingsList(t, settings, "platform_groups")
	first, _ := groups[0].(map[string]any)
	id, _ := first["id"].(string)
	return id
}

func lastGroupID(t *testing.T, settings map[string]any) string {
	t.Helper()
	groups := settingsList(t, settings, "platform_groups")
	last, _ := groups[len(groups)-1].(map[string]any)
	id, _ := last["id"].(string)
	return id
}

func settingsList(t *testing.T, settings map[string]any, key string) []any {
	t.Helper()
	data, _ := settings["data"].(map[string]any)
	items, _ := data[key].([]any)
	if len(items) == 0 {
		t.Fatalf("settings %s is empty: %v", key, settings["data"])
	}
	return items
}
