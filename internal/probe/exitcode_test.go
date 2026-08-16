//go:build !windows

package probe

import (
	"os"
	"path/filepath"
	"testing"
)

// Вивід упалого ffprobe не йде в розбір.
func TestStreamParamsChecksExitCode(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "ffprobe")
	body := `{"streams":[` +
		`{"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"r_frame_rate":"30/1"},` +
		`{"codec_type":"audio","codec_name":"aac","channels":2,"sample_rate":"48000"}]}`
	write := func(exitCode int) {
		t.Helper()
		script := "#!/bin/sh\necho '" + body + "'\nexit " + string(rune('0'+exitCode)) + "\n"
		if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	write(1)
	if params, ok := ProbeStreamParams("stream.flv", 0, 0); ok {
		t.Fatalf("params %+v accepted from a failed ffprobe", params)
	}

	write(0)
	params, ok := ProbeStreamParams("stream.flv", 0, 0)
	if !ok || params.Width != 1920 || params.AudioCodec != "aac" {
		t.Fatalf("params %+v ok=%v, want the parsed values on a clean exit", params, ok)
	}
}
