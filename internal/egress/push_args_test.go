package egress

import (
	"reflect"
	"testing"
)

// TestBuildFLVPushArgs — argv ffmpeg-адаптера FLV-виходу.
func TestBuildFLVPushArgs(t *testing.T) {
	got := BuildFLVPushArgs("rtmp://x.example.com/app/key")
	want := []string{
		"ffmpeg", "-hide_banner", "-loglevel", "warning",
		"-y",
		"-i", "pipe:0",
		"-c", "copy",
		"-rw_timeout", "15000000",
		"-f", "flv", "rtmp://x.example.com/app/key",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildFLVPushArgs = %#v, want %#v", got, want)
	}
}

// TestBuildSRTPushArgs — argv srt-транспорту
// Platform._build_srt_push_transport_args.
func TestBuildSRTPushArgs(t *testing.T) {
	got := BuildSRTPushArgs("srt://x.example.com:9999?streamid=publish:foo")
	want := []string{
		"srt-live-transmit", "-ll", "warn", "-chunk", "1316", "-a", "no",
		"file://con", "srt://x.example.com:9999?streamid=publish:foo",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildSRTPushArgs = %#v, want %#v", got, want)
	}
}
