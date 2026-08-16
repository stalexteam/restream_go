package control

import (
	"log"
	"math/rand"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"restream_go/internal/fallback"
	"restream_go/internal/platform"
	"restream_go/internal/probe"
	"restream_go/internal/route"
	"restream_go/internal/timeline"
	"restream_go/internal/wire/ts"
)

// Константи Manager-а.
const (
	// ConnectTimeoutToastCooldownSec — антиспам тостів про connect timeout.
	ConnectTimeoutToastCooldownSec = 15.0
	// OracleWindowSec — вікно «штатний стоп OBS ↔ обрив aux-source».
	OracleWindowSec = platform.OracleWindowSec
	// PingIntervalSec — пауза між циклами пінгу площадок.
	PingIntervalSec = 3.0
	// AspectTolerance — допуск на округлення при звірці сходинок драбини.
	AspectTolerance = 0.02
	// DefaultOfflineTimeoutSec — дефолт сесійного вікна очікування.
	DefaultOfflineTimeoutSec = 1800
)

// Probes — probe-функції контракту; шов для golden-сверки.
type Probes struct {
	TSManifest   func(url string) (ts.Manifest, bool)
	TrackCounts  func(url string) (probe.TrackCounts, bool)
	StreamParams func(url string, audioIndex, videoIndex int) (probe.StreamParams, bool)
}

// Prober — вимір RTT площадки (net_probe, етап 6); nil = ping-петлі немає.
type Prober func(url string, useICMP bool) (ms int, ok bool)

// Options — залежності Manager-а; нульові значення дають прод-дефолти.
type Options struct {
	BaseDir    string
	ConfigPath string

	Clock   platform.Clock
	Wall    func() float64
	Timers  platform.TimerFactory
	Probes  Probes
	Ping    Prober
	Sleeper fallback.Sleeper
	Rand    *rand.Rand

	// newRuntime / spawn — шви golden-реплею; прод бере platformRuntime і `go`.
	newRuntime func(m *Manager, e *platformEntry) Runtime
	spawn      func(fn func())
	persist    func(path string, config *Dict) error
	transcodes func() []fallback.ActiveTranscode
}

// platformEntry — реєстрова частина платформи: копія cfg, топологічний
// Spec і сам Runtime.
type platformEntry struct {
	name string
	cfg  *Dict
	spec platform.Spec
	src  *Source
	rt   Runtime

	segments Segments

	ptype        string
	vodTrack     bool
	enabled      bool
	gate         bool
	groupID      string
	sourceName   string
	audio        int
	audioVOD     int
	video        int
	audioMap     []any
	server       string
	key          string
	streamID     string
	passphrase   string
	backupPreset string
}

// Manager — верхній рівень «один OBS»: реєстри, роутинг хуків, контракт
// source, сесія/латч/детектор штатного стопу, CRUD і персист.
type Manager struct {
	config     *Dict
	baseDir    string
	configPath string
	logDir     string

	mu       sync.Mutex
	stopping atomic.Bool
	stopped  chan struct{}

	publicHost    string
	rtmpPort      int
	srtPort       int
	obsIngestPass string
	readTimeoutMS atomic.Int64

	lastStartedObsID          string
	lastHaltedObsID           string
	lastConnectTimeoutToastAt float64

	// Детектор штатного стопу і сесія головного OBS — читаються платформами
	// лок-фрі, міняються лише під mu.
	gracefulStopAt   atomic.Pointer[float64]
	sessionState     atomic.Int32
	timeoutTimer     platform.Timer
	fallbackDeadline *float64

	// Колбеки в hub — підключаються ззовні (cmd/restreamd).
	OnChange  func()
	OnEvent   func(level, text string)
	OnControl func(command string)

	backupCache *fallback.Cache
	warmStore   *fallback.WarmStore
	warmAbort   chan struct{}
	abortOnce   sync.Once

	presets []*Dict
	groups  []*Dict

	sources       *registry[*Source]
	byPath        *registry[*Source]
	defaultSource atomic.Pointer[Source]

	platforms *registry[*platformEntry]

	clock      platform.Clock
	wall       func() float64
	timers     platform.TimerFactory
	probes     Probes
	ping       Prober
	sleeper    fallback.Sleeper
	rand       *rand.Rand
	newRuntime func(m *Manager, e *platformEntry) Runtime
	spawn      func(fn func())
	persist    func(path string, config *Dict) error
	transcodes func() []fallback.ActiveTranscode
}

// New піднімає Manager над уже прочитаним config.json.
func New(config *Dict, opts Options) *Manager {
	m := &Manager{
		config:     config,
		baseDir:    opts.BaseDir,
		configPath: opts.ConfigPath,
		logDir:     filepath.Join(opts.BaseDir, "logs"),
		stopped:    make(chan struct{}),
		warmAbort:  make(chan struct{}),
		sources:    newRegistry[*Source](),
		byPath:     newRegistry[*Source](),
		platforms:  newRegistry[*platformEntry](),
		clock:      opts.Clock,
		wall:       opts.Wall,
		timers:     opts.Timers,
		probes:     opts.Probes,
		ping:       opts.Ping,
		sleeper:    opts.Sleeper,
		rand:       opts.Rand,
		newRuntime: opts.newRuntime,
		spawn:      opts.spawn,
		persist:    opts.persist,
		transcodes: opts.transcodes,
	}
	if m.configPath == "" {
		m.configPath = filepath.Join(opts.BaseDir, "config.json")
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
	if m.probes.TSManifest == nil {
		m.probes.TSManifest = func(url string) (ts.Manifest, bool) { return probe.ProbeTSManifest(url, 8.0, 1.0) }
	}
	if m.probes.TrackCounts == nil {
		m.probes.TrackCounts = probe.ProbeTrackCounts
	}
	if m.probes.StreamParams == nil {
		m.probes.StreamParams = probe.ProbeStreamParams
	}
	if m.rand == nil {
		m.rand = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	if m.newRuntime == nil {
		m.newRuntime = newPlatformRuntime
	}
	if m.spawn == nil {
		m.spawn = func(fn func()) { go fn() }
	}
	if m.persist == nil {
		m.persist = Persist
	}
	if m.transcodes == nil {
		m.transcodes = func() []fallback.ActiveTranscode { return m.backupCache.ActiveTranscodes() }
	}

	m.publicHost = pyStr(config.GetOr("public_host", ""))
	m.rtmpPort = intOr(config, "mediamtx_rtmp_port", 1935)
	m.srtPort = intOr(config, "mediamtx_srt_port", 8890)
	m.obsIngestPass = pyStr(config.GetOr("obs_pass", ""))
	m.readTimeoutMS.Store(int64(intOr(config, "read_timeout_ms", 0)))
	m.sessionState.Store(int32(platform.StateOffline))

	maxConcurrent := intOr(config, "max_concurrent_transcodes", 1)
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	threads := intOr(config, "transcode_threads", 1)
	if threads < 0 {
		threads = 0
	}
	m.backupCache = fallback.NewCache(filepath.Join(m.baseDir, "tmp", "backup-cache"), maxConcurrent, threads)
	m.warmStore = fallback.NewWarmStore(filepath.Join(m.baseDir, "tmp", "backup-warm.json"))

	// Пресети — ДО платформ: їхня конструкція резолвить сегменти пресета.
	m.presets = NormalizePresets(config)
	m.groups = NormalizeGroups(config)

	for _, scfg := range NormalizeSources(config) {
		if lp, _ := scfg.Get("live_path"); !pyTruthy(lp) {
			scfg.Set("live_path", m.assignPath(pyStr(scfg.GetOr("name", ""))))
		}
		m.instantiateSource(scfg)
	}
	for _, pcfg := range NormalizePlatforms(config) {
		m.instantiatePlatform(pcfg)
	}
	config.Pop("pipelines")

	m.warmStore.Retain(m.platforms.Keys())
	if m.ping != nil {
		go m.pingLoop()
	}
	m.spawn(m.warmBackupCache)
	return m
}

func wallNow() float64 { return float64(time.Now().UnixNano()) / 1e9 }

type stdTimer struct{ t *time.Timer }

func (s stdTimer) Stop() { s.t.Stop() }

func systemTimers(_ string, d time.Duration, fire func()) platform.Timer {
	return stdTimer{time.AfterFunc(d, fire)}
}

func intOr(d *Dict, key string, def int) int {
	v, ok := pyInt(d.GetOr(key, int64(def)))
	if !ok {
		return def
	}
	return int(v)
}

// --- інстанціювання ---

func (m *Manager) instantiateSource(scfg *Dict) *Source {
	source := newSource(scfg, ingestPorts{rtmp: m.rtmpPort, srt: m.srtPort},
		pyStr(m.config.GetOr("internal_user", "")), pyStr(m.config.GetOr("internal_pass", "")))
	m.sources.Set(source.name, source)
	m.byPath.Set(source.livePath, source)
	if source.isDefault {
		m.defaultSource.Store(source)
	}
	return source
}

func (m *Manager) instantiatePlatform(pcfg *Dict) *platformEntry {
	e := &platformEntry{
		name:         pyStr(pcfg.GetOr("name", "")),
		cfg:          pcfg,
		ptype:        pyStr(pcfg.GetOr("type", "rtmp")),
		vodTrack:     pyTruthy(pcfg.GetOr("vod_track", false)),
		enabled:      pyTruthy(pcfg.GetOr("enabled", false)),
		groupID:      pyStrOr(pcfg.GetOr("group", ""), defaultGroupID),
		sourceName:   pyStrOr(pcfg.GetOr("source", ""), ""),
		server:       pyStr(pcfg.GetOr("server", "")),
		key:          pyStr(pcfg.GetOr("key", "")),
		streamID:     pyStr(pcfg.GetOr("streamid", "")),
		passphrase:   pyStr(pcfg.GetOr("passphrase", "")),
		backupPreset: pyStrOr(pcfg.GetOr("backup_preset", ""), defaultPresetID),
	}
	e.audio = cfgInt(pcfg, "audio", 0)
	e.audioVOD = cfgInt(pcfg, "audio_vod", 1)
	e.src, _ = m.sources.Get(e.sourceName)
	if e.ptype == "srt" {
		am, _ := pcfg.Get("audio_map")
		e.audioMap = clampAudioMap(am)
		e.audio = firstMappedTrack(e.audioMap)
	}
	e.spec = platform.NewSpec(platform.Config{
		Name:       e.name,
		Type:       e.ptype,
		VODTrack:   e.vodTrack,
		SourceName: e.sourceName,
		Video:      cfgInt(pcfg, "video", 0),
	}, m.sourceContract(e.src))
	e.video = e.spec.Plan.Video
	e.gate = m.groupEnabledLocked(e.groupID)
	e.segments = m.segmentsForPreset(pyStr(pcfg.GetOr("backup_preset", "")))

	e.rt = m.newRuntime(m, e)
	m.platforms.Set(e.name, e)
	return e
}

// sourceContract — контракт source для Spec; невідомий source поводиться як
// однодоріжковий rtmp (аксессори віддають дефолти).
func (m *Manager) sourceContract(src *Source) platform.Source {
	if src == nil {
		return platform.Source{Type: "rtmp", AudioTracks: 1}
	}
	return platform.Source{Type: src.stype, AudioTracks: src.audioTracks, EB: src.enhancedBroadcasting}
}

func (m *Manager) platformsOf(sourceName string) []*platformEntry {
	var out []*platformEntry
	for _, e := range m.platforms.Values() {
		if e.sourceName == sourceName {
			out = append(out, e)
		}
	}
	return out
}

// --- прогрів кеша заглушок ---

// NoteBackupTarget — платформа визначила ціль нормалізації.
func (m *Manager) NoteBackupTarget(platformName string, live fallback.LiveParams, target fallback.TargetParams) {
	m.warmStore.Put(platformName, live, target)
}

// BackupWarmEntry — ціль минулої сесії для платформи.
func (m *Manager) BackupWarmEntry(platformName string) (fallback.WarmEntry, bool) {
	return m.warmStore.Get(platformName)
}

// warmBackupCache — зібрати заглушки під цілі минулої сесії, поки машина
// простоює; перерва на першій же публікації.
func (m *Manager) warmBackupCache() {
	m.mu.Lock()
	entries := m.platforms.Values()
	m.mu.Unlock()
	for _, e := range entries {
		if m.stopping.Load() || aborted(m.warmAbort) {
			return
		}
		entry, ok := m.warmStore.Get(e.name)
		if !ok {
			continue
		}
		e.rt.WarmBackup(entry, m.warmAbort)
	}
}

func aborted(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func (m *Manager) groupEnabledLocked(groupID string) bool {
	for _, g := range m.groups {
		if pyStr(g.GetOr("id", "")) == groupID {
			return pyTruthy(g.GetOr("enabled", false))
		}
	}
	return pyTruthy(m.defaultGroupLocked().GetOr("enabled", false))
}

func (m *Manager) defaultGroupLocked() *Dict {
	for _, g := range m.groups {
		if v, _ := g.Get("is_default"); pyTruthy(v) {
			return g
		}
	}
	return m.groups[0]
}

// --- лок-фрі аксессори для платформ ---

// ReadbackURL — readback source.
func (m *Manager) ReadbackURL(sourceName string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	src, _ := m.sources.Get(sourceName)
	return src.ReadbackURL()
}

// IsDefaultSource — чи це дефолтний source.
func (m *Manager) IsDefaultSource(sourceName string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	src, ok := m.sources.Get(sourceName)
	return ok && src.isDefault
}

// SourceAudioTracks / SourceIsEB / SourceProbeGen / SourceVideoIndex —
// аксессори:1738-1754.
func (m *Manager) SourceAudioTracks(sourceName string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if src, ok := m.sources.Get(sourceName); ok {
		return src.audioTracks
	}
	return 1
}

func (m *Manager) SourceIsEB(sourceName string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	src, ok := m.sources.Get(sourceName)
	return ok && src.enhancedBroadcasting
}

func (m *Manager) SourceProbeGen(sourceName string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	src, _ := m.sources.Get(sourceName)
	return src.ProbeGen()
}

func (m *Manager) SourceVideoIndex(sourceName string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if src, ok := m.sources.Get(sourceName); ok {
		return int(src.videoIdx.Load())
	}
	return 0
}

// IsGracefulRecent — детектор штатного стопу; лок-фрі.
// BaseDir — корінь інсталяції.
func (m *Manager) BaseDir() string { return m.baseDir }

// ConfigValue — глобальне поле config, прочитане під локом.
func (m *Manager) ConfigValue(key string) (any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.config.Get(key)
}

func (m *Manager) IsGracefulRecent() bool {
	at := m.gracefulStopAt.Load()
	return at != nil && (m.clock.Now()-*at) < OracleWindowSec
}

// IsMainSessionLive — чи головна сесія активна; лок-фрі.
func (m *Manager) IsMainSessionLive() bool {
	return platform.State(m.sessionState.Load()) != platform.StateOffline
}

// --- дрібне ---

func (m *Manager) notify() {
	if m.OnChange != nil {
		m.OnChange()
	}
}

func (m *Manager) emitEvent(level, text string) {
	if m.OnEvent != nil {
		m.OnEvent(level, text)
	}
}

func (m *Manager) emitPlatformEvent(platformName, level, text string) {
	m.emitEvent(level, "["+platformName+"] "+text)
}

func (m *Manager) requestStopStreamingInOBS() {
	log.Printf("sending stop_streaming control to any connected obs-source.html " +
		"(requires its Page permission set to \"Full access to OBS\" to take effect)")
	if m.OnControl != nil {
		m.OnControl("stop_streaming")
	}
}

// blockingUnlocked віддає лок на час блокуючого виклику в платформу: тримати
// його там означало б чекати горутину, яка сама чекає цей лок.
func (m *Manager) blockingUnlocked(fn func()) {
	m.mu.Unlock()
	fn()
	m.mu.Lock()
}

// Shutdown — знести все.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.stopping.Swap(true) {
		close(m.stopped)
	}
	m.cancelSessionTimeoutLocked()
	for _, e := range m.platforms.Values() {
		rt := e.rt
		m.blockingUnlocked(rt.Shutdown)
	}
}

// --- ping-петля ---

func (m *Manager) pingLoop() {
	inflight := map[string]bool{}
	var inflightMu sync.Mutex
	for !m.stopping.Load() {
		m.mu.Lock()
		useICMP := pyTruthy(m.config.GetOr("icmp_ping", false))
		entries := m.platforms.Values()
		m.mu.Unlock()
		for _, e := range entries {
			if !e.rt.EffectiveEnabled() {
				e.rt.SetRTT(0, false)
				continue
			}
			inflightMu.Lock()
			busy := inflight[e.name]
			if !busy {
				inflight[e.name] = true
			}
			inflightMu.Unlock()
			if busy {
				continue
			}
			go func(e *platformEntry) {
				defer func() {
					inflightMu.Lock()
					delete(inflight, e.name)
					inflightMu.Unlock()
				}()
				e.rt.SetRTT(m.ping(e.rt.URL(), useICMP))
			}(e)
		}
		select {
		case <-m.stopped:
			return
		case <-time.After(floatSeconds(PingIntervalSec)):
		}
	}
}

func floatSeconds(sec float64) time.Duration { return time.Duration(sec * float64(time.Second)) }

// --- шляхи ---

var assignPathRe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// assignPath — дружній slug імені -> live/<slug>, унікальний у _by_path.
func (m *Manager) assignPath(name string) string {
	slug := strings.ToLower(strings.Trim(assignPathRe.ReplaceAllString(name, "-"), "-"))
	if slug == "" {
		slug = "source"
	}
	candidate := "live/" + slug
	for i := 2; m.byPath.Has(candidate); i++ {
		candidate = "live/" + slug + "-" + strconv.Itoa(i)
	}
	return candidate
}

// --- конверсії audio_map ---

// audioMapTracks — конфіг-форма (int64/nil) у форму роутера (route.Unmapped).
func audioMapTracks(raw []any) []int {
	out := make([]int, len(raw))
	for i, v := range raw {
		track, ok := pyInt(v)
		if !ok {
			out[i] = route.Unmapped
			continue
		}
		out[i] = int(track)
	}
	return out
}

func firstMappedTrack(raw []any) int {
	for _, v := range raw {
		if track, ok := pyInt(v); ok {
			return int(track)
		}
	}
	return 0
}

func cfgInt(cfg *Dict, key string, def int64) int {
	v, ok := pyInt(pyOr(cfg.GetOr(key, def), int64(0)))
	if !ok {
		return 0
	}
	return int(v)
}

func pyStrOr(v any, def string) string {
	if s := pyStr(v); s != "" {
		return s
	}
	return def
}
