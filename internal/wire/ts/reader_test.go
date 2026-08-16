package ts

import (
	"bytes"
	"os"
	"runtime"
	"testing"
	"time"
)

// ReadTags мусить віддати той самий потік тегів, що прямий Feed+Flush.
func TestReadTagsParity(t *testing.T) {
	data := synthTS(t, 60)
	want, _ := demuxAll(t, data, false, false)
	var got []recTag
	if err := ReadTags(bytes.NewReader(data), recordTag(&got), nil); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("ReadTags delivered nothing")
	}
	compareTags(t, "readtags", got, want)
}

// Пауза довша за ReadTimeout -> OnStall, повернення даних -> OnResume;
// теги не губляться.
func TestReadTagsStall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Pipe deadlines unsupported on Windows")
	}
	data := synthTS(t, 60)
	want, _ := demuxAll(t, data, false, false)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer w.Close()
		if _, err := w.Write(data[:len(data)/2]); err != nil {
			return
		}
		time.Sleep(400 * time.Millisecond)
		_, _ = w.Write(data[len(data)/2:])
	}()

	stalls, resumes := 0, 0
	var got []recTag
	err = ReadTags(r, recordTag(&got), &ReadTagsOptions{
		ReadTimeout: 100 * time.Millisecond,
		OnStall:     func() { stalls++ },
		OnResume:    func() { resumes++ },
	})
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if stalls < 1 || resumes < 1 {
		t.Fatalf("stall detector: stalls=%d resumes=%d", stalls, resumes)
	}
	compareTags(t, "stall", got, want)
}
