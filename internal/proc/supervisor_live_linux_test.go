package proc

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func readLog(t *testing.T, dir, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "ffmpeg-"+name+".log"))
	if err != nil {
		return ""
	}
	return string(raw)
}

// Живий смоук: справжній процес під супервізором.
func TestLiveSleepProcess(t *testing.T) {
	dir := t.TempDir()
	started := make(chan int, 4)
	s := NewSupervisor("sleeper", func() []string { return []string{"sleep", "30"} }, dir, Options{
		OnStart: func(st *Started) { started <- st.PID },
	})
	s.Start()

	var pid int
	select {
	case pid = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("process never started")
	}
	if !s.IsRunning() {
		t.Fatal("IsRunning is false right after on_start")
	}
	if got, ok := s.PID(); !ok || got != pid {
		t.Fatalf("PID = %d, %v; on_start reported %d", got, ok, pid)
	}
	if !pidAlive(pid) {
		t.Fatalf("pid %d is not alive", pid)
	}
	time.Sleep(30 * time.Millisecond)
	if s.UptimeSec() <= 0 {
		t.Fatal("uptime of a live process is 0")
	}
	if s.EverRanLong() {
		t.Fatal("ever_ran_long is set before the ever-succeeded threshold")
	}
	if s.RestartCount() != 0 {
		t.Fatalf("restart count %d on the first run", s.RestartCount())
	}

	s.Stop(2 * time.Second)
	if s.IsRunning() {
		t.Fatal("IsRunning after Stop")
	}
	waitFor(t, "child to disappear", func() bool { return !pidAlive(pid) })
}

// stderr дитини реально йде у ffmpeg-<name>.log (append між запусками).
func TestLiveStderrGoesToLogFile(t *testing.T) {
	dir := t.TempDir()
	runs := make(chan struct{}, 8)
	s := NewSupervisor("logger", func() []string {
		return []string{"sh", "-c", "echo to-stdout; echo to-stderr >&2; sleep 30"}
	}, dir, Options{OnStart: func(*Started) { runs <- struct{}{} }})
	s.Start()
	<-runs
	waitFor(t, "log to be written", func() bool {
		return strings.Contains(readLog(t, dir, "logger"), "to-stderr")
	})
	body := readLog(t, dir, "logger")
	if !strings.Contains(body, "to-stdout") {
		t.Fatalf("stdout is missing from the log: %q", body)
	}
	s.Stop(2 * time.Second)
}

// capture_stdout: stdout іде в пайп хука, stderr — у лог.
func TestLiveCaptureStdout(t *testing.T) {
	dir := t.TempDir()
	got := make(chan string, 4)
	s := NewSupervisor("capture", func() []string {
		return []string{"sh", "-c", "printf payload; echo noise >&2; sleep 30"}
	}, dir, Options{
		CaptureStdout: true,
		OnStart: func(st *Started) {
			go func() {
				buf := make([]byte, 7)
				n, _ := io.ReadFull(st.Stdout, buf)
				got <- string(buf[:n])
			}()
		},
	})
	s.Start()
	select {
	case body := <-got:
		if body != "payload" {
			t.Fatalf("stdout = %q", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing arrived on the captured stdout")
	}
	waitFor(t, "stderr in the log", func() bool {
		return strings.Contains(readLog(t, dir, "capture"), "noise")
	})
	if strings.Contains(readLog(t, dir, "capture"), "payload") {
		t.Fatal("captured stdout leaked into the log file")
	}
	s.Stop(2 * time.Second)
}

// stdin_pipe: Stop закриває stdin, дитина виходить сама, без terminate/kill.
func TestLiveStdinPipeStopClosesStdin(t *testing.T) {
	dir := t.TempDir()
	started := make(chan int, 4)
	var mu sync.Mutex
	var stdin io.WriteCloser
	s := NewSupervisor("stdin", func() []string {
		return []string{"sh", "-c", "cat > /dev/null; sleep 0.05"}
	}, dir, Options{
		StdinPipe: true,
		OnStart: func(st *Started) {
			mu.Lock()
			stdin = st.Stdin
			mu.Unlock()
			started <- st.PID
		},
	})
	s.Start()
	pid := <-started
	mu.Lock()
	w := stdin
	mu.Unlock()
	if w == nil {
		t.Fatal("stdin pipe was not handed to the hook")
	}
	if _, err := w.Write([]byte("bytes\n")); err != nil {
		t.Fatalf("write to the child stdin: %v", err)
	}

	start := time.Now()
	s.Stop(3 * time.Second)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("Stop took %v — the child did not exit on stdin EOF", took)
	}
	waitFor(t, "child to disappear", func() bool { return !pidAlive(pid) })
}

// Уперта дитина (ігнорує SIGTERM) добивається послідовністю terminate/kill.
func TestLiveKillSequenceForStubbornChild(t *testing.T) {
	dir := t.TempDir()
	started := make(chan int, 4)
	s := NewSupervisor("stubborn", func() []string {
		return []string{"sh", "-c", `trap "" TERM; sleep 30`}
	}, dir, Options{OnStart: func(st *Started) { started <- st.PID }})
	s.killWait = 200 * time.Millisecond
	s.reapWait = 2 * time.Second
	s.Start()
	pid := <-started
	waitFor(t, "trap to be installed", func() bool { return pidAlive(pid) })
	time.Sleep(100 * time.Millisecond)

	s.Stop(200 * time.Millisecond)
	if pidAlive(pid) {
		t.Fatalf("pid %d survived Stop", pid)
	}
}

// Помічник для TestChildDiesWithParent: спавнить довгу дитину і зависає.
func TestPdeathsigHelper(t *testing.T) {
	if os.Getenv("PROC_PDEATHSIG_HELPER") != "1" {
		t.Skip("runs only as a child of TestChildDiesWithParent")
	}
	cmd := exec.Command("sleep", "300")
	if err := StartCmd(cmd); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("GRANDCHILD %d\n", cmd.Process.Pid)
	os.Stdout.Sync()
	time.Sleep(60 * time.Second)
}

// P1 наживо: SIGKILL батька забирає дитину, спавнену через StartCmd.
func TestChildDiesWithParent(t *testing.T) {
	helper := exec.Command(os.Args[0], "-test.run=TestPdeathsigHelper", "-test.v")
	helper.Env = append(os.Environ(), "PROC_PDEATHSIG_HELPER=1")
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = helper.Process.Kill()
		_ = helper.Wait()
	}()

	grandchild := 0
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if _, err := fmt.Sscanf(scanner.Text(), "GRANDCHILD %d", &grandchild); err == nil {
			break
		}
	}
	if grandchild == 0 {
		t.Fatal("the helper never reported its child pid")
	}
	if !pidAlive(grandchild) {
		t.Fatalf("grandchild %d is not alive", grandchild)
	}

	if err := helper.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()
	waitFor(t, "grandchild to die with its parent", func() bool { return !pidAlive(grandchild) })
}

// Супервізор перезапускає процес, поки його хочуть.
func TestLiveRestartsUntilStopped(t *testing.T) {
	dir := t.TempDir()
	s := NewSupervisor("restarter", func() []string { return []string{"true"} }, dir, Options{})
	s.restartBackoff = 10 * time.Millisecond
	s.Start()
	waitFor(t, "three restarts", func() bool { return s.RestartCount() >= 3 })
	s.Stop(time.Second)
	settled := s.RestartCount()
	time.Sleep(100 * time.Millisecond)
	if s.RestartCount() != settled {
		t.Fatalf("restart count moved after Stop: %d -> %d", settled, s.RestartCount())
	}
}
