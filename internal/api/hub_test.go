package api

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"restream_go/internal/control"
	"restream_go/internal/proc"
)

// slowConn — запис займає stall, як переповнений буфер повільного клієнта.
type slowConn struct {
	fakeConn
	stall time.Duration
	mu    sync.Mutex
}

func (c *slowConn) Write(p []byte) (int, error) {
	time.Sleep(c.stall)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fakeConn.Write(p)
}

func emptySource() *fakeSource {
	return &fakeSource{status: []byte(`{"sources": [], "platforms": [], "manual_halt": false}`)}
}

// TestBroadcastIsSerialAndDropsDead — розсилка йде послідовно під локом хаба,
// а мертвий сокет знімається, не блокуючи решту.
func TestBroadcastIsSerialAndDropsDead(t *testing.T) {
	src := emptySource()
	hub := newHub(src, t.TempDir())
	hub.alive = func(int) bool { return false }
	hub.mtxPID = func() *int { return nil }
	hub.ownPID = func() int { return 1 }

	slow := &slowConn{stall: 60 * time.Millisecond}
	dead := &fakeConn{dead: true}
	good := &fakeConn{}
	for _, sock := range []net.Conn{slow, dead, good} {
		hub.Register(&wsConn{raw: sock})
	}
	if len(hub.conns) != 3 {
		t.Fatalf("connections = %d, want 3", len(hub.conns))
	}
	good.take(t)
	slow.fakeConn.take(t)

	start := time.Now()
	hub.PushEvent("info", "hello")
	elapsed := time.Since(start)
	if elapsed < 60*time.Millisecond {
		t.Errorf("broadcast took %v; the oracle serialises sends under its lock", elapsed)
	}
	if len(hub.conns) != 2 {
		t.Errorf("connections after a dead socket = %d, want 2", len(hub.conns))
	}
	if len(good.take(t)) != 1 {
		t.Error("the live client did not get the event")
	}
	if len(slow.fakeConn.take(t)) != 1 {
		t.Error("the slow client did not get the event")
	}
}

// Платформа без "output" усе одно віддає cpu/rss.
func TestOutputStatsSurviveMissingOutput(t *testing.T) {
	src := &fakeSource{status: []byte(`{"platforms": [
		{"name": "P1"},
		{"name": "P2", "output": {"pid": 42}}
	]}`)}
	hub := newHub(src, t.TempDir())
	hub.alive = func(int) bool { return true }
	hub.sample = func(int) (proc.Stats, bool) { return proc.Stats{CPUPercent: 7.5, RSSMB: 12.5}, true }

	snap := hub.buildSnapshot()
	var payload struct {
		Platforms []struct {
			Name   string `json:"name"`
			Output *struct {
				PID *int     `json:"pid"`
				CPU *float64 `json:"cpu_percent"`
				RSS *float64 `json:"rss_mb"`
			} `json:"output"`
		} `json:"platforms"`
	}
	if err := json.Unmarshal(snap.raw["platforms"], &payload.Platforms); err != nil {
		t.Fatalf("snapshot is not JSON: %v", err)
	}
	if len(payload.Platforms) != 2 {
		t.Fatalf("platforms: %+v", payload.Platforms)
	}
	for _, p := range payload.Platforms {
		if p.Output == nil {
			t.Fatalf("%s: no output block in the payload", p.Name)
		}
	}
	if p := payload.Platforms[0]; p.Output.PID != nil || p.Output.CPU != nil || p.Output.RSS != nil {
		t.Fatalf("P1 without a pid must carry empty stats: %+v", *p.Output)
	}
	if p := payload.Platforms[1]; p.Output.CPU == nil || *p.Output.CPU != 7.5 {
		t.Fatalf("P2 stats: %+v", *p.Output)
	}
}

// TestDeltaCarriesOnlyChangedKeys — дельта несе лише змінені верхні ключі.
func TestDeltaCarriesOnlyChangedKeys(t *testing.T) {
	src := emptySource()
	hub := newHub(src, t.TempDir())
	hub.alive = func(int) bool { return false }
	hub.mtxPID = func() *int { return nil }
	hub.ownPID = func() int { return 1 }

	sock := &fakeConn{}
	hub.Register(&wsConn{raw: sock})
	sock.take(t)

	hub.broadcast()
	if frames := sock.take(t); len(frames) != 0 {
		t.Fatalf("unchanged snapshot pushed %d frame(s): %q", len(frames), frames)
	}

	src.status = []byte(`{"sources": [], "platforms": [], "manual_halt": true}`)
	hub.broadcast()
	frames := sock.take(t)
	if len(frames) != 1 {
		t.Fatalf("changed snapshot pushed %d frame(s)", len(frames))
	}
	var message struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(frames[0]), &message); err != nil {
		t.Fatal(err)
	}
	if message.Type != "delta" {
		t.Fatalf("type = %q, want delta", message.Type)
	}
	if _, ok := message.Data["manual_halt"]; !ok || len(message.Data) != 1 {
		t.Errorf("delta = %v, want only manual_halt", message.Data)
	}
}

// TestHubLoopPushesOnNotify — фонова петля прокидається на Notify.
func TestHubLoopPushesOnNotify(t *testing.T) {
	src := emptySource()
	hub := NewHub(src, t.TempDir())
	defer hub.Close()
	hub.alive = func(int) bool { return false }
	hub.mtxPID = func() *int { return nil }
	hub.ownPID = func() int { return 1 }

	sock := &fakeConn{}
	hub.Register(&wsConn{raw: sock})
	sock.take(t)

	src.status = []byte(`{"sources": [], "platforms": [], "manual_halt": true}`)
	hub.Notify()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hub.mu.Lock()
		frames := sock.take(t)
		hub.mu.Unlock()
		if len(frames) > 0 {
			if !strings.Contains(frames[0], `"manual_halt": true`) {
				t.Errorf("pushed frame = %s", frames[0])
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the loop never pushed the change")
}

// TestConcurrentClientsAndPushes — паралельні реєстрації, пуші й відключення
// не рвуть інваріанти хаба.
func TestConcurrentClientsAndPushes(t *testing.T) {
	src := emptySource()
	hub := NewHub(src, t.TempDir())
	defer hub.Close()
	hub.alive = func(int) bool { return false }
	hub.mtxPID = func() *int { return nil }
	hub.ownPID = func() int { return 1 }

	var wg sync.WaitGroup
	conns := make([]*wsConn, 16)
	for i := range conns {
		conns[i] = &wsConn{raw: &fakeConn{}}
	}
	for i, conn := range conns {
		wg.Add(1)
		go func(i int, conn *wsConn) {
			defer wg.Done()
			hub.Register(conn)
			hub.MarkSource(conn)
			hub.PushEvent("info", fmt.Sprintf("event %d", i))
			hub.Notify()
			if i%2 == 0 {
				hub.Unregister(conn)
			}
		}(i, conn)
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hub.PushControl("stop_streaming")
			hub.PushMessage(control.D("type", "files", "data", nil))
		}()
	}
	wg.Wait()

	hub.mu.Lock()
	live, sources := len(hub.conns), len(hub.sourceConns)
	hub.mu.Unlock()
	if live != 8 {
		t.Errorf("live connections = %d, want 8", live)
	}
	if sources != 8 || hub.sourceCount.Load() != int64(sources) {
		t.Errorf("source connections = %d (counter %d), want 8", sources, hub.sourceCount.Load())
	}
}

// TestConnWriteLockSerialisesFrames — паралельні писачі не перемішують фрейми.
func TestConnWriteLockSerialisesFrames(t *testing.T) {
	sock := &fakeConn{}
	conn := &wsConn{raw: sock}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = conn.sendText([]byte(fmt.Sprintf(`{"n": %02d}`, i)))
		}(i)
	}
	wg.Wait()
	frames := sock.take(t)
	if len(frames) != 32 {
		t.Fatalf("frames = %d, want 32", len(frames))
	}
	seen := map[string]bool{}
	for _, frame := range frames {
		if len(frame) != 9 || seen[frame] {
			t.Fatalf("corrupted or duplicated frame %q", frame)
		}
		seen[frame] = true
	}
}
