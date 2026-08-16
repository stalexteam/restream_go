package fallback

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"restream_go/internal/probe"
)

// Контрактні тести бюджету CPU на РЕАЛЬНОМУ ffmpeg: скільки потоків бере
// живий транскод і чи серіалізує семафор воркерів.

func procThreads(t *testing.T, pid int) (int, bool) {
	t.Helper()
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "Threads:") {
			continue
		}
		count, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Threads:")))
		if err != nil {
			return 0, false
		}
		return count, true
	}
	return 0, false
}

func procNice(t *testing.T, pid int) string {
	t.Helper()
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	tail := string(raw)[strings.LastIndex(string(raw), ")")+1:]
	fields := strings.Fields(tail)
	if len(fields) < 17 {
		return ""
	}
	return fields[16]
}

// twoTrackClip — той самий матеріал, але з двома аудіодоріжками.
func twoTrackClip(t *testing.T, path, size string, seconds int) {
	t.Helper()
	runFFmpeg(t, "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size="+size+":rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000",
		"-t", strconv.Itoa(seconds), "-map", "0:v", "-map", "1:a", "-map", "2:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", path)
}

// TestRawSourceIsCopiedOnlyWhenSingleTrack — звірка параметрів дивиться доріжки
// 0/0, тож мультитрек-файл не має проскочити в -c copy.
func TestRawSourceIsCopiedOnlyWhenSingleTrack(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	single := filepath.Join(dir, "single.mp4")
	tinyClip(t, single, "320x240", 1)
	dual := filepath.Join(dir, "dual.mp4")
	twoTrackClip(t, dual, "320x240", 1)

	preparer := NewPreparer(PreparerOptions{Kind: KindSequence, Loop: single,
		Cache: NewCache(filepath.Join(dir, "cache"), 1, 1)})

	live, ok := probe.ProbeStreamParams(single, 0, 0)
	if !ok {
		t.Fatal("could not probe the single-track clip")
	}
	target := TargetParams{Width: live.Width, Height: live.Height, FPS: live.FPS,
		Channels: live.Channels, SampleRate: live.SampleRate,
		VideoBitrateKbps: 800, AudioBitrateKbps: 160}
	if got := preparer.normalizeSegment(single, live, target); got != single {
		t.Fatalf("a matching single-track source must go on air as is: %q", got)
	}

	dualLive, ok := probe.ProbeStreamParams(dual, 0, 0)
	if !ok {
		t.Fatal("could not probe the two-track clip")
	}
	got := preparer.normalizeSegment(dual, dualLive, target)
	if got == dual {
		t.Fatal("a multitrack file matched on tracks 0/0 must still be transcoded")
	}
	if got == "" {
		t.Fatal("the transcode of the multitrack file failed")
	}
	if counts, ok := probe.ProbeTrackCounts(got); !ok || counts.Audio != 1 {
		t.Fatalf("the artifact must carry a single audio track: %+v (ok=%v)", counts, ok)
	}
}

// TestLiveTranscodeCPUBudget — `-threads` з ОБОХ боків реально стримує libx264
// (без обмежень ffmpeg брав 89 потоків замість 9), а сам транскод іде
// на nice 19.
func TestLiveTranscodeCPUBudget(t *testing.T) {
	requireFFmpeg(t)
	if runtime.GOOS != "linux" {
		t.Skip("thread counts are read from /proc")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "stub.mp4")
	tinyClip(t, source, "1280x720", 4)
	target := TargetParams{Width: 1280, Height: 720, FPS: 30, Channels: 2,
		SampleRate: 48000, VideoBitrateKbps: 2500, AudioBitrateKbps: 160}

	measure := func(threads int) (peak int, nice string, artifact string) {
		cache := NewCache(filepath.Join(dir, "cache"+strconv.Itoa(threads)), 1, threads)
		done := make(chan string, 1)
		go func() { done <- cache.GetOrBuild(source, target) }()
		deadline := time.Now().Add(60 * time.Second)
		pid := 0
		for time.Now().Before(deadline) {
			if active := cache.ActiveTranscodes(); len(active) > 0 {
				pid = active[0].PID
				if count, ok := procThreads(t, pid); ok && count > peak {
					peak = count
				}
				if nice == "" {
					nice = procNice(t, pid)
				}
			} else if pid != 0 {
				break
			}
			select {
			case artifact = <-done:
				return peak, nice, artifact
			default:
			}
			time.Sleep(3 * time.Millisecond)
		}
		return peak, nice, <-done
	}

	limited, nice, artifact := measure(1)
	if artifact == "" {
		t.Fatal("the limited transcode produced no artifact")
	}
	if limited == 0 {
		t.Skip("the transcode was too quick to sample /proc")
	}
	if limited > 12 {
		t.Fatalf("the limited transcode ran %d threads", limited)
	}
	if nice != "19" {
		t.Fatalf("the transcode runs at nice %q, want 19", nice)
	}

	unlimited, _, _ := measure(0)
	if runtime.NumCPU() >= 4 && unlimited > 0 && unlimited <= limited {
		t.Fatalf("the thread budget changed nothing: %d limited vs %d unlimited on %d CPUs",
			limited, unlimited, runtime.NumCPU())
	}
	t.Logf("%d threads limited vs %d unlimited on %d CPUs, nice %s",
		limited, unlimited, runtime.NumCPU(), nice)
}

// TestWorkersSerializeOnRealFfmpeg — дві РІЗНІ цілі (ключі різні, дедуп не
// рятує) при max_concurrent=1 ніколи не крутять два ffmpeg одночасно.
func TestWorkersSerializeOnRealFfmpeg(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "stub.mp4")
	tinyClip(t, source, "640x360", 2)
	cache := NewCache(filepath.Join(dir, "cache"), 1, 1)

	target := func(vbitrate int) TargetParams {
		return TargetParams{Width: 640, Height: 360, FPS: 30, Channels: 2,
			SampleRate: 48000, VideoBitrateKbps: vbitrate, AudioBitrateKbps: 160}
	}
	var wg sync.WaitGroup
	artifacts := make([]string, 2)
	for i, vbitrate := range []int{800, 1200} {
		wg.Add(1)
		go func(i, vbitrate int) {
			defer wg.Done()
			artifacts[i] = cache.GetOrBuild(source, target(vbitrate))
		}(i, vbitrate)
	}

	stop, watched := make(chan struct{}), make(chan struct{})
	peak := 0
	go func() {
		defer close(watched)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if n := len(cache.ActiveTranscodes()); n > peak {
				peak = n
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	wg.Wait()
	close(stop)
	<-watched

	for i, artifact := range artifacts {
		if artifact == "" {
			t.Fatalf("worker %d produced no artifact", i)
		}
		if _, err := os.Stat(artifact); err != nil {
			t.Fatal(err)
		}
	}
	if artifacts[0] == artifacts[1] {
		t.Fatal("different targets must not share an artifact")
	}
	if peak != 1 {
		t.Fatalf("%d transcodes ran at once with max_concurrent=1", peak)
	}
	// Готовий артефакт реюзиться без ffmpeg: та сама ціль повертається миттєво.
	started := time.Now()
	if again := cache.GetOrBuild(source, target(800)); again != artifacts[0] {
		t.Fatalf("a ready artifact was not reused: %q", again)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("the reuse path took %s -- it re-ran ffmpeg", elapsed)
	}
}
