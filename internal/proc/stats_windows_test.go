package proc

import (
	"os/exec"
	"testing"
	"time"
)

// TestSampleLiveProcess — P9 на реальному Windows-процесі; компілюється й
// виконується лише на Windows.
func TestSampleLiveProcess(t *testing.T) {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", "while($true){}")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn busy process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	s := NewSampler()
	time.Sleep(300 * time.Millisecond)
	stats, ok := s.Sample(cmd.Process.Pid, true)
	if !ok {
		t.Fatal("expected data for live process")
	}
	if stats.CPUPercent <= 0 {
		t.Fatalf("P9: first sample of busy process must be > 0%%, got %v", stats.CPUPercent)
	}
	if stats.RSSMB <= 0 {
		t.Fatalf("expected RSS > 0, got %v", stats.RSSMB)
	}

	time.Sleep(200 * time.Millisecond)
	if stats2, ok := s.Sample(cmd.Process.Pid, true); !ok || stats2.RSSMB <= 0 {
		t.Fatalf("expected consistent second sample, got %+v ok=%v", stats2, ok)
	}

	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if _, ok := s.Sample(cmd.Process.Pid, true); ok {
		t.Fatal("expected no data for dead pid")
	}
}
