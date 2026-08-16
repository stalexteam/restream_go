package mtx

import (
	"bytes"
	"io"
	"os"
	"regexp"
	"time"
)

var (
	publishingRe   = regexp.MustCompile(`\[conn ([\d.]+:\d+)\] is publishing to path`)
	timeoutCloseRe = regexp.MustCompile(`\[conn ([\d.]+:\d+)\] closed: read tcp .*: i/o timeout`)
	closedRe       = regexp.MustCompile(`\[conn ([\d.]+:\d+)\] closed:`)
)

const logPollInterval = 500 * time.Millisecond

// Watch стежить за mediamtx.log: readTimeout-закриття з'єднання, що ще НЕ
// дійшло до "is publishing", шле onConnectTimeout.
// Блокує до закриття stop; caller запускає в own goroutine.
func Watch(logPath string, onConnectTimeout func(), stop <-chan struct{}) {
	watchTail(logPath, onConnectTimeout, stop, logPollInterval)
}

func watchTail(logPath string, onConnectTimeout func(), stop <-chan struct{}, interval time.Duration) {
	f := openWhenExists(logPath, stop, interval)
	if f == nil {
		return
	}
	_, _ = f.Seek(0, io.SeekEnd)
	tail := &logTail{f: f}
	defer func() { _ = tail.f.Close() }()

	publishing := map[string]bool{}
	for {
		select {
		case <-stop:
			return
		default:
		}

		line := tail.readLine()
		if line == "" {
			select {
			case <-stop:
				return
			case <-time.After(interval):
			}
			if isRotated(logPath, tail) {
				_ = tail.f.Close()
				nf := openWhenExists(logPath, stop, interval)
				if nf == nil {
					return
				}
				tail = &logTail{f: nf}
				publishing = map[string]bool{}
			}
			continue
		}

		if m := publishingRe.FindStringSubmatch(line); m != nil {
			publishing[m[1]] = true
			continue
		}
		if m := timeoutCloseRe.FindStringSubmatch(line); m != nil {
			conn := m[1]
			if !publishing[conn] {
				onConnectTimeout()
			}
			delete(publishing, conn)
			continue
		}
		if m := closedRe.FindStringSubmatch(line); m != nil {
			delete(publishing, m[1])
		}
	}
}

// isRotated — truncate/rename-safe: нова inode (os.SameFile,
// крос-платформно) або розмір менший за прочитану позицію.
func isRotated(logPath string, tail *logTail) bool {
	st, err := os.Stat(logPath)
	if err != nil {
		return false
	}
	cur, err := tail.f.Stat()
	if err != nil {
		return false
	}
	return !os.SameFile(st, cur) || st.Size() < tail.pos
}

func openWhenExists(path string, stop <-chan struct{}, interval time.Duration) *os.File {
	for {
		f, err := os.Open(path)
		if err == nil {
			return f
		}
		select {
		case <-stop:
			return nil
		case <-time.After(interval):
		}
	}
}

// logTail — python readline на регулярному файлі: читає до "\n" або до
// справжнього EOF (read повертає 0), частковий хвіст без "\n" при цьому
// віддається як є, без перенесення в наступний виклик.
type logTail struct {
	f   *os.File
	buf []byte
	pos int64
}

func (t *logTail) readLine() string {
	for {
		if i := bytes.IndexByte(t.buf, '\n'); i >= 0 {
			line := t.buf[:i+1]
			t.buf = t.buf[i+1:]
			t.pos += int64(len(line))
			return string(line)
		}
		chunk := make([]byte, 4096)
		n, _ := t.f.Read(chunk)
		if n == 0 {
			line := t.buf
			t.buf = nil
			t.pos += int64(len(line))
			return string(line)
		}
		t.buf = append(t.buf, chunk[:n]...)
	}
}
