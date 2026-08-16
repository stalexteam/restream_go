package api

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"restream_go/internal/control"
)

// fakeConn — сокет-заглушка: накопичує байти, `dead` імітує обрив.
type fakeConn struct {
	buf  []byte
	dead bool
}

func (c *fakeConn) Read([]byte) (int, error) { return 0, net.ErrClosed }

func (c *fakeConn) Write(p []byte) (int, error) {
	if c.dead {
		return 0, net.ErrClosed
	}
	c.buf = append(c.buf, p...)
	return len(p), nil
}

func (c *fakeConn) Close() error                       { return nil }
func (c *fakeConn) LocalAddr() net.Addr                { return nil }
func (c *fakeConn) RemoteAddr() net.Addr               { return nil }
func (c *fakeConn) SetDeadline(time.Time) error        { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error    { return nil }
func (c *fakeConn) SetWriteDeadline(t time.Time) error { return nil }

// take — тексти всіх фреймів, накопичених від минулого виклику.
func (c *fakeConn) take(t *testing.T) []string {
	t.Helper()
	data := c.buf
	c.buf = nil
	var out []string
	for len(data) > 0 {
		payload, rest, ok := decodeServerFrame(t, data)
		if !ok {
			t.Fatalf("недочитаний фрейм: % x", data)
		}
		out = append(out, string(payload))
		data = rest
	}
	return out
}

// decodeServerFrame — один текстовий websocket-фрейм сервера (без маски).
func decodeServerFrame(t *testing.T, data []byte) (payload, rest []byte, ok bool) {
	t.Helper()
	if len(data) < 2 {
		return nil, nil, false
	}
	length := int(data[1] & 0x7F)
	offset := 2
	switch length {
	case 126:
		if len(data) < 4 {
			return nil, nil, false
		}
		length = int(data[2])<<8 | int(data[3])
		offset = 4
	case 127:
		if len(data) < 10 {
			return nil, nil, false
		}
		length = 0
		for _, b := range data[2:10] {
			length = length<<8 | int(b)
		}
		offset = 10
	}
	if len(data) < offset+length {
		return nil, nil, false
	}
	return data[offset : offset+length], data[offset+length:], true
}

// fakeSource — Manager-заглушка для хаба.
type fakeSource struct {
	status   []byte
	progress control.FallbackProgress
}

func (f *fakeSource) Status() *control.Dict {
	d, err := control.Loads(f.status)
	if err != nil {
		panic(err)
	}
	return d
}

func (f *fakeSource) FallbackProgress() control.FallbackProgress { return f.progress }

// jsonOf — розбір фрейму в дерево для перевірок.
func jsonOf(t *testing.T, frame string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(frame), &out); err != nil {
		t.Fatalf("bad frame %q: %v", frame, err)
	}
	return out
}
