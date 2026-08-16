package egress

import (
	"errors"
	"io"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

// deaf — з'єднання, яке не вмирає від Teardown (лише від kill у тесті).
type fakeConn struct {
	host      string
	failWith  error
	dead      chan struct{}
	deadOnce  sync.Once
	closes    atomic.Int32
	teardowns atomic.Int32
	deaf      bool
}

func newFakeConn() *fakeConn {
	return &fakeConn{host: "fake.host", dead: make(chan struct{})}
}

func (f *fakeConn) Host() string { return f.host }

func (f *fakeConn) ConnectAndPublish() error { return f.failWith }

func (f *fakeConn) IsAlive() bool {
	select {
	case <-f.dead:
		return false
	default:
		return true
	}
}

func (f *fakeConn) WaitDead() { <-f.dead }

func (f *fakeConn) kill() { f.deadOnce.Do(func() { close(f.dead) }) }

func (f *fakeConn) Teardown() {
	f.teardowns.Add(1)
	if !f.deaf {
		f.kill()
	}
}

func (f *fakeConn) Close() {
	f.closes.Add(1)
	f.kill()
}

func (f *fakeConn) WriteHeader() error                 { return nil }
func (f *fakeConn) WriteTag(byte, int64, []byte) error { return nil }

// harness — інʼєктована фабрика зʼєднань плюс журнал хуків.
type harness struct {
	mu     sync.Mutex
	conns  []*fakeConn
	urls   []string
	codecs [][]string
	starts int
	exits  int
	flaps  []bool

	script func(attempt int, conn *fakeConn)
}

func (h *harness) factory(_, url string, codecs []string) (PushConn, error) {
	h.mu.Lock()
	attempt := len(h.conns)
	conn := newFakeConn()
	h.conns = append(h.conns, conn)
	h.urls = append(h.urls, url)
	h.codecs = append(h.codecs, codecs)
	script := h.script
	h.mu.Unlock()
	if script != nil {
		script(attempt, conn)
	}
	return conn, nil
}

func (h *harness) conn(i int) *fakeConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i >= len(h.conns) {
		return nil
	}
	return h.conns[i]
}

func (h *harness) attempts() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

func (h *harness) counts() (starts, exits int, flaps []bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.starts, h.exits, append([]bool(nil), h.flaps...)
}

func (h *harness) flapCount(neverSucceeded bool) int {
	_, _, flaps := h.counts()
	n := 0
	for _, f := range flaps {
		if f == neverSucceeded {
			n++
		}
	}
	return n
}

func (h *harness) options() RtmpPushOptions {
	return RtmpPushOptions{
		OnStart: func(PushConn) {
			h.mu.Lock()
			h.starts++
			h.mu.Unlock()
		},
		OnExit: func() {
			h.mu.Lock()
			h.exits++
			h.mu.Unlock()
		},
		OnFlapping: func(neverSucceeded bool) {
			h.mu.Lock()
			h.flaps = append(h.flaps, neverSucceeded)
			h.mu.Unlock()
		},
	}
}

// newTestClient — клієнт на фейковій фабриці зі скороченими порогами.
func newTestClient(t *testing.T, h *harness, opts RtmpPushOptions) *RtmpPushClient {
	t.Helper()
	c := NewRtmpPushClient("test", func() string { return "rtmp://host/app/key" }, opts)
	c.newConn = h.factory
	c.restartBackoff = 5 * time.Millisecond
	c.flappingExitThreshold = 100 * time.Millisecond
	c.everSucceededThreshold = 200 * time.Millisecond
	t.Cleanup(func() { c.Stop(time.Second) })
	return c
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitDone(t *testing.T, c *RtmpPushClient) {
	t.Helper()
	c.mu.Lock()
	done := c.done
	c.mu.Unlock()
	if done == nil {
		t.Fatal("supervisor was never started")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor goroutine did not exit")
	}
}

// (а) до першого успіху КОЖНА невдача = on_flapping(true), ретраї тривають.
func TestFlappingBeforeFirstSuccess(t *testing.T) {
	h := &harness{script: func(_ int, conn *fakeConn) {
		conn.failWith = errors.New("connection refused")
	}}
	c := newTestClient(t, h, h.options())
	c.Start()

	waitFor(t, "three failed attempts", func() bool { return h.flapCount(true) >= 3 })
	if got := h.flapCount(false); got != 0 {
		t.Fatalf("on_flapping(false) fired %d times before any success", got)
	}
	starts, exits, _ := h.counts()
	if starts != 0 {
		t.Fatalf("on_start fired %d times without a successful connect", starts)
	}
	if exits < 3 {
		t.Fatalf("on_exit fired %d times for %d failed attempts", exits, h.attempts())
	}
	if c.RestartCount() < 3 {
		t.Fatalf("restart count %d after %d attempts", c.RestartCount(), h.attempts())
	}
	if c.EverRanLong() {
		t.Fatal("ever_ran_long set without a long run")
	}
	// Кожна невдала спроба закриває своє зʼєднання.
	for i := 0; i < 3; i++ {
		if n := h.conn(i).closes.Load(); n == 0 {
			t.Fatalf("conn %d was not closed", i)
		}
	}
	before := h.attempts()
	waitFor(t, "retries to continue", func() bool { return h.attempts() > before })
	c.Stop(50 * time.Millisecond)
	waitDone(t, c)
}

// (б) довгий запуск, далі швидкі падіння: on_flapping(false) рівно раз на третьому.
func TestFlappingCountAfterLongRun(t *testing.T) {
	h := &harness{}
	h.script = func(attempt int, conn *fakeConn) {
		if attempt == 0 {
			go func() {
				time.Sleep(300 * time.Millisecond)
				conn.kill()
			}()
			return
		}
		conn.kill() // миттєве падіння
	}
	c := newTestClient(t, h, h.options())
	c.Start()

	waitFor(t, "long run to be credited", func() bool { return c.EverRanLong() })
	waitFor(t, "three quick exits", func() bool { return h.attempts() >= 6 })
	if got := h.flapCount(false); got != 1 {
		t.Fatalf("on_flapping(false) fired %d times, want exactly 1", got)
	}
	if got := h.flapCount(true); got != 0 {
		t.Fatalf("on_flapping(true) fired %d times after a long run", got)
	}
	c.Stop(50 * time.Millisecond)
	waitDone(t, c)
}

// (в) P2: обрив здорової сесії не трактується як «ключ невалідний».
func TestHealthyRunNeverReportsNeverSucceeded(t *testing.T) {
	h := &harness{}
	h.script = func(attempt int, conn *fakeConn) {
		if attempt == 0 {
			go func() {
				time.Sleep(250 * time.Millisecond)
				conn.kill()
			}()
		}
	}
	c := newTestClient(t, h, h.options())
	c.Start()

	waitFor(t, "second attempt after the healthy session", func() bool { return h.attempts() >= 2 })
	if got := h.flapCount(true); got != 0 {
		t.Fatalf("on_flapping(true) fired %d times after a >=threshold session", got)
	}
	if !c.EverRanLong() {
		t.Fatal("ever_ran_long not set after a long session")
	}
	if c.RestartCount() != 1 {
		t.Fatalf("restart count %d after one reconnect", c.RestartCount())
	}
	c.Stop(50 * time.Millisecond)
	waitDone(t, c)
}

// (г) Stop під час живого зʼєднання: teardown будить супервізора, on_exit двічі.
func TestStopDuringActiveConnection(t *testing.T) {
	h := &harness{}
	c := newTestClient(t, h, h.options())
	c.Start()

	waitFor(t, "connection to go live", func() bool { return c.IsRunning() })
	if c.UptimeSec() <= 0 {
		t.Fatal("uptime is zero while the connection is live")
	}
	c.Stop(50 * time.Millisecond)
	waitDone(t, c)

	if n := h.conn(0).teardowns.Load(); n != 1 {
		t.Fatalf("teardown called %d times", n)
	}
	starts, exits, _ := h.counts()
	if starts != 1 {
		t.Fatalf("on_start fired %d times", starts)
	}
	if exits != 2 { // один зі Stop, один із супервізора
		t.Fatalf("on_exit fired %d times, want 2", exits)
	}
	if c.IsRunning() || c.UptimeSec() != 0 {
		t.Fatalf("client still reports running: running=%v uptime=%v", c.IsRunning(), c.UptimeSec())
	}
	if _, ok := c.PID(); ok {
		t.Fatal("PID reported for a client with no process")
	}
}

func TestAnnounceCodecsReachEveryConnection(t *testing.T) {
	h := &harness{script: func(_ int, conn *fakeConn) { conn.kill() }}
	opts := h.options()
	opts.AnnounceCodecs = []string{"avc1", "hvc1"}
	c := newTestClient(t, h, opts)
	c.Start()

	waitFor(t, "two attempts", func() bool { return h.attempts() >= 2 })
	c.Stop(50 * time.Millisecond)
	waitDone(t, c)

	h.mu.Lock()
	defer h.mu.Unlock()
	for i, got := range h.codecs {
		if len(got) != 2 || got[0] != "avc1" || got[1] != "hvc1" {
			t.Fatalf("attempt %d got announce codecs %v", i, got)
		}
	}
}

func TestStopWithoutStartIsSilent(t *testing.T) {
	h := &harness{}
	c := newTestClient(t, h, h.options())
	c.Stop(50 * time.Millisecond)

	starts, exits, flaps := h.counts()
	if starts != 0 || exits != 0 || len(flaps) != 0 {
		t.Fatalf("hooks fired on a never-started client: starts=%d exits=%d flaps=%v", starts, exits, flaps)
	}
	if h.attempts() != 0 {
		t.Fatalf("%d connections attempted", h.attempts())
	}
}

// (д) повторний Start — no-op; новий Start скидає лічильники.
func TestStartIsIdempotentAndResetsCounters(t *testing.T) {
	h := &harness{}
	c := newTestClient(t, h, h.options())
	c.Start()
	waitFor(t, "connection to go live", func() bool { return c.IsRunning() })
	c.Start()
	time.Sleep(50 * time.Millisecond)
	if h.attempts() != 1 {
		t.Fatalf("%d connections attempted after a redundant Start", h.attempts())
	}

	h.conn(0).kill() // рестарт, щоб лічильники стали ненульовими
	waitFor(t, "restart", func() bool { return c.RestartCount() > 0 })
	c.Stop(50 * time.Millisecond)
	waitDone(t, c)

	c.Start()
	defer func() {
		c.Stop(50 * time.Millisecond)
		waitDone(t, c)
	}()
	if c.RestartCount() != 0 || c.EverRanLong() {
		t.Fatalf("counters not reset by Start: restarts=%d everRanLong=%v", c.RestartCount(), c.EverRanLong())
	}
}

// Q28/Q54: Start під час недожатого Stop лишає РІВНО один супервізор.
func TestStartAfterUnfinishedStopLeavesOneSupervisor(t *testing.T) {
	h := &harness{}
	release := make(chan struct{})
	h.script = func(attempt int, conn *fakeConn) {
		if attempt == 0 {
			<-release
		}
	}
	c := newTestClient(t, h, h.options())
	c.Start()
	waitFor(t, "перший конект", func() bool { return h.attempts() == 1 })

	// Стара горутина сидить у фабриці, тож Stop її не дочекається.
	go c.Stop(10 * time.Millisecond)
	waitFor(t, "знятий desired", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return !c.desired
	})
	c.Start()
	waitFor(t, "другий конект", func() bool { return h.attempts() == 2 })
	close(release)

	waitFor(t, "старт другого зʼєднання", func() bool { starts, _, _ := h.counts(); return starts >= 1 })
	time.Sleep(100 * time.Millisecond)
	if starts, _, _ := h.counts(); starts != 1 {
		t.Fatalf("on_start fired %d times: the stale supervisor kept running", starts)
	}
	if got := h.attempts(); got != 2 {
		t.Fatalf("%d connection attempts, want 2", got)
	}
	if h.conn(0).teardowns.Load() == 0 {
		t.Fatal("the stale connection was left publishing")
	}
	c.mu.Lock()
	live := c.conn
	c.mu.Unlock()
	if live != PushConn(h.conn(1)) {
		t.Fatal("the client holds a connection from the stale supervisor")
	}
}

// Витіснена горутина не шле on_exit: він детачив би sink нового з'єднання.
func TestSupersededRunDoesNotFireExit(t *testing.T) {
	h := &harness{}
	h.script = func(attempt int, conn *fakeConn) {
		if attempt == 0 {
			conn.deaf = true
		}
	}
	c := newTestClient(t, h, h.options())
	c.Start()
	waitFor(t, "публікацію першого з'єднання", func() bool { starts, _, _ := h.counts(); return starts == 1 })

	// Stop не дочекається горутини: з'єднання не реагує на Teardown.
	go c.Stop(10 * time.Millisecond)
	waitFor(t, "знятий desired", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return !c.desired
	})
	if _, exits, _ := h.counts(); exits != 1 {
		t.Fatalf("Stop fired on_exit %d time(s), want 1", exits)
	}

	c.Start()
	waitFor(t, "публікацію другого з'єднання", func() bool { starts, _, _ := h.counts(); return starts == 2 })
	h.conn(0).kill() // стара горутина прокидається вже витісненою

	time.Sleep(100 * time.Millisecond)
	if _, exits, _ := h.counts(); exits != 1 {
		t.Fatalf("on_exit fired %d time(s): the superseded run detached the live sink", exits)
	}
	c.mu.Lock()
	live := c.conn
	c.mu.Unlock()
	if live != PushConn(h.conn(1)) {
		t.Fatal("the client lost the live connection")
	}

	// Двобічність: чинний запуск on_exit і далі шле.
	c.Stop(50 * time.Millisecond)
	if _, exits, _ := h.counts(); exits < 2 {
		t.Fatalf("on_exit count %d after stopping the live run, want it to fire", exits)
	}
}

// (е) порожній URL від провайдера: помилка справжнього rtmp.NewConn іде шляхом
// ретраю, супервізор живий.
func TestEmptyURLTakesRetryPath(t *testing.T) {
	h := &harness{}
	c := NewRtmpPushClient("test", func() string { return "" }, h.options())
	c.restartBackoff = 5 * time.Millisecond
	c.flappingExitThreshold = 100 * time.Millisecond
	c.everSucceededThreshold = 200 * time.Millisecond
	c.Start()

	waitFor(t, "retries on an invalid URL", func() bool { return h.flapCount(true) >= 3 })
	starts, _, _ := h.counts()
	if starts != 0 {
		t.Fatalf("on_start fired %d times for an invalid URL", starts)
	}
	if c.RestartCount() < 3 {
		t.Fatalf("restart count %d", c.RestartCount())
	}
	c.Stop(50 * time.Millisecond)
	waitDone(t, c)
}

// URL перечитується перед КОЖНИМ (пере)підключенням.
func TestURLProviderReadEveryAttempt(t *testing.T) {
	h := &harness{script: func(_ int, conn *fakeConn) { conn.kill() }}
	var calls atomic.Int32
	c := NewRtmpPushClient("test", func() string {
		return "rtmp://host/app/key" + string(rune('0'+calls.Add(1)%10))
	}, h.options())
	c.newConn = h.factory
	c.restartBackoff = 5 * time.Millisecond
	c.flappingExitThreshold = 100 * time.Millisecond
	c.everSucceededThreshold = 200 * time.Millisecond
	c.Start()

	waitFor(t, "three attempts", func() bool { return h.attempts() >= 3 })
	c.Stop(50 * time.Millisecond)
	waitDone(t, c)

	h.mu.Lock()
	urls := append([]string(nil), h.urls[:3]...)
	h.mu.Unlock()
	if urls[0] == urls[1] || urls[1] == urls[2] {
		t.Fatalf("url provider not re-read per attempt: %v", urls)
	}
}

// Паніка в хуку не валить супервізора.
func TestHookPanicDoesNotKillSupervisor(t *testing.T) {
	h := &harness{}
	opts := h.options()
	inner := opts.OnStart
	opts.OnStart = func(conn PushConn) {
		inner(conn)
		panic("boom")
	}
	c := newTestClient(t, h, opts)
	c.Start()

	waitFor(t, "first connection", func() bool { return h.attempts() >= 1 })
	h.conn(0).kill()
	waitFor(t, "supervisor to retry after the panic", func() bool { return h.attempts() >= 2 })
	c.Stop(50 * time.Millisecond)
	waitDone(t, c)
}

// Stop із хука (сценарій never_succeeded) не чекає власну горутину.
func TestStopFromHookDoesNotSelfWait(t *testing.T) {
	h := &harness{script: func(_ int, conn *fakeConn) {
		conn.failWith = errors.New("connection refused")
	}}
	opts := h.options()
	var c *RtmpPushClient
	stopped := make(chan time.Duration, 1)
	opts.OnFlapping = func(neverSucceeded bool) {
		if !neverSucceeded {
			return
		}
		start := time.Now()
		c.Stop(5 * time.Second)
		select {
		case stopped <- time.Since(start):
		default:
		}
	}
	c = newTestClient(t, h, opts)
	c.Start()

	select {
	case took := <-stopped:
		if took > time.Second {
			t.Fatalf("Stop from a hook waited %v for its own goroutine", took)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop from a hook never returned")
	}
	waitDone(t, c)
	if h.attempts() != 1 {
		t.Fatalf("%d attempts after stopping from the hook", h.attempts())
	}
}
