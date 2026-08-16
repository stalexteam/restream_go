package media

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUploadSlotSemantics(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)

	if !store.AcquireUploadSlot() {
		t.Fatal("first acquire should succeed")
	}
	if store.AcquireUploadSlot() {
		t.Fatal("second concurrent acquire should fail")
	}
	store.ReleaseUploadSlot()
	store.ReleaseUploadSlot() // ідемпотентно, як python release() без acquire
	if !store.AcquireUploadSlot() {
		t.Fatal("slot should be reusable after release")
	}
	store.ReleaseUploadSlot()
}

func TestScheduleDurationsDedupAndCallback(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.mp4"), 16)
	store := NewStore(root)

	release := make(chan struct{})
	var calls int
	store.probeDuration = func(path string) (float64, bool) {
		calls++
		<-release
		return 1.5, true
	}

	done := make(chan string, 4)
	store.OnDurationsReady = func(rel string) { done <- rel }

	l1, ok := store.ListDir("")
	if !ok {
		t.Fatal("ListDir failed")
	}
	l2, ok := store.ListDir("")
	if !ok {
		t.Fatal("second ListDir failed")
	}
	_ = l1
	_ = l2
	close(release)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for OnDurationsReady")
	}
	select {
	case <-done:
		t.Fatal("callback fired twice: scheduleDurations did not dedup concurrent ListDir calls")
	case <-time.After(200 * time.Millisecond):
	}
	if calls != 1 {
		t.Errorf("probe calls = %d, want 1 (dedup via probing set)", calls)
	}
}

// TestFinalizeUploadF10 -- контракт на реальному ffprobe: відео+аудіо
// обов'язкові, відхилений файл видаляється разом із.part.
func TestFinalizeUploadF10(t *testing.T) {
	if _, err := os.Stat(filepath.Join("..", "probe", "testdata", "clip_a.mp4")); err != nil {
		t.Skip("internal/probe/testdata fixtures missing")
	}
	root := t.TempDir()
	store := NewStore(root)

	partOK := filepath.Join(root, ".good.mp4.part")
	copyFile(t, filepath.Join("..", "probe", "testdata", "clip_a.mp4"), partOK)
	finalOK := filepath.Join(root, "good.mp4")
	rel, errMsg, _ := store.FinalizeUpload(partOK, finalOK)
	if errMsg != "" {
		t.Fatalf("valid upload rejected: %s", errMsg)
	}
	if rel != "good.mp4" {
		t.Errorf("rel = %q, want good.mp4", rel)
	}
	if _, err := os.Stat(finalOK); err != nil {
		t.Errorf("final file missing: %v", err)
	}
	if _, err := os.Stat(partOK); !os.IsNotExist(err) {
		t.Errorf("part file should be gone after a successful finalize")
	}

	partBad := filepath.Join(root, ".bad.mp4.part")
	copyFile(t, filepath.Join("..", "probe", "testdata", "video_only.mp4"), partBad)
	finalBad := filepath.Join(root, "bad.mp4")
	_, errMsg2, _ := store.FinalizeUpload(partBad, finalBad)
	if errMsg2 == "" {
		t.Fatal("video without audio should be rejected")
	}
	if _, err := os.Stat(partBad); !os.IsNotExist(err) {
		t.Errorf("rejected .part should be removed")
	}
	if _, err := os.Stat(finalBad); !os.IsNotExist(err) {
		t.Errorf("rejected upload must not produce a final file")
	}
}

// Q84: без теки-батька потрібен status, а не «нічого».
func TestSuggestMissingParentStillReportsStatus(t *testing.T) {
	root, _ := setupTree(t)
	store := NewStore(root)

	for _, prefix := range []string{"clipss/", "nosuch/deep/"} {
		got, ok := store.Suggest(prefix, true, 8)
		if !ok {
			t.Errorf("suggest(%q) не віддав status", prefix)
			continue
		}
		if got.Status.Exists || len(got.Entries) != 0 {
			t.Errorf("suggest(%q) = %+v", prefix, got)
		}
	}
	if _, ok := store.Suggest("../../etc/", true, 8); ok {
		t.Error("вихід за корінь мусить лишатись «нічого»")
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open %s: %v", src, err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("create %s: %v", dst, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatalf("copy %s -> %s: %v", src, dst, err)
	}
}
