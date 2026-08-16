package fallback

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"restream_go/internal/wire/flv"
)

func idlePlayer(t *testing.T) *Player {
	t.Helper()
	return New(Options{
		Name: "idle", LogDir: logsDir(t),
		Sources: func(string) string { return "" },
		Process: func(string, byte, int64, []byte) {},
	})
}

// Q36: PlayEnd не з'їдає onDone мовчки.
func TestPlayEndDoesNotDropTheCallback(t *testing.T) {
	player := idlePlayer(t)
	if player.PlayEnd(func() {}) {
		t.Error("незапущений гравець узяв play_end")
	}

	player.Start(0)
	t.Cleanup(player.Stop)
	time.Sleep(100 * time.Millisecond)

	player.mu.Lock()
	player.phase = PhaseEnd
	player.mu.Unlock()

	fired := make(chan struct{})
	if !player.PlayEnd(func() { close(fired) }) {
		t.Fatal("play_end у фазі End відхилено")
	}
	player.fireDone()
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("onDone загубився")
	}
}

// Пізній PlayEnd не згорає при поверненні з фази End у тіло.
func TestLatePlayEndSurvivesTheEndPhase(t *testing.T) {
	player := idlePlayer(t)
	player.Start(0)
	t.Cleanup(player.Stop)
	time.Sleep(100 * time.Millisecond)

	player.mu.Lock()
	player.phase = PhaseEnd
	player.mu.Unlock()

	fired := make(chan struct{})
	if !player.PlayEnd(func() { close(fired) }) {
		t.Fatal("play_end у фазі End відхилено")
	}
	// Callback прийшов ПІСЛЯ останнього fireDone епізоду -- аутро вже дограло.
	if !player.leaveEndPhase() {
		t.Fatal("гравець не повернувся в тіло")
	}
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("onDone згорів при виході з фази End")
	}
	if phase := player.Phase(); phase != PhaseLoop {
		t.Fatalf("phase %q after leaving End, want %q", phase, PhaseLoop)
	}

	// Двобічність: без заявки вихід із фази нікого не кличе.
	player.mu.Lock()
	player.phase = PhaseEnd
	player.mu.Unlock()
	if !player.leaveEndPhase() {
		t.Fatal("другий вихід із фази End не спрацював")
	}
}

// Порожній пресет: епізод крутиться на cv.wait і мусить зупинитись одразу.
func TestStopWakesTheIdleLoop(t *testing.T) {
	player := idlePlayer(t)
	player.Start(0)
	time.Sleep(100 * time.Millisecond)
	if player.Phase() != PhaseLoop {
		t.Fatalf("phase %q, want %q", player.Phase(), PhaseLoop)
	}
	started := time.Now()
	player.Stop()
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Stop took %v", elapsed)
	}
	if player.Phase() != PhaseLoop || player.IsRunning() {
		t.Fatalf("after Stop: phase=%q running=%v", player.Phase(), player.IsRunning())
	}
	if _, ok := player.PID(); ok {
		t.Fatal("a stopped player still reports a pid")
	}
}

func TestStopBeforeStartIsNoop(t *testing.T) {
	idlePlayer(t).Stop()
}

func TestPlayEndAndRestartOnANeverStartedPlayer(t *testing.T) {
	player := idlePlayer(t)
	fired := false
	player.PlayEnd(func() { fired = true })
	if fired {
		t.Fatal("play_end fired on a player that never started")
	}
	player.Restart()
	if player.Phase() != "" {
		t.Fatalf("phase %q on an untouched player", player.Phase())
	}
}

func TestFireSurvivesAPanickingCallback(t *testing.T) {
	idlePlayer(t).fire(func() { panic("boom") })
}

func TestHasEndReadyFollowsSources(t *testing.T) {
	end := ""
	player := New(Options{
		Name: "end", LogDir: logsDir(t),
		Sources: func(role string) string {
			if role == "end" {
				return end
			}
			return ""
		},
		Process: func(string, byte, int64, []byte) {},
	})
	if player.HasEndReady() {
		t.Fatal("no end artifact yet")
	}
	end = "/tmp/end.mp4"
	if !player.HasEndReady() {
		t.Fatal("the prepared end artifact is not visible")
	}
}

// F5: аутро грає до кінця, on_done летить після нього, і гравець НЕ мовчить —
// повертається в тіло, поки Platform не заріже на relay.
func TestPlayEndRunsOutroThenReturnsToTheBody(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	body := filepath.Join(dir, "body.mp4")
	outro := filepath.Join(dir, "outro.mp4")
	for _, spec := range []struct{ src, out string }{{"body", body}, {"outro", outro}} {
		raw := filepath.Join(dir, spec.src+"-raw.mp4")
		tinyClip(t, raw, "320x240", 1)
		normalize(t, raw, spec.out, 320, 240, 30, 500, 128)
	}

	var mu sync.Mutex
	var count int
	lastByTrack := map[string]int64{}
	var regressions []string
	record := func(_ string, tagType byte, ts int64, payload []byte) {
		mu.Lock()
		defer mu.Unlock()
		count++
		key := "meta"
		if tagType != flv.TagScript {
			key = flv.HeaderKey(tagType, payload)
		}
		if last, ok := lastByTrack[key]; ok && ts < last {
			regressions = append(regressions, fmt.Sprintf("%s %d after %d", key, ts, last))
		}
		lastByTrack[key] = ts
	}
	tagCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}

	player := New(Options{
		Name: "resume", Process: record, LogDir: logsDir(t),
		Sources: func(role string) string {
			switch role {
			case "loop":
				return body
			case "end":
				return outro
			}
			return ""
		},
	})
	if !player.HasEndReady() {
		t.Fatal("the outro must be visible to Platform")
	}
	player.Start(0)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && tagCount() < 30 {
		time.Sleep(50 * time.Millisecond)
	}

	done := make(chan string, 1)
	player.PlayEnd(func() { done <- player.Phase() })
	var phaseAtDone string
	select {
	case phaseAtDone = <-done:
	case <-time.After(10 * time.Second):
		player.Stop()
		t.Fatal("on_done never fired after play_end")
	}
	if phaseAtDone != PhaseEnd {
		t.Fatalf("on_done fired in phase %q, want %q", phaseAtDone, PhaseEnd)
	}

	after := tagCount()
	time.Sleep(1500 * time.Millisecond)
	grown := tagCount() - after
	player.Stop()
	if grown == 0 {
		t.Fatal("the player went silent after the outro (F5: silence = a latency jump)")
	}
	if len(regressions) != 0 {
		t.Fatalf("per-track timeline went backwards: %v", regressions)
	}
	t.Logf("%d tags total, %d after on_done", tagCount(), grown)
}

// Q34: фазування стику мусить лягти на доріжку, що йшла ДО стику, а не
// витратитись на першу-ліпшу нову.
func TestSeamPhasingSkipsNewTracks(t *testing.T) {
	type outTag struct {
		typ byte
		ts  int64
		key string
	}
	var out []outTag
	player := New(Options{
		Name: "seam", LogDir: logsDir(t),
		Sources: func(string) string { return "" },
		Process: func(_ string, tagType byte, ts int64, payload []byte) {
			out = append(out, outTag{tagType, ts, flv.HeaderKey(tagType, payload)})
		},
	})
	player.mu.Lock()
	player.resetEpisodeLocked(0)
	player.beginSegmentLocked()
	player.mu.Unlock()

	a0 := func(ts int64) { player.onTag("backup", flv.TagAudio, ts, adataTag(0)) }
	a1 := func(ts int64) { player.onTag("backup", flv.TagAudio, ts, adataTag(1)) }
	vk := func(ts int64) { player.onTag("backup", flv.TagVideo, ts, vkeyTag()) }

	// Перший сегмент: крок доріжки 0 -- 21/22мс.
	vk(0)
	a0(0)
	a0(21)
	a0(43)

	// Стик: нова доріжка 1 приходить першою, потім справжня доріжка стику.
	player.mu.Lock()
	player.beginSegmentLocked()
	player.mu.Unlock()
	vk(100)
	a1(90)
	a0(90)

	var audio []outTag
	for _, tag := range out {
		if tag.typ == flv.TagAudio {
			audio = append(audio, tag)
		}
	}
	if len(audio) != 5 {
		t.Fatalf("audio tags: %+v", audio)
	}
	// Перший сегмент не фазується взагалі: 0, 21, 43 -> 0, 21, 43 (+1мс якоря).
	first := audio[2]
	if first.key != "audio" || first.ts != 44 {
		t.Fatalf("the first segment must not be phased: %+v", audio)
	}
	// Нова доріжка стику йде як є, без зсуву.
	newTrack := audio[3]
	if newTrack.key != "audio1" || newTrack.ts != 35 {
		t.Fatalf("the new track must go out unshifted: %+v", audio)
	}
	// Доріжка стику продовжується РІВНО через свій крок (44 + 22) і не гине.
	seam := audio[4]
	if seam.key != "audio" || seam.ts != 66 {
		t.Fatalf("the seam track was not phased: %+v", audio)
	}
}

// adataTag/vkeyTag — мінімальні payload-и доріжок для onTag.
func adataTag(track byte) []byte {
	raw := []byte{0xAF, 0x01, 0x21, 0x22}
	if track == 0 {
		return raw
	}
	return flv.WrapMultitrackAudio(track, raw)
}

func vkeyTag() []byte { return []byte{0x17, 0x01, 0, 0, 0, 0x65, 0x88} }
