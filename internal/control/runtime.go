package control

import (
	"sync/atomic"
	"time"

	"restream_go/internal/fallback"
	"restream_go/internal/platform"
)

// Segments — сегменти fallback-пресета з уже вирішеними шляхами (порт
// _segments_for_preset); "" = ролі немає.
type Segments struct {
	Kind        string
	Start       string
	End         string
	Loop        string
	Separator   string
	FolderFiles []string
}

// Runtime — усе, що Manager робить із однією платформою. Прод-реалізація —
// Pipeline+Machine; golden-реплей підставляє рекордер.
type Runtime interface {
	OnSourceAvailable()
	OnSourceUnavailable()

	SetEnabled(active bool)
	SetGate(open bool)
	SetGroup(groupID string, gateOpen bool)
	UpdateCredentials(server, key, streamID, passphrase string)
	UpdateTracks(audio, audioVOD int)
	UpdateAudioMap(audioMap []int)
	// ApplyPreset — сегменти йдуть тим самим викликом, тож джерело сегментів
	// оновлюється ДО перестворення препарера.
	ApplyPreset(presetID string, seg Segments)

	EnsureEBSession() bool
	WarmBackup(entry fallback.WarmEntry, abort <-chan struct{})

	Halt() bool
	GracefulStopIfFallback()
	Shutdown()

	State() platform.State
	EffectiveEnabled() bool
	Failed() bool
	URL() string
	SetRTT(ms int, ok bool)
	Status() platform.Status
	BackupProgress() fallback.Progress
}

// platformRuntime — прод-реалізація Runtime поверх конвеєра і стейт-машини.
type platformRuntime struct {
	pipeline *platform.Pipeline
	machine  *platform.Machine
	segments atomic.Pointer[Segments]
	shut     atomic.Bool
}

func newPlatformRuntime(m *Manager, e *platformEntry) Runtime {
	r := &platformRuntime{}
	seg := e.segments
	r.segments.Store(&seg)

	src := e.src
	r.pipeline = platform.New(platform.Options{
		Spec: e.spec,
		Deps: platform.Deps{
			ReadbackURL: src.ReadbackURL,
			ReadTimeout: m.readTimeout,
			NewPreparer: func(bitrate fallback.BitrateProvider, ladderMode bool) *fallback.Preparer {
				return m.newPreparer(e.name, r.segments.Load(), e.cfg, bitrate, ladderMode)
			},
			Emit: func(level, text string) { m.emitPlatformEvent(e.name, level, text) },
			EB: platform.EBManagerDeps{
				ProbeGen:       src.ProbeGen,
				ObservedLadder: src.ObservedLadder,
			},
			LogDir:  m.logDir,
			Clock:   m.clock,
			Sleeper: m.sleeper,
			Rand:    m.rand,
		},
		Audio:      e.audio,
		AudioVOD:   e.audioVOD,
		AudioMap:   audioMapTracks(e.audioMap),
		Server:     e.server,
		Key:        e.key,
		StreamID:   e.streamID,
		Passphrase: e.passphrase,
	})
	r.machine = platform.NewMachine(r.pipeline, platform.MachineOptions{
		Manager:     m.managerDeps(e),
		Group:       e.groupID,
		Enabled:     e.enabled,
		Gate:        e.gate,
		ReadTimeout: m.readTimeout,
		Clock:       m.clock,
		Wall:        m.wall,
		Timers:      m.timers,
	})
	return r
}

func (r *platformRuntime) OnSourceAvailable()   { r.machine.OnSourceAvailable() }
func (r *platformRuntime) OnSourceUnavailable() { r.machine.OnSourceUnavailable() }

func (r *platformRuntime) SetEnabled(active bool) { r.machine.SetEnabled(active) }
func (r *platformRuntime) SetGate(open bool)      { r.machine.SetGate(open) }

func (r *platformRuntime) SetGroup(groupID string, gateOpen bool) {
	r.machine.SetGroup(groupID, gateOpen)
}

func (r *platformRuntime) UpdateCredentials(server, key, streamID, passphrase string) {
	r.machine.UpdateCredentials(server, key, streamID, passphrase)
}

func (r *platformRuntime) UpdateTracks(audio, audioVOD int) { r.machine.UpdateTracks(audio, audioVOD) }
func (r *platformRuntime) UpdateAudioMap(audioMap []int)    { r.machine.UpdateAudioMap(audioMap) }

func (r *platformRuntime) ApplyPreset(_ string, seg Segments) {
	r.segments.Store(&seg)
	r.machine.ApplyPreset()
}

func (r *platformRuntime) EnsureEBSession() bool {
	if r.pipeline.EB == nil {
		return false
	}
	return r.pipeline.EB.EnsureSession()
}

func (r *platformRuntime) WarmBackup(entry fallback.WarmEntry, abort <-chan struct{}) {
	if r.shut.Load() {
		return
	}
	r.pipeline.Preparer().PrepareWarm(entry.Live, entry.Target, abort)
}

func (r *platformRuntime) Halt() bool              { return r.machine.Halt() }
func (r *platformRuntime) GracefulStopIfFallback() { r.machine.GracefulStopIfFallback() }

func (r *platformRuntime) Shutdown() {
	r.shut.Store(true)
	r.machine.Shutdown()
}

func (r *platformRuntime) State() platform.State  { return r.machine.State() }
func (r *platformRuntime) EffectiveEnabled() bool { return r.machine.EffectiveEnabled() }
func (r *platformRuntime) Failed() bool           { return r.pipeline.Failed() }
func (r *platformRuntime) URL() string            { return r.pipeline.URL() }

func (r *platformRuntime) SetRTT(ms int, ok bool) { r.machine.SetRTT(ms, ok) }

func (r *platformRuntime) Status() platform.Status           { return r.machine.Status() }
func (r *platformRuntime) BackupProgress() fallback.Progress { return r.pipeline.BackupProgress() }

// managerDeps — колбеки платформи в Manager: усі три предикати лок-фрі,
// решта бере Manager.lock із горутини-власника машини.
func (m *Manager) managerDeps(e *platformEntry) platform.ManagerDeps {
	return platform.ManagerDeps{
		IsDefaultSource:   func() bool { return e.src != nil && m.defaultSource.Load() == e.src },
		IsGracefulRecent:  m.IsGracefulRecent,
		IsMainSessionLive: m.IsMainSessionLive,
		OnStalled:         func() { m.OnPlatformStalled(e.name) },
		OnRecovered:       func() { m.OnPlatformRecovered(e.name) },
		OnGaveUp:          func() { m.OnPlatformGaveUp(e.name) },

		FallbackProblem:    func() string { return m.fallbackPresetProblem(e) },
		OnFallbackUnusable: func(problem string) { m.OnPlatformFallbackUnusable(e.name, problem) },

		Notify: m.notify,
		Emit:   func(level, text string) { m.emitPlatformEvent(e.name, level, text) },
	}
}

// newPreparer — сегменти пресета + спільний кеш +
// ціль минулої сесії.
func (m *Manager) newPreparer(name string, seg *Segments, cfg *Dict,
	bitrate fallback.BitrateProvider, ladderMode bool) *fallback.Preparer {

	var prevTarget fallback.TargetParams
	if entry, ok := m.warmStore.Get(name); ok {
		prevTarget = entry.Target
	}
	videoOverride, _ := pyInt(cfg.GetOr("output_video_bitrate_kbps", int64(0)))
	audioOverride, _ := pyInt(cfg.GetOr("output_audio_bitrate_kbps", int64(0)))
	return fallback.NewPreparer(fallback.PreparerOptions{
		Kind:        seg.Kind,
		Loop:        seg.Loop,
		Start:       seg.Start,
		End:         seg.End,
		Separator:   seg.Separator,
		FolderFiles: seg.FolderFiles,

		Cache:      m.backupCache,
		LadderMode: ladderMode,
		Bitrate:    bitrate,

		VideoBitrateOverrideKbps: int(videoOverride),
		AudioBitrateOverrideKbps: int(audioOverride),

		OnTargetParams: func(live fallback.LiveParams, target fallback.TargetParams) {
			m.NoteBackupTarget(name, live, target)
		},
		PreviousTarget: prevTarget,
		Clock:          m.clock,
		Sleeper:        m.sleeper,
	})
}

// readTimeout — global_config["read_timeout_ms"] живцем (K3: кличуть чужі горутини).
func (m *Manager) readTimeout() time.Duration {
	return time.Duration(m.readTimeoutMS.Load()) * time.Millisecond
}
