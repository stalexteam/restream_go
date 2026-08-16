package ingest

import "strconv"

// BuildRTMPReadbackArgs — порт Platform._build_relay_args.
func BuildRTMPReadbackArgs(readbackURL string, audioIdx int) []string {
	return []string{
		"ffmpeg", "-hide_banner", "-loglevel", "warning",
		"-i", readbackURL,
		"-map", "0:v:0", "-map", "0:a:" + strconv.Itoa(audioIdx),
		"-c", "copy",
		"-f", "flv", "pipe:1",
	}
}

// BuildSRTReadbackArgs — порт Platform._build_srt_transport_args: сирі
// TS-байти в stdout, парсинг контейнера — окремо (wire/ts).
func BuildSRTReadbackArgs(readbackURL string) []string {
	return []string{"srt-live-transmit", "-ll", "warn", readbackURL, "file://con"}
}
