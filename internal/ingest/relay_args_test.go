package ingest

import (
	"reflect"
	"testing"
)

// TestBuildRTMPReadbackArgs перевіряє argv.
func TestBuildRTMPReadbackArgs(t *testing.T) {
	got := BuildRTMPReadbackArgs("rtmp://127.0.0.1:1935/live/foo?user=u&pass=p", 2)
	want := []string{
		"ffmpeg", "-hide_banner", "-loglevel", "warning",
		"-i", "rtmp://127.0.0.1:1935/live/foo?user=u&pass=p",
		"-map", "0:v:0", "-map", "0:a:2",
		"-c", "copy",
		"-f", "flv", "pipe:1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildRTMPReadbackArgs = %#v, want %#v", got, want)
	}
}

// TestBuildSRTReadbackArgs перевіряє argv.
func TestBuildSRTReadbackArgs(t *testing.T) {
	got := BuildSRTReadbackArgs("srt://127.0.0.1:8890?streamid=read:live/bar:u:p")
	want := []string{
		"ffmpeg", "-hide_banner", "-loglevel", "warning",
		"-f", "data", "-i", "srt://127.0.0.1:8890?streamid=read:live/bar:u:p",
		"-map", "0", "-c", "copy",
		"-f", "data", "pipe:1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildSRTReadbackArgs = %#v, want %#v", got, want)
	}
}
