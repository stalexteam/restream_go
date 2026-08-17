// Package platform — збірка конвеєра однієї платформи з уже портованих етапів
// (конструктор Platform зі, рішення). Стейт-машина
// OFFLINE/LIVE/FALLBACK сюди не входить: тут лише конструкція, старт/стоп
// вузлів і атомарні live-apply-сеттери.
package platform

import (
	"restream_go/internal/proc"
	"restream_go/internal/route"
)

// Форми вузлів конвеєра.
const (
	BackupTimelineSwitcher   = "switcher"
	BackupTimelineChimera    = "chimera"
	BackupTimelineMultitrack = "multitrack"

	OutputRTMPPush = "rtmppush" // власний RTMP-клієнт (EB-драбина / хімера)
	OutputSRTPush  = "srtpush"  // ts_mux у stdin srt-транспорту
	OutputFLVPush  = "flvpush"  // FLV у stdin push-ffmpeg

	RelaySRT  = "srt"  // srt-транспорт + TS-демукс
	RelayRTMP = "rtmp" // ffmpeg-readback + FLV-рідер
)

// Source — контракт джерела, зафіксований на момент збірки (S5: його зміна
// перестворює платформу, а не міняє наживо).
type Source struct {
	Type        string
	AudioTracks int
	EB          bool
}

// Config — топологічні поля платформи. Live-apply-поля (audio/audio_vod/
// audio_map/credentials/enabled/gate/preset) сюди свідомо НЕ входять.
type Config struct {
	Name       string
	Type       string
	VODTrack   bool
	SourceName string
	// Video уже нормалізований control-шаром (RT11: невалідне значення → 0).
	Video int
}

// Spec — знімок топології пари source+platform: усе, що конструктор
// читає ОДИН РАЗ, похідні прапорці (434-470, 574-577) і обрані форми вузлів.
type Spec struct {
	Name       string
	ProcName   string // _safe_proc_name: іде в ім'я лог-файлу ffmpeg
	Type       string
	VODTrack   bool
	SourceName string
	Source     Source

	// Plan — режим роутера й конфігурація демуксера (route.Select).
	Plan route.Plan

	EB               bool
	Chimera          bool
	MultitrackSRT    bool
	DualTrackRelay   bool
	DualTrackChimera bool
	MultitrackSource bool

	BackupTimeline string
	Output         string
	Relay          string
}

// NewSpec — вибір форм вузлів за парою source+platform.
func NewSpec(cfg Config, src Source) Spec {
	plan := route.Select(
		route.Source{Type: src.Type, AudioTracks: src.AudioTracks, EB: src.EB},
		route.Platform{Type: cfg.Type, VODTrack: cfg.VODTrack, Video: cfg.Video},
	)
	s := Spec{
		Name:       cfg.Name,
		ProcName:   proc.SafeName(cfg.Name),
		Type:       cfg.Type,
		VODTrack:   cfg.VODTrack,
		SourceName: cfg.SourceName,
		Source:     src,
		Plan:       plan,
		EB:         plan.EB,
	}
	s.Chimera = cfg.Type == "rtmp" && cfg.VODTrack
	s.MultitrackSRT = cfg.Type == "srt" && src.AudioTracks > 1
	s.DualTrackRelay = !s.Chimera && !s.MultitrackSRT && src.AudioTracks == 2
	s.DualTrackChimera = s.Chimera && src.AudioTracks == 2
	s.MultitrackSource = src.AudioTracks > 1 || src.EB

	// Гілка заглушки для srt іде по type, а не по _multitrack_srt —.
	switch {
	case s.Chimera:
		s.BackupTimeline = BackupTimelineChimera
	case cfg.Type == "srt":
		s.BackupTimeline = BackupTimelineMultitrack
	default:
		s.BackupTimeline = BackupTimelineSwitcher
	}

	switch {
	case s.EB, s.Chimera:
		s.Output = OutputRTMPPush
	case cfg.Type == "srt":
		s.Output = OutputSRTPush
	default:
		s.Output = OutputFLVPush
	}

	// Ланцюжок relay-їв збігається з порядком гілок route.Select, тож
	// «сирий RTMP-readback» — рівно ModePlainFLV.
	s.Relay = RelaySRT
	if plan.Mode == route.ModePlainFLV {
		s.Relay = RelayRTMP
	}
	return s
}

// Video — сходинка, яку ця платформа віддає (−1 = уся драбина EB-плеча).
func (s Spec) Video() int { return s.Plan.Video }
