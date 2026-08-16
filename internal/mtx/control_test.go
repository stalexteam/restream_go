// Unix-only: фікстури — sh-скрипти.
//go:build !windows

package mtx

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func pidAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// fakeMediamtx — виконуваний sh-скрипт на місці bin/mediamtx: echo-ить
// argv у лог і поводиться за script (sleep 30 за замовч.).
func fakeMediamtx(t *testing.T, baseDir, body string) {
	t.Helper()
	binDir := filepath.Join(baseDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(binDir, "mediamtx")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func newTestController(t *testing.T, body string) (*Controller, string) {
	t.Helper()
	base := t.TempDir()
	fakeMediamtx(t, base, body)
	c := NewController(base)
	c.stopTimeout = 300 * time.Millisecond
	c.startupCheckDelay = 100 * time.Millisecond
	return c, base
}

func testConfig() map[string]any {
	return map[string]any{"obs_pass": "x", "internal_pass": "y", "connect_timeout_ms": int64(2500), "read_timeout_ms": int64(300)}
}

func TestControllerRestartRendersAndStarts(t *testing.T) {
	c, base := newTestController(t, "sleep 30\n")
	if err := c.Restart(testConfig()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	defer c.Stop()

	yml, err := os.ReadFile(filepath.Join(base, "tmp", "mediamtx.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(string(yml), "pass: x", "pass: y", "readTimeout: 2800ms") {
		t.Errorf("rendered yml missing expected substitutions:\n%s", yml)
	}

	c.mu.Lock()
	p := c.proc
	c.mu.Unlock()
	if p == nil {
		t.Fatal("no process tracked after Restart")
	}
	if !pidAlive(p.cmd.Process.Pid) {
		t.Fatal("mediamtx pid is not alive")
	}
}

func TestControllerWritesPIDFile(t *testing.T) {
	c, base := newTestController(t, "sleep 30\n")
	pidPath := filepath.Join(base, "tmp", ".mediamtx.pid")
	if err := c.Restart(testConfig()); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	c.mu.Lock()
	pid := c.proc.cmd.Process.Pid
	c.mu.Unlock()
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	if want := strconv.Itoa(pid) + "\n"; string(raw) != want {
		t.Errorf("pid file = %q, want %q", raw, want)
	}

	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("pid file survived Stop(): err=%v", err)
	}
}

func TestControllerRestartKillsPreviousInstance(t *testing.T) {
	c, _ := newTestController(t, "sleep 30\n")
	if err := c.Restart(testConfig()); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	firstPID := c.proc.cmd.Process.Pid
	c.mu.Unlock()

	if err := c.Restart(testConfig()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	if pidAlive(firstPID) {
		t.Fatalf("first instance (pid=%d) survived a second Restart", firstPID)
	}
}

func TestControllerStopEscalatesToSIGKILL(t *testing.T) {
	c, _ := newTestController(t, `trap "" TERM; sleep 30`+"\n")
	if err := c.Restart(testConfig()); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	pid := c.proc.cmd.Process.Pid
	c.mu.Unlock()

	start := time.Now()
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("Stop took %v against a SIGTERM-trapping child", took)
	}
	if pidAlive(pid) {
		t.Fatalf("pid %d survived Stop's SIGKILL escalation", pid)
	}
}

func TestControllerStopWithNoProcessIsNoop(t *testing.T) {
	c, _ := newTestController(t, "sleep 30\n")
	if err := c.Stop(); err != nil {
		t.Fatalf("Stop on a never-started controller: %v", err)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
