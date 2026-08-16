package egress

import "strconv"

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

// BuildSRTPushArgs — argv srt-транспорту
// (srt-live-transmit push зі stdin). -chunk 1316 = 7×188 (дефолт 1456 не
// кратний 188); -a no вимикає внутрішній авто-реконнект.
func BuildSRTPushArgs(url string) []string {
	return []string{"srt-live-transmit", "-ll", "warn", "-chunk", "1316", "-a", "no", "file://con", url}
}
