package timeline

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"restream_go/internal/egress"
	"restream_go/internal/wire/flv"
	"restream_go/internal/wire/ts"
)

// Вихідні типи інших пакетів мусять лягати в SinkOutput без правок у них.
var (
	_ SinkOutput = (*flv.PipeOutput)(nil)
	_ SinkOutput = (*ts.MuxOutput)(nil)
	_ SinkOutput = egress.PushConn(nil)
	_ Sink       = (*OutputSink)(nil)
)

// gateOut — вихід, який можна затримати ВСЕРЕДИНІ запису: так писар ловиться
// рівно в мить attach/overflow.
type gateOut struct {
	mu      sync.Mutex
	lines   []string
	entered chan struct{}
	release chan struct{}
}

func newGateOut(blocked bool) *gateOut {
	o := &gateOut{entered: make(chan struct{}, 64), release: make(chan struct{})}
	if !blocked {
		close(o.release)
	}
	return o
}

func (o *gateOut) WriteHeader() error { o.record("header"); return nil }

func (o *gateOut) WriteTag(tagType byte, ts int64, payload []byte) error {
	o.record(fmt.Sprintf("tag %d %d", tagType, ts))
	return nil
}

func (o *gateOut) record(line string) {
	select {
	case o.entered <- struct{}{}:
	default:
	}
	<-o.release
	o.mu.Lock()
	o.lines = append(o.lines, line)
	o.mu.Unlock()
}

func (o *gateOut) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.lines...)
}

func waitEntered(t *testing.T, out *gateOut) {
	t.Helper()
	select {
	case <-out.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("writer never entered the output")
	}
}

func waitLines(t *testing.T, out *gateOut, want int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		lines := out.snapshot()
		if len(lines) >= want {
			return lines
		}
		if time.Now().After(deadline) {
			t.Fatalf("got %d lines, want %d: %v", len(lines), want, lines)
		}
		time.Sleep(time.Millisecond)
	}
}

func wantLines(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("output %v, want %v", got, want)
	}
}

func TestQueueDepthByRole(t *testing.T) {
	primary := NewOutputSink("primary", true, &virtualClock{t: 1000})
	defer primary.Close()
	restream := NewOutputSink("restream", false, &virtualClock{t: 1000})
	defer restream.Close()
	if primary.maxlen != PrimaryQueueMax || restream.maxlen != RestreamQueueMax {
		t.Fatalf("queue depths %d/%d, want %d/%d",
			primary.maxlen, restream.maxlen, PrimaryQueueMax, RestreamQueueMax)
	}
}

// Без attach-нутого виходу Offer нічого не чіпає і не блокує.
func TestOfferWithoutOutputIsNoop(t *testing.T) {
	sink := NewOutputSink("restream", false, &virtualClock{t: 1000})
	defer sink.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < RestreamQueueMax*3; i++ {
			sink.Offer(flv.TagAudio, int64(i), adata(0x40, 12))
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Offer blocked without an attached output")
	}
	sink.mu.Lock()
	queued, dropped := len(sink.queue), sink.dropped
	sink.mu.Unlock()
	if queued != 0 || dropped != 0 {
		t.Fatalf("queued=%d dropped=%d, want 0/0", queued, dropped)
	}
}

// Переповнення: черга чиститься цілком, dropped росте на КІЛЬКІСТЬ викинутих
// тегів, писар ресинкається на наступному keyframe.
func TestOverflowClearsQueueAndResyncs(t *testing.T) {
	clock := &virtualClock{t: 1000}
	out := newGateOut(true)
	sink := NewOutputSink("restream", false, clock)
	defer sink.Close()
	sink.Attach(out, nil)

	sink.Offer(flv.TagVideo, 0, vkey(0xA1, 8))
	waitEntered(t, out) // писар усередині write_header, черга порожня

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < RestreamQueueMax+50; i++ {
			sink.Offer(flv.TagAudio, int64(i), adata(0x40, 12))
		}
		sink.Offer(flv.TagVideo, 999, vkey(0xA2, 8))
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Offer blocked while the writer was stuck")
	}

	if st := sink.Stats(); st.Dropped != RestreamQueueMax || !st.Behind {
		t.Fatalf("stats %+v, want dropped=%d behind=true", st, RestreamQueueMax)
	}
	close(out.release)
	lines := waitLines(t, out, 4)
	wantLines(t, lines, []string{"header", "tag 9 0", "header", "tag 9 999"})

	clock.advance(behindWindowSec)
	if st := sink.Stats(); st.Behind {
		t.Fatalf("still behind %v after the window elapsed", st)
	}
}

// T4: теги, що надійшли ПІСЛЯ attach (той самий gen), писар не сміє вимити.
func TestWriterKeepsTagsOfferedAfterAttach(t *testing.T) {
	clock := &virtualClock{t: 1000}
	first := newGateOut(true)
	sink := NewOutputSink("restream", false, clock)
	defer sink.Close()
	sink.Attach(first, nil)
	sink.Offer(flv.TagVideo, 0, vkey(0xA1, 8))
	waitEntered(t, first) // писар зайнятий записом у старий вихід

	second := newGateOut(false)
	sink.Attach(second, map[string][]byte{"video": vseq(0x1F)})
	sink.Offer(flv.TagVideo, 10, vkey(0xA2, 8))
	sink.Offer(flv.TagAudio, 10, adata(0x40, 12))
	close(first.release)

	lines := waitLines(t, second, 4)
	wantLines(t, lines, []string{"header", "tag 9 10", "tag 9 10", "tag 8 10"})
}

func TestDetachIsIdempotent(t *testing.T) {
	out := newGateOut(false)
	sink := NewOutputSink("restream", false, &virtualClock{t: 1000})
	defer sink.Close()
	sink.Attach(out, nil)
	sink.Offer(flv.TagVideo, 0, vkey(0xA1, 8))
	waitLines(t, out, 2)

	sink.Detach()
	sink.Detach()
	sink.Offer(flv.TagVideo, 33, vkey(0xA2, 8))
	time.Sleep(20 * time.Millisecond)
	wantLines(t, out.snapshot(), []string{"header", "tag 9 0"})
}

func TestCloseStopsWriter(t *testing.T) {
	sink := NewOutputSink("restream", false, &virtualClock{t: 1000})
	sink.Close()
	select {
	case <-sink.done:
	case <-time.After(2 * time.Second):
		t.Fatal("writer goroutine still running after Close")
	}
}

// Порядок сінків — порядок реєстрації; повторна реєстрація тримає позицію
// (python-dict), unregister невідомого імені — no-op.
func TestSinkOrderIsInsertionOrder(t *testing.T) {
	rec := &traceRecorder{}
	sw := NewSwitcher(&virtualClock{t: 1000})
	sw.RegisterSink(&recSink{name: "a", rec: rec})
	sw.RegisterSink(&recSink{name: "b", rec: rec})
	sw.RegisterSink(&recSink{name: "a", rec: rec})
	sw.SetActive("relay")
	sw.Process("relay", flv.TagVideo, 0, vkey(0xA1, 8))
	if len(rec.lines) != 2 ||
		!strings.HasPrefix(rec.lines[0], "offer a ") || !strings.HasPrefix(rec.lines[1], "offer b ") {
		t.Fatalf("offers %v, want a then b", rec.lines)
	}

	sw.UnregisterSink("a")
	sw.UnregisterSink("never-registered")
	rec.lines = nil
	sw.Process("relay", flv.TagVideo, 33, vinter(0xB1, 8))
	if len(rec.lines) != 1 || !strings.HasPrefix(rec.lines[0], "offer b ") {
		t.Fatalf("offers %v, want b only", rec.lines)
	}
}
