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

// BuildSRTReadbackArgs — сирі TS-байти в stdout, парсинг контейнера окремо
// (wire/ts). -f data + -map 0: без ремуксу і без автовибору потоків.
func BuildSRTReadbackArgs(readbackURL string) []string {
	return []string{
		"ffmpeg", "-hide_banner", "-loglevel", "warning",
		"-f", "data", "-i", readbackURL,
		"-map", "0", "-c", "copy",
		"-f", "data", "pipe:1",
	}
}
