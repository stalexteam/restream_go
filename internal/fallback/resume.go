package fallback

import (
	"log"
	"math"
	"sync"
	"time"
)

// Константи фазування аутро.
const (
	resumeKeyframeWaitSec = 8.0  // скільки чекаємо перший keyframe live
	cutArmLeadSec         = 0.3  // запас озброєння, коли GOP невідомий
	endStartWaitSec       = 5.0  // скільки чекаємо перший тег аутро
	endTailGuardSec       = 0.25 // запас від цільового keyframe до кінця аутро
	endEarlyCutMaxSec     = 0.5  // наскільки раніше кінця аутро дозволено різати
	endStartLatencySec    = 0.08 // від play_end до першого тега аутро
)

// Причини виходу з фазування — символьні мітки точок повернення
// в golden-сліді.
const (
	outcomeInvalidKeyframeWait  = "invalid-keyframe-wait"
	outcomeInvalidPhaseLoop     = "invalid-phase-loop"
	outcomeInvalidBeforePlayEnd = "invalid-before-play-end"
	outcomeNoTarget             = "no-target"
	outcomeInvalidStartWait     = "invalid-start-wait"
	outcomeOutroNeverStarted    = "outro-never-started"
	outcomeInvalidArmWait       = "invalid-arm-wait"
	outcomeInvalidBeforeArm     = "invalid-before-arm"
	outcomeArmed                = "armed"
	outcomePlayEndRejected      = "play-end-rejected"
	outcomeEndGuardFailed       = "end-guard-failed"
	outcomeEndSwitch            = "end-switch"
)

// ResumeOptions — вузькі залежності оркестратора: примітиви таймлайна, гравець,
// препарер і зовнішня частина валідності від Platform.
type ResumeOptions struct {
	Name string

	// RelayKeyframeAt / RelayGOPSec / RequestSwitch — timeline.Switcher.
	// RequestSwitch уже прив'язаний Platform-ом до "relay" і свого колбека.
	RelayKeyframeAt func() float64
	RelayGOPSec     func() (float64, bool)
	RequestSwitch   func(notBefore *float64)

	// Гравець заглушки.
	HasEndReady      func() bool
	PlayEnd          func(onDone func()) bool
	PlayerPhase      func() string
	SegmentStartedAt func() (float64, bool)

	// EndDuration — Preparer.SegmentDuration("end").
	EndDuration func() (float64, bool)

	// StateValid — частина `_resume_valid` поза власними полями:
	// state == FALLBACK && !shutdown. Кличеться і з-під власного лока Resume,
	// тож мусить читати стан лок-фрі, інакше отримаємо інверсію локів.
	StateValid func() bool

	// Clock мусить бути ТИМ САМИМ інстансом, що у switcher-а і гравця.
	Clock   Clock
	Sleeper Sleeper
}

// Resume — плавне повернення FALLBACK -> LIVE: чекає keyframe live, фазує аутро
// так, щоб воно скінчилось на keyframe, і озброює cut (
// `_run_end_before_cut` / `_on_backup_end` із Platform).
type Resume struct {
	opts    ResumeOptions
	clock   Clock
	sleeper Sleeper

	mu       sync.Mutex
	resuming bool
	gen      uint64

	// Тестові шви (golden-слід рішень); прод їх не бачить.
	validN    int
	validHook func(n int, ok bool)
	doneHook  func(reason string)
}

// NewResume — оркестратор одного плеча платформи.
func NewResume(opts ResumeOptions) *Resume {
	r := &Resume{opts: opts, clock: opts.Clock, sleeper: opts.Sleeper}
	if r.clock == nil {
		r.clock = systemClock{base: time.Now()}
	}
	if r.sleeper == nil {
		r.sleeper = systemSleeper{}
	}
	return r
}

// Begin — почати плавне повернення (Platform кличе під своїм локом). Є готовий
// End — фазуємо аутро окремою горутиною, немає — одразу просимо перемикання.
func (r *Resume) Begin() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resuming = true
	r.gen++
	if r.opts.HasEndReady() {
		generation := r.gen
		go r.runEnd(generation)
		return
	}
	r.opts.RequestSwitch(nil)
}

// Cancel — скасувати resume (source впав знову / cut уже стався): поточна
// горутина фазування помре на найближчій перевірці.
func (r *Resume) Cancel() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resuming = false
}

// IsResuming — чи триває плавне повернення.
func (r *Resume) IsResuming() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.resuming
}

// OnBackupEnd — гравець дограв аутро; робота в окремій горутині, щоб не тримати
// його потік (і не інвертувати локи).
func (r *Resume) OnBackupEnd() {
	go func() { r.report(r.finishAfterEnd()) }()
}

func (r *Resume) finishAfterEnd() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.resuming || !r.opts.StateValid() {
		return outcomeEndGuardFailed
	}
	r.opts.RequestSwitch(nil)
	return outcomeEndSwitch
}

func (r *Resume) runEnd(generation uint64) {
	r.report(r.runEndBeforeCut(generation))
}

func (r *Resume) report(reason string) {
	r.mu.Lock()
	hook := r.doneHook
	r.mu.Unlock()
	if hook != nil {
		hook(reason)
	}
}

// runEndBeforeCut — аутро грає ПІСЛЯ того, як live підтвердив себе keyframe-ом,
// і так сфазовано, щоб СКІНЧИТИСЬ на черговому keyframe: cut можливий лише на
// keyframe, тож без фазування після аутро лишалось вікно до цілого GOP.
// Не знаємо GOP чи тривалості — граємо одразу.
func (r *Resume) runEndBeforeCut(generation uint64) string {
	startedAt := r.clock.Now()
	keyframeAt := 0.0
	seen := false
	for r.clock.Now()-startedAt < resumeKeyframeWaitSec {
		if !r.valid(generation) {
			return outcomeInvalidKeyframeWait
		}
		keyframeAt = r.opts.RelayKeyframeAt()
		if keyframeAt > startedAt {
			seen = true
			break
		}
		r.sleeper.Sleep(0.05)
	}
	if !seen {
		keyframeAt = 0.0
	}

	// Нуль тут — і «немає значення», і falsy-гілка.
	gop := 0.0
	if value, ok := r.opts.RelayGOPSec(); ok {
		gop = value
	}
	duration := 0.0
	if value, ok := r.opts.EndDuration(); ok {
		duration = value
	}
	// Цілимось так, щоб keyframe прийшов трохи РАНІШЕ природного кінця аутро:
	// урізаний хвіст у чверть секунди непомітний, промах на GOP — ні.
	target := 0.0
	if duration != 0 {
		target = math.Max(0.0, duration-endTailGuardSec)
	}
	if keyframeAt != 0 && gop != 0 && target != 0 {
		log.Printf("[%s] live is back (keyframe seen, gop %.2fs) -> waiting to start the "+
			"outro so that it ends on a keyframe", r.opts.Name, gop)
		// Фазу перераховуємо до самого запуску, від НАЙСВІЖІШОГО keyframe:
		// інакше екстраполяція на кілька GOP уперед з'їдає весь допуск.
		for {
			fresh := r.opts.RelayKeyframeAt()
			if fresh == 0 {
				fresh = keyframeAt
			}
			if value, ok := r.opts.RelayGOPSec(); ok && value != 0 {
				gop = value
			}
			ahead := target + endStartLatencySec
			delay := pyMod(pyMod(-ahead, gop)-pyMod(r.clock.Now()-fresh, gop), gop)
			if delay <= 0.03 || delay >= gop-0.1 {
				break // трохи проскочили — краще зараз, ніж цілий GOP чекати
			}
			if !r.sleepWhileResuming(math.Min(delay, 0.05), generation) {
				return outcomeInvalidPhaseLoop
			}
		}
	}

	r.mu.Lock()
	if !r.validLocked(generation) {
		r.mu.Unlock()
		return outcomeInvalidBeforePlayEnd
	}
	if !r.opts.PlayEnd(r.OnBackupEnd) {
		// Гравець аутро не взяв -- OnBackupEnd не прийде, тож ріжемо одразу.
		r.opts.RequestSwitch(nil)
		r.mu.Unlock()
		return outcomePlayEndRejected
	}
	r.mu.Unlock()

	if target == 0 {
		return outcomeNoTarget
	}
	// Якоримось на ФАКТИЧНОМУ старті аутро (запит на нього і перший його тег —
	// не одне й те саме).
	segStartedAt := 0.0
	haveStart := false
	deadline := r.clock.Now() + endStartWaitSec
	for !haveStart && r.clock.Now() < deadline {
		if !r.sleepWhileResuming(0.02, generation) {
			return outcomeInvalidStartWait
		}
		if r.opts.PlayerPhase() == PhaseEnd {
			if value, ok := r.opts.SegmentStartedAt(); ok {
				segStartedAt, haveStart = value, true
			}
		}
	}
	if !haveStart {
		return outcomeOutroNeverStarted // аутро так і не пішло — cut зробить OnBackupEnd
	}
	// Озброюємо З ЗАПАСОМ (горутина може прокинутись пізно), а межу «не раніше»
	// тримає сам switcher.
	lead := cutArmLeadSec
	if gop != 0 {
		lead = gop / 2
	}
	wait := segStartedAt + target - lead - r.clock.Now()
	if !r.sleepWhileResuming(math.Max(0.0, wait), generation) {
		return outcomeInvalidArmWait
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.validLocked(generation) {
		return outcomeInvalidBeforeArm
	}
	notBefore := segStartedAt + duration - endEarlyCutMaxSec
	log.Printf("[%s] outro is ending -> arming the cut (no earlier than %.0f ms before the "+
		"outro ends)", r.opts.Name, endEarlyCutMaxSec*1000)
	r.opts.RequestSwitch(&notBefore)
	return outcomeArmed
}

// sleepWhileResuming — пауза дрібними кроками з перевіркою валідності на кожному
// ; false — resume скасовано.
func (r *Resume) sleepWhileResuming(seconds float64, generation uint64) bool {
	deadline := r.clock.Now() + seconds
	for r.clock.Now() < deadline {
		if !r.valid(generation) {
			return false
		}
		r.sleeper.Sleep(math.Min(0.05, math.Max(0.0, deadline-r.clock.Now())))
	}
	return r.valid(generation)
}

func (r *Resume) valid(generation uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.validLocked(generation)
}

// validLocked — `_resume_valid`: власні resuming/gen плюс зовнішня частина.
// Оригінал бере тут реентрантний RLock, у Go рівні розділені явно.
func (r *Resume) validLocked(generation uint64) bool {
	ok := r.resuming && r.gen == generation && r.opts.StateValid()
	r.validN++
	if r.validHook != nil {
		r.validHook(r.validN, ok)
	}
	return ok
}

// pyMod — float-`%` python-семантики: знак результату береться від ДІЛЬНИКА
// (math.Mod бере від діленого).
func pyMod(x, y float64) float64 {
	rem := math.Mod(x, y)
	if rem != 0 {
		if (rem < 0) != (y < 0) {
			rem += y
		}
		return rem
	}
	return math.Copysign(0, y)
}
