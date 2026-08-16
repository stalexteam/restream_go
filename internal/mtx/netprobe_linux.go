package mtx

import (
	"regexp"
	"strconv"
)

// icmpArgs — iputils/busybox: -n без reverse-DNS, -c count, -w deadline-сек.
func icmpArgs(host string) []string {
	return []string{"ping", "-n", "-c", strconv.Itoa(icmpCount), "-w", strconv.Itoa(icmpDeadlineSec), host}
}

// avg — другий елемент "min/avg/max[/mdev]" (iputils і busybox обидва).
var icmpAvgRe = regexp.MustCompile(`=\s*[\d.]+/([\d.]+)/`)

func parseICMPAvg(output string) (float64, bool) {
	m := icmpAvgRe.FindStringSubmatch(output)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
