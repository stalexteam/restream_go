package proc

import (
	"io"
	"os/exec"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

var procIsProcessInJob = kernel32.NewProc("IsProcessInJob")

func processInJob(t *testing.T, pid int, job syscall.Handle) bool {
	t.Helper()
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatalf("OpenProcess(%d): %v", pid, err)
	}
	defer syscall.CloseHandle(h)
	var in int32
	r, _, err := procIsProcessInJob.Call(uintptr(h), uintptr(job), uintptr(unsafe.Pointer(&in)))
	if r == 0 {
		t.Fatalf("IsProcessInJob: %v", err)
	}
	return in != 0
}

// Довгоживуча дитина: чекає на stdin, поки її не приберуть.
func spawnChild(t *testing.T) (*exec.Cmd, io.WriteCloser, chan error) {
	t.Helper()
	cmd := exec.Command("cmd", "/c", "pause")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := StartCmd(cmd); err != nil {
		t.Fatalf("StartCmd: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return cmd, stdin, done
}

// Кожен спавн потрапляє в джоб контролера (P1 на Windows).
func TestSpawnJoinsControllerJob(t *testing.T) {
	if controllerJob() == 0 {
		t.Skip("controller job object is unavailable")
	}
	cmd, stdin, done := spawnChild(t)
	defer func() {
		stdin.Close()
		_ = cmd.Process.Kill()
		<-done
	}()
	if !processInJob(t, cmd.Process.Pid, controllerJob()) {
		t.Fatalf("pid %d is not in the controller job", cmd.Process.Pid)
	}
}

// Закриття хендла джоба вбиває дитину.
func TestJobCloseKillsChild(t *testing.T) {
	job, err := newKillOnCloseJob()
	if err != nil {
		t.Skipf("could not create a job object: %v", err)
	}
	cmd := exec.Command("cmd", "/c", "pause")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer stdin.Close()

	if err := assignToJob(job, cmd.Process.Pid); err != nil {
		_ = syscall.CloseHandle(job)
		_ = cmd.Process.Kill()
		<-done
		t.Skipf("AssignProcessToJobObject failed (nested job?): %v", err)
	}
	if !processInJob(t, cmd.Process.Pid, job) {
		t.Fatal("child is not in the freshly created job")
	}

	if err := syscall.CloseHandle(job); err != nil {
		t.Fatalf("CloseHandle(job): %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("child survived closing the job handle")
	}
}
