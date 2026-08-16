package proc

import "regexp"

var unsafeNameRe = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// SafeName — ім'я процесу, придатне для імені лог-файлу ffmpeg-<name>.log
func SafeName(name string) string {
	out := unsafeNameRe.ReplaceAllString(name, "_")
	if out == "" {
		return "unnamed"
	}
	return out
}
