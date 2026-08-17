package probe

import (
	"math"
	"reflect"
	"testing"
)

// TestBuildStreamParamsArgs перевіряє argv.
func TestBuildStreamParamsArgs(t *testing.T) {
	got := BuildStreamParamsArgs("srt://127.0.0.1:8890?streamid=read:live/foo:u:p")
	want := []string{
		"ffprobe", "-v", "error",
		"-analyzeduration", "2500000", "-probesize", "3000000",
		"-show_entries", "stream=codec_type,codec_name,width,height,r_frame_rate,channels,sample_rate",
		"-of", "json", "srt://127.0.0.1:8890?streamid=read:live/foo:u:p",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildStreamParamsArgs = %#v, want %#v", got, want)
	}
}

// TestBuildMediaTracksArgs перевіряє argv (без live-капів).
func TestBuildMediaTracksArgs(t *testing.T) {
	got := BuildMediaTracksArgs("/backup/clip.mp4")
	want := []string{"ffprobe", "-v", "error", "-show_entries", "stream=codec_type", "-of", "json", "/backup/clip.mp4"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildMediaTracksArgs = %#v, want %#v", got, want)
	}
}

// TestBuildDurationArgs перевіряє argv.
func TestBuildDurationArgs(t *testing.T) {
	got := BuildDurationArgs("/backup/clip.mp4")
	want := []string{"ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", "/backup/clip.mp4"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildDurationArgs = %#v, want %#v", got, want)
	}
}

// TestBuildTrackCountsArgs перевіряє argv (з live-капами).
func TestBuildTrackCountsArgs(t *testing.T) {
	got := BuildTrackCountsArgs("srt://127.0.0.1:8890?streamid=read:live/foo:u:p")
	want := []string{
		"ffprobe", "-v", "error",
		"-analyzeduration", "2500000", "-probesize", "3000000",
		"-show_entries", "stream=codec_type", "-of", "json", "srt://127.0.0.1:8890?streamid=read:live/foo:u:p",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildTrackCountsArgs = %#v, want %#v", got, want)
	}
}

// TestBuildManifestTransportArgs перевіряє argv.
func TestBuildManifestTransportArgs(t *testing.T) {
	got := BuildManifestTransportArgs("srt://127.0.0.1:8890?streamid=read:live/foo:u:p")
	want := []string{
		"ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "data", "-i", "srt://127.0.0.1:8890?streamid=read:live/foo:u:p",
		"-map", "0", "-c", "copy",
		"-f", "data", "pipe:1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildManifestTransportArgs = %#v, want %#v", got, want)
	}
}

func TestComputeFPS(t *testing.T) {
	cases := []struct {
		rate string
		want int
	}{
		{"30/1", 30},
		{"30000/1001", 30}, // 29.97 -> округлення до 30
		{"25/1", 25},
		{"0/0", 0}, // den парситься як 0 -> коротке замикання, num не парситься
		{"50/2", 25},
		{"1/0", 0}, // den=0
		{"25", 0},  // немає "/": den="" -> 0
	}
	for _, c := range cases {
		got, err := computeFPS(c.rate)
		if err != nil {
			t.Fatalf("computeFPS(%q): %v", c.rate, err)
		}
		if got != c.want {
			t.Errorf("computeFPS(%q) = %d, want %d", c.rate, got, c.want)
		}
	}
}

// TestComputeFPSBankersRounding: round Python -- half-to-even, не half-away.
func TestComputeFPSBankersRounding(t *testing.T) {
	// 2.5 -> 2 (парне), 3.5 -> 4 (парне): math.RoundToEven -- та сама семантика,
	// що python round для.5 меж.
	got, err := computeFPS("5/2")
	if err != nil {
		t.Fatal(err)
	}
	if want := int(math.RoundToEven(2.5)); got != want {
		t.Errorf("computeFPS(5/2) = %d, want %d (banker's rounding of 2.5)", got, want)
	}
	got, err = computeFPS("7/2")
	if err != nil {
		t.Fatal(err)
	}
	if want := int(math.RoundToEven(3.5)); got != want {
		t.Errorf("computeFPS(7/2) = %d, want %d (banker's rounding of 3.5)", got, want)
	}
}
