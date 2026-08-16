package fallback

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"restream_go/internal/proc"
)

// Синтетика конкурентності кеша й бюджету потоків.

func plainTarget(vbitrate int) TargetParams {
	return TargetParams{Width: 1920, Height: 1080, FPS: 60, Channels: 2, SampleRate: 48000,
		VideoBitrateKbps: vbitrate, AudioBitrateKbps: 160}
}

func liveParams() LiveParams {
	return LiveParams{VideoCodec: "h264", Width: 1920, Height: 1080, FPS: 60,
		AudioCodec: "aac", Channels: 2, SampleRate: 48000}
}

func writeFile(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// spawnProbe — підміна точки запуску ffmpeg.
type spawnProbe struct {
	mu      sync.Mutex
	running int
	peak    int
	calls   int
	hold    time.Duration
	gate    chan struct{}
	started chan struct{}
}

func (s *spawnProbe) run(_ []string, _, artifact, meta string, id sourceIdentity, target TargetParams) bool {
	s.mu.Lock()
	s.running++
	s.calls++
	if s.running > s.peak {
		s.peak = s.running
	}
	s.mu.Unlock()
	if s.started != nil {
		s.started <- struct{}{}
	}
	if s.gate != nil {
		<-s.gate
	} else {
		time.Sleep(s.hold)
	}
	s.mu.Lock()
	s.running--
	s.mu.Unlock()
	if err := os.WriteFile(artifact, []byte("x"), 0o644); err != nil {
		return false
	}
	return os.WriteFile(meta, sidecarBytes(id, target), 0o644) == nil
}

func (s *spawnProbe) snapshot() (peak, calls int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peak, s.calls
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestTranscodeSemaphore(t *testing.T) {
	dir := t.TempDir()
	source := writeFile(t, filepath.Join(dir, "src.mp4"), "source")

	// Різні цілі -> різні ключі -> дедуп за ключем НЕ рятує; саме це й мусить
	// серіалізувати семафор.
	run := func(cache *Cache, probe *spawnProbe, targets []int) []string {
		cache.spawn = probe.run
		results := make([]string, len(targets))
		var wg sync.WaitGroup
		for i, bitrate := range targets {
			wg.Add(1)
			go func(i, bitrate int) {
				defer wg.Done()
				results[i] = cache.GetOrBuild(source, plainTarget(bitrate))
			}(i, bitrate)
		}
		wg.Wait()
		return results
	}

	probe1 := &spawnProbe{hold: 250 * time.Millisecond}
	run(NewCache(filepath.Join(dir, "c1"), 1, 1), probe1, []int{4000, 6000, 8000, 10000})
	if peak, _ := probe1.snapshot(); peak != 1 {
		t.Fatalf("four different targets ran %d transcodes at once", peak)
	}

	probe2 := &spawnProbe{hold: 250 * time.Millisecond}
	run(NewCache(filepath.Join(dir, "c2"), 2, 1), probe2, []int{4000, 6000, 8000, 10000})
	if peak, _ := probe2.snapshot(); peak != 2 {
		t.Fatalf("a raised budget peaked at %d transcodes, want 2", peak)
	}

	probe3 := &spawnProbe{hold: 250 * time.Millisecond}
	results := run(NewCache(filepath.Join(dir, "c3"), 4, 1), probe3, []int{6000, 6000, 6000, 6000})
	peak, calls := probe3.snapshot()
	if peak != 1 || calls != 1 {
		t.Fatalf("identical targets ran %d transcodes (peak %d), want exactly one", calls, peak)
	}
	for i, artifact := range results {
		if artifact == "" {
			t.Fatalf("waiter %d did not get the artifact", i)
		}
	}
}

// TestSemaphoreTakenOutsideLock — A8/K4: чекання слота не тримає лок кеша, тож
// другий ключ встигає зареєструвати свій inflight, поки перший транскодить.
func TestSemaphoreTakenOutsideLock(t *testing.T) {
	dir := t.TempDir()
	source := writeFile(t, filepath.Join(dir, "src.mp4"), "source")
	cache := NewCache(filepath.Join(dir, "cache"), 1, 1)
	probe := &spawnProbe{gate: make(chan struct{}), started: make(chan struct{}, 2)}
	cache.spawn = probe.run

	go cache.GetOrBuild(source, plainTarget(4000))
	<-probe.started // перший тримає єдиний слот
	go cache.GetOrBuild(source, plainTarget(6000))

	waitFor(t, "the second key to register its in-flight entry", func() bool {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		return len(cache.inflight) == 2
	})
	_ = cache.ActiveTranscodes() // лок кеша вільний, поки чекаємо слот
	close(probe.gate)
	waitFor(t, "both transcodes to finish", func() bool {
		_, calls := probe.snapshot()
		return calls == 2
	})
	if peak, _ := probe.snapshot(); peak != 1 {
		t.Fatalf("the semaphore let %d transcodes run at once", peak)
	}
}

func TestThreadArgs(t *testing.T) {
	dir := t.TempDir()
	cache := NewCache(filepath.Join(dir, "t1"), 1, 1)
	want := []string{"-threads", "1", "-filter_threads", "1", "-filter_complex_threads", "1"}
	if got := cache.ThreadArgs(); !equalArgs(got, want) {
		t.Fatalf("default thread budget: %v", got)
	}
	if got := NewCache(filepath.Join(dir, "t2"), 1, 0).ThreadArgs(); len(got) != 0 {
		t.Fatalf("zero means: leave it to ffmpeg, got %v", got)
	}
	if got := cache.EncoderThreadArgs(); !equalArgs(got, []string{"-threads", "1"}) {
		t.Fatalf("the encoder gets its own limit: %v", got)
	}

	ladder := TargetParams{Ladder: []Rung{{1920, 1080, 60, 6000}}, Channels: 2,
		SampleRate: 48000, AudioBitrateKbps: 160}
	cmd := BuildLadderCommand("/src.mp4", "/out.ts", ladder, cache.ThreadArgs(), cache.EncoderThreadArgs())
	first, input := indexOf(cmd, "-threads", 0), indexOf(cmd, "-i", 0)
	if first < 0 || first > input {
		t.Fatalf("the ladder command does not limit the decoder side: %v", cmd)
	}
	if second := indexOf(cmd, "-threads", input); second < 0 || second >= len(cmd)-1 {
		t.Fatalf("the ladder command does not limit the encoder side: %v", cmd)
	}
	if bare := BuildLadderCommand("/src.mp4", "/out.ts", ladder, nil, nil); indexOf(bare, "-threads", 0) >= 0 {
		t.Fatalf("callers that pass nothing keep the old command: %v", bare)
	}

	plain := BuildSingleCommand("/src.mp4", "/out.mp4", plainTarget(6000),
		cache.ThreadArgs(), cache.EncoderThreadArgs())
	if at := indexOf(plain, "-threads", 0); at < 0 || at > indexOf(plain, "-i", 0) {
		t.Fatalf("the plain transcode does not limit the decoder side: %v", plain)
	}
	if n := countOf(plain, "-threads"); n != 2 {
		t.Fatalf("the plain transcode limits %d sides, want both", n)
	}
}

// TestLowPriorityChild — транскод іде на найнижчому пріоритеті, а все, що
// обслуговує ефір, лишається на звичайному.
func TestLowPriorityChild(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("nice values are read from /proc")
	}
	niceOf := func(pid int) string {
		raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			t.Fatal(err)
		}
		tail := raw[strings.LastIndex(string(raw), ")")+1:]
		return strings.Fields(string(tail))[16]
	}

	cmd := lowPrioCmd([]string{"sleep", "5"})
	if err := proc.StartCmd(cmd); err != nil {
		t.Skipf("could not start sleep: %v", err)
	}
	applyLowPrio(cmd.Process.Pid)
	if got := niceOf(cmd.Process.Pid); got != "19" {
		t.Fatalf("the transcode child runs at nice %s, want 19", got)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	plain := exec.Command("sleep", "5")
	if err := proc.StartCmd(plain); err != nil {
		t.Skipf("could not start sleep: %v", err)
	}
	if got := niceOf(plain.Process.Pid); got != "0" {
		t.Fatalf("a broadcast-serving child runs at nice %s, want 0", got)
	}
	_ = plain.Process.Kill()
	_ = plain.Wait()
}

func TestWarmStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "warm.json")
	store := NewWarmStore(path)
	if _, ok := store.Get("Twitch"); ok {
		t.Fatal("an empty store must read as empty")
	}

	live := liveParams()
	store.Put("Twitch", live, plainTarget(6000))
	entry, ok := NewWarmStore(path).Get("Twitch")
	if !ok || !entry.Target.equal(plainTarget(6000)) || entry.Live != live {
		t.Fatalf("the entry did not survive a reload: %+v", entry)
	}

	store.Put("Kick", live, plainTarget(4000))
	store.Retain([]string{"Twitch"})
	reloaded := NewWarmStore(path)
	if _, ok := reloaded.Get("Kick"); ok {
		t.Fatal("platforms that are gone must be pruned")
	}
	if _, ok := reloaded.Get("Twitch"); !ok {
		t.Fatal("and the ones that remain must not")
	}

	ladder := TargetParams{Ladder: []Rung{{1920, 1080, 60, 6000}, {1280, 720, 30, 3000}},
		Channels: 2, SampleRate: 48000, AudioBitrateKbps: 160}
	store.Put("EB", live, ladder)
	if entry, ok := NewWarmStore(path).Get("EB"); !ok || !entry.Target.equal(ladder) {
		t.Fatalf("a ladder target did not survive a reload: %+v", entry)
	}

	writeFile(t, path, "{ not json")
	if _, ok := NewWarmStore(path).Get("Twitch"); ok {
		t.Fatal("a corrupted file must not be fatal")
	}
}

func testPreparer(t *testing.T, opts PreparerOptions) (*Preparer, string) {
	t.Helper()
	dir := t.TempDir()
	loop := writeFile(t, filepath.Join(dir, "loop.mp4"), "loop")
	if opts.Cache == nil {
		opts.Cache = NewCache(filepath.Join(dir, "cache"), 1, 1)
	}
	opts.Kind, opts.Loop = KindSequence, loop
	return NewPreparer(opts), loop
}

// TestWarmAndStaleTargets — прогрів під збережену ціль і скидання артефактів
// минулої цілі.
func TestWarmAndStaleTargets(t *testing.T) {
	preparer, loop := testPreparer(t, PreparerOptions{})
	artifact := writeFile(t, filepath.Join(t.TempDir(), "art.mp4"), "art")
	var built []TargetParams
	preparer.normalize = func(_ string, _ LiveParams, target TargetParams) string {
		built = append(built, target)
		return artifact
	}

	live := liveParams()
	preparer.PrepareWarm(live, plainTarget(6000), nil)
	if got := preparer.SegmentSource("loop"); filepath.Base(got) != "art.mp4" {
		t.Fatalf("warming did not build the segment: %q", got)
	}
	if _, ok := preparer.LastLiveParams(); ok {
		t.Fatal("warming must not fake live parameters for the dashboard")
	}
	if !preparer.Progress().Started {
		t.Fatal("warming must still report progress")
	}

	preparer.buildAll(live, plainTarget(6000), nil)
	if got := preparer.SegmentSource("loop"); filepath.Base(got) != "art.mp4" {
		t.Fatalf("re-running the same target dropped the artifact: %q", got)
	}

	var stale []TargetParams
	preparer.normalize = func(_ string, _ LiveParams, target TargetParams) string {
		stale = append(stale, target)
		return ""
	}
	preparer.buildAll(live, plainTarget(8000), nil)
	if got := preparer.SegmentSource("loop"); got != loop {
		t.Fatalf("a changed target must drop the artifact built for the old one: %q", got)
	}
	if len(stale) != 1 || stale[0].VideoBitrateKbps != 8000 {
		t.Fatalf("the rebuild did not use the new target: %+v", stale)
	}
	if progress := preparer.Progress(); progress.ReadyFiles != 0 || progress.FailedFiles != 1 {
		t.Fatalf("progress was not reset with the artifacts: %+v", progress)
	}
}

// TestWarmAbortsOnPublish — прогрів зупиняється на першій публікації, лишаючи
// вже підготовлене.
func TestWarmAbortsOnPublish(t *testing.T) {
	dir := t.TempDir()
	folder := filepath.Join(dir, "clips")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	var files []string
	for i := 0; i < 5; i++ {
		files = append(files, writeFile(t, filepath.Join(folder, "clip"+strconv.Itoa(i)+".mp4"), "clip"))
	}
	preparer := NewPreparer(PreparerOptions{
		Kind: KindFolder, FolderFiles: files, Cache: NewCache(filepath.Join(dir, "cache"), 1, 1)})

	abort := make(chan struct{})
	var seen []string
	preparer.normalize = func(src string, _ LiveParams, _ TargetParams) string {
		seen = append(seen, src)
		if len(seen) == 2 {
			close(abort) // «почалась публікація»
		}
		return src
	}
	preparer.PrepareWarm(liveParams(), plainTarget(6000), abort)
	if len(seen) != 2 {
		t.Fatalf("warming did not stop on publish: %d files normalized", len(seen))
	}
	if ready := preparer.FolderReadyFiles(); len(ready) != 2 {
		t.Fatalf("warming dropped what it already prepared: %v", ready)
	}
}

// TestTargetIsReportedForNextStart — ціль віддається назовні для прогріву
// наступного запуску і тримається дедбендом.
func TestTargetIsReportedForNextStart(t *testing.T) {
	var noted []TargetParams
	preparer, _ := testPreparer(t, PreparerOptions{
		Bitrate:        func() (int, int) { return 4400, 150 },
		OnTargetParams: func(_ LiveParams, target TargetParams) { noted = append(noted, target) },
	})
	preparer.normalize = func(string, LiveParams, TargetParams) string { return "" }

	live := LiveParams{VideoCodec: "h264", Width: 1280, Height: 720, FPS: 30,
		AudioCodec: "aac", Channels: 2, SampleRate: 48000}
	preparer.buildForLive(live)
	if len(noted) != 1 {
		t.Fatalf("the resolved target was not handed over: %+v", noted)
	}
	if noted[0].VideoBitrateKbps != 4500 || noted[0].AudioBitrateKbps != 160 {
		t.Fatalf("the measured bitrate was not snapped to the ladder: %+v", noted[0])
	}

	next, _ := testPreparer(t, PreparerOptions{
		Bitrate:        func() (int, int) { return 4600, 150 },
		PreviousTarget: noted[0],
	})
	target, ok := next.targetParams(live)
	if !ok || target.VideoBitrateKbps != 4500 {
		t.Fatalf("a jittery next session must reuse the same cache key: %+v", target)
	}
}

// TestLadderNeverFallsBackToRaw — F7: сирий однорендішенний файл не годиться
// драбині ніколи, навіть поки артефакт не готовий.
func TestLadderNeverFallsBackToRaw(t *testing.T) {
	preparer, _ := testPreparer(t, PreparerOptions{LadderMode: true})
	if got := preparer.SegmentSource("loop"); got != "" {
		t.Fatalf("the ladder arm handed out a raw file: %q", got)
	}
	plain, loop := testPreparer(t, PreparerOptions{})
	if got := plain.SegmentSource("loop"); got != loop {
		t.Fatalf("the sequence arm must fall back to the raw Loop: %q", got)
	}
	if preparer.HasLadder() {
		t.Fatal("no ladder was set yet")
	}
	preparer.SetLadder([]LadderRung{{Width: 1920, Height: 1080}})
	if !preparer.HasLadder() {
		t.Fatal("the ladder was not stored")
	}
	target, ok := preparer.targetParams(liveParams())
	if !ok || len(target.Ladder) != 1 {
		t.Fatalf("ladder target: %+v", target)
	}
	// fps/бітрейт сходинки не оголошені -> беруться з live і виміру.
	if target.Ladder[0].FPS != 60 || target.Ladder[0].VideoBitrateKbps != defaultVideoBitrateKbps {
		t.Fatalf("undeclared rung fields were not filled from live: %+v", target.Ladder[0])
	}
	if target.Width != 0 || target.VideoBitrateKbps != 0 {
		t.Fatalf("a ladder target must not carry a single geometry: %+v", target)
	}
}

func TestProgressCountsBytes(t *testing.T) {
	dir := t.TempDir()
	loop := writeFile(t, filepath.Join(dir, "loop.mp4"), "0123456789")
	start := writeFile(t, filepath.Join(dir, "start.mp4"), "01234")
	preparer := NewPreparer(PreparerOptions{Kind: KindSequence, Loop: loop, Start: start,
		Cache: NewCache(filepath.Join(dir, "cache"), 1, 1)})
	if progress := preparer.Progress(); progress.Started || progress.TotalFiles != 2 ||
		progress.TotalBytes != 15 || progress.ReadyBytes != 0 {
		t.Fatalf("before the first start: %+v", progress)
	}
	preparer.normalize = func(src string, _ LiveParams, _ TargetParams) string {
		if src == start {
			return ""
		}
		return src
	}
	preparer.PrepareWarm(liveParams(), plainTarget(6000), nil)
	progress := preparer.Progress()
	if !progress.Started || progress.ReadyFiles != 1 || progress.FailedFiles != 1 ||
		progress.ReadyBytes != 10 || progress.Current != "" {
		t.Fatalf("after warming: %+v", progress)
	}
}

// Розмір файла, якого ще не було, не кешується нулем назавжди.
func TestMissingFileSizeIsNotCachedAsZero(t *testing.T) {
	dir := t.TempDir()
	loop := writeFile(t, filepath.Join(dir, "loop.mp4"), "0123456789")
	late := filepath.Join(dir, "start.mp4")
	preparer := NewPreparer(PreparerOptions{Kind: KindSequence, Loop: loop, Start: late,
		Cache: NewCache(filepath.Join(dir, "cache"), 1, 1)})

	if got := preparer.Progress().TotalBytes; got != 10 {
		t.Fatalf("TotalBytes = %d before the file appears, want just the loop", got)
	}
	writeFile(t, late, "01234")
	if got := preparer.Progress().TotalBytes; got != 15 {
		t.Fatalf("TotalBytes = %d after the file appeared, want 15", got)
	}
	// Розмір наявного файла кешується: підміна вмісту вже не читається.
	writeFile(t, late, "0123456789")
	if got := preparer.Progress().TotalBytes; got != 15 {
		t.Fatalf("TotalBytes = %d, want the cached 15", got)
	}
}

// Зміна цілі чистить і прогресні хвости минулої.
func TestTargetChangeClearsProgressTails(t *testing.T) {
	preparer, loop := testPreparer(t, PreparerOptions{})
	artifact := writeFile(t, filepath.Join(t.TempDir(), "art.mp4"), "art")
	preparer.normalize = func(string, LiveParams, TargetParams) string { return artifact }
	preparer.buildAll(liveParams(), plainTarget(6000), nil)
	if got := preparer.Progress().TotalBytes; got == 0 {
		t.Fatal("the first build must measure the source")
	}

	preparer.mu.Lock()
	preparer.current = "loop.mp4"
	preparer.sizes[loop] = 999999
	preparer.mu.Unlock()

	preparer.normalize = func(string, LiveParams, TargetParams) string { return "" }
	preparer.buildAll(liveParams(), plainTarget(3000), nil)
	progress := preparer.Progress()
	if progress.Current != "" {
		t.Fatalf("progress still shows %q from the discarded target", progress.Current)
	}
	if progress.TotalBytes != 4 {
		t.Fatalf("TotalBytes = %d, want the re-measured source (4)", progress.TotalBytes)
	}
}

func TestSourceIdentity(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, filepath.Join(dir, "clip.mp4"), "0123456789")
	stamp := time.Unix(1700000000, 987654321)
	if err := os.Chtimes(file, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	id, ok := identityOf(file)
	if !ok || id.MTime != 1700000000 || id.Size != 10 {
		t.Fatalf("identity: %+v (ok=%v)", id, ok)
	}
	if !filepath.IsAbs(id.Path) {
		t.Fatalf("identity path is not absolute: %s", id.Path)
	}

	link := filepath.Join(dir, "link.mp4")
	if err := os.Symlink(file, link); err != nil {
		t.Logf("symlinks unavailable: %v", err)
	} else {
		linked, ok := identityOf(link)
		if !ok || linked != id {
			t.Fatalf("a symlink must resolve to its target: %+v vs %+v", linked, id)
		}
	}
	if _, ok := identityOf(filepath.Join(dir, "missing.mp4")); ok {
		t.Fatal("a missing source must have no identity")
	}
}

func TestArtifactValidity(t *testing.T) {
	dir := t.TempDir()
	source := writeFile(t, filepath.Join(dir, "src.mp4"), "source")
	id, _ := identityOf(source)
	target := plainTarget(6000)
	artifact := writeFile(t, filepath.Join(dir, "art.mp4"), "x")
	meta := filepath.Join(dir, "art.json")

	if artifactValid(artifact, meta, id, target) {
		t.Fatal("no sidecar -> not valid")
	}
	writeFile(t, meta, string(sidecarBytes(id, target)))
	if !artifactValid(artifact, meta, id, target) {
		t.Fatal("a matching sidecar must validate the artifact")
	}
	if artifactValid(artifact, meta, id, plainTarget(8000)) {
		t.Fatal("another target must not reuse the artifact")
	}
	stale := id
	stale.MTime++
	if artifactValid(artifact, meta, stale, target) {
		t.Fatal("a touched source must not reuse the artifact")
	}
	// Порядок ключів у чужому sidecar на валідність не впливає.
	writeFile(t, meta, `{"target": `+pyDumps(target.json(true))+`, "source": `+pyDumps(id.json(true))+`}`)
	if !artifactValid(artifact, meta, id, target) {
		t.Fatal("sidecar comparison must be semantic, not byte-wise")
	}
	writeFile(t, meta, "{ not json")
	if artifactValid(artifact, meta, id, target) {
		t.Fatal("a corrupted sidecar must invalidate the artifact")
	}
}

// Артефакт без sidecar не валідний ніколи — при збої sidecar його бути не має.
func TestPublishWritesSidecarFirst(t *testing.T) {
	dir := t.TempDir()
	source := writeFile(t, filepath.Join(dir, "src.mp4"), "source")
	id, _ := identityOf(source)
	target := plainTarget(6000)
	artifact := filepath.Join(dir, "art.mp4")
	meta := filepath.Join(dir, "art.json")
	tmp := writeFile(t, tmpArtifactPath(artifact), "art")

	if err := os.Mkdir(meta, 0o755); err != nil { // сюди WriteFile не зможе
		t.Fatal(err)
	}
	if publishArtifact(tmp, artifact, meta, id, target) {
		t.Fatal("publishing must fail when the sidecar cannot be written")
	}
	if _, err := os.Stat(artifact); err == nil {
		t.Fatal("the artifact must not appear without its sidecar")
	}

	if err := os.Remove(meta); err != nil {
		t.Fatal(err)
	}
	if !publishArtifact(tmp, artifact, meta, id, target) {
		t.Fatal("the happy path must publish")
	}
	if !artifactValid(artifact, meta, id, target) {
		t.Fatal("a published artifact must be valid right away")
	}
}

// TestLadderNeedsTheBodyNotJustTheEdges — F7: поки готові лише start/end/
// separator, драбині грати нічого, і наглядач мусить чекати далі.
func TestLadderNeedsTheBodyNotJustTheEdges(t *testing.T) {
	dir := t.TempDir()
	preparer := NewPreparer(PreparerOptions{
		Kind:       KindSequence,
		Loop:       writeFile(t, filepath.Join(dir, "loop.mp4"), "loop"),
		Start:      writeFile(t, filepath.Join(dir, "start.mp4"), "start"),
		LadderMode: true,
		Cache:      NewCache(filepath.Join(dir, "cache"), 1, 1),
	})
	preparer.artifacts["start"] = "start.ts"
	preparer.artifacts["end"] = "end.ts"
	if preparer.HasReadySegment() {
		t.Fatal("start/end alone are not something the player can put on air")
	}
	preparer.artifacts["loop"] = "loop.ts"
	if !preparer.HasReadySegment() {
		t.Fatal("the ladder body makes it ready")
	}

	folder := NewPreparer(PreparerOptions{Kind: KindFolder, LadderMode: true,
		Cache: NewCache(filepath.Join(dir, "cache"), 1, 1)})
	folder.artifacts["separator"] = "sep.ts"
	if folder.HasReadySegment() {
		t.Fatal("a separator without clips is not ready either")
	}
	folder.folderReady = []string{"clip.ts"}
	if !folder.HasReadySegment() {
		t.Fatal("a prepared clip makes the folder arm ready")
	}

	plain, _ := testPreparer(t, PreparerOptions{})
	if !plain.HasReadySegment() {
		t.Fatal("the sequence arm still falls back to the raw Loop")
	}
}

// TestWarmTargetHoldsTheDeadband — F9 діє й між прогрівом і реальним стартом
// у одному процесі, не лише між сесіями.
func TestWarmTargetHoldsTheDeadband(t *testing.T) {
	preparer, _ := testPreparer(t, PreparerOptions{Bitrate: func() (int, int) { return 4600, 150 }})
	preparer.normalize = func(string, LiveParams, TargetParams) string { return "" }

	if target, ok := preparer.targetParams(liveParams()); !ok || target.VideoBitrateKbps != 5000 {
		t.Fatalf("without a previous target the measurement snaps up: %+v", target)
	}
	preparer.PrepareWarm(liveParams(), plainTarget(6000), nil)
	target, ok := preparer.targetParams(liveParams())
	if !ok || target.VideoBitrateKbps != 6000 {
		t.Fatalf("the warmed target must survive into the real start: %+v", target)
	}
}

func indexOf(args []string, want string, from int) int {
	for i := from; i < len(args); i++ {
		if args[i] == want {
			return i
		}
	}
	return -1
}

func countOf(args []string, want string) int {
	n := 0
	for _, arg := range args {
		if arg == want {
			n++
		}
	}
	return n
}
