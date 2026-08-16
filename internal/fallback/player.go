// Package fallback — гравець заглушки: Start -> Loop(∞) -> End одним
// неперервним потоком "backup".
package fallback

import (
	"bytes"
	"io"
	"log"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"restream_go/internal/proc"
	"restream_go/internal/route"
	"restream_go/internal/wire/flv"
	"restream_go/internal/wire/ts"
)

// Крок канонічного timeline між сегментами: рівно стільки, щоб мітки лишались
// строго зростаючими (33мс давали дірку В ШКАЛІ на КОЖНОМУ стику).
const stepMS = 1

// Наскільки далеко ПОПЕРЕД реального часу дозволяємо піти заглушці, і стеля
// надолуження на вході у FALLBACK (більше за буфер плеєра віддавати шкідливо).
const (
	pacingLeadSec = 0.2
	maxCatchUpSec = 3.0
)

// Скільки разів повторити АУТРО, чекаючи cut, перш ніж повернутись до тіла.
const maxEndRepeats = 3

// Фази епізоду; "" — гравець не стартував.
const (
	PhaseStart = "start"
	PhaseLoop  = "loop"
	PhaseEnd   = "end"
)

// Process — вихід гравця; сумісний із timeline.Switcher.Process і route.Emit.
type Process func(source string, tagType byte, ts int64, payload []byte)

// Clock — монотонний час у секундах.
type Clock interface{ Now() float64 }

// Sleeper — пауза пейсингу в секундах.
type Sleeper interface{ Sleep(sec float64) }

type systemClock struct{ base time.Time }

func (c systemClock) Now() float64 { return time.Since(c.base).Seconds() }

type systemSleeper struct{}

func (systemSleeper) Sleep(sec float64) { time.Sleep(time.Duration(sec * float64(time.Second))) }

// LoopItem — елемент тіла FALLBACK: файл і чи крутити його `-stream_loop -1`.
type LoopItem struct {
	Path string
	Loop bool
}

// Options — конструктор гравця. Sources(role) для role ∈ {"start","loop","end"}
// віддає готовий артефакт або "" (не заданий/не готовий); LoopNext (folder-режим)
// віддає елементи плейлиста тіла, nil — легасі single-loop через Sources("loop").
type Options struct {
	Name     string
	Process  Process
	Sources  func(role string) string
	LogDir   string
	LoopNext func() (LoopItem, bool)
	Ladder   bool
	Clock    Clock
	Sleeper  Sleeper
}

// segment — процес одного сегмента; exited знімає гонку з cmd.Wait.
type segment struct {
	cmd    *exec.Cmd
	exited atomic.Bool
}

// Player — послідовний програвач Start -> Loop(∞) -> End у джерело "backup".
type Player struct {
	name     string
	process  Process
	sources  func(role string) string
	ladder   bool
	loopNext func() (LoopItem, bool)
	logPath  string
	clock    Clock
	sleeper  Sleeper

	mu      sync.Mutex
	cv      *sync.Cond
	desired bool
	resume  bool
	restart bool
	onDone  func()
	phase   string
	proc    *segment
	pid     int
	done    chan struct{}

	// Стан канонічного timeline епізоду (скидається лише в Start).
	epOffset     int64
	lastOutTS    int64 // максимум по ВСІХ доріжках (анкер сегмента)
	trackTS      map[string]int64
	needAnchor   bool
	interrupted  bool
	segStarted   bool
	segStartedAt float64
	alignAudio   bool
	alignBase    map[string]int64 // шкала доріжок на момент якоря сегмента
	audioStep    int64
	lastSeq      map[string][]byte
	epWall0      float64
	hasEpWall0   bool
	epTS0        int64
	hasEpTS0     bool
}

// New — гравець одного плеча платформи. Clock має бути ТИМ САМИМ інстансом,
// що у switcher-а: по ньому Platform якорить фазування аутро.
func New(opts Options) *Player {
	p := &Player{
		name:     opts.Name,
		process:  opts.Process,
		sources:  opts.Sources,
		ladder:   opts.Ladder,
		loopNext: opts.LoopNext,
		logPath:  filepath.Join(opts.LogDir, "ffmpeg-"+opts.Name+".log"),
		clock:    opts.Clock,
		sleeper:  opts.Sleeper,
		trackTS:  map[string]int64{},
		lastSeq:  map[string][]byte{},
	}
	if p.clock == nil {
		p.clock = systemClock{base: time.Now()}
	}
	if p.sleeper == nil {
		p.sleeper = systemSleeper{}
	}
	p.cv = sync.NewCond(&p.mu)
	return p
}

// --- публічний інтерфейс (Platform кличе його під власним локом) ---

// Start — почати FALLBACK-епізод (Start якщо готовий -> Loop∞). Ідемпотентно.
// catchUpSec — скільки вихід уже мовчав: годинник епізоду зсувається в минуле,
// тож перші сегменти йдуть швидше за 1x, поки не наздоженуть реальний час.
func (p *Player) Start(catchUpSec float64) {
	p.mu.Lock()
	if p.desired {
		p.mu.Unlock()
		return
	}
	p.resetEpisodeLocked(catchUpSec)
	done := make(chan struct{})
	p.done = done
	p.mu.Unlock()
	go func() {
		defer close(done)
		p.run()
	}()
}

// Stop — жорсткий стоп епізоду (teardown / після безшовного cut на relay).
func (p *Player) Stop() {
	p.mu.Lock()
	if !p.desired && p.proc == nil {
		p.mu.Unlock()
		return
	}
	p.desired = false
	p.resume = false
	p.restart = false
	p.onDone = nil
	p.killLocked()
	p.cv.Broadcast()
	done := p.done
	p.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(7 * time.Second):
	}
}

// PlayEnd — плавний вихід: доіграти поточний сегмент (Start — до кінця, Loop —
// перервати на місці), програти End, тоді викликати onDone. false — запит не взято.
func (p *Player) PlayEnd(onDone func()) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.desired {
		return false
	}
	switch p.phase {
	case PhaseStart, PhaseLoop:
		p.resume = true
		p.onDone = onDone
		if p.phase == PhaseLoop {
			p.killLocked()
		}
		p.cv.Broadcast()
		return true
	case PhaseEnd:
		p.onDone = onDone // аутро вже грає -- чіпляємось до поточного
		return true
	}
	return false
}

// Restart — re-drop під час/після End: повернути гравця на Start (timeline не скидаємо).
func (p *Player) Restart() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.desired {
		return
	}
	p.restart = true
	p.resume = false
	p.onDone = nil
	p.killLocked()
	p.cv.Broadcast()
}

// HasEndReady — чи є готовий End-сегмент (рішення грати аутро — за Platform).
func (p *Player) HasEndReady() bool { return p.sources("end") != "" }

// SegmentStartedAt — момент першого тега поточного сегмента; false — ще не пішов.
func (p *Player) SegmentStartedAt() (float64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.segStartedAt, p.segStarted
}

// Phase — поточна фаза епізоду ("" — гравець не стартував).
func (p *Player) Phase() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.phase
}

// IsRunning — чи живий процес поточного сегмента.
func (p *Player) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.proc != nil && !p.proc.exited.Load()
}

// PID — pid процесу сегмента; false — сегмента зараз немає.
func (p *Player) PID() (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pid, p.proc != nil
}

// --- супервізор епізоду ---

func (p *Player) run() {
	logFile, err := os.OpenFile(p.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("backup[%s] could not open %s: %v", p.name, p.logPath, err)
		return
	}
	defer logFile.Close()

	for {
		p.mu.Lock()
		if !p.desired {
			p.mu.Unlock()
			return
		}
		p.resume = false
		p.restart = false
		p.phase = PhaseStart
		p.mu.Unlock()

		// Start одноразовий, до EOF; resume його НЕ перериває.
		if src := p.sources("start"); src != "" {
			p.playSegment(src, false, logFile)
		}

		p.mu.Lock()
		if !p.desired {
			p.mu.Unlock()
			return
		}
		if p.restart {
			p.mu.Unlock()
			continue
		}
		resume := p.resume
		p.mu.Unlock()

		if !resume {
			p.mu.Lock()
			p.phase = PhaseLoop
			p.mu.Unlock()
			p.runLoopPhase(logFile)
			p.mu.Lock()
			if !p.desired {
				p.mu.Unlock()
				return
			}
			if p.restart {
				p.mu.Unlock()
				continue
			}
			p.mu.Unlock()
		}

		p.mu.Lock()
		p.phase = PhaseEnd
		// Мітка старту — лише про НОВИЙ сегмент: Platform чекає перший тег аутро.
		p.segStarted = false
		p.mu.Unlock()

		endSrc := p.sources("end")
		if endSrc != "" {
			p.playSegment(endSrc, false, logFile)
		}
		p.fireDone()

		// Поки cut не стався, активне джерело — усе ще заглушка, тож замовкати
		// не можна; спізнений cut крутить АУТРО ще раз, а не тіло.
		if endSrc != "" {
			for i := 0; i < maxEndRepeats; i++ {
				p.mu.Lock()
				stop := !p.desired || p.restart
				p.mu.Unlock()
				if stop {
					break
				}
				p.playSegment(endSrc, false, logFile)
				p.fireDone()
			}
		}

		if !p.leaveEndPhase() {
			return
		}

		p.runLoopPhase(logFile)

		p.mu.Lock()
		if !p.desired {
			p.mu.Unlock()
			return
		}
		p.mu.Unlock() // restart -> назад на Start
	}
}

// leaveEndPhase — повернення в тіло після аутро (пізній PlayEnd дограється);
// false, якщо гравця вже спинили.
func (p *Player) leaveEndPhase() bool {
	p.mu.Lock()
	if !p.desired {
		p.mu.Unlock()
		return false
	}
	p.phase = PhaseLoop
	p.resume = false
	p.mu.Unlock()
	p.fireDone()
	return true
}

// runLoopPhase — тіло FALLBACK; true, якщо треба переходити до End (resume).
func (p *Player) runLoopPhase(logFile *os.File) bool {
	for {
		p.mu.Lock()
		if !p.desired || p.restart {
			p.mu.Unlock()
			return false
		}
		if p.resume {
			p.mu.Unlock()
			return true
		}
		p.mu.Unlock()

		var item LoopItem
		var ok bool
		if p.loopNext != nil {
			item, ok = p.loopNext()
		}
		if !ok {
			if src := p.sources("loop"); src != "" { // легасі fallback (sequence)
				item, ok = LoopItem{Path: src, Loop: true}, true
			}
		}
		if !ok {
			p.mu.Lock()
			if p.desired && !p.restart && !p.resume {
				p.waitLocked(300 * time.Millisecond)
			}
			p.mu.Unlock()
			continue
		}
		p.playSegment(item.Path, item.Loop, logFile)
	}
}

// waitLocked — cv.Wait із таймаутом.
func (p *Player) waitLocked(d time.Duration) {
	timer := time.AfterFunc(d, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.cv.Broadcast()
	})
	p.cv.Wait()
	timer.Stop()
}

func (p *Player) playSegment(path string, loop bool, logFile *os.File) {
	args := []string{"-hide_banner", "-loglevel", "warning"}
	if loop {
		args = append(args, "-stream_loop", "-1")
	}
	// БЕЗ `-re`: темп тримає гейт у onTag (він і стелю знає, і вміє надолужити).
	if p.ladder {
		// FLV не несе кількох відеодоріжок — драбину віддаємо як MPEG-TS;
		// -muxdelay/-muxpreload 0 інакше розводять відео й аудіо на ~0.7с.
		args = append(args, "-i", path, "-map", "0", "-c", "copy",
			"-muxdelay", "0", "-muxpreload", "0", "-f", "mpegts", "pipe:1")
	} else {
		args = append(args, "-i", path, "-c", "copy", "-f", "flv", "pipe:1")
	}
	kind := "once"
	if loop {
		kind = "loop"
	}
	log.Printf("backup[%s] playing %s (%s)", p.name, path, kind)

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = logFile
	stdout, err := cmd.StdoutPipe()
	if err == nil {
		err = proc.StartCmd(cmd)
	}
	if err != nil {
		log.Printf("backup[%s] failed to start ffmpeg for %s: %v", p.name, path, err)
		return
	}
	seg := &segment{cmd: cmd}

	p.mu.Lock()
	p.proc = seg
	p.pid = cmd.Process.Pid
	p.beginSegmentLocked()
	p.mu.Unlock()

	p.readSegment(stdout)
	waitSegment(seg)

	p.mu.Lock()
	if p.proc == seg {
		p.proc = nil
		p.pid = 0
	}
	p.mu.Unlock()
}

// readSegment — рідер сегмента віддає теги з source "seg" (гравець кличе
// process уже з "backup").
func (p *Player) readSegment(stdout io.Reader) {
	if p.ladder {
		reader := route.NewEBBackup(p.onTag)
		_ = ts.ReadTags(stdout, reader.Route, &ts.ReadTagsOptions{AllVideoTracks: true})
		return
	}
	_ = flv.ReadTags(stdout, "seg", func(source string, tagType byte, tsMS uint32, payload []byte) {
		p.onTag(source, tagType, int64(tsMS), payload)
	}, nil)
}

// waitSegment — SIGKILL, якщо процес не вийшов за 5с після EOF рідера.
func waitSegment(seg *segment) {
	done := make(chan struct{})
	go func() {
		_ = seg.cmd.Wait()
		seg.exited.Store(true)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = seg.cmd.Process.Kill()
		<-done
	}
}

// beginSegmentLocked — новий сегмент: перезаякорити ep-offset на його першому тезі.
func (p *Player) beginSegmentLocked() {
	p.interrupted = false
	p.segStarted = false
	p.needAnchor = true
}

// resetEpisodeLocked — свіжий епізод -> свіжий timeline (switcher теж
// перезакориться на set_active("backup"), тож ts стартують з ~0).
func (p *Player) resetEpisodeLocked(catchUpSec float64) {
	p.desired = true
	p.resume = false
	p.restart = false
	p.onDone = nil
	p.epOffset = 0
	p.lastOutTS = 0
	p.trackTS = map[string]int64{}
	p.needAnchor = true
	p.lastSeq = map[string][]byte{}
	p.epWall0 = p.clock.Now() - math.Min(math.Max(catchUpSec, 0), maxCatchUpSec)
	p.hasEpWall0 = true
	p.epTS0 = 0
	p.hasEpTS0 = false
}

// onTag — сирий тег сегмента в неперервний канонічний "backup"-потік:
// монотонний ts через межу сегментів + дедуп seq-header/meta.
func (p *Player) onTag(_ string, tagType byte, tsMS int64, payload []byte) {
	seq := flv.IsSeqHeader(tagType, payload)
	isMeta := tagType == flv.TagScript

	p.mu.Lock()
	if p.interrupted {
		p.mu.Unlock()
		return
	}
	if !p.segStarted {
		p.segStarted = true
		p.segStartedAt = p.clock.Now()
	}
	if p.needAnchor {
		p.epOffset = p.lastOutTS + stepMS - tsMS
		p.needAnchor = false
		p.alignAudio = true
		p.alignBase = maps.Clone(p.trackTS)
	}
	outTS := tsMS + p.epOffset
	if p.alignAudio && tagType == flv.TagAudio && !seq && p.audioStep != 0 {
		// Аудіо мусить продовжитись РІВНО через свій кадр; зсуваємо СПІЛЬНИЙ
		// offset, тож відео їде разом і A/V-вирівнювання сегмента не змінюється.
		// Продовжити можна лише доріжку, що йшла ДО стику.
		if previous, ok := p.alignBase[flv.HeaderKey(tagType, payload)]; ok {
			shift := previous + p.audioStep - outTS
			p.epOffset += shift
			outTS += shift
			p.alignAudio = false
		}
	}

	// Кламп монотонності — ПО ДОРІЖЦІ; спільним лишається лише максимум для
	// заякорення НАСТУПНОГО сегмента.
	key := "meta"
	if !isMeta {
		key = flv.HeaderKey(tagType, payload)
	}
	lastForTrack, hasTrack := p.trackTS[key]
	if hasTrack && outTS < lastForTrack && !seq && !isMeta {
		// ДРОПАЄМО, а не клампимо: клампнутий тег лишається реальними семплами
		// під чужою міткою, і надлишок осідає в буфері плеєра площадки.
		p.mu.Unlock()
		return
	}

	switch {
	case seq:
		if prev, ok := p.lastSeq[key]; ok && bytes.Equal(prev, payload) {
			p.mu.Unlock()
			return
		}
		p.lastSeq[key] = payload
	case isMeta:
		if prev, ok := p.lastSeq["meta"]; ok && bytes.Equal(prev, payload) {
			p.mu.Unlock()
			return
		}
		p.lastSeq["meta"] = payload
	default:
		if tagType == flv.TagAudio && hasTrack {
			if step := outTS - lastForTrack; step > 0 && step < 100 { // крок кадру, не стик
				p.audioStep = step
			}
		}
		p.trackTS[key] = outTS
		if outTS > p.lastOutTS {
			p.lastOutTS = outTS
		}
	}

	if !p.hasEpTS0 {
		p.hasEpTS0 = true
		p.epTS0 = outTS
		if !p.hasEpWall0 {
			p.hasEpWall0 = true
			p.epWall0 = p.clock.Now()
		}
	}
	due := p.epWall0 + float64(outTS-p.epTS0)/1000 - pacingLeadSec
	p.mu.Unlock()

	// Пейсинг — ПОЗА локом: Stop чекає на той самий лок, а затримка тут
	// просто підпирає pipe сегмента.
	if delay := due - p.clock.Now(); delay > 0 {
		p.sleeper.Sleep(math.Min(delay, 1.0))
	}
	p.process("backup", tagType, outTS, payload)
}

// --- helpers ---

// killLocked — м'яке завершення сегмента; хвіст перерваного сегмента більше НЕ
// пейситься (у pipe лишається до секунди медіа, і фазування аутро промахувалось).
func (p *Player) killLocked() {
	p.interrupted = true
	if p.proc == nil {
		return
	}
	proc.TerminateProc(p.proc.cmd)
}

// fireDone — віддати зареєстрований onDone рівно раз.
func (p *Player) fireDone() {
	p.mu.Lock()
	callback := p.onDone
	p.onDone = nil
	p.mu.Unlock()
	if callback != nil {
		p.fire(callback)
	}
}

func (p *Player) fire(callback func()) {
	defer func() {
		if err := recover(); err != nil {
			log.Printf("backup[%s]: error in on_done callback: %v", p.name, err)
		}
	}()
	callback()
}
