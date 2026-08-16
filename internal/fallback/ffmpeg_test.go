package fallback

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"restream_go/internal/wire/flv"
)

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not available")
	}
}

func runFFmpeg(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("ffmpeg", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

// tinyClip — вихідний матеріал під нормалізацію.
func tinyClip(t *testing.T, path, size string, seconds int) {
	t.Helper()
	runFFmpeg(t, "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size="+size+":rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", strconv.Itoa(seconds), "-c:v", "libx264", "-preset", "ultrafast",
		"-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", path)
}

// normalize — рецепт препарера (BuildSingleCommand): усі сегменти під однакові
// параметри енкодера, інакше seq-header на стику розходяться.
func normalize(t *testing.T, src, out string, w, h, fps, vkbps, akbps int) {
	t.Helper()
	target := TargetParams{Width: w, Height: h, FPS: fps, Channels: 2, SampleRate: 48000,
		VideoBitrateKbps: vkbps, AudioBitrateKbps: akbps}
	runFFmpeg(t, BuildSingleCommand(src, out, target, nil, nil)[1:]...)
}

// ladderArtifact — N рендішенів + аудіо одним ffmpeg в ОДИН TS (BuildLadderCommand).
func ladderArtifact(t *testing.T, src, out string, rungs []Rung) {
	t.Helper()
	target := TargetParams{Ladder: rungs, Channels: 2, SampleRate: 48000, AudioBitrateKbps: 160}
	runFFmpeg(t, BuildLadderCommand(src, out, target, nil, nil)[1:]...)
}

func logsDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestBackupPacingDrift —: медіа-час не
// біжить поперед реального, а мовчання перед перемиканням надолужується.
func TestBackupPacingDrift(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	clips := make([]string, 3)
	for i := range clips {
		src := filepath.Join(dir, fmt.Sprintf("clip%d.mp4", i))
		tinyClip(t, src, "640x360", 1)
		clips[i] = filepath.Join(dir, fmt.Sprintf("clip%d-norm.mp4", i))
		normalize(t, src, clips[i], 640, 360, 30, 800, 160)
	}

	type row struct {
		wall time.Time
		ts   int64
	}
	var mu sync.Mutex
	var rows []row
	played := 0
	record := func(_ string, _ byte, ts int64, _ []byte) {
		mu.Lock()
		rows = append(rows, row{time.Now(), ts})
		mu.Unlock()
	}
	loopNext := func() (LoopItem, bool) {
		mu.Lock()
		defer mu.Unlock()
		item := LoopItem{Path: clips[played%len(clips)]}
		played++
		return item, true
	}
	newPlayer := func(name string) *Player {
		return New(Options{
			Name: name, Process: record, LogDir: logsDir(t),
			Sources: func(string) string { return "" }, LoopNext: loopNext,
		})
	}

	player := newPlayer("pace")
	player.Start(0)
	time.Sleep(9 * time.Second)
	player.Stop()

	mu.Lock()
	segments := played
	snapshot := append([]row(nil), rows...)
	mu.Unlock()
	if len(snapshot) <= 50 {
		t.Fatalf("the fallback player produced %d tags", len(snapshot))
	}
	if segments < 4 {
		t.Fatalf("only %d segments played -- boundaries not exercised", segments)
	}
	wallMS := snapshot[len(snapshot)-1].wall.Sub(snapshot[0].wall).Seconds() * 1000
	mediaMS := float64(snapshot[len(snapshot)-1].ts - snapshot[0].ts)
	drift := mediaMS - wallMS
	if drift >= 600 {
		t.Fatalf("media time runs ahead of the wall clock (drift %+.0f ms over %d segments)",
			drift, segments)
	}
	t.Logf("drift %+.0f ms over %d segments (%d tags)", drift, segments, len(snapshot))

	// Другий бік того самого гейта: мовчання перед перемиканням надолужується.
	mu.Lock()
	rows = nil
	played = 0
	mu.Unlock()
	const catchUp = 0.8
	switchAt := time.Now()
	time.Sleep(time.Duration(catchUp * float64(time.Second)))
	player = newPlayer("catchup")
	player.Start(catchUp)
	time.Sleep(2500 * time.Millisecond)
	player.Stop()

	mu.Lock()
	snapshot = append([]row(nil), rows...)
	mu.Unlock()
	var window []row
	for _, r := range snapshot {
		if r.wall.Sub(switchAt) <= 2500*time.Millisecond {
			window = append(window, r)
		}
	}
	if len(window) < 2 {
		t.Fatalf("only %d tags in the catch-up window", len(window))
	}
	delivered := window[len(window)-1].ts - window[0].ts
	wallOfWindow := window[len(window)-1].wall.Sub(window[0].wall).Seconds() * 1000
	if float64(delivered) <= wallOfWindow+400 {
		t.Fatalf("the silence before the switch is not caught up (%d ms of media in %.0f ms of wall time)",
			delivered, wallOfWindow)
	}
	t.Logf("catch-up: %d ms of media in %.0f ms of wall time", delivered, wallOfWindow)
}

// TestLadderPlaybackTimeline —:
// монотонний кламп ПО ДОРІЖЦІ дає рівні аудіо-мітки на драбині.
func TestLadderPlaybackTimeline(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	stub := filepath.Join(dir, "stub.mp4")
	tinyClip(t, stub, "1280x720", 4)
	artifact := filepath.Join(dir, "ladder.ts")
	ladderArtifact(t, stub, artifact, []Rung{
		{640, 360, 30, 800}, {480, 270, 30, 500}, {320, 180, 30, 300}})

	var mu sync.Mutex
	var audio []int64
	byTrack := map[string]int{}
	record := func(_ string, tagType byte, ts int64, payload []byte) {
		mu.Lock()
		defer mu.Unlock()
		byTrack[flv.HeaderKey(tagType, payload)]++
		if tagType == flv.TagAudio && !flv.IsSeqHeader(tagType, payload) {
			audio = append(audio, ts)
		}
	}
	audioCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(audio)
	}

	player := New(Options{
		Name: "timeline", Process: record, LogDir: logsDir(t), Ladder: true,
		Sources: func(role string) string {
			if role == "loop" {
				return artifact
			}
			return ""
		},
	})
	player.Start(0)
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) && audioCount() <= 120 {
		time.Sleep(200 * time.Millisecond)
	}
	player.Stop()

	mu.Lock()
	defer mu.Unlock()
	if len(audio) <= 100 {
		t.Fatalf("the ladder fallback produced %d audio tags", len(audio))
	}
	zeros, minDelta, maxDelta := 0, int64(1<<62), int64(-1<<62)
	for i := 1; i < len(audio); i++ {
		d := audio[i] - audio[i-1]
		if d == 0 {
			zeros++
		}
		if d < minDelta {
			minDelta = d
		}
		if d > maxDelta {
			maxDelta = d
		}
	}
	if minDelta < 20 || maxDelta > 23 || zeros != 0 {
		t.Fatalf("audio timestamps are not evenly spaced: min=%d max=%d zero=%d",
			minDelta, maxDelta, zeros)
	}
	for _, key := range []string{"video", "video1", "video2", "audio"} {
		if byTrack[key] == 0 {
			t.Fatalf("no tags for %q on the wire: %v", key, byTrack)
		}
	}
	t.Logf("%d audio tags, deltas %d..%d ms, per-key counts %v",
		len(audio), minDelta, maxDelta, byTrack)
}
