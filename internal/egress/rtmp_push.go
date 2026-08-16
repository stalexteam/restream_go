package egress

import (
	"log"
	"sync"
	"sync/atomic"
	"time"

	"restream_go/internal/proc"
	"restream_go/internal/wire/rtmp"
)

// PushConn — те, що супервізор вимагає від з'єднання і віддає в OnStart
// (реалізація — *rtmp.Conn; інтерфейс заразом шов для тестів).
type PushConn interface {
	Host() string
	ConnectAndPublish() error
	IsAlive() bool
	WaitDead()
	Teardown()
	Close()
	WriteHeader() error
	WriteTag(tagType byte, ts int64, payload []byte) error
}

// RtmpPushOptions — keyword-параметри конструктора RtmpPushClient.
type RtmpPushOptions struct {
	OnStart        func(PushConn)
	OnExit         func()
	OnFlapping     func(neverSucceeded bool)
	AnnounceCodecs []string
}

// RtmpPushClient — супервізор нативного push-з'єднання в контракті
// FfmpegProcess; urlProvider читається перед КОЖНИМ (пере)підключенням.
type RtmpPushClient struct {
	name           string
	urlProvider    func() string
	announceCodecs []string
	onStart        func(PushConn)
	onExit         func()
	onFlapping     func(neverSucceeded bool)

	newConn func(name, url string, announceCodecs []string) (PushConn, error)

	restartBackoff         time.Duration
	flappingExitThreshold  time.Duration
	flappingCountThreshold int
	everSucceededThreshold time.Duration

	mu                    sync.Mutex
	conn                  PushConn
	desired               bool
	epoch                 uint64
	done                  chan struct{}
	consecutiveEarlyExits int
	everRanLong           bool
	restartCount          int
	startedAt             time.Time

	inHook atomic.Bool
}

// NewRtmpPushClient — порт RtmpPushClient.__init__.
func NewRtmpPushClient(name string, urlProvider func() string, opts RtmpPushOptions) *RtmpPushClient {
	return &RtmpPushClient{
		name:                   name,
		urlProvider:            urlProvider,
		announceCodecs:         opts.AnnounceCodecs,
		onStart:                opts.OnStart,
		onExit:                 opts.OnExit,
		onFlapping:             opts.OnFlapping,
		newConn:                newRTMPPushConn,
		restartBackoff:         proc.RestartBackoff,
		flappingExitThreshold:  proc.FlappingExitThreshold,
		flappingCountThreshold: proc.FlappingCountThreshold,
		everSucceededThreshold: proc.EverSucceededThreshold,
	}
}

func newRTMPPushConn(name, url string, announceCodecs []string) (PushConn, error) {
	conn, err := rtmp.NewConn(name, url, announceCodecs)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (c *RtmpPushClient) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil && c.conn.IsAlive()
}

// PID — окремого процесу немає (у контракті FfmpegProcess тут pid, тут — «немає»).
func (c *RtmpPushClient) PID() (int, bool) { return 0, false }

func (c *RtmpPushClient) EverRanLong() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.everRanLong
}

func (c *RtmpPushClient) RestartCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.restartCount
}

func (c *RtmpPushClient) UptimeSec() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || !c.conn.IsAlive() || c.startedAt.IsZero() {
		return 0.0
	}
	return time.Since(c.startedAt).Seconds()
}

func (c *RtmpPushClient) Start() {
	c.mu.Lock()
	if c.desired {
		c.mu.Unlock()
		return
	}
	c.desired = true
	c.consecutiveEarlyExits = 0
	c.everRanLong = false
	c.restartCount = 0
	c.startedAt = time.Time{}
	c.epoch++
	epoch := c.epoch
	done := make(chan struct{})
	c.done = done
	c.mu.Unlock()
	go c.runSupervised(done, epoch)
}

// wantedLocked — чи цей запуск усе ще чинний.
func (c *RtmpPushClient) wantedLocked(epoch uint64) bool {
	return c.desired && c.epoch == epoch
}

func (c *RtmpPushClient) Stop(timeout time.Duration) {
	c.mu.Lock()
	wasDesired := c.desired
	c.desired = false
	conn := c.conn
	done := c.done
	c.mu.Unlock()
	if !wasDesired && conn == nil {
		return
	}
	c.fireOnExit(false)
	if conn != nil {
		conn.Teardown() // будить WaitDead супервізора
	}
	if done == nil || c.inHook.Load() {
		return
	}
	select {
	case <-done:
	case <-time.After(timeout + 2*time.Second):
	}
}

// hook: помилка в хуку не валить супервізора (аналог logging.exception).
func (c *RtmpPushClient) hook(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("rtmp-push[%s]: error in %s hook: %v", c.name, name, r)
		}
	}()
	fn()
}

// supHook — хук із горутини супервізора: поки він виконується, Stop із нього
// не чекає завершення цієї ж горутини (аналог `thread is not current_thread`).
func (c *RtmpPushClient) supHook(name string, fn func()) {
	c.inHook.Store(true)
	defer c.inHook.Store(false)
	c.hook(name, fn)
}

func (c *RtmpPushClient) fireOnStart(conn PushConn) {
	if c.onStart == nil {
		return
	}
	c.supHook("on_start", func() { c.onStart(conn) })
}

func (c *RtmpPushClient) fireOnExit(fromSupervisor bool) {
	if c.onExit == nil {
		return
	}
	if fromSupervisor {
		c.supHook("on_exit", c.onExit)
		return
	}
	c.hook("on_exit", c.onExit)
}

func (c *RtmpPushClient) fireOnFlapping(neverSucceeded bool) {
	if c.onFlapping == nil {
		return
	}
	c.supHook("on_flapping", func() { c.onFlapping(neverSucceeded) })
}

// runSupervised — та сама схема рішень, що в FfmpegProcess._run_supervised: одна
// невдача до першого успіху = невалідний ключ; після успіху — нескінченний
// ретрай із порогом N швидких падінь поспіль.
func (c *RtmpPushClient) runSupervised(done chan struct{}, epoch uint64) {
	defer close(done)
	for {
		c.mu.Lock()
		if !c.wantedLocked(epoch) {
			c.mu.Unlock()
			return
		}
		url := c.urlProvider()
		c.mu.Unlock()

		startedAt := time.Now()
		// Помилку конструктора теж ловить цикл: невалідний URL від urlProvider
		// має піти шляхом ретраю, а не вбити супервізорну горутину.
		conn, err := c.newConn(c.name, url, c.announceCodecs)
		if err == nil {
			log.Printf("rtmp-push[%s] connecting to %s", c.name, conn.Host())
			err = conn.ConnectAndPublish()
		}
		if err != nil {
			log.Printf("rtmp-push[%s] connect/publish failed: %v", c.name, err)
			if conn != nil {
				conn.Close()
			}
		} else {
			c.mu.Lock()
			if !c.wantedLocked(epoch) {
				conn.Teardown() // під локом: конн уже нікому не належить
				c.mu.Unlock()
				return
			}
			c.conn = conn
			c.startedAt = time.Now()
			c.mu.Unlock()
			c.fireOnStart(conn)
			conn.WaitDead()
		}
		ranFor := time.Since(startedAt)

		c.mu.Lock()
		exitedDesired := c.wantedLocked(epoch)
		// on_exit детачить sink, який уже належить новому запуску.
		superseded := c.epoch != epoch
		if c.conn == conn {
			c.conn = nil
		}
		c.mu.Unlock()
		if conn != nil {
			conn.Close()
		}
		if !superseded {
			c.fireOnExit(true)
		}

		if !exitedDesired {
			return
		}

		c.mu.Lock()
		// Довгий запуск сам по собі доводить, що ключ/URL робочі — зараховуємо
		// його ДО рішення нижче.
		if ranFor >= c.everSucceededThreshold {
			c.everRanLong = true
		}
		flapping, neverSucceeded := false, false
		if !c.everRanLong {
			flapping, neverSucceeded = true, true
		} else if ranFor < c.flappingExitThreshold {
			c.consecutiveEarlyExits++
			flapping = c.consecutiveEarlyExits == c.flappingCountThreshold
		} else {
			c.consecutiveEarlyExits = 0
		}
		c.mu.Unlock()
		if flapping {
			c.fireOnFlapping(neverSucceeded)
		}

		c.mu.Lock()
		if !c.wantedLocked(epoch) {
			c.mu.Unlock()
			return
		}
		c.restartCount++
		c.mu.Unlock()
		log.Printf("rtmp-push[%s] connection ended unexpectedly, retrying in %.1fs",
			c.name, c.restartBackoff.Seconds())
		time.Sleep(c.restartBackoff)
	}
}
