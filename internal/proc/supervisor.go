package proc

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Пороги послідовності зупинки: wait(timeout) → terminate → wait →
// kill → wait.
const (
	stopKillWait = 3 * time.Second
	stopReapWait = 5 * time.Second
)

// Options — keyword-параметри конструктора FfmpegProcess.
type Options struct {
	CaptureStdout bool
	StdinPipe     bool
	OnStart       func(*Started)
	OnExit        func()
	OnFlapping    func(neverSucceeded bool)
}

// Supervisor тримає живим один довгоживучий зовнішній процес: перезапускає,
// поки його хочуть, і сигналить назовні про флепінг.
type Supervisor struct {
	name          string
	args          func() []string
	logPath       string
	captureStdout bool
	stdinPipe     bool
	onStart       func(*Started)
	onExit        func()
	onFlapping    func(bool)

	spawn func(spawnSpec) (child, error)
	sleep func(time.Duration)

	restartBackoff         time.Duration
	flappingExitThreshold  time.Duration
	flappingCountThreshold int
	everSucceededThreshold time.Duration
	killWait               time.Duration
	reapWait               time.Duration

	mu                    sync.Mutex
	proc                  child
	desired               bool
	epoch                 uint64
	done                  chan struct{}
	consecutiveEarlyExits int
	everRanLong           bool
	restartCount          int
	startedAt             time.Time

	inHook atomic.Bool
}

// NewSupervisor — порт FfmpegProcess.__init__; args кличеться заново перед
// КОЖНИМ (пере)запуском.
func NewSupervisor(name string, args func() []string, logDir string, opts Options) *Supervisor {
	return &Supervisor{
		name:                   name,
		args:                   args,
		logPath:                filepath.Join(logDir, "ffmpeg-"+name+".log"),
		captureStdout:          opts.CaptureStdout,
		stdinPipe:              opts.StdinPipe,
		onStart:                opts.OnStart,
		onExit:                 opts.OnExit,
		onFlapping:             opts.OnFlapping,
		spawn:                  spawnExec,
		sleep:                  time.Sleep,
		restartBackoff:         RestartBackoff,
		flappingExitThreshold:  FlappingExitThreshold,
		flappingCountThreshold: FlappingCountThreshold,
		everSucceededThreshold: EverSucceededThreshold,
		killWait:               stopKillWait,
		reapWait:               stopReapWait,
	}
}

func (s *Supervisor) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proc != nil && s.proc.alive()
}

// PID — pid живого процесу; ok=false, якщо процесу немає.
func (s *Supervisor) PID() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc == nil {
		return 0, false
	}
	return s.proc.pid(), true
}

// EverRanLong — чи хоч один запуск від останнього Start протримався довше
// EverSucceededThreshold.
func (s *Supervisor) EverRanLong() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.everRanLong
}

func (s *Supervisor) RestartCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restartCount
}

// UptimeSec — тривалість поточного живого запуску (0, якщо не працює).
func (s *Supervisor) UptimeSec() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc == nil || !s.proc.alive() || s.startedAt.IsZero() {
		return 0.0
	}
	return time.Since(s.startedAt).Seconds()
}

func (s *Supervisor) Start() {
	s.mu.Lock()
	if s.desired {
		s.mu.Unlock()
		return
	}
	s.desired = true
	s.consecutiveEarlyExits = 0
	s.everRanLong = false
	s.restartCount = 0
	s.startedAt = time.Time{}
	s.epoch++
	epoch := s.epoch
	done := make(chan struct{})
	s.done = done
	s.mu.Unlock()
	go s.runSupervised(done, epoch)
}

// wantedLocked — чи цей запуск усе ще чинний: Start після недожатого Stop
// піднімає новий, і старий мусить піти.
func (s *Supervisor) wantedLocked(epoch uint64) bool {
	return s.desired && s.epoch == epoch
}

func (s *Supervisor) Stop(timeout time.Duration) {
	s.mu.Lock()
	s.desired = false
	c := s.proc
	done := s.done
	s.mu.Unlock()
	if c == nil {
		return
	}

	log.Printf("stopping ffmpeg[%s] (pid=%d)", s.name, c.pid())
	s.fireOnExit(false)

	if s.stdinPipe && c.hasStdin() {
		// Чистий EOF на pipe:0 замість SIGTERM: ffmpeg сам коректно закриває
		// вихідне з'єднання, не чекаючи сигналу під блокуючим читанням.
		c.closeStdin()
	} else {
		c.terminate()
	}
	if !c.waitFor(timeout) {
		log.Printf("ffmpeg[%s] did not exit within %.1fs -- terminate/kill", s.name, timeout.Seconds())
		c.terminate()
		if !c.waitFor(s.killWait) {
			c.kill()
			c.waitFor(s.reapWait)
		}
	}

	s.mu.Lock()
	// Поки йшла kill-послідовність, новий Start міг покласти сюди свій процес.
	if s.proc == c {
		s.proc = nil
	}
	s.mu.Unlock()
	if done == nil || s.inHook.Load() {
		return
	}
	select {
	case <-done:
	case <-time.After(timeout + 2*time.Second):
	}
}

// hook: помилка в хуку не валить супервізора (аналог logging.exception).
func (s *Supervisor) hook(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ffmpeg[%s]: error in %s hook: %v", s.name, name, r)
		}
	}()
	fn()
}

// supHook — хук із горутини супервізора: поки він виконується, Stop із нього
// не чекає завершення цієї ж горутини.
func (s *Supervisor) supHook(name string, fn func()) {
	s.inHook.Store(true)
	defer s.inHook.Store(false)
	s.hook(name, fn)
}

func (s *Supervisor) fireOnStart(st *Started) {
	if s.onStart == nil {
		return
	}
	s.supHook("on_start", func() { s.onStart(st) })
}

func (s *Supervisor) fireOnExit(fromSupervisor bool) {
	if s.onExit == nil {
		return
	}
	if fromSupervisor {
		s.supHook("on_exit", s.onExit)
		return
	}
	s.hook("on_exit", s.onExit)
}

func (s *Supervisor) fireOnFlapping(neverSucceeded bool) {
	if s.onFlapping == nil {
		return
	}
	s.supHook("on_flapping", func() { s.onFlapping(neverSucceeded) })
}

// runSupervised — схема рішень FfmpegProcess._run_supervised: до першого
// довгого запуску досить ОДНІЄЇ невдачі, після нього — нескінченний ретрай із
// порогом N швидких падінь поспіль.
func (s *Supervisor) runSupervised(done chan struct{}, epoch uint64) {
	defer close(done)
	logFile, err := os.OpenFile(s.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("ffmpeg[%s]: could not open %s: %v", s.name, s.logPath, err)
		return
	}
	defer logFile.Close()

	var prev child
	release := func() {
		if prev != nil {
			prev.closePipes()
			prev = nil
		}
	}
	defer release()

	for {
		s.mu.Lock()
		if !s.wantedLocked(epoch) {
			s.mu.Unlock()
			return
		}
		args := s.args()
		log.Printf("starting ffmpeg[%s]: %s", s.name, strings.Join(args, " "))
		startedAt := time.Now()
		c, err := s.spawn(spawnSpec{
			args:          args,
			logFile:       logFile,
			captureStdout: s.captureStdout,
			stdinPipe:     s.stdinPipe,
		})
		if err != nil {
			// Збій запуску = запуск нульової тривалості: далі те саме рішення
			// про флепінг і ретрай, що й після миттєвої смерті.
			s.mu.Unlock()
			log.Printf("ffmpeg[%s]: could not start: %v", s.name, err)
		} else {
			s.proc = c
			s.startedAt = startedAt
			s.mu.Unlock()
			release()
			prev = c
			s.fireOnStart(c.started())
			c.wait()
		}
		ranFor := time.Since(startedAt)

		s.mu.Lock()
		exitedDesired := s.wantedLocked(epoch)
		// on_exit детачить sink, який уже належить новому запуску.
		superseded := s.epoch != epoch
		if c != nil && s.proc == c {
			s.proc = nil
		}
		s.mu.Unlock()
		if !superseded {
			s.fireOnExit(true)
		}

		if !exitedDesired {
			return
		}

		s.mu.Lock()
		// Довгий запуск сам по собі доводить, що вхід/вихід робочі —
		// зараховуємо його ДО рішення нижче.
		if ranFor >= s.everSucceededThreshold {
			s.everRanLong = true
		}
		flapping, neverSucceeded := false, false
		if !s.everRanLong {
			flapping, neverSucceeded = true, true
		} else if ranFor < s.flappingExitThreshold {
			s.consecutiveEarlyExits++
			flapping = s.consecutiveEarlyExits == s.flappingCountThreshold
		} else {
			s.consecutiveEarlyExits = 0
		}
		s.mu.Unlock()
		if flapping {
			s.fireOnFlapping(neverSucceeded)
		}

		s.mu.Lock()
		// on_flapping міг сам покликати Stop — перевіряємо ЗНОВУ.
		if !s.wantedLocked(epoch) {
			s.mu.Unlock()
			return
		}
		s.restartCount++
		s.mu.Unlock()
		log.Printf("ffmpeg[%s] exited unexpectedly, restarting in %.1fs",
			s.name, s.restartBackoff.Seconds())
		s.sleep(s.restartBackoff)
	}
}
