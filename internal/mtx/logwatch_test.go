package mtx

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testPollInterval = 5 * time.Millisecond

func openAppend(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func appendLines(t *testing.T, f *os.File, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

func startWatch(t *testing.T, logPath string) (*int32, func()) {
	t.Helper()
	var count int32
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		watchTail(logPath, func() { atomic.AddInt32(&count, 1) }, stop, testPollInterval)
		close(done)
	}()
	time.Sleep(30 * time.Millisecond) // watcher: open+seek до першого append-у
	return &count, func() {
		close(stop)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("watchTail did not exit after stop")
		}
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}

func TestWatchFiresOnTimeoutBeforePublishing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mediamtx.log")
	w := openAppend(t, path)
	defer w.Close()

	count, stop := startWatch(t, path)
	defer stop()

	appendLines(t, w, `2026-01-01 [conn 1.2.3.4:5] closed: read tcp 1.2.3.4:5->9.9.9.9:1935: i/o timeout`)
	waitUntil(t, func() bool { return atomic.LoadInt32(count) == 1 })
}

func TestWatchIgnoresTimeoutAfterPublishing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mediamtx.log")
	w := openAppend(t, path)
	defer w.Close()

	count, stop := startWatch(t, path)
	defer stop()

	appendLines(t, w,
		`[conn 1.2.3.4:5] is publishing to path 'live/main'`,
		`[conn 1.2.3.4:5] closed: read tcp 1.2.3.4:5->9.9.9.9:1935: i/o timeout`,
	)
	appendLines(t, w, `[conn 6.6.6.6:7] is publishing to path 'live/aux'`)
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt32(count); got != 0 {
		t.Errorf("count = %d, want 0", got)
	}
}

func TestWatchGenericCloseClearsPublishing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mediamtx.log")
	w := openAppend(t, path)
	defer w.Close()

	count, stop := startWatch(t, path)
	defer stop()

	appendLines(t, w,
		`[conn 1.2.3.4:5] is publishing to path 'live/main'`,
		`[conn 1.2.3.4:5] closed: EOF`,
		`[conn 1.2.3.4:5] closed: read tcp 1.2.3.4:5->9.9.9.9:1935: i/o timeout`,
	)
	waitUntil(t, func() bool { return atomic.LoadInt32(count) == 1 })
}

func TestWatchTruncateMidStreamResetsPublishing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mediamtx.log")
	w := openAppend(t, path)

	count, stop := startWatch(t, path)
	defer stop()

	appendLines(t, w, `[conn 1.2.3.4:5] is publishing to path 'live/main'`)
	waitDrained(t, path)

	// Мідстрім-truncate: читач уже пройшов позицію більшу за новий розмір.
	if err := w.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // дати watcher-у помітити truncate ДО наступного запису

	// publishing скинувся при truncate -- цей conn знову фаєрить timeout.
	appendLines(t, w, `[conn 1.2.3.4:5] closed: read tcp 1.2.3.4:5->9.9.9.9:1935: i/o timeout`)
	waitUntil(t, func() bool { return atomic.LoadInt32(count) == 1 })
	_ = w.Close()
}

func TestWatchRotateToNewInodeResetsPublishing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mediamtx.log")
	w := openAppend(t, path)

	count, stop := startWatch(t, path)
	defer stop()

	appendLines(t, w, `[conn 1.2.3.4:5] is publishing to path 'live/main'`)
	waitDrained(t, path)
	_ = w.Close()

	// logrotate-стиль: той самий шлях, нова inode.
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	nw := openAppend(t, path)
	defer nw.Close()

	appendLines(t, nw, `[conn 1.2.3.4:5] closed: read tcp 1.2.3.4:5->9.9.9.9:1935: i/o timeout`)
	waitUntil(t, func() bool { return atomic.LoadInt32(count) == 1 })
}

func TestWatchSeeksToEndOfExistingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mediamtx.log")
	w := openAppend(t, path)
	// Контент ДО старту watch ігнорується (seek на кінець).
	appendLines(t, w, `[conn 1.2.3.4:5] closed: read tcp 1.2.3.4:5->9.9.9.9:1935: i/o timeout`)

	count, stop := startWatch(t, path)
	defer stop()
	time.Sleep(30 * time.Millisecond)
	if got := atomic.LoadInt32(count); got != 0 {
		t.Errorf("pre-existing line was processed: count = %d, want 0", got)
	}

	appendLines(t, w, `[conn 6.6.6.6:7] closed: read tcp 6.6.6.6:7->9.9.9.9:1935: i/o timeout`)
	waitUntil(t, func() bool { return atomic.LoadInt32(count) == 1 })
	_ = w.Close()
}

func TestWatchStopUnblocksWaitingForFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.log")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		watchTail(path, func() {}, stop, testPollInterval)
	}()
	close(stop)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchTail did not exit when waiting for a non-existent file")
	}
}

// waitDrained — дати watcher-у час прочитати щойно дописане (у синтетичних
// тестах немає окремого сигналу "рядок оброблено").
func waitDrained(t *testing.T, path string) {
	t.Helper()
	time.Sleep(30 * time.Millisecond)
}
