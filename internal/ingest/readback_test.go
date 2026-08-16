package ingest

import "testing"

func TestUseSRTReadback(t *testing.T) {
	cases := []struct {
		audioTracks int
		eb          bool
		want        bool
	}{
		{1, false, false},
		{0, false, false},
		{2, false, true},
		{1, true, true},
		{3, true, true},
	}
	for _, c := range cases {
		if got := UseSRTReadback(c.audioTracks, c.eb); got != c.want {
			t.Errorf("UseSRTReadback(%d,%v) = %v, want %v", c.audioTracks, c.eb, got, c.want)
		}
	}
}

// TestReadbackURL перевіряє argv.
func TestReadbackURL(t *testing.T) {
	cases := []struct {
		name                 string
		path                 string
		audioTracks          int
		enhancedBroadcasting bool
		srtPort, rtmpPort    int
		user, pass           string
		want                 string
	}{
		{
			name: "single-track rtmp",
			path: "live/foo", audioTracks: 1, enhancedBroadcasting: false,
			srtPort: 8890, rtmpPort: 1935, user: "u", pass: "p",
			want: "rtmp://127.0.0.1:1935/live/foo?user=u&pass=p",
		},
		{
			name: "multitrack srt",
			path: "live/bar", audioTracks: 3, enhancedBroadcasting: false,
			srtPort: 8890, rtmpPort: 1935, user: "u", pass: "p",
			want: "srt://127.0.0.1:8890?streamid=read:live/bar:u:p",
		},
		{
			name: "eb srt even at audio_tracks==1",
			path: "live/eb_abc123", audioTracks: 1, enhancedBroadcasting: true,
			srtPort: 8890, rtmpPort: 1935, user: "u", pass: "p",
			want: "srt://127.0.0.1:8890?streamid=read:live/eb_abc123:u:p",
		},
		{
			name: "empty credentials still concatenated",
			path: "live/baz", audioTracks: 1, enhancedBroadcasting: false,
			srtPort: 8890, rtmpPort: 1935, user: "", pass: "",
			want: "rtmp://127.0.0.1:1935/live/baz?user=&pass=",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ReadbackURL(c.path, c.audioTracks, c.enhancedBroadcasting, c.srtPort, c.rtmpPort, c.user, c.pass)
			if got != c.want {
				t.Errorf("ReadbackURL = %q, want %q", got, c.want)
			}
		})
	}
}
