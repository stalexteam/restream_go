package mtx

import (
	"regexp"
	"strconv"
)

// icmpArgs — Windows ping.exe: -n count, -w timeout-мс (не -c/сек, нотатка).
func icmpArgs(host string) []string {
	return []string{"ping", "-n", strconv.Itoa(icmpCount), "-w", strconv.Itoa(icmpDeadlineSec * 1000), host}
}

// English-locale ping.exe: "Average = 15ms" (нотатка: інша локаль не
// покрита).
var icmpAvgRe = regexp.MustCompile(`Average\s*=\s*(\d+)\s*ms`)

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
