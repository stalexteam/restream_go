package proc

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"restream_go/internal/wire/flv"
)

// blockingSource — джерело БЕЗ SetReadDeadline: read висить, поки не покладуть
// байти (модель exec-пайпа Windows).
type blockingSource struct {
	mu     sync.Mutex
	cv     *sync.Cond
	buf    []byte
	closed bool
	err    error
}

func newBlockingSource() *blockingSource {
	s := &blockingSource{}
	s.cv = sync.NewCond(&s.mu)
	return s
}

func (s *blockingSource) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.buf) == 0 && !s.closed {
		s.cv.Wait()
	}
	if len(s.buf) == 0 {
		if s.err != nil {
			return 0, s.err
		}
		return 0, io.EOF
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

func (s *blockingSource) push(b []byte) {
	s.mu.Lock()
	s.buf = append(s.buf, b...)
	s.mu.Unlock()
	s.cv.Broadcast()
}

func (s *blockingSource) close(err error) {
	s.mu.Lock()
	s.closed, s.err = true, err
	s.mu.Unlock()
	s.cv.Broadcast()
}

// Форма інтерфейсу, який шукають flv.ReadTags і ts.ReadTags.
type wireDeadlineReader interface {
	Read([]byte) (int, error)
	SetReadDeadline(time.Time) error
}

var _ wireDeadlineReader = (*DeadlineReader)(nil)

// Дедлайн у минулому — негайний таймаут, дані не з'їдаються.
func TestDeadlineReaderPastDeadline(t *testing.T) {
	src := newBlockingSource()
	d := NewDeadlineReader(src)
	defer d.Close()

	if err := d.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	n, err := d.Read(buf)
	if n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Read = %d, %v; want 0, os.ErrDeadlineExceeded", n, err)
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("error %v is not a net.Error with Timeout()==true", err)
	}

	src.push([]byte("abc"))
	if err := d.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	n, err = d.Read(buf)
	if err != nil || string(buf[:n]) != "abc" {
		t.Fatalf("after the deadline was cleared: %q, %v", buf[:n], err)
	}
}

// Дані до дедлайну проходять; невичерпаний чанк лишається в буфері.
func TestDeadlineReaderDataBeforeDeadline(t *testing.T) {
	src := newBlockingSource()
	d := NewDeadlineReader(src)
	defer d.Close()

	go func() {
		time.Sleep(20 * time.Millisecond)
		src.push([]byte("hello"))
	}()
	if err := d.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	n, err := d.Read(buf)
	if err != nil || string(buf[:n]) != "he" {
		t.Fatalf("first read: %q, %v", buf[:n], err)
	}
	rest := make([]byte, 8)
	n, err = d.Read(rest)
	if err != nil || string(rest[:n]) != "llo" {
		t.Fatalf("leftover read: %q, %v", rest[:n], err)
	}
}

// Знятий дедлайн — блокуюче читання без стелі.
func TestDeadlineReaderNoDeadlineBlocks(t *testing.T) {
	src := newBlockingSource()
	d := NewDeadlineReader(src)
	defer d.Close()

	done := make(chan string, 1)
	go func() {
		buf := make([]byte, 8)
		n, err := d.Read(buf)
		if err != nil {
			done <- "err: " + err.Error()
			return
		}
		done <- string(buf[:n])
	}()
	select {
	case got := <-done:
		t.Fatalf("read returned %q before any data arrived", got)
	case <-time.After(60 * time.Millisecond):
	}
	src.push([]byte("late"))
	select {
	case got := <-done:
		if got != "late" {
			t.Fatalf("read = %q, want \"late\"", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read did not wake up on data")
	}
}

// EOF джерела проходить крізь обгортку і повторюється.
func TestDeadlineReaderEOF(t *testing.T) {
	d := NewDeadlineReader(bytes.NewReader([]byte("xy")))
	defer d.Close()

	buf := make([]byte, 8)
	if n, err := d.Read(buf); err != nil || string(buf[:n]) != "xy" {
		t.Fatalf("read = %q, %v", buf[:n], err)
	}
	for i := 0; i < 2; i++ {
		if n, err := d.Read(buf); n != 0 || err != io.EOF {
			t.Fatalf("read %d after EOF = %d, %v", i, n, err)
		}
	}
}

// Закриття джерела будить читача його ж помилкою.
func TestDeadlineReaderSourceClosed(t *testing.T) {
	src := newBlockingSource()
	d := NewDeadlineReader(src)
	defer d.Close()

	go func() {
		time.Sleep(20 * time.Millisecond)
		src.close(os.ErrClosed)
	}()
	buf := make([]byte, 8)
	n, err := d.Read(buf)
	if n != 0 || !errors.Is(err, os.ErrClosed) {
		t.Fatalf("read = %d, %v; want 0, os.ErrClosed", n, err)
	}
}

// Close зупиняє помпу; читач, що чекає, дістає ErrClosed.
func TestDeadlineReaderClose(t *testing.T) {
	src := newBlockingSource()
	d := NewDeadlineReader(src)

	got := make(chan error, 1)
	go func() {
		buf := make([]byte, 8)
		_, err := d.Read(buf)
		got <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-got:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("read after Close = %v, want os.ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not wake the blocked read")
	}
	src.close(nil)
}

// Стал-детектор flv.ReadTags працює поверх обгортки.
func TestDeadlineReaderDrivesFLVStallDetector(t *testing.T) {
	src := newBlockingSource()
	keyframe := []byte{0x17, 0x01, 0x00, 0x00, 0x00}

	var head bytes.Buffer
	head.Write(flv.FileHeader)
	if err := flv.WriteTag(&head, flv.TagVideo, 0, keyframe); err != nil {
		t.Fatal(err)
	}
	src.push(head.Bytes())

	stalls, resumes := make(chan struct{}, 4), make(chan struct{}, 4)
	tags := make(chan uint32, 8)
	d := NewDeadlineReader(src)
	defer d.Close()

	done := make(chan error, 1)
	go func() {
		done <- flv.ReadTags(d, "relay", func(_ string, _ byte, ts uint32, _ []byte) {
			tags <- ts
		}, &flv.ReadTagsOptions{
			ReadTimeout: 50 * time.Millisecond,
			OnStall:     func() { stalls <- struct{}{} },
			OnResume:    func() { resumes <- struct{}{} },
		})
	}()

	if got := <-tags; got != 0 {
		t.Fatalf("first tag ts = %d", got)
	}
	select {
	case <-stalls:
	case <-time.After(3 * time.Second):
		t.Fatal("stall detector never fired through the wrapper")
	}

	var more bytes.Buffer
	if err := flv.WriteTag(&more, flv.TagVideo, 33, keyframe); err != nil {
		t.Fatal(err)
	}
	src.push(more.Bytes())
	if got := <-tags; got != 33 {
		t.Fatalf("second tag ts = %d", got)
	}
	select {
	case <-resumes:
	case <-time.After(3 * time.Second):
		t.Fatal("resume never fired through the wrapper")
	}

	src.close(nil)
	if err := <-done; err != nil {
		t.Fatalf("ReadTags: %v", err)
	}
}
