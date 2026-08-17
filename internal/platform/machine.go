package platform

import (
	"log"
	"sync/atomic"
	"time"

	"restream_go/internal/timeline"
)

// State — стан непреривності платформи.
type State int32

const (
	StateOffline State = iota
	StateLive
	StateFallback
)

func (s State) String() string {
	switch s {
	case StateLive:
		return "LIVE"
	case StateFallback:
		return "FALLBACK"
	default:
		return "OFFLINE"
	}
}

// Константи стейт-машини.
const (
	// FlappingToastCooldownSec — мінімум між тостами про нестабільний вихід.
	FlappingToastCooldownSec = 30.0
	// OracleWindowSec — вікно кореляції «штатний стоп OBS ↔ обрив aux-source».
	OracleWindowSec = 1.5
	// ladderWatchIntervalSec — крок наглядача за готовністю драбинної заглушки.
	ladderWatchIntervalSec = 1.0
	// cmdQueue — стеля черги подій; переповнення = дизайн-помилка.
	cmdQueue = 256
)

// Timer — скасовуваний одноразовий таймер.
type Timer interface{ Stop() }

// TimerFactory — фабрика таймерів: шов для віртуального часу в golden-сверці.
// Колбек кличеться з чужої горутини й лише кладе подію в канал машини.
type TimerFactory func(name string, d time.Duration, fire func()) Timer

type stdTimer struct{ t *time.Timer }

func (s stdTimer) Stop() { s.t.Stop() }

func systemTimers(_ string, d time.Duration, fire func()) Timer {
	return stdTimer{time.AfterFunc(d, fire)}
}

// ManagerDeps — контракт до control-шару (Manager). Усі функції кличуться з
// горутини-власника, яка НЕ тримає жодного лока, тож Manager
// вільний брати свої локи. Зворотна вимога: жоден із цих викликів не сміє
// синхронно чекати на цю ж машину — усі її входи неблокуючі саме тому.
type ManagerDeps struct {
	// IsDefaultSource — Manager.is_default_source(source); aux = !IsDefaultSource.
	IsDefaultSource func() bool
	// IsGracefulRecent / IsMainSessionLive — детектор штатного стопу.
	IsGracefulRecent  func() bool
	IsMainSessionLive func() bool

	// OnStalled / OnRecovered / OnGaveUp — сесійний стан веде Manager.
	OnStalled   func()
	OnRecovered func()
	OnGaveUp    func()

	// FallbackProblem — чому пресет не дасть заглушки прямо зараз, або ""
	// (перевірка по ФС живе в Manager). nil = мовчить.
	FallbackProblem func() string
	// OnFallbackUnusable — заглушка знадобилась, але непридатна: Manager знімає
	// галочку з платформи.
	OnFallbackUnusable func(problem string)

	// Notify — Manager._notify: дашборд перечитує знімок.
	Notify func()
	// Emit — Manager.emit_platform_event: тост користувачу.
	Emit func(level, text string)
}

// MachineOptions — усе, що машина бере ззовні, крім самого конвеєра.
type MachineOptions struct {
	Manager ManagerDeps

	// Group / Enabled / Gate — початкові значення полів, які веде control-шар.
	Group   string
	Enabled bool
	Gate    bool

	// ReadTimeout — global_config["read_timeout_ms"] для тексту стал-лога.
	ReadTimeout func() time.Duration

	// Clock — ТОЙ САМИЙ монотонний годинник, що в конвеєра.
	Clock Clock
	// Wall — час стіни для state_since.
	Wall func() float64
	// Timers — фабрика таймерів (дефолт — time.AfterFunc).
	Timers TimerFactory
}

// snapshot — узгоджена пара «стан машини + shutdown», яку читають лок-фрі
// (StateValid для фазування аутро — RS5, і Status для дашборда — K2/K3).
type snapshot struct {
	state      State
	stateSince float64
	halted     bool
	enabled    bool
	gate       bool
	group      string
	shut       bool
}

type command struct {
	name string
	run  func()
}

// Machine — стейт-машина однієї платформи: OFFLINE/LIVE/FALLBACK у ВЛАСНІЙ
// горутині без лока. Усі події приходять командним каналом,
// увесь стан живе в горутині, назовні видно лише атомарний знімок.
type Machine struct {
	name  string
	spec  Spec
	nodes nodes
	mgr   ManagerDeps

	clock       Clock
	wall        func() float64
	timers      TimerFactory
	readTimeout func() time.Duration

	cmds     chan command
	shutdown chan struct{}
	done     chan struct{}

	snap    atomic.Pointer[snapshot]
	rtt     atomic.Int64 // −1 = None
	dropped atomic.Int64

	// ↓ лише горутина-власник
	state               State
	stateSince          float64
	halted              bool
	enabled             bool
	gate                bool
	group               string
	shut                bool
	lastFlappingToastAt float64
	oracleTimer         Timer
	ladderTimer         Timer
	ladderWatching      bool

	// Шов golden-сліду: прод його не виставляє.
	trace func(string)
}

// NewMachine піднімає стейт-машину над зібраним конвеєром і підвʼязує його
// колбеки (PL11: до першого Start будь-якого вузла).
func NewMachine(p *Pipeline, opts MachineOptions) *Machine {
	m := newMachine(p.Spec, pipelineNodes{p}, opts)
	p.OnRelayStalled = m.OnRelayStalled
	p.OnRelayResumed = m.OnRelayResumed
	p.OnOutFlapping = m.OnOutFlapping
	p.OnSwitchedToRelay = m.OnSwitchedToRelay
	p.OnLadderMinted = m.OnLadderMinted
	p.StateValid = m.StateValid
	return m
}

func newMachine(spec Spec, n nodes, opts MachineOptions) *Machine {
	m := &Machine{
		name:        spec.Name,
		spec:        spec,
		nodes:       n,
		mgr:         opts.Manager,
		clock:       opts.Clock,
		wall:        opts.Wall,
		timers:      opts.Timers,
		readTimeout: opts.ReadTimeout,
		cmds:        make(chan command, cmdQueue),
		shutdown:    make(chan struct{}, 1),
		done:        make(chan struct{}),
		enabled:     opts.Enabled,
		gate:        opts.Gate,
		group:       opts.Group,
	}
	if m.clock == nil {
		m.clock = timeline.SystemClock()
	}
	if m.wall == nil {
		m.wall = wallNow
	}
	if m.timers == nil {
		m.timers = systemTimers
	}
	m.rtt.Store(-1)
	m.stateSince = m.wall()
	m.publish()
	go m.loop()
	return m
}

// wallNow — епохні секунди для state_since.
func wallNow() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// settle — бар'єр для тестів: чекає опрацювання всього, що вже в черзі.
func (m *Machine) settle() {
	done := make(chan struct{})
	select {
	case m.cmds <- command{"settle", func() { close(done) }}:
	case <-m.done:
		return
	}
	select {
	case <-done:
	case <-m.done:
	}
}

// --- канал команд ---

// post — неблокуючий send із чужої горутини (свічер, супервізори, таймери,
// control-шар). Після shutdown подія тихо гине.
func (m *Machine) post(name string, run func()) {
	if m.snap.Load().shut {
		return
	}
	select {
	case m.cmds <- command{name, run}:
	default:
		m.dropped.Add(1)
		log.Printf("[%s] platform event queue overflow -- dropping %s", m.name, name)
	}
}

func (m *Machine) loop() {
	defer close(m.done)
	for {
		select {
		case cmd := <-m.cmds:
			cmd.run()
		case <-m.shutdown:
			// Дожати знімок черги: поставлене до shutdown обробляється,
			// а флуд нових подій знесення не голодує.
			for n := len(m.cmds); n > 0; n-- {
				(<-m.cmds).run()
			}
			m.doShutdown()
			return
		}
	}
}

// --- лок-фрі читання ---

// StateValid — `state == FALLBACK && !shutdown` для фазування аутро:
// один атомарний load дає узгоджену пару.
func (m *Machine) StateValid() bool {
	s := m.snap.Load()
	return s.state == StateFallback && !s.shut
}

// State — поточний стан (лок-фрі).
func (m *Machine) State() State { return m.snap.Load().state }

// Enabled / Gate — власна галочка й гейт групи (лок-фрі).
func (m *Machine) Enabled() bool { return m.snap.Load().enabled }
func (m *Machine) Gate() bool    { return m.snap.Load().gate }

// EffectiveEnabled — enabled AND gate.
func (m *Machine) EffectiveEnabled() bool {
	s := m.snap.Load()
	return s.enabled && s.gate
}

// DroppedEvents — скільки подій загубила переповнена черга (діагностика PM3).
func (m *Machine) DroppedEvents() int { return int(m.dropped.Load()) }

// SetRTT — Manager.ping-петля пише сюди виміряний RTT (ok=false — немає даних).
func (m *Machine) SetRTT(ms int, ok bool) {
	if !ok {
		m.rtt.Store(-1)
		return
	}
	m.rtt.Store(int64(ms))
}

func (m *Machine) publish() {
	m.snap.Store(&snapshot{
		state: m.state, stateSince: m.stateSince, halted: m.halted,
		enabled: m.enabled, gate: m.gate, group: m.group, shut: m.shut,
	})
}

// --- події ззовні (усі неблокуючі, крім Shutdown) ---

// OnSourceAvailable / OnSourceUnavailable — хуки MediaMTX (латч і валідацію
// контракту перевіряє Manager ПЕРЕД делегуванням).
func (m *Machine) OnSourceAvailable()   { m.post("source_available", m.onSourceAvailable) }
func (m *Machine) OnSourceUnavailable() { m.post("source_unavailable", m.onSourceUnavailable) }

// OnRelayStalled / OnRelayResumed — стал-детектор рідера relay.
func (m *Machine) OnRelayStalled() { m.post("relay_stalled", m.onRelayStalled) }
func (m *Machine) OnRelayResumed() { m.post("relay_resumed", m.onRelayResumed) }

// OnSwitchedToRelay — колбек свічера з reader-горутини.
func (m *Machine) OnSwitchedToRelay(paramsChanged bool) {
	m.post("switched_to_relay", func() { m.onSwitchedToRelay(paramsChanged) })
}

// OnOutFlapping — супервізор виходу помітив серію падінь.
func (m *Machine) OnOutFlapping(neverSucceeded bool) {
	m.post("out_flapping", func() { m.onOutFlapping(neverSucceeded) })
}

// OnLadderMinted — go-live-обмін щойно видав драбину.
func (m *Machine) OnLadderMinted() { m.post("ladder_minted", m.onLadderMinted) }

// SetEnabled / SetGate — власна галочка й гейт групи.
func (m *Machine) SetEnabled(active bool) { m.post("set_enabled", func() { m.setEnabled(active) }) }
func (m *Machine) SetGate(open bool)      { m.post("set_gate", func() { m.setGate(open) }) }

// SetGroup — порт set_group: нова група + її гейт.
func (m *Machine) SetGroup(groupID string, gateOpen bool) {
	m.post("set_group", func() {
		m.group = groupID
		m.publish()
		m.setGate(gateOpen)
	})
}

// UpdateCredentials / UpdateTracks / UpdateAudioMap — live-apply-диспатч
// : самі сеттери живуть у конвеєрі, тут лише порядок і bounce.
func (m *Machine) UpdateCredentials(server, key, streamID, passphrase string) {
	m.post("update_credentials", func() {
		changed := m.nodes.SetCredentials(server, key, streamID, passphrase)
		if changed && m.effectiveEnabled() && m.onAir() {
			m.nodes.BounceOutput() // bounce лише цієї площадки з новим URL
		}
	})
}

func (m *Machine) UpdateTracks(audio, audioVOD int) {
	m.post("update_tracks", func() { m.nodes.SetAudio(audio, audioVOD) })
}

func (m *Machine) UpdateAudioMap(audioMap []int) {
	cp := append([]int(nil), audioMap...)
	m.post("update_audio_map", func() { m.nodes.SetAudioMap(cp) })
}

// ApplyPreset — порт apply_preset: конвеєр підміняє препарер, машина вирішує,
// чи готувати сегменти зараз (сам пресет і сегменти веде control-шар).
func (m *Machine) ApplyPreset() {
	m.post("apply_preset", func() {
		prev, ok := m.nodes.ApplyPreset()
		if m.onAir() {
			m.nodes.ResumePreparation(prev, ok)
		}
	})
}

// Halt — механічний стоп (рішення про OBS/події — на Manager). Повертає стан
// на МОМЕНТ ВИКЛИКУ (лок-фрі): сам знос виконується горутиною-власником.
func (m *Machine) Halt() bool {
	wasActive := m.snap.Load().state != StateOffline
	m.post("halt", m.teardownClean)
	return wasActive
}

// GracefulStopIfFallback — штатний стоп OBS, коли платформа aux-source уже
// сидить у FALLBACK (нового on_source_unavailable вона не отримає).
func (m *Machine) GracefulStopIfFallback() {
	m.post("graceful_stop", func() {
		if m.state == StateFallback {
			log.Printf("[%s] graceful stop while in fallback -> clean end", m.name)
			m.teardownClean()
		}
	})
}

// Shutdown — знести платформу; ЄДИНИЙ блокуючий вхід.
// Викликач не сміє тримати лок, потрібний колбекам
// Manager, інакше горутина-власник не дійде до знесення.
func (m *Machine) Shutdown() {
	select {
	case m.shutdown <- struct{}{}:
	default:
	}
	select {
	case <-m.done:
	case <-time.After(shutdownWait):
		log.Printf("[%s] platform shutdown did not finish in %s", m.name, shutdownWait)
	}
}

const shutdownWait = 30 * time.Second

// --- обробники (тіла методів Platform, 755-1102) ---

func (m *Machine) onSourceAvailable() {
	if m.shut {
		return // платформу вже знесли -- нічого не піднімаємо
	}
	m.cancelOracle()

	if m.state == StateFallback {
		// Source повернувся: є готовий End -- гравець доіграє аутро і лише
		// тоді буде безшовний cut; немає -- одразу request_switch.
		log.Printf("[%s] source reconnected -> resuming (playing End if ready, then seamless switch)", m.name)
		m.nodes.StartRelay()
		if m.effectiveEnabled() {
			m.nodes.StartOutput() // міг бути погашений (EB без готової заглушки)
		}
		m.nodes.ResumeBegin()
	} else {
		if m.state == StateOffline {
			log.Printf("[%s] source is publishing -> starting this platform", m.name)
			m.lastFlappingToastAt = 0
			m.halted = false
			if m.effectiveEnabled() {
				m.nodes.StartOutput()
			}
		}
		m.nodes.BackupStop()
		m.nodes.SetActive("relay")
		m.nodes.StartRelay()
		m.setState(StateLive)
	}
	m.nodes.PrepareBackup()
}

func (m *Machine) onSwitchedToRelay(paramsChanged bool) {
	if m.state != StateFallback {
		return
	}
	if paramsChanged {
		log.Printf("[%s] live parameters changed while the source was unavailable -> "+
			"reconnecting the platform with a clean connection instead of a "+
			"seamless switch", m.name)
		m.nodes.SetActive("relay")
		if m.effectiveEnabled() && !m.nodes.Failed() {
			m.nodes.BounceOutput()
		}
	} else {
		log.Printf("[%s] live is ready (first keyframe received) -> seamless switch, stopping the backup video", m.name)
	}
	m.setState(StateLive)
	m.nodes.ResumeCancel() // RS10: друге місце скидання _resuming (815)
	m.nodes.BackupStop()
	// Сесійний стан (offline-таймер) веде Manager -- звемо ПІСЛЯ роботи.
	m.call(m.mgr.OnRecovered)
}

// beginFallbackPlayback — порт _begin_fallback_playback: на EB-плечі без
// готового сегмента віддавати нічого, тож вихід гасимо до готовності.
// false = заглушки не буде, платформу знято з ефіру.
func (m *Machine) beginFallbackPlayback() bool {
	if problem := m.fallbackProblem(); problem != "" {
		log.Printf("[%s] the backup video is unusable (%s) -> taking this platform off air", m.name, problem)
		m.teardownClean()
		if m.mgr.OnFallbackUnusable != nil {
			m.mgr.OnFallbackUnusable(problem)
		}
		return false
	}
	if m.spec.EB && !m.nodes.HasReadySegment() {
		log.Printf("[%s] the per-rung fallback is still being prepared -- taking this platform "+
			"off air until it is ready or the source comes back", m.name)
		m.emit("warning", "the fallback video for the ladder is still being prepared -- this platform is "+
			"off air until it is ready")
		m.nodes.StopOutput()
		m.watchLadderBackup()
		return true
	}
	// Скільки вихід мовчав від останніх живих даних -- рівно стільки гравцю
	// дозволено надолужити.
	m.nodes.BackupStart(m.nodes.SecondsSinceRelayData())
	return true
}

// watchLadderBackup — порт _watch_ladder_backup: потік із `sleep` став
// перезарядним таймером, кожен його фаєр = одна ітерація нагляду.
func (m *Machine) watchLadderBackup() {
	if m.ladderWatching {
		return
	}
	m.ladderWatching = true
	m.tracef("ladder_watch_start")
	m.armLadderTick()
}

func (m *Machine) armLadderTick() {
	m.ladderTimer = m.timers("ladder", floatSeconds(ladderWatchIntervalSec),
		func() { m.post("ladder_tick", m.ladderTick) })
}

func (m *Machine) ladderTick() {
	if !m.ladderWatching {
		return
	}
	m.tracef("ladder_tick")
	if m.shut || m.state != StateFallback {
		m.ladderWatching = false
		m.tracef("ladder_watch_return")
		return
	}
	if !m.nodes.HasReadySegment() {
		m.armLadderTick()
		m.tracef("ladder_watch_continue")
		return
	}
	log.Printf("[%s] the per-rung fallback is ready -> back on air with the backup video", m.name)
	m.nodes.SetActive("backup")
	if m.effectiveEnabled() {
		m.nodes.StartOutput()
	}
	m.nodes.BackupStart(m.nodes.SecondsSinceRelayData())
	m.ladderWatching = false
	m.tracef("ladder_watch_return")
}

func (m *Machine) onSourceUnavailable() {
	m.nodes.StopRelay()

	if m.state == StateOffline {
		return
	}

	if m.state == StateFallback {
		if m.nodes.IsResuming() {
			// Source впав знову під час плавного повернення -> скасувати
			// resume, скинути pending relay-switch і повернути гравця на Start.
			log.Printf("[%s] source dropped again during resume -> restarting the fallback sequence", m.name)
			m.nodes.ResumeCancel() // RS10: перше місце скидання _resuming (1005)
			m.nodes.SetActive("backup")
			m.nodes.BackupRestart()
			return
		}
		// Уже в FALLBACK -- найімовірніше, власний стал-детектор устиг раніше.
		return
	}

	// Платформа aux-source не вмикає заглушку, якщо немає «сесії», на яку
	// спертись: обрив у вікні штатного стопу або мертва головна сесія.
	if m.isAux() && (m.gracefulRecent() || !m.mainSessionLive()) {
		log.Printf("[%s] source disconnected with no live main session to lean on -> clean end (no backup)", m.name)
		m.teardownClean()
		return
	}

	if m.effectiveEnabled() && !m.nodes.OutputAlive() {
		log.Printf("[%s] source disconnected, and the platform was never reached this "+
			"broadcast -- no point looping the backup video, stopping", m.name)
		m.giveUpOnUnreachable()
		return
	}

	log.Printf("[%s] source disconnected -> switching to backup video (session timeout is managed globally)", m.name)
	m.nodes.SetActive("backup")
	if !m.beginFallbackPlayback() {
		return
	}
	if m.isAux() {
		// Сигнал штатного стопу міг прийти трохи ПІЗНІШЕ за обрив -- перепровірка.
		m.scheduleOracleRecheck()
	}
	m.setState(StateFallback)
}

func (m *Machine) onRelayStalled() {
	if m.state != StateLive {
		return
	}
	if m.isAux() && (m.gracefulRecent() || !m.mainSessionLive()) {
		log.Printf("[%s] relay stalled with no live main session to lean on -> clean end (no backup)", m.name)
		m.teardownClean()
		return
	}
	log.Printf("[%s] no data from relay for %dms (network to the source looks stalled) -> "+
		"switching to backup video without dropping the relay connection", m.name, m.readTimeoutMS())
	m.nodes.SetActive("backup")
	if !m.beginFallbackPlayback() {
		return
	}
	if m.isAux() {
		m.scheduleOracleRecheck()
	}
	m.setState(StateFallback)
	// Сесійний таймер веде Manager -- сигналимо ПІСЛЯ роботи.
	m.call(m.mgr.OnStalled)
}

func (m *Machine) onRelayResumed() {
	if m.state != StateFallback {
		return
	}
	log.Printf("[%s] data from relay resumed -> waiting for a keyframe for a seamless switch back", m.name)
	m.nodes.RequestSwitch()
}

func (m *Machine) onLadderMinted() {
	// PL10: перевірка OFFLINE лишилась у машині, решта -- у конвеєрі.
	if m.state == StateOffline {
		return
	}
	m.nodes.EnsureLadderBackup()
}

// --- завершення (механічне; рішення про OBS -- на Manager) ---

func (m *Machine) teardownClean() {
	m.nodes.StopRelay()
	m.nodes.BackupStop()
	m.nodes.StopOutput()
	m.nodes.ResetTimeline()
	m.cancelOracle()
	m.setState(StateOffline)
}

// giveUpOnUnreachable — площадка недосяжна (never_succeeded): стоп усього на
// нашому боці й делегування Manager рішення про OBS.
func (m *Machine) giveUpOnUnreachable() {
	m.nodes.StopRelay()
	m.nodes.BackupStop()
	m.nodes.StopOutput()
	m.nodes.ResetTimeline()
	m.cancelOracle()
	m.halted = true
	m.setState(StateOffline)
	m.call(m.mgr.OnGaveUp)
}

func (m *Machine) doShutdown() {
	m.shut = true
	m.publish()
	m.cancelOracle()
	if m.ladderTimer != nil {
		m.ladderTimer.Stop()
		m.ladderTimer = nil
	}
	m.nodes.Shutdown()
}

// --- гейти ---

func (m *Machine) setEnabled(active bool) {
	if m.enabled == active {
		return
	}
	m.enabled = active
	m.publish()
	if active {
		if m.gate && m.onAir() {
			m.nodes.StartOutput()
			log.Printf("[%s] platform enabled (started live)", m.name)
		} else {
			log.Printf("[%s] platform enabled (starts on next broadcast)", m.name)
		}
		return
	}
	m.nodes.SetFailed(false)
	m.nodes.StopOutput()
	log.Printf("[%s] platform disabled", m.name)
}

func (m *Machine) setGate(gateOpen bool) {
	if m.gate == gateOpen {
		return
	}
	m.gate = gateOpen
	m.publish()
	if !gateOpen {
		m.nodes.StopOutput()
		return
	}
	if m.enabled && m.onAir() {
		m.nodes.StartOutput()
	}
}

// --- флепінг виходу ---

func (m *Machine) onOutFlapping(neverSucceeded bool) {
	if neverSucceeded {
		// Жодного успішного під'єднання від старту -- майже напевно
		// невалідний URL/ключ; повторні спроби нічого не змінять.
		if m.state == StateOffline {
			return
		}
		m.nodes.SetFailed(true)
		log.Printf("[%s] platform could not be reached this broadcast (likely invalid URL/key) -- stopping it", m.name)
		m.giveUpOnUnreachable()
		return
	}

	// Було успішне з'єднання цієї трансляції -- схоже на тимчасовий збій.
	// Ретраїмо нескінченно, лише антиспам тостів.
	log.Printf("[%s] connection keeps failing after a previously working one -- possible network issue, still retrying", m.name)
	now := m.clock.Now()
	if now-m.lastFlappingToastAt < FlappingToastCooldownSec {
		return
	}
	m.lastFlappingToastAt = now
	m.emit("warning", "connection keeps failing -- still retrying...")
}

// --- стан / детектор штатного стопу ---

func (m *Machine) setState(newState State) {
	m.state = newState
	m.stateSince = m.wall()
	m.publish()
	m.tracef("set_state " + newState.String())
	m.notify()
}

func (m *Machine) scheduleOracleRecheck() {
	m.cancelOracle()
	m.oracleTimer = m.timers("oracle", floatSeconds(OracleWindowSec),
		func() { m.post("oracle_recheck", m.oracleRecheck) })
}

func (m *Machine) cancelOracle() {
	if m.oracleTimer == nil {
		return
	}
	m.oracleTimer.Stop()
	m.oracleTimer = nil
}

func (m *Machine) oracleRecheck() {
	// No-op, якщо source уже повернувся (реконнект почав безшовний cut) або
	// стан уже не FALLBACK.
	if m.state != StateFallback {
		return
	}
	if m.nodes.PendingSource() != "" {
		return
	}
	if m.gracefulRecent() {
		log.Printf("[%s] graceful stop confirmed after the drop -> clean end (no backup)", m.name)
		m.teardownClean()
	}
}

// --- дрібне ---

func (m *Machine) effectiveEnabled() bool { return m.enabled && m.gate }

// onAir — стан LIVE або FALLBACK.
func (m *Machine) onAir() bool { return m.state == StateLive || m.state == StateFallback }

func (m *Machine) isAux() bool {
	return m.mgr.IsDefaultSource != nil && !m.mgr.IsDefaultSource()
}

// Без Manager-а детектор штатного стопу мовчить, а сесія вважається живою.
func (m *Machine) gracefulRecent() bool {
	return m.mgr.IsGracefulRecent != nil && m.mgr.IsGracefulRecent()
}

func (m *Machine) mainSessionLive() bool {
	return m.mgr.IsMainSessionLive == nil || m.mgr.IsMainSessionLive()
}

func (m *Machine) fallbackProblem() string {
	if m.mgr.FallbackProblem == nil {
		return ""
	}
	return m.mgr.FallbackProblem()
}

func (m *Machine) call(fn func()) {
	if fn != nil {
		fn()
	}
}

func (m *Machine) emit(level, text string) {
	if m.mgr.Emit != nil {
		m.mgr.Emit(level, text)
	}
}

func (m *Machine) notify() {
	if m.mgr.Notify != nil {
		m.mgr.Notify()
	}
}

func (m *Machine) tracef(line string) {
	if m.trace != nil {
		m.trace(line)
	}
}

func (m *Machine) readTimeoutMS() int {
	if m.readTimeout == nil {
		return 0
	}
	return int(m.readTimeout() / time.Millisecond)
}

func floatSeconds(sec float64) time.Duration {
	return time.Duration(sec * float64(time.Second))
}
