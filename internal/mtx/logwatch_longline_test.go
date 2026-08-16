package mtx

import (
	"bytes"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestWatchLongLineWithoutNewlineSurvivesTruncate -- MX7: рядок >4096Б без
// '\n' (кілька внутрішніх Read у readLine), truncate посеред нього --
// довгий рядок у лозі.
func TestWatchLongLineWithoutNewlineSurvivesTruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mediamtx.log")
	w := openAppend(t, path)

	count, stop := startWatch(t, path)
	defer stop()

	longNoNewline := bytes.Repeat([]byte("A"), 6000)
	if _, err := w.Write(longNoNewline); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // дати watcher-у прочитати рядок кількома чанками й впертись у EOF

	if err := w.Truncate(2000); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // дати watcher-у помітити truncate ДО наступного запису

	// publishing скинувся при truncate -- цей conn знову фаєрить timeout.
	appendLines(t, w, `[conn 1.2.3.4:5] closed: read tcp 1.2.3.4:5->9.9.9.9:1935: i/o timeout`)
	waitUntil(t, func() bool { return atomic.LoadInt32(count) == 1 })

	// нормальна робота після відновлення: publish перед timeout -- НЕ фаєрить.
	appendLines(t, w,
		`[conn 6.6.6.6:7] is publishing to path 'live/aux'`,
		`[conn 6.6.6.6:7] closed: read tcp 6.6.6.6:7->9.9.9.9:1935: i/o timeout`,
	)
	time.Sleep(30 * time.Millisecond)
	if got := atomic.LoadInt32(count); got != 1 {
		t.Errorf("count = %d, want 1 (publish-before-timeout must not fire)", got)
	}
	_ = w.Close()
}
