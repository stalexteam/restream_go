package proc

import (
	"io"
	"os"
	"sync"
	"time"
)

// deadlineChunk — розмір читання помпи (як буфер рідера ts.ReadTags).
const deadlineChunk = 65536

// DeadlineReader додає SetReadDeadline джерелу, яке його не має: горутина-помпа
// читає сире джерело, споживач бере дані з каналу під таймером. Потрібно для
// exec-пайпів на Windows — вони не pollable, і рідний SetReadDeadline там
// повертає помилку.
type DeadlineReader struct {
	chunks chan []byte
	closed chan struct{}
	once   sync.Once

	mu       sync.Mutex
	buf      []byte
	deadline time.Time
	err      error
}

// NewDeadlineReader піднімає помпу над src і повертає рідер із дедлайнами.
func NewDeadlineReader(src io.Reader) *DeadlineReader {
	d := &DeadlineReader{
		chunks: make(chan []byte, 1),
		closed: make(chan struct{}),
	}
	go d.pump(src)
	return d
}

func (d *DeadlineReader) pump(src io.Reader) {
	defer close(d.chunks)
	buf := make([]byte, deadlineChunk)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			select {
			case d.chunks <- chunk:
			case <-d.closed:
				return
			}
		}
		if err != nil {
			d.setErr(err)
			return
		}
	}
}

func (d *DeadlineReader) setErr(err error) {
	d.mu.Lock()
	if d.err == nil {
		d.err = err
	}
	d.mu.Unlock()
}

func (d *DeadlineReader) srcErr() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err == nil {
		return io.EOF
	}
	return d.err
}

// SetReadDeadline діє на наступні Read; нульовий час знімає дедлайн.
func (d *DeadlineReader) SetReadDeadline(t time.Time) error {
	d.mu.Lock()
	d.deadline = t
	d.mu.Unlock()
	return nil
}

// Read віддає накопичене помпою; при вичерпаному дедлайні — os.ErrDeadlineExceeded
// (він же net.Error з Timeout==true), не споживаючи даних.
func (d *DeadlineReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	d.mu.Lock()
	if len(d.buf) > 0 {
		n := copy(p, d.buf)
		d.buf = d.buf[n:]
		d.mu.Unlock()
		return n, nil
	}
	deadline := d.deadline
	d.mu.Unlock()

	var timeout <-chan time.Time
	if !deadline.IsZero() {
		left := time.Until(deadline)
		if left <= 0 {
			return 0, os.ErrDeadlineExceeded
		}
		timer := time.NewTimer(left)
		defer timer.Stop()
		timeout = timer.C
	}

	select {
	case chunk, ok := <-d.chunks:
		if !ok {
			return 0, d.srcErr()
		}
		n := copy(p, chunk)
		if n < len(chunk) {
			d.mu.Lock()
			d.buf = chunk[n:]
			d.mu.Unlock()
		}
		return n, nil
	case <-timeout:
		return 0, os.ErrDeadlineExceeded
	case <-d.closed:
		return 0, os.ErrClosed
	}
}

// Close зупиняє помпу; джерело закриває власник (помпа побачить його помилку).
func (d *DeadlineReader) Close() error {
	d.once.Do(func() { close(d.closed) })
	return nil
}
