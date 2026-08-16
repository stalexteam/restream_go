//go:build windows

package mtx

import (
	"os/exec"
	"testing"
)

func TestParseICMPAvgSamples(t *testing.T) {
	if v, ok := parseICMPAvg("    Minimum = 0ms, Maximum = 1ms, Average = 15ms"); !ok || v != 15 {
		t.Fatalf("english sample: got %v, %v", v, ok)
	}
	if _, ok := parseICMPAvg("    Минимальное = 0мсек, Максимальное = 1 мсек, Среднее = 15 мсек"); ok {
		t.Fatal("non-english sample parsed unexpectedly")
	}
}

// Чужа локаль ping штатно дає ok=false — тест вимагає лише узгодженості з фактичним виводом.
func TestICMPRTTMsLiveLocalhost(t *testing.T) {
	args := icmpArgs("127.0.0.1")
	raw, err := exec.Command(args[0], args[1:]...).Output()
	if err != nil {
		t.Skipf("ping unavailable: %v", err)
	}
	_, wantOK := parseICMPAvg(string(raw))
	rtt, ok := ICMPRTTMs("rtmp://127.0.0.1/live")
	if ok != wantOK {
		t.Fatalf("ICMPRTTMs ok=%v, parseICMPAvg on raw ping output ok=%v\n%s", ok, wantOK, raw)
	}
	t.Logf("locale diagnostic: ok=%v rtt=%d\n%s", ok, rtt, raw)
}
