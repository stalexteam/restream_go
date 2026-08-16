package mtx

import (
	"net"
	"os/exec"
	"testing"
	"time"
)

func TestTCPRTTMsLiveListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	url := "rtmp://" + ln.Addr().String() + "/app/key"
	ms, ok := TCPRTTMs(url, time.Second)
	if !ok {
		t.Fatal("TCPRTTMs: ok=false against a live listener")
	}
	if ms < 0 || ms > 1000 {
		t.Errorf("implausible rtt: %dms", ms)
	}
}

func TestTCPRTTMsUnresolvableHost(t *testing.T) {
	//.invalid — гарантовано непридатний TLD (RFC 2606).
	if _, ok := TCPRTTMs("rtmp://this-host-does-not-exist.invalid/app", 500*time.Millisecond); ok {
		t.Error("expected ok=false against an unresolvable host")
	}
}

func TestTCPRTTMsSRTSchemeSkipped(t *testing.T) {
	if _, ok := TCPRTTMs("srt://127.0.0.1:9000?streamid=x", time.Second); ok {
		t.Error("srt:// must not attempt a TCP connect")
	}
}

func TestICMPRTTMsRejectsInjectionHost(t *testing.T) {
	if _, ok := ICMPRTTMs("rtmp://-oQ/tmp/pwned"); ok {
		t.Error("B4: leading-dash host must be rejected before spawning ping")
	}
}

func TestICMPRTTMsLive(t *testing.T) {
	if _, err := exec.LookPath("ping"); err != nil {
		t.Skip("ping not on PATH")
	}
	ms, ok := ICMPRTTMs("rtmp://127.0.0.1/app")
	if !ok {
		t.Skip("ICMP likely blocked/unprivileged in this environment")
	}
	if ms < 0 {
		t.Errorf("implausible rtt: %dms", ms)
	}
}

func TestProbeDispatchesByICMPFlag(t *testing.T) {
	if _, ok := Probe("srt://host:9000", false); ok {
		t.Error("Probe(useICMP=false) on srt:// must be ok=false (TCP path, no probe)")
	}
	if _, ok := Probe("rtmp://-oQ/x", true); ok {
		t.Error("Probe(useICMP=true) with an injection host must be ok=false")
	}
}
