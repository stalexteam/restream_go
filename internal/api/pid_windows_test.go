//go:build windows

package api

import (
	"os"
	"os/exec"
	"testing"
)

func TestPidAliveLive(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Fatal("pidAlive(self) = false")
	}
	cmd := exec.Command("ping", "-n", "30", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	if !pidAlive(pid) {
		t.Fatalf("pidAlive(%d) = false for running child", pid)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	_ = cmd.Wait()
	if pidAlive(pid) {
		t.Fatalf("pidAlive(%d) = true after kill+wait", pid)
	}
}
