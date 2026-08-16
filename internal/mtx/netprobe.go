package mtx

import (
	"context"
	"math"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"restream_go/internal/proc"
)

const (
	defaultRTMPPort  = 1935
	defaultRTMPSPort = 443

	icmpCount       = 3
	icmpDeadlineSec = 3
	icmpProcTimeout = (icmpDeadlineSec + icmpCount + 2) * time.Second

	defaultTCPTimeout = 3 * time.Second
)

// hostRe — лише hostname/IP: жодних argv-injection символів у системний
// ping. Провідний дефіс міг би стати опцією ping.
var hostRe = regexp.MustCompile(`^[A-Za-z0-9_:][A-Za-z0-9._:-]*$`)

// Probe — Prober-сумісна сигнатура (control.Options.Ping): порт вибору
// probe = icmp_rtt_ms if use_icmp else tcp_rtt_ms.
func Probe(url string, useICMP bool) (int, bool) {
	if useICMP {
		return ICMPRTTMs(url)
	}
	return TCPRTTMs(url, defaultTCPTimeout)
}

// RTMPHostPort — hostname(lower-case)+порт ingest-хоста (rtmp_host_port,
// :33). ok=false: немає хоста, або порт невалідний (python тут кинув би
// ValueError — клон не панікує на мережевому вводі, нотатка).
func RTMPHostPort(rawURL string) (host string, port int, ok bool) {
	scheme, netloc := splitSchemeNetloc(rawURL)
	rawHost, rawPort := hostinfo(netloc)
	host, hostOK := pyHostname(rawHost)
	if !hostOK {
		return "", 0, false
	}
	p, hasPort, portOK := pyPort(rawPort)
	if !portOK {
		return "", 0, false
	}
	def := defaultRTMPPort
	if scheme == "rtmps" {
		def = defaultRTMPSPort
	}
	if !hasPort || p == 0 { // python: parts.port or default -- 0 теж falsy
		return host, def, true
	}
	return host, p, true
}

// TCPRTTMs — час TCP-рукостискання до ingest-хоста в мс (порт tcp_rtt_ms,
// :39). SRT — UDP, TCP-конект туди сенсу не має.
func TCPRTTMs(rawURL string, timeout time.Duration) (int, bool) {
	scheme, _ := splitSchemeNetloc(rawURL)
	if scheme == "srt" {
		return 0, false
	}
	host, port, ok := RTMPHostPort(rawURL)
	if !ok || host == "" {
		return 0, false
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return 0, false
	}
	_ = conn.Close()
	return pyRoundMs(time.Since(start)), true
}

// ICMPRTTMs — середній ICMP RTT через системний ping (порт icmp_rtt_ms,
// :59); ok=false на невалідному хості, збої спавну/таймауту/парсингу.
func ICMPRTTMs(rawURL string) (int, bool) {
	host, _, ok := RTMPHostPort(rawURL)
	if !ok || host == "" || !hostRe.MatchString(host) {
		return 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), icmpProcTimeout)
	defer cancel()
	args := icmpArgs(host)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var out strings.Builder
	cmd.Stdout = &out
	if err := proc.StartCmd(cmd); err != nil {
		return 0, false
	}
	if err := cmd.Wait(); err != nil {
		return 0, false
	}
	avg, ok := parseICMPAvg(out.String())
	if !ok {
		return 0, false
	}
	return int(math.RoundToEven(avg)), true
}

func pyRoundMs(d time.Duration) int {
	return int(math.RoundToEven(d.Seconds() * 1000))
}
