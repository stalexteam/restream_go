package platform

import (
	"math/rand"
	"os"
	"sync"
	"testing"
	"time"

	"restream_go/internal/fallback"
	"restream_go/internal/route"
	"restream_go/internal/wire/flv"
)

// Синтетика проводу конвеєра: ці шматки golden не покриває (він фіксує лише
// конструкцію).

var (
	keyframeTag = []byte{0x17, 0x01, 0x00, 0x00, 0x00, 0xAA}
	audioTag    = []byte{0xAF, 0x01, 0x21, 0x10}
)

type recSink struct {
	mu    sync.Mutex
	offer []byte
	count int
}

func (s *recSink) Name() string { return "rec" }

func (s *recSink) Offer(tagType byte, ts int64, payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count++
	s.offer = payload
}

func (s *recSink) n() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

type recOutput struct {
	mu      sync.Mutex
	headers int
	tags    int
}

func (o *recOutput) WriteHeader() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.headers++
	return nil
}

func (o *recOutput) WriteTag(byte, int64, []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.tags++
	return nil
}

func (o *recOutput) counts() (int, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.headers, o.tags
}

func testPipeline(t *testing.T, spec Spec, readTimeout time.Duration) *Pipeline {
	t.Helper()
	cache := fallback.NewCache(t.TempDir(), 1, 1)
	p := New(Options{
		Spec: spec,
		Deps: Deps{
			ReadbackURL: func() string { return "rtmp://127.0.0.1:1935/live/main" },
			ReadTimeout: func() time.Duration { return readTimeout },
			NewPreparer: func(bitrate fallback.BitrateProvider, ladderMode bool) *fallback.Preparer {
				return fallback.NewPreparer(fallback.PreparerOptions{
					Kind: fallback.KindSequence, Cache: cache,
					LadderMode: ladderMode, Bitrate: bitrate,
				})
			},
			Emit:   func(string, string) {},
			LogDir: t.TempDir(),
			Rand:   rand.New(rand.NewSource(1)),
		},
		Server: "rtmp://ingest.example/app", Key: "sk",
	})
	t.Cleanup(p.Sink.Close)
	return p
}

func plainSpec() Spec {
	return NewSpec(Config{Name: "P", Type: "rtmp", SourceName: "main"},
		Source{Type: "rtmp", AudioTracks: 1})
}

func writeFLVPrologue(t *testing.T, w *os.File) {
	t.Helper()
	out := flv.NewPipeOutput(w)
	if err := out.WriteHeader(); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if err := out.WriteTag(flv.TagVideo, 0, keyframeTag); err != nil {
		t.Fatalf("WriteTag: %v", err)
	}
}

func TestRelayReaderStopsOnEOF(t *testing.T) {
	p := testPipeline(t, plainSpec(), 0)
	sink := &recSink{}
	p.Switcher.RegisterSink(sink)
	p.Switcher.SetActive("relay")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	defer r.Close()
	writeFLVPrologue(t, w)
	if err := flv.NewPipeOutput(w).WriteTag(flv.TagAudio, 10, audioTag); err != nil {
		t.Fatalf("WriteTag: %v", err)
	}
	w.Close()

	done := make(chan struct{})
	go func() { defer close(done); p.ReadRelay(r) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ReadRelay не завершився на EOF")
	}
	if got := sink.n(); got != 2 {
		t.Errorf("сінк отримав %d тегів, want 2", got)
	}
}

func TestRelayReaderStallAndResume(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	defer r.Close()
	if err := r.SetReadDeadline(time.Time{}); err != nil {
		t.Skipf("пайп без дедлайнів: %v", err)
	}

	p := testPipeline(t, plainSpec(), 60*time.Millisecond)
	stalled := make(chan struct{}, 4)
	resumed := make(chan struct{}, 4)
	p.OnRelayStalled = func() { stalled <- struct{}{} }
	p.OnRelayResumed = func() { resumed <- struct{}{} }

	writeFLVPrologue(t, w)
	done := make(chan struct{})
	go func() { defer close(done); p.ReadRelay(r) }()

	select {
	case <-stalled:
	case <-time.After(3 * time.Second):
		t.Fatal("OnRelayStalled не спрацював")
	}
	if err := flv.NewPipeOutput(w).WriteTag(flv.TagAudio, 20, audioTag); err != nil {
		t.Fatalf("WriteTag: %v", err)
	}
	select {
	case <-resumed:
	case <-time.After(3 * time.Second):
		t.Fatal("OnRelayResumed не спрацював")
	}
	w.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ReadRelay не завершився після закриття пайпа")
	}
}

func TestOnOutStartAttachThenDetach(t *testing.T) {
	p := testPipeline(t, plainSpec(), 0)
	out := &recOutput{}
	p.onOutStart(out)
	p.Switcher.SetActive("relay")
	p.Switcher.Process("relay", flv.TagVideo, 0, keyframeTag)

	waitFor(t, func() bool { h, tags := out.counts(); return h == 1 && tags == 1 })

	p.Sink.Detach()
	p.Switcher.Process("relay", flv.TagAudio, 10, audioTag)
	time.Sleep(50 * time.Millisecond)
	if _, tags := out.counts(); tags != 1 {
		t.Errorf("після Detach записано %d тегів, want 1", tags)
	}
}

func TestLiveApplySetters(t *testing.T) {
	spec := NewSpec(Config{Name: "P", Type: "srt", SourceName: "main"},
		Source{Type: "srt", AudioTracks: 3})
	p := testPipeline(t, spec, 0)

	p.SetAudioMap([]int{route.Unmapped, 2, 1})
	if got := p.Audio(); got != 2 {
		t.Errorf("representative audio = %d, want 2", got)
	}
	p.SetAudioMap([]int{route.Unmapped, route.Unmapped})
	if got := p.Audio(); got != 0 {
		t.Errorf("порожня мапа має давати audio 0, дала %d", got)
	}

	p.SetAudio(4, 5)
	if p.Audio() != 4 || p.AudioVOD() != 5 {
		t.Errorf("SetAudio дало (%d,%d), want (4,5)", p.Audio(), p.AudioVOD())
	}

	before := p.URL()
	if changed := p.SetCredentials("srt://h:9000", "", "sid", "pw"); !changed {
		t.Error("SetCredentials не помітив зміни")
	}
	if p.URL() == before {
		t.Error("URL не перебудувався")
	}
	if changed := p.SetCredentials("srt://h:9000", "", "sid", "pw"); changed {
		t.Error("повторний SetCredentials із тими самими даними — не зміна")
	}
}

func TestApplyPresetSwapsPreparer(t *testing.T) {
	p := testPipeline(t, plainSpec(), 0)
	first := p.Preparer()
	if _, ok := p.ApplyPreset(); ok {
		t.Error("нового препарера не було з чого прогрівати")
	}
	if p.Preparer() == first {
		t.Error("ApplyPreset не підмінив препарер")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("умова не настала за 3с")
}
