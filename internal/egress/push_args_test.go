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

// TestBuildSRTPushArgs — argv srt-транспорту.
func TestBuildSRTPushArgs(t *testing.T) {
	got := BuildSRTPushArgs("srt://x.example.com:9999?streamid=publish:foo")
	want := []string{
		"ffmpeg", "-hide_banner", "-loglevel", "warning",
		"-f", "data", "-i", "pipe:0",
		"-map", "0", "-c", "copy",
		"-f", "data", "srt://x.example.com:9999?streamid=publish:foo&pkt_size=1316&linger=2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildSRTPushArgs = %#v, want %#v", got, want)
	}
}

// TestWithSRTOptions — роздільник залежить від наявного query.
func TestWithSRTOptions(t *testing.T) {
	if got := WithSRTOptions("srt://h:1/"); got != "srt://h:1/?pkt_size=1316&linger=2" {
		t.Errorf("no query: %q", got)
	}
	if got := WithSRTOptions("srt://h:1/?a=b"); got != "srt://h:1/?a=b&pkt_size=1316&linger=2" {
		t.Errorf("with query: %q", got)
	}
}
