package proc

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

type statsGoldenResult struct {
	CPUPercent float64 `json:"cpu_percent"`
	RSSMB      float64 `json:"rss_mb"`
}

type statsGoldenCall struct {
	PID    int                `json:"pid"`
	Now    float64            `json:"now"`
	Stat   *string            `json:"stat"`
	Status *string            `json:"status"`
	Uptime *string            `json:"uptime"`
	Result *statsGoldenResult `json:"result"`
}

type statsGoldenScenario struct {
	Name  string            `json:"name"`
	Calls []statsGoldenCall `json:"calls"`
}

type statsGoldenDoc struct {
	ClkTck    float64               `json:"clk_tck"`
	Scenarios []statsGoldenScenario `json:"scenarios"`
}

func loadStatsGolden(t *testing.T) statsGoldenDoc {
	t.Helper()
	raw, err := os.ReadFile("testdata/stats_golden.json")
	if err != nil {
		t.Skipf("missing testdata/stats_golden.json (run DEV/tools/gen_stats_golden.py): %v", err)
	}
	var doc statsGoldenDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("bad stats_golden.json: %v", err)
	}
	return doc
}

// AT_CLKTCK з auxv мусить збігатися з фактичним USER_HZ системи.
func TestClkTckMatchesSystem(t *testing.T) {
	doc := loadStatsGolden(t)
	if linuxClkTck != doc.ClkTck {
		t.Fatalf("linuxClkTck = %v, oracle = %v", linuxClkTck, doc.ClkTck)
	}
}

// TestSampleAgainstOracle — свіжий readFile на кожен виклик; pid без
// зареєстрованого /proc/pid/stat == "зниклий процес".
func TestSampleAgainstOracle(t *testing.T) {
	doc := loadStatsGolden(t)
	for _, sc := range doc.Scenarios {
		sc := sc
		t.Run(sc.Name, func(t *testing.T) {
			s := &Sampler{prev: make(map[int]prevSample)}
			for i, call := range sc.Calls {
				files := map[string][]byte{}
				if call.Stat != nil {
					files[fmt.Sprintf("/proc/%d/stat", call.PID)] = []byte(*call.Stat)
				}
				if call.Status != nil {
					files[fmt.Sprintf("/proc/%d/status", call.PID)] = []byte(*call.Status)
				}
				if call.Uptime != nil {
					files["/proc/uptime"] = []byte(*call.Uptime)
				}
				s.readFile = func(name string) ([]byte, error) {
					if data, ok := files[name]; ok {
						return data, nil
					}
					return nil, os.ErrNotExist
				}
				now := call.Now
				s.now = func() float64 { return now }

				got, ok := s.Sample(call.PID, true)
				if call.Result == nil {
					if ok {
						t.Fatalf("call %d: got %+v, oracle says None", i, got)
					}
					continue
				}
				if !ok {
					t.Fatalf("call %d: got None, oracle says %+v", i, *call.Result)
				}
				want := Stats{CPUPercent: call.Result.CPUPercent, RSSMB: call.Result.RSSMB}
				if got != want {
					t.Fatalf("call %d: got %+v, want %+v", i, got, want)
				}
			}
		})
	}
}

func TestSampleNoPID(t *testing.T) {
	s := NewSampler()
	if _, ok := s.Sample(0, false); ok {
		t.Fatal("expected no data when havePID=false")
	}
}

// Q49: битий /proc не панікує.
func TestMalformedProcLineIsNoData(t *testing.T) {
	bad := []string{
		"1 (x) S 1 1 1 0 -1 0 0 0 0 0 notanumber 0\n",
		"garbage without paren",
		"1 (x) S 1 1 1\n",
	}
	for _, text := range bad {
		if _, _, ok := parseLinuxStat(text); ok {
			t.Fatalf("parseLinuxStat(%q) accepted malformed input", text)
		}
	}
	good := "1 (x) S 1 1 1 0 -1 0 0 0 0 0 7 3 0 0 20 0 1 0 4242 0 0\n"
	ticks, fields, ok := parseLinuxStat(good)
	if !ok || ticks != 10 || len(fields) < 20 {
		t.Fatalf("parseLinuxStat(good) = %d, %d fields, ok=%v", ticks, len(fields), ok)
	}

	if _, ok := parseVmRSSKB("VmRSS:\n"); ok {
		t.Fatal("parseVmRSSKB accepted a truncated VmRSS line")
	}
	if _, ok := parseVmRSSKB("VmRSS:\tnotanumber kB\n"); ok {
		t.Fatal("parseVmRSSKB accepted a non-numeric VmRSS line")
	}
	if kb, ok := parseVmRSSKB("Name:\tx\nVmRSS:\t2048 kB\n"); !ok || kb != 2048 {
		t.Fatalf("parseVmRSSKB(good) = %d, ok=%v", kb, ok)
	}
	if kb, ok := parseVmRSSKB("Name:\tkthread\n"); !ok || kb != 0 {
		t.Fatalf("parseVmRSSKB(no VmRSS) = %d, ok=%v", kb, ok)
	}
}

// Семплер переживає битий рядок і читає наступний.
func TestSampleSurvivesMalformedStat(t *testing.T) {
	s := &Sampler{prev: make(map[int]prevSample)}
	files := map[string][]byte{
		"/proc/7/stat":   []byte("7 (ffmpeg) S 1 1 1 0 -1 0 0 0 0 0 x y\n"),
		"/proc/7/status": []byte("VmRSS:\t1024 kB\n"),
		"/proc/uptime":   []byte("100.0 50.0\n"),
	}
	s.readFile = func(name string) ([]byte, error) {
		if data, ok := files[name]; ok {
			return data, nil
		}
		return nil, os.ErrNotExist
	}
	s.now = func() float64 { return 1000.0 }
	if _, ok := s.Sample(7, true); ok {
		t.Fatal("expected no data for malformed /proc/pid/stat")
	}

	files["/proc/7/stat"] = []byte("7 (ffmpeg) S 1 1 1 0 -1 0 0 0 0 0 7 3 0 0 20 0 1 0 4242 0 0\n")
	if _, ok := s.Sample(7, true); !ok {
		t.Fatal("expected data once /proc/pid/stat is readable again")
	}
}

// TestSampleLiveProcess — P9 на реальному процесі: перший семпл щойно
// стартованого busy-процесу не 0%, RSS>0, зниклий pid після kill — немає даних.
func TestSampleLiveProcess(t *testing.T) {
	cmd := exec.Command("sh", "-c", "while :; do :; done")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn busy process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	s := NewSampler()
	time.Sleep(150 * time.Millisecond)
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

	time.Sleep(100 * time.Millisecond)
	if stats2, ok := s.Sample(cmd.Process.Pid, true); !ok || stats2.RSSMB <= 0 {
		t.Fatalf("expected consistent second sample, got %+v ok=%v", stats2, ok)
	}

	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if _, ok := s.Sample(cmd.Process.Pid, true); ok {
		t.Fatal("expected no data for dead pid")
	}
}
