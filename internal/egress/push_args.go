package egress

import (
	"strconv"
	"strings"
)

// OutboundRWTimeoutUsec — ffmpeg -rw_timeout виходів, мкс: сокет живий,
// дані не йдуть.
const OutboundRWTimeoutUsec = 15_000_000

// BuildFLVPushArgs — ffmpeg-адаптер: FLV зі
// stdin у push-URL).
func BuildFLVPushArgs(url string) []string {
	return []string{
		"ffmpeg", "-hide_banner", "-loglevel", "warning",
		"-y",
		"-i", "pipe:0",
		"-c", "copy",
		"-rw_timeout", strconv.Itoa(OutboundRWTimeoutUsec),
		"-f", "flv", url,
	}
}

// SRTPayloadSize — 7×188; дефолтні 1456 не кратні розміру TS-пакета.
const SRTPayloadSize = 1316

// SRTLingerSec — дочікування зливу буфера на закритті сокета.
const SRTLingerSec = 2

// BuildSRTPushArgs — argv srt-транспорту (push зі stdin). -f data возить
// байти як є: TS ми змуксували самі, ремукс зіпсував би PID/PCR/CC.
// -map 0 обовʼязковий — автовибір потоків data не бере.
func BuildSRTPushArgs(url string) []string {
	return []string{
		"ffmpeg", "-hide_banner", "-loglevel", "warning",
		"-f", "data", "-i", "pipe:0",
		"-map", "0", "-c", "copy",
		"-f", "data", WithSRTOptions(url),
	}
}

// WithSRTOptions додає в query транспортні опції push-сокета.
func WithSRTOptions(url string) string {
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	return url + sep + "pkt_size=" + strconv.Itoa(SRTPayloadSize) +
		"&linger=" + strconv.Itoa(SRTLingerSec)
}
