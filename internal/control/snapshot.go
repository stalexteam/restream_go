package control

import (
	"log"
	"strconv"
	"strings"

	"restream_go/internal/platform"
)

// SourceStatus — форма source у snapshot дашборда (C2,:2796).
type SourceStatus struct {
	Name                 string       `json:"name"`
	Type                 string       `json:"type"`
	VODTrack             bool         `json:"vod_track"`
	EnhancedBroadcasting bool         `json:"enhanced_broadcasting"`
	IsDefault            bool         `json:"is_default"`
	LivePath             string       `json:"live_path"`
	Available            bool         `json:"available"`
	Validated            bool         `json:"validated"`
	ContractError        *string      `json:"contract_error"`
	AvailableSince       *float64     `json:"available_since"`
	AudioTracks          int          `json:"audio_tracks"`
	VideoTracks          int          `json:"video_tracks"`
	TrackLabels          []string     `json:"track_labels"`
	Media                *SourceMedia `json:"media"`
}

// SessionStatus — стан сесії головного OBS у snapshot.
type SessionStatus struct {
	State            string   `json:"state"`
	FallbackDeadline *float64 `json:"fallback_deadline"`
}

// Snapshot — повний знімок для дашборда.
type Snapshot struct {
	Sources              []SourceStatus    `json:"sources"`
	Platforms            []platform.Status `json:"platforms"`
	Groups               []*Dict           `json:"groups"`
	Session              SessionStatus     `json:"session"`
	ManualHalt           bool              `json:"manual_halt"`
	OBSWidgetShowBitrate bool              `json:"obs_widget_show_bitrate"`
}

// FallbackProgress — зведений прогрес підготовки заглушок.
type FallbackProgress struct {
	TotalBytes  int64    `json:"total_bytes"`
	ReadyBytes  int64    `json:"ready_bytes"`
	TotalFiles  int      `json:"total_files"`
	ReadyFiles  int      `json:"ready_files"`
	FailedFiles int      `json:"failed_files"`
	Started     bool     `json:"started"`
	Converting  []string `json:"converting"`
	Platforms   int      `json:"platforms"`
	Transcodes  [][]any  `json:"transcodes"`
}

// Status — знімок для дашборда; status платформ береться ПОЗА Manager.lock
func (m *Manager) Status() Snapshot {
	m.mu.Lock()
	sources := []SourceStatus{}
	for _, s := range m.sourcesDefaultFirstLocked() {
		sources = append(sources, SourceStatus{
			Name:                 s.name,
			Type:                 s.stype,
			VODTrack:             s.vodTrack,
			EnhancedBroadcasting: s.enhancedBroadcasting,
			IsDefault:            s.isDefault,
			LivePath:             s.livePath,
			Available:            s.available,
			Validated:            s.validated,
			ContractError:        optString(s.contractError),
			AvailableSince:       optFloat(s.availableSince, s.hasAvailableSince),
			AudioTracks:          s.audioTracks,
			VideoTracks:          s.videoTracks,
			TrackLabels:          s.trackLabels(),
			Media:                s.media,
		})
	}
	groups := []*Dict{}
	for _, g := range defaultFirst(m.groups) {
		groups = append(groups, g.Clone())
	}
	entries := m.platforms.Values()
	session := SessionStatus{
		State:            platform.State(m.sessionState.Load()).String(),
		FallbackDeadline: m.fallbackDeadline,
	}
	manualHalt := m.isCurrentSessionHaltedLocked()
	showBitrate := pyTruthy(m.config.GetOr("obs_widget_show_bitrate", false))
	m.mu.Unlock()

	platforms := make([]platform.Status, 0, len(entries))
	for _, e := range entries {
		platforms = append(platforms, e.rt.Status())
	}
	return Snapshot{
		Sources:              sources,
		Platforms:            platforms,
		Groups:               groups,
		Session:              session,
		ManualHalt:           manualHalt,
		OBSWidgetShowBitrate: showBitrate,
	}
}

// FallbackProgress — сума по всіх платформах: той самий файл рахується стільки
// разів, скільки платформ його готують.
func (m *Manager) FallbackProgress() FallbackProgress {
	m.mu.Lock()
	entries := m.platforms.Values()
	m.mu.Unlock()

	total := FallbackProgress{Converting: []string{}}
	for _, e := range entries {
		progress := e.rt.BackupProgress()
		total.TotalBytes += progress.TotalBytes
		total.ReadyBytes += progress.ReadyBytes
		total.TotalFiles += progress.TotalFiles
		total.ReadyFiles += progress.ReadyFiles
		total.FailedFiles += progress.FailedFiles
		total.Started = total.Started || progress.Started
		if progress.Current != "" {
			total.Converting = append(total.Converting, e.name+": "+progress.Current)
		}
	}
	total.Platforms = len(entries)
	total.Transcodes = [][]any{}
	for _, t := range m.transcodes() {
		total.Transcodes = append(total.Transcodes, []any{t.PID, t.Name})
	}
	return total
}

// --- Settings ---

// SourcesForSettings — форма вкладки Settings; дефолтний source перший.
func (m *Manager) SourcesForSettings() []*Dict {
	m.mu.Lock()
	defer m.mu.Unlock()
	server := ""
	if m.publicHost != "" {
		server = "rtmp://" + m.publicHost + ":" + strconv.Itoa(m.rtmpPort) + "/live"
	}
	out := []*Dict{}
	for _, s := range m.sourcesDefaultFirstLocked() {
		out = append(out, D(
			"name", s.name,
			"is_default", s.isDefault,
			"type", s.stype,
			"vod_track", s.vodTrack,
			"enhanced_broadcasting", s.enhancedBroadcasting,
			"video_tracks", int64(s.videoTracks),
			"live_path", s.livePath,
			"audio_tracks", int64(s.audioTracks),
			"track_labels", stringList(s.trackLabels()),
			"video_track_labels", stringList(s.videoTrackLabels()),
			"ingest_server", server,
			"ingest_key", m.ingestKey(s.livePath),
			"ingest_url", m.srtIngestURL(s.livePath),
		))
	}
	return out
}

// PlatformsForSettings — to_config кожної платформи плюс зібраний URL.
func (m *Manager) PlatformsForSettings() []*Dict {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Dict{}
	for _, e := range m.platforms.Values() {
		cfg := e.toConfig()
		cfg.Set("url", e.rt.URL())
		out = append(out, cfg)
	}
	return out
}

// GroupsForSettings — групи, дефолтна перша.
func (m *Manager) GroupsForSettings() []*Dict {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Dict{}
	for _, g := range defaultFirst(m.groups) {
		out = append(out, g.Clone())
	}
	return out
}

// SystemSettings — глобальний System-блок вкладки Settings.
type SystemSettings struct {
	ConnectTimeoutMS int
	ReadTimeoutMS    int
	OfflineTimeoutS  int
	ICMPPing         bool
}

// ApplySettings — глобальні налаштування; тайминги MediaMTX застосовує
// викликач окремо.
func (m *Manager) ApplySettings(values SystemSettings) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config.Set("connect_timeout_ms", int64(values.ConnectTimeoutMS))
	m.config.Set("read_timeout_ms", int64(values.ReadTimeoutMS))
	m.config.Set("offline_timeout_sec", int64(values.OfflineTimeoutS))
	m.config.Set("icmp_ping", values.ICMPPing)
	m.readTimeoutMS.Store(int64(values.ReadTimeoutMS))
	m.persistLocked()
}

// SetOBSWidgetShowBitrate — негайний тумблер, окремо від System Apply.
func (m *Manager) SetOBSWidgetShowBitrate(value bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config.Set("obs_widget_show_bitrate", value)
	m.persistLocked()
}

// --- аксессори для валідації ---

// SourceNames / PlatformNames / GroupIDs — реєстри в порядку вставки.
func (m *Manager) SourceNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sources.Keys()
}

func (m *Manager) PlatformNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.platforms.Keys()
}

func (m *Manager) GroupIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []string{}
	for _, g := range m.groups {
		out = append(out, pyStr(g.GetOr("id", "")))
	}
	return out
}

// SourceAudioCounts — {source: кількість аудіодоріжок}.
func (m *Manager) SourceAudioCounts() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int{}
	for _, s := range m.sources.Values() {
		out[s.name] = s.audioTracks
	}
	return out
}

// --- персист ---

// persistLocked — під Manager.lock.
func (m *Manager) persistLocked() {
	sources := []any{}
	for _, s := range m.sources.Values() {
		sources = append(sources, s.toConfig())
	}
	platforms := []any{}
	for _, e := range m.platforms.Values() {
		platforms = append(platforms, e.toConfig())
	}
	groups := []any{}
	for _, g := range m.groups {
		groups = append(groups, g.Clone())
	}
	presets := []any{}
	for _, p := range m.presets {
		presets = append(presets, p)
	}
	m.config.Set("sources", sources)
	m.config.Set("platforms", platforms)
	m.config.Set("platform_groups", groups)
	m.config.Set("fallback_presets", presets)
	if err := m.persist(m.configPath, m.config); err != nil {
		log.Printf("failed to persist config.json: %v", err)
	}
}

// --- дрібне ---

func (m *Manager) sourcesDefaultFirstLocked() []*Source {
	out := make([]*Source, 0, m.sources.Len())
	for _, s := range m.sources.Values() {
		if s.isDefault {
			out = append(out, s)
		}
	}
	for _, s := range m.sources.Values() {
		if !s.isDefault {
			out = append(out, s)
		}
	}
	return out
}

// ingestKey — готовий OBS Stream Key "<sub>?user=obs&pass=<obspass>".
func (m *Manager) ingestKey(livePath string) string {
	if m.obsIngestPass == "" || livePath == "" {
		return ""
	}
	sub := strings.TrimPrefix(livePath, "live/")
	return sub + "?user=obs&pass=" + m.obsIngestPass
}

func (m *Manager) srtIngestURL(livePath string) string {
	if m.publicHost == "" || m.obsIngestPass == "" {
		return ""
	}
	return "srt://" + m.publicHost + ":" + strconv.Itoa(m.srtPort) +
		"?streamid=publish:" + livePath + ":obs:" + m.obsIngestPass
}

func stringList(items []string) []any {
	out := make([]any, len(items))
	for i, s := range items {
		out[i] = s
	}
	return out
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optFloat(v float64, ok bool) *float64 {
	if !ok {
		return nil
	}
	return &v
}
