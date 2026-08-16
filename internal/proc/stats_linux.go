package proc

import (
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// atClkTck — тип запису AT_CLKTCK у auxv (те саме, що дає sysconf(SC_CLK_TCK)).
const atClkTck = 17

// linuxClkTck — тіки/сек, обчислюється раз при старті процесу.
// USER_HZ=100 — фолбек, якщо auxv
// недоступний (той самий дефолт, що в glibc на відсутньому записі).
var linuxClkTck = detectClkTck()

func detectClkTck() float64 {
	data, err := os.ReadFile("/proc/self/auxv")
	if err == nil {
		for i := 0; i+16 <= len(data); i += 16 {
			typ := binary.LittleEndian.Uint64(data[i : i+8])
			if typ == 0 { // AT_NULL — кінець вектора
				break
			}
			if typ == atClkTck {
				return float64(binary.LittleEndian.Uint64(data[i+8 : i+16]))
			}
		}
	}
	return 100
}

func (s *Sampler) readRaw(pid int) (rawSample, bool) {
	statText, err := s.readFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return rawSample{}, false
	}
	statusText, err := s.readFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return rawSample{}, false
	}

	cpuTicks, fields, ok := parseLinuxStat(string(statText))
	if !ok {
		return rawSample{}, false
	}
	rssKB, ok := parseVmRSSKB(string(statusText))
	if !ok {
		return rawSample{}, false
	}

	return rawSample{
		cpuUnits:    cpuTicks,
		unitsPerSec: linuxClkTck,
		rssRaw:      rssKB,
		rssDivisor:  1024,
		age: func() (float64, bool) {
			startTicks, ok := startTicksFromFields(fields)
			if !ok {
				return 0, false
			}
			uptimeText, err := s.readFile("/proc/uptime")
			if err != nil {
				return 0, false
			}
			uptime, ok := parseUptime(string(uptimeText))
			if !ok {
				return 0, false
			}
			return uptime - float64(startTicks)/linuxClkTck, true
		},
	}, true
}

// parseLinuxStat — ділимо по ОСТАННЬОМУ ")" (comm може містити пробіли/дужки):
// utime — fields[11], stime — fields[12]. Битий рядок — ok=false: один
// нечитний семпл не сміє вбивати контролер.
func parseLinuxStat(statText string) (cpuTicks int64, fields []string, ok bool) {
	idx := strings.LastIndex(statText, ")")
	if idx < 0 {
		return 0, nil, false
	}
	fields = strings.Fields(statText[idx+1:])
	if len(fields) <= 12 {
		return 0, nil, false
	}
	utime, errU := strconv.ParseInt(fields[11], 10, 64)
	stime, errS := strconv.ParseInt(fields[12], 10, 64)
	if errU != nil || errS != nil {
		return 0, nil, false
	}
	return utime + stime, fields, true
}

// startTicksFromFields — поле 22 (starttime), fields[19]; помилка тут
// гаситься мовчки: без віку процесу семпл усе одно валідний.
func startTicksFromFields(fields []string) (int64, bool) {
	if len(fields) <= 19 {
		return 0, false
	}
	v, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// parseVmRSSKB — VmRSS у КБ; рядка немає (ядерний потік) — 0, битий — ok=false.
func parseVmRSSKB(statusText string) (int64, bool) {
	for _, line := range strings.Split(statusText, "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			f := strings.Fields(line)
			if len(f) < 2 {
				return 0, false
			}
			v, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil {
				return 0, false
			}
			return v, true
		}
	}
	return 0, true
}

func parseUptime(uptimeText string) (float64, bool) {
	fields := strings.Fields(uptimeText)
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
