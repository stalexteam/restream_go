package platform

import (
	"io"
	"log"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"restream_go/internal/egress"
	"restream_go/internal/fallback"
	"restream_go/internal/ingest"
	"restream_go/internal/proc"
	"restream_go/internal/route"
	"restream_go/internal/timeline"
	"restream_go/internal/wire/flv"
	"restream_go/internal/wire/ts"
)

// StopTimeout — дефолтний таймаут stop обох супервізорів.
const StopTimeout = 5 * time.Second

// Clock — монотонні секунди; ОДИН інстанс на весь конвеєр.
type Clock interface{ Now() float64 }

// OutputProc — спільний контракт двох реалізацій виходу (RtmpPushClient і
// proc.Supervisor).
type OutputProc interface {
	Start()
	Stop(timeout time.Duration)
	IsRunning() bool
	PID() (int, bool)
	EverRanLong() bool
	RestartCount() int
	UptimeSec() float64
}

// Deps — те, що конвеєр бере ззовні: у Python це методи Manager і глобальний конфіг.
type Deps struct {
	// ReadbackURL — Manager.readback_url(source); читається перед КОЖНИМ спавном.
	ReadbackURL func() string
	// ReadTimeout — global_config["read_timeout_ms"]; читається на старті рідера.
	ReadTimeout func() time.Duration
	// NewPreparer — Manager.segments_for_platform + спільний кеш + warm-entry.
	NewPreparer func(bitrate fallback.BitrateProvider, ladderMode bool) *fallback.Preparer
	// Emit — Manager.emit_platform_event.
	Emit func(level, text string)
	// EB — половина go-live-обміну з Manager (потрібна лише EB-плечу).
	EB EBManagerDeps

	LogDir  string
	Clock   Clock
	Sleeper fallback.Sleeper
	Rand    *rand.Rand
}

// Options — Spec, залежності і початкові значення live-apply-полів.
type Options struct {
	Spec Spec
	Deps Deps

	Audio    int
	AudioVOD int
	AudioMap []int

	Server     string
	Key        string
	StreamID   string
	Passphrase string
}

// Pipeline — зібраний конвеєр одного виходу: таймлайн, заглушка, relay і push.
type Pipeline struct {
	Spec Spec
	deps Deps

	Switcher *timeline.Switcher
	Sink     *timeline.OutputSink
	Player   *fallback.Player
	Resume   *fallback.Resume
	Relay    *proc.Supervisor
	Out      OutputProc
	EB       *EBArm

	// Колбеки подій. Стейт-машина підвʼязує їх одразу після New, до першого
	// Start будь-якого вузла.
	OnRelayStalled    func()
	OnRelayResumed    func()
	OnOutFlapping     func(neverSucceeded bool)
	OnSwitchedToRelay func(paramsChanged bool)
	// OnLadderMinted — _ensure_ladder_backup: стейт-машина додає перевірку OFFLINE
	// і кличе EnsureLadderBackup.
	OnLadderMinted func()
	// StateValid — `state == FALLBACK && !shutdown` для фазування аутро (RS5:
	// читати лок-фрі).
	StateValid func() bool

	audio    atomic.Int64
	audioVOD atomic.Int64
	audioMap atomic.Pointer[[]int]
	url      atomic.Pointer[string]
	failed   atomic.Bool

	credMu     sync.Mutex
	server     string
	key        string
	streamID   string
	passphrase string

	preparer atomic.Pointer[fallback.Preparer]
	playlist atomic.Pointer[fallback.FolderPlaylist]

	relayArgs      func() []string
	outputArgs     func() []string
	outputURL      func() string
	announceCodecs []string
}

// New збирає конвеєр: вузли, супервізори, sink-и.
func New(opts Options) *Pipeline {
	p := &Pipeline{Spec: opts.Spec, deps: opts.Deps}
	if p.deps.Clock == nil {
		p.deps.Clock = timeline.SystemClock()
	}
	if p.deps.Rand == nil {
		p.deps.Rand = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	p.audio.Store(int64(opts.Audio))
	p.audioVOD.Store(int64(opts.AudioVOD))
	audioMap := append([]int(nil), opts.AudioMap...)
	p.audioMap.Store(&audioMap)
	if p.Spec.Type == "srt" {
		p.audio.Store(int64(representativeTrack(audioMap)))
	}
	p.server, p.key, p.streamID, p.passphrase = opts.Server, opts.Key, opts.StreamID, opts.Passphrase
	url := p.buildURL()
	p.url.Store(&url)

	p.Switcher = timeline.NewSwitcher(p.deps.Clock)
	if p.Spec.EB {
		p.EB = newEBArm(ebDeps{
			EBManagerDeps: p.deps.EB,
			name:          p.Spec.Name,
			vodTrack:      p.Spec.VODTrack,
			streamKey:     p.Key,
			emit:          p.emit,
			ladderMinted:  p.ladderMinted,
			clock:         p.deps.Clock,
		})
	}
	p.preparer.Store(p.newPreparer())
	p.rebuildPlaylist()

	tag := p.Spec.ProcName
	p.Player = fallback.New(fallback.Options{
		Name:     "backup-" + tag,
		Process:  p.backupTimeline(),
		Sources:  func(role string) string { return p.Preparer().SegmentSource(role) },
		LogDir:   p.deps.LogDir,
		LoopNext: p.loopNext,
		Ladder:   p.Spec.EB,
		Clock:    p.deps.Clock,
		Sleeper:  p.deps.Sleeper,
	})
	p.Resume = fallback.NewResume(fallback.ResumeOptions{
		Name:             p.Spec.Name,
		RelayKeyframeAt:  p.Switcher.RelayKeyframeAt,
		RelayGOPSec:      p.Switcher.RelayGOPSec,
		RequestSwitch:    p.requestSwitchToRelay,
		HasEndReady:      p.Player.HasEndReady,
		PlayEnd:          p.Player.PlayEnd,
		PlayerPhase:      p.Player.Phase,
		SegmentStartedAt: p.Player.SegmentStartedAt,
		EndDuration:      func() (float64, bool) { return p.Preparer().SegmentDuration("end") },
		StateValid:       p.stateValid,
		Clock:            p.deps.Clock,
		Sleeper:          p.deps.Sleeper,
	})

	p.Sink = timeline.NewOutputSink(p.Spec.Name, true, p.deps.Clock)
	p.Out = p.newOutput(tag)
	p.Relay = p.newRelay(tag)
	return p
}

// --- живі значення ---

func (p *Pipeline) Audio() int    { return int(p.audio.Load()) }
func (p *Pipeline) AudioVOD() int { return int(p.audioVOD.Load()) }

func (p *Pipeline) AudioMap() []int { return *p.audioMap.Load() }

func (p *Pipeline) URL() string { return *p.url.Load() }

func (p *Pipeline) Key() string {
	p.credMu.Lock()
	defer p.credMu.Unlock()
	return p.key
}

// Failed — ключ/URL визнано невалідним (never_succeeded); скидається StartOutput.
func (p *Pipeline) Failed() bool     { return p.failed.Load() }
func (p *Pipeline) SetFailed(v bool) { p.failed.Store(v) }

// Preparer — поточний препарер заглушки (ApplyPreset підмінює його наживо).
func (p *Pipeline) Preparer() *fallback.Preparer { return p.preparer.Load() }

// --- live-apply ---

// SetAudio — порт update_tracks: relay уже тримає обидві доріжки, рідер
// підхопить нові індекси на наступному аудіотезі (S7, без bounce).
func (p *Pipeline) SetAudio(audio, audioVOD int) {
	p.audio.Store(int64(audio))
	p.audioVOD.Store(int64(audioVOD))
}

// SetAudioMap — порт update_audio_map; мапа має бути вже клампнута control-шаром.
func (p *Pipeline) SetAudioMap(m []int) {
	cp := append([]int(nil), m...)
	p.audioMap.Store(&cp)
	p.audio.Store(int64(representativeTrack(cp)))
}

// SetCredentials перебудовує URL і повідомляє, чи він реально змінився; bounce
// виходу робить викликач.
func (p *Pipeline) SetCredentials(server, key, streamID, passphrase string) bool {
	p.credMu.Lock()
	changed := server != p.server || key != p.key ||
		streamID != p.streamID || passphrase != p.passphrase
	p.server, p.key, p.streamID, p.passphrase = server, key, streamID, passphrase
	url := p.buildURL()
	p.credMu.Unlock()
	p.url.Store(&url)
	return changed
}

// buildURL — порт _build_url; кличеться під credMu.
func (p *Pipeline) buildURL() string {
	if p.Spec.Type == "srt" {
		return egress.BuildSRTURL(p.server, p.streamID, p.passphrase)
	}
	return egress.BuildPushURL(p.server, p.key)
}

// --- вузли ---

// StartOutput — порт _start_output.
func (p *Pipeline) StartOutput() {
	p.failed.Store(false)
	p.Switcher.RegisterSink(p.Sink)
	p.Out.Start()
}

// StopOutput — порт _stop_output.
func (p *Pipeline) StopOutput() {
	p.Out.Stop(StopTimeout)
	p.Switcher.UnregisterSink(p.Spec.Name)
}

// OutputAlive — порт _output_alive.
func (p *Pipeline) OutputAlive() bool {
	return !p.failed.Load() && (p.Out.EverRanLong() || p.Out.IsRunning())
}

// BounceOutput — рестарт лише цього виходу з новим URL.
func (p *Pipeline) BounceOutput() {
	p.Out.Stop(StopTimeout)
	p.Out.Start()
}

func (p *Pipeline) StartRelay() { p.Relay.Start() }
func (p *Pipeline) StopRelay()  { p.Relay.Stop(StopTimeout) }

// Shutdown — порт shutdown без state-полів.
func (p *Pipeline) Shutdown() {
	p.Relay.Stop(StopTimeout)
	p.Player.Stop()
	p.Out.Stop(StopTimeout)
	p.Sink.Close()
}

// --- заглушка ---

func (p *Pipeline) newPreparer() *fallback.Preparer {
	preparer := p.deps.NewPreparer(p.bitrate, p.Spec.EB)
	if p.Spec.EB {
		preparer.SetLadder(p.EB.LadderRungs())
	}
	return preparer
}

func (p *Pipeline) bitrate() (int, int) {
	stats := p.Switcher.SourceStats()
	return stats.VideoKbps, stats.AudioKbps
}

// ApplyPreset — порт apply_preset без state-гілки: новий препарер під нові
// сегменти, повертає ціль минулої сесії для ResumePreparation.
func (p *Pipeline) ApplyPreset() (fallback.LiveParams, bool) {
	prev, ok := p.Preparer().LastLiveParams()
	p.preparer.Store(p.newPreparer())
	p.rebuildPlaylist()
	return prev, ok
}

// ResumePreparation — хвіст apply_preset/_ensure_ladder_backup: у FALLBACK
// живий probe недоступний, тож беремо параметри, збережені до обриву.
func (p *Pipeline) ResumePreparation(prev fallback.LiveParams, ok bool) {
	if ok {
		p.Preparer().PrepareFromParams(prev)
		return
	}
	p.PrepareBackup()
}

// PrepareBackup — порт _prepare_backup.
func (p *Pipeline) PrepareBackup() {
	preparer := p.Preparer()
	if p.Spec.EB {
		rungs := p.EB.LadderRungs()
		if len(rungs) == 0 {
			log.Printf("[%s] ladder not minted yet -- backup will be prepared once it is", p.Spec.Name)
			return
		}
		preparer.SetLadder(rungs)
	}
	preparer.PrepareAsync(p.deps.ReadbackURL(), p.Audio(), max(p.Spec.Plan.Video, 0))
}

// EnsureLadderBackup — порт _ensure_ladder_backup без перевірки OFFLINE (її
// робить стейт-машина перед викликом).
func (p *Pipeline) EnsureLadderBackup() {
	preparer := p.Preparer()
	if p.EB == nil || preparer.HasLadder() {
		return
	}
	preparer.SetLadder(p.EB.LadderRungs())
	p.ResumePreparation(preparer.LastLiveParams())
}

// BackupProgress — прогрес підготовки заглушки для дашборда.
func (p *Pipeline) BackupProgress() fallback.Progress { return p.Preparer().Progress() }

// backupTimeline — куди гравець заглушки віддає теги.
func (p *Pipeline) backupTimeline() fallback.Process {
	switch p.Spec.BackupTimeline {
	case BackupTimelineChimera:
		return fallback.Process(route.ChimeraBackupTimeline(p.Switcher.Process))
	case BackupTimelineMultitrack:
		return fallback.Process(route.MultitrackBackupTimeline(p.Switcher.Process, p.AudioMap))
	default:
		return p.Switcher.Process
	}
}

// rebuildPlaylist — порт _rebuild_playlist: тип пресета міг змінитись.
func (p *Pipeline) rebuildPlaylist() {
	preparer := p.Preparer()
	if !preparer.IsFolder() {
		p.playlist.Store(nil)
		return
	}
	p.playlist.Store(fallback.NewFolderPlaylist(
		preparer.FolderReadyFiles,
		func() string { return preparer.SegmentSource("separator") },
		p.deps.Rand))
}

// loopNext — порт _loop_next: folder → shuffle-плейлист, sequence → один Loop-файл.
func (p *Pipeline) loopNext() (fallback.LoopItem, bool) {
	if pl := p.playlist.Load(); pl != nil {
		return pl.Next()
	}
	loop := p.Preparer().SegmentSource("loop")
	if loop == "" {
		return fallback.LoopItem{}, false
	}
	return fallback.LoopItem{Path: loop, Loop: true}, true
}

// --- вихід ---

func (p *Pipeline) newOutput(tag string) OutputProc {
	name := "out-" + tag
	switch p.Spec.Output {
	case OutputRTMPPush:
		// push-ffmpeg знищив би 0x95-обгортку VOD-доріжки й Ex-відео драбини.
		p.outputURL = p.URL
		if p.Spec.EB {
			// URL не з конфіга — його видає go-live-обмін.
			p.outputURL = p.EB.PushURL
			p.announceCodecs = []string{"avc1"}
		}
		return egress.NewRtmpPushClient(name, p.outputURL, egress.RtmpPushOptions{
			OnStart:        func(conn egress.PushConn) { p.onOutStart(p.sinkOutput(conn, nil)) },
			OnExit:         p.Sink.Detach,
			OnFlapping:     p.outFlapping,
			AnnounceCodecs: p.announceCodecs,
		})
	case OutputSRTPush:
		// ffmpeg не демуксує наш N-трековий 0x95-мультитрек назад у TS —
		// муксуємо самі, srt-live-transmit лише транспортує байти.
		p.outputArgs = func() []string { return egress.BuildSRTPushArgs(p.URL()) }
	default:
		p.outputArgs = func() []string { return egress.BuildFLVPushArgs(p.URL()) }
	}
	return proc.NewSupervisor(name, p.outputArgs, p.deps.LogDir, proc.Options{
		StdinPipe:  true,
		OnStart:    func(s *proc.Started) { p.onOutStart(p.sinkOutput(nil, s)) },
		OnExit:     p.Sink.Detach,
		OnFlapping: p.outFlapping,
	})
}

// sinkOutput — порт гілок _on_out_start: чим саме sink пише в поточний вихід.
func (p *Pipeline) sinkOutput(conn egress.PushConn, started *proc.Started) timeline.SinkOutput {
	switch {
	case p.Spec.Chimera || p.Spec.EB:
		return conn
	case p.Spec.Type == "srt":
		return ts.NewMuxOutput(started.Stdin)
	default:
		return flv.NewPipeOutput(started.Stdin)
	}
}

// onOutStart — порт _on_out_start.
func (p *Pipeline) onOutStart(out timeline.SinkOutput) {
	p.Switcher.RegisterSink(p.Sink)
	p.Sink.Attach(out, p.Switcher.CurrentHeaders())
}

// RelayArgs / OutputArgs / OutputURL — знімок команд конвеєра (для сверки й
// дашборда); OutputArgs порожній на RTMP-push-виході, OutputURL — на решті.
func (p *Pipeline) RelayArgs() []string { return p.relayArgs() }

func (p *Pipeline) OutputArgs() []string {
	if p.outputArgs == nil {
		return nil
	}
	return p.outputArgs()
}

func (p *Pipeline) OutputURL() (string, bool) {
	if p.outputURL == nil {
		return "", false
	}
	return p.outputURL(), true
}

// AnnounceCodecs — fourcc, які RTMP-push оголошує в connect (лише EB-плече).
func (p *Pipeline) AnnounceCodecs() []string { return p.announceCodecs }

// --- relay ---

func (p *Pipeline) newRelay(tag string) *proc.Supervisor {
	p.relayArgs = func() []string { return ingest.BuildSRTReadbackArgs(p.deps.ReadbackURL()) }
	if p.Spec.Relay == RelayRTMP {
		p.relayArgs = func() []string { return ingest.BuildRTMPReadbackArgs(p.deps.ReadbackURL(), p.Audio()) }
	}
	return proc.NewSupervisor("relay-"+tag, p.relayArgs, p.deps.LogDir, proc.Options{
		CaptureStdout: true,
		OnStart:       func(s *proc.Started) { go p.ReadRelay(s.Stdout) },
	})
}

// ReadRelay читає один запуск relay до EOF і розкладає теги по канонічному
// таймлайну згідно з режимом Spec.Plan.
func (p *Pipeline) ReadRelay(stdout io.Reader) {
	var timeout time.Duration
	if p.deps.ReadTimeout != nil {
		timeout = p.deps.ReadTimeout()
	}
	if p.Spec.Plan.Mode == route.ModePlainFLV {
		_ = flv.ReadTags(stdout, "relay", p.forwardFLV, &flv.ReadTagsOptions{
			ReadTimeout: timeout,
			OnStall:     p.relayStalled,
			OnResume:    p.relayResumed,
		})
		return
	}
	_ = ts.ReadTags(stdout, p.NewRouter().Route, &ts.ReadTagsOptions{
		ReadTimeout:    timeout,
		OnStall:        p.relayStalled,
		OnResume:       p.relayResumed,
		AllVideoTracks: p.Spec.Plan.AllVideoTracks,
	})
}

// NewRouter — роутер режиму цього конвеєра; фіксовані режими беруть поточні
// індекси доріжок у мить старту рідера (їхня зміна топологічна).
func (p *Pipeline) NewRouter() *route.Router {
	emit := route.Emit(p.Switcher.Process)
	role := p.Spec.Plan.VideoRole
	switch p.Spec.Plan.Mode {
	case route.ModeEB:
		var vod func() int
		if p.Spec.Chimera {
			vod = p.AudioVOD
		}
		return route.NewEB(emit, p.Audio, vod)
	case route.ModeChimeraSelect:
		return route.NewChimeraSelect(emit, p.Audio, p.AudioVOD, role)
	case route.ModeChimeraFixed:
		return route.NewChimeraFixed(emit, p.Audio(), p.AudioVOD(), role)
	case route.ModeMultitrackSelect:
		return route.NewMultitrackSelect(emit, p.AudioMap, role)
	case route.ModeTrackSelect:
		return route.NewTrackSelect(emit, p.Audio, role)
	default:
		return route.NewMultitrackFixed(emit, p.Audio(), role)
	}
}

func (p *Pipeline) forwardFLV(source string, tagType byte, timestamp uint32, payload []byte) {
	p.Switcher.Process(source, tagType, int64(timestamp), payload)
}

// --- колбеки ---

func (p *Pipeline) relayStalled() {
	if p.OnRelayStalled != nil {
		p.OnRelayStalled()
	}
}

func (p *Pipeline) relayResumed() {
	if p.OnRelayResumed != nil {
		p.OnRelayResumed()
	}
}

func (p *Pipeline) outFlapping(neverSucceeded bool) {
	if p.OnOutFlapping != nil {
		p.OnOutFlapping(neverSucceeded)
	}
}

func (p *Pipeline) requestSwitchToRelay(notBefore *float64) {
	p.Switcher.RequestSwitch("relay", p.switched, notBefore)
}

func (p *Pipeline) switched(paramsChanged bool) {
	if p.OnSwitchedToRelay != nil {
		p.OnSwitchedToRelay(paramsChanged)
	}
}

func (p *Pipeline) stateValid() bool { return p.StateValid != nil && p.StateValid() }

func (p *Pipeline) ladderMinted() {
	if p.OnLadderMinted != nil {
		p.OnLadderMinted()
	}
}

func (p *Pipeline) emit(level, text string) {
	if p.deps.Emit != nil {
		p.deps.Emit(level, text)
	}
}

// representativeTrack — self.audio для srt-виходу: перша замаплена доріжка.
func representativeTrack(audioMap []int) int {
	for _, track := range audioMap {
		if track != route.Unmapped {
			return track
		}
	}
	return 0
}
