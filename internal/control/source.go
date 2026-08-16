package control

import (
	"strconv"
	"sync/atomic"

	"restream_go/internal/eb"
	"restream_go/internal/ingest"
	"restream_go/internal/wire/ts"
)

// SourceMedia — паспорт останнього успішного probe для дашборда; ключі
// заморожені; порядок ключів фіксований.
type SourceMedia struct {
	VideoCodec        string          `json:"video_codec"`
	Width             int             `json:"width"`
	Height            int             `json:"height"`
	FPS               int             `json:"fps"`
	AudioCodec        string          `json:"audio_codec"`
	Channels          int             `json:"channels"`
	SampleRate        int             `json:"sample_rate"`
	AudioTracksActual int             `json:"audio_tracks_actual"`
	VideoTracksActual []ts.VideoTrack `json:"video_tracks_actual"`
	AudioTracksDetail []ts.AudioTrack `json:"audio_tracks_detail"`
}

// Source — один вхід. Поля міняє лише Manager під
// своїм локом; що читають гарячі шляхи платформ — продубльовано атоміками.
type Source struct {
	scfg *Dict

	name                 string
	isDefault            bool
	stype                string
	vodTrack             bool
	enhancedBroadcasting bool
	videoTracks          int
	livePath             string
	activePath           string
	audioTracks          int

	available         bool
	validated         bool
	contractError     string
	availableSince    float64
	hasAvailableSince bool

	media             *SourceMedia
	videoManifest     []ts.VideoTrack
	audioManifest     []ts.AudioTrack
	primaryVideoIndex int
	probeGenValue     int

	// Лок-фрі для платформ: readback-URL (args-провайдер relay), покоління
	// публікації та спостережена драбина (обидва — EB-плече).
	readback  atomic.Pointer[string]
	probeGen  atomic.Int64
	observed  atomic.Pointer[[]eb.Rung]
	videoIdx  atomic.Int64
	ports     ingestPorts
	credsUser string
	credsPass string
}

type ingestPorts struct {
	rtmp int
	srt  int
}

func newSource(scfg *Dict, ports ingestPorts, user, pass string) *Source {
	s := &Source{
		scfg:                 scfg,
		name:                 pyStr(scfg.GetOr("name", "")),
		isDefault:            pyTruthy(scfg.GetOr("is_default", false)),
		stype:                pyStr(scfg.GetOr("type", "rtmp")),
		vodTrack:             pyTruthy(scfg.GetOr("vod_track", false)),
		enhancedBroadcasting: pyTruthy(scfg.GetOr("enhanced_broadcasting", false)),
		videoTracks:          SourceVideoTracks(scfg),
		livePath:             pyStr(scfg.GetOr("live_path", "")),
		audioTracks:          SourceTrackCount(scfg),
		ports:                ports,
		credsUser:            user,
		credsPass:            pass,
	}
	s.publish()
	return s
}

// publish оновлює лок-фрі знімки; кличеться після кожної зміни полів, від яких
// вони залежать.
func (s *Source) publish() {
	url := ingest.ReadbackURL(s.path(), s.audioTracks, s.enhancedBroadcasting,
		s.ports.srt, s.ports.rtmp, s.credsUser, s.credsPass)
	s.readback.Store(&url)
	s.probeGen.Store(int64(s.probeGenValue))
	s.videoIdx.Store(int64(s.primaryVideoIndex))
	rungs := make([]eb.Rung, len(s.videoManifest))
	for i, t := range s.videoManifest {
		rungs[i] = eb.Rung{Width: t.Width, Height: t.Height, Fps: t.FPS}
	}
	s.observed.Store(&rungs)
}

// path — active_path or live_path: EB переписує ключ публікації.
func (s *Source) path() string {
	if s.activePath != "" {
		return s.activePath
	}
	return s.livePath
}

// Name / IsDefault / LivePath — незмінні між CRUD-операціями поля.
func (s *Source) Name() string     { return s.name }
func (s *Source) LivePath() string { return s.livePath }

// ReadbackURL — вхід relay-їв і probe контракту; лок-фрі.
func (s *Source) ReadbackURL() string {
	if s == nil {
		return ""
	}
	return *s.readback.Load()
}

// ProbeGen — покоління публікації; лок-фрі.
func (s *Source) ProbeGen() int {
	if s == nil {
		return 0
	}
	return int(s.probeGen.Load())
}

// ObservedLadder — спостережена драбина EB-source; ok=false = невідомий source.
func (s *Source) ObservedLadder() ([]eb.Rung, bool) {
	if s == nil {
		return nil, false
	}
	return *s.observed.Load(), true
}

// trackLabels — підписи аудіодоріжок для UI.
func (s *Source) trackLabels() []string {
	if s.stype == "rtmp" && s.vodTrack {
		return []string{"#Live", "#VOD"}
	}
	labels := make([]string, 0, s.audioTracks)
	for i := 0; i < s.audioTracks; i++ {
		labels = append(labels, "#"+strconv.Itoa(i+1))
	}
	return labels
}

// videoTrackLabels — підписи відеосходинок: паспорт readback, інакше декларація
func (s *Source) videoTrackLabels() []string {
	if !s.enhancedBroadcasting {
		return []string{"#1"}
	}
	if len(s.videoManifest) > 0 {
		labels := make([]string, 0, len(s.videoManifest))
		for _, t := range s.videoManifest {
			label := "#" + strconv.Itoa(t.Index+1) + " " +
				strconv.Itoa(t.Width) + "x" + strconv.Itoa(t.Height)
			if t.FPS != 0 {
				label += "@" + strconv.Itoa(t.FPS)
			}
			labels = append(labels, label)
		}
		return labels
	}
	n := s.videoTracks
	if n == 0 {
		n = MaxEBVideoTracks
	}
	labels := make([]string, 0, n)
	for i := 0; i < n; i++ {
		labels = append(labels, "#"+strconv.Itoa(i+1))
	}
	return labels
}

// toConfig — фіксований порядок полів персисту.
func (s *Source) toConfig() *Dict {
	return D(
		"name", s.name,
		"is_default", s.isDefault,
		"type", s.stype,
		"vod_track", s.vodTrack,
		"enhanced_broadcasting", s.enhancedBroadcasting,
		"video_tracks", int64(s.videoTracks),
		"live_path", s.livePath,
		"audio_tracks", int64(s.audioTracks),
	)
}
