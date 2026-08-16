package egress

import "testing"

func TestBuildPushURL(t *testing.T) {
	cases := []struct{ server, key, want string }{
		{"rtmp://ingest.example/app", "sk", "rtmp://ingest.example/app/sk"},
		{"rtmp://ingest.example/app/", "/sk", "rtmp://ingest.example/app/sk"},
		{"  rtmp://ingest.example/app  ", "  sk  ", "rtmp://ingest.example/app/sk"},
		{"rtmp://ingest.example/app/sk", "", "rtmp://ingest.example/app/sk"},
		{"", "sk", ""},
		{"   ", "sk", ""},
		// IVS: rtmps + *.live-video.net ->:443 і /app, ідемпотентно.
		{"rtmps://a.live-video.net", "sk", "rtmps://a.live-video.net:443/app/sk"},
		{"rtmps://a.live-video.net:443/app", "sk", "rtmps://a.live-video.net:443/app/sk"},
		{"rtmps://live-video.net/x", "sk", "rtmps://live-video.net:443/app/x/sk"},
		{"rtmp://a.live-video.net", "sk", "rtmp://a.live-video.net/sk"},
		{"rtmps://other.example/live", "sk", "rtmps://other.example/live/sk"},
	}
	for _, c := range cases {
		if got := BuildPushURL(c.server, c.key); got != c.want {
			t.Errorf("BuildPushURL(%q, %q) = %q, want %q", c.server, c.key, got, c.want)
		}
	}
}

func TestBuildSRTURL(t *testing.T) {
	cases := []struct{ server, streamid, passphrase, want string }{
		{"srt://host:9000", "", "", "srt://host:9000"},
		{"srt://host:9000", "sid", "", "srt://host:9000?streamid=sid"},
		{"srt://host:9000", "", "pw", "srt://host:9000?passphrase=pw"},
		{"srt://host:9000", "sid", "pw", "srt://host:9000?streamid=sid&passphrase=pw"},
		{"srt://host:9000?latency=200", "sid", "", "srt://host:9000?latency=200&streamid=sid"},
		{"  srt://host:9000  ", "  sid  ", "  pw  ", "srt://host:9000?streamid=sid&passphrase=pw"},
		// Значення не URL-енкодяться: площадка звіряє їх посимвольно.
		{"srt://host:9000", "a b&c", "", "srt://host:9000?streamid=a b&c"},
		{"", "sid", "pw", ""},
	}
	for _, c := range cases {
		if got := BuildSRTURL(c.server, c.streamid, c.passphrase); got != c.want {
			t.Errorf("BuildSRTURL(%q, %q, %q) = %q, want %q", c.server, c.streamid, c.passphrase, got, c.want)
		}
	}
}
