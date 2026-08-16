package rtmp

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var rtmpURLRe = regexp.MustCompile(`^(rtmps?)://([^/:?]+)(?::(\d+))?/(.+)$`)

type pushTarget struct {
	scheme, host, app, stream string
	port                      int
}

// parsePushURL — порт _parse_rtmp_url: app/stream діляться ОСТАННІМ слешем
// (rpartition), дефолтні порти 1935/443.
func parsePushURL(url string) (pushTarget, error) {
	m := rtmpURLRe.FindStringSubmatch(url)
	if m == nil {
		return pushTarget{}, fmt.Errorf("not an rtmp(s) URL: %q", url)
	}
	scheme, host, portStr, path := m[1], m[2], m[3], m[4]
	var app, stream string
	if slash := strings.LastIndexByte(path, '/'); slash >= 0 {
		app, stream = path[:slash], path[slash+1:]
	} else {
		stream = path
	}
	if app == "" || stream == "" {
		return pushTarget{}, errors.New("URL must be rtmp(s)://host[:port]/app/streamkey")
	}
	port := 1935
	if scheme == "rtmps" {
		port = 443
	}
	if portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err != nil {
			return pushTarget{}, fmt.Errorf("not an rtmp(s) URL: %q", url)
		}
		port = p
	}
	return pushTarget{scheme: scheme, host: host, app: app, stream: stream, port: port}, nil
}
