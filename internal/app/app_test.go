// Unix-only: фікстура mediamtx — sh-скрипт, зупинка — SIGTERM.
//go:build !windows

package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"restream_go/internal/control"
)

const smokeToken = "tttttttttttttttttttt"

// obsSentinel — вміст obs-dock.html, який штатний старт не сміє перезаписати.
const obsSentinel = "sentinel obs-dock\n"

// TestControllerSmoke — живий процес: підняття HTTP, дочірній MediaMTX,
// зупинка по SIGTERM, формат логу.
func TestControllerSmoke(t *testing.T) {
	base := scratchBase(t)
	port := freePort(t)
	mtxPIDFile := filepath.Join(base, "tmp", "fake-mediamtx.pid")
	buildTree(t, base, port, mtxPIDFile)
	binary := buildBinary(t, base)

	stdio, err := os.Create(filepath.Join(base, "stdio.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer stdio.Close()
	cmd := exec.Command(binary)
	cmd.Dir = base
	cmd.Stdout, cmd.Stderr = stdio, stdio
	if err := cmd.Start(); err != nil {
		t.Fatalf("start controller: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	client := &http.Client{Timeout: 2 * time.Second}
	var statusBody []byte
	waitFor(t, base, "GET /status", 30*time.Second, func() bool {
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/status?token=%s", port, smokeToken))
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		statusBody, _ = io.ReadAll(resp.Body)
		return true
	})
	var status map[string]any
	if err := json.Unmarshal(statusBody, &status); err != nil {
		t.Fatalf("/status is not JSON: %v (%q)", err, statusBody)
	}
	if _, ok := status["platforms"]; !ok {
		t.Errorf("/status has no 'platforms' key: %q", statusBody)
	}

	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/dashboard?token=%s", port, smokeToken))
	if err != nil {
		t.Fatalf("GET /dashboard: %v", err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// Дашборд — з вшитої FS.
	if resp.StatusCode != http.StatusOK || !bytes.Contains(page, []byte("<title>restream-controller</title>")) {
		t.Errorf("GET /dashboard: status=%d body=%q", resp.StatusCode, page)
	}
	// obs-файли пише лише --config: сентинел цілий, другого файлу немає.
	if got := readFile(t, filepath.Join(base, "obs-dock.html")); got != obsSentinel {
		t.Errorf("startup rewrote obs-dock.html: %q", got)
	}
	if _, err := os.Stat(filepath.Join(base, "obs-source.html")); !os.IsNotExist(err) {
		t.Errorf("startup generated obs-source.html (stat err = %v)", err)
	}

	var mtxPID int
	waitFor(t, base, "mediamtx child", 15*time.Second, func() bool {
		raw, err := os.ReadFile(mtxPIDFile)
		if err != nil {
			return false
		}
		mtxPID, err = strconv.Atoi(strings.TrimSpace(string(raw)))
		return err == nil && mtxPID > 0
	})
	if !pidAlive(mtxPID) {
		t.Fatalf("mediamtx child (pid=%d) is not alive", mtxPID)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(base, "tmp", ".mediamtx.pid"))); got != strconv.Itoa(mtxPID) {
		t.Errorf(".mediamtx.pid = %q, want %d", got, mtxPID)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("controller exited with %v\n%s", err, dumpLogs(base))
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("controller did not exit after SIGTERM\n%s", dumpLogs(base))
	}
	waitFor(t, base, "mediamtx child to die", 10*time.Second, func() bool { return !pidAlive(mtxPID) })

	logText := readFile(t, filepath.Join(base, "logs", "controller.log"))
	startedRe := regexp.MustCompile(
		`(?m)^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2},\d{3} \[INFO\] controller started on 127\.0\.0\.1:` +
			strconv.Itoa(port) + `$`)
	if !startedRe.MatchString(logText) {
		t.Errorf("no python-formatted 'controller started on' line in the log:\n%s", logText)
	}
	if !strings.Contains(logText, "received termination signal (15), stopping ffmpeg processes") {
		t.Errorf("no signal line in the log:\n%s", logText)
	}
}

// buildTree — макет інсталяції: конфіг і фейковий бінар mediamtx, що лишає
// свій pid у файлі.
func buildTree(t *testing.T, base string, port int, mtxPIDFile string) {
	t.Helper()
	for _, dir := range []string{"media", "logs", "bin"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	config := control.D(
		"public_host", "127.0.0.1",
		"listen_host", "127.0.0.1",
		"listen_port", int64(port),
		"dashboard_token", smokeToken,
		"obs_pass", "p",
		"internal_user", "i",
		"internal_pass", "q",
		"offline_timeout_sec", int64(60),
		"connect_timeout_ms", int64(2500),
		"read_timeout_ms", int64(300),
		"icmp_ping", false,
		"sources", []any{},
		"platforms", []any{},
		"platform_groups", []any{},
		"fallback_presets", []any{},
	)
	if err := control.Persist(filepath.Join(base, "config.json"), config); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(base, "bin", "mediamtx"),
		"#!/bin/sh\necho $$ > "+mtxPIDFile+"\nexec sleep 60\n", 0o755)
	writeFile(t, filepath.Join(base, "obs-dock.html"), obsSentinel, 0o600)
}

func buildBinary(t *testing.T, base string) string {
	t.Helper()
	out := filepath.Join(base, "bin", "restreamd")
	build := exec.Command(goTool(t), "build", "-o", out, "restream_go")
	var stderr bytes.Buffer
	build.Stderr = &stderr
	if err := build.Run(); err != nil {
		t.Fatalf("go build: %v\n%s", err, stderr.String())
	}
	return out
}

func goTool(t *testing.T) string {
	t.Helper()
	if path, err := exec.LookPath("go"); err == nil {
		return path
	}
	candidate := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	t.Skip("go tool not found")
	return ""
}

// scratchBase — TempDir без символьних лінків: бінар знаходить base через
// EvalSymlinks, тест мусить дивитись на ті самі шляхи.
func scratchBase(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return base
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func waitFor(t *testing.T, base, what string, timeout time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if ready() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s\n%s", what, dumpLogs(base))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func pidAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func dumpLogs(base string) string {
	var b strings.Builder
	for _, name := range []string{"stdio.log", filepath.Join("logs", "controller.log"), filepath.Join("logs", "mediamtx.log")} {
		raw, err := os.ReadFile(filepath.Join(base, name))
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "--- %s ---\n%s\n", name, raw)
	}
	return b.String()
}
