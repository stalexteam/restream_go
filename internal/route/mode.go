package route

import "strconv"

// Mode — режим роутера для пари source+platform ( і
// ланцюжок 574-633; порядок гілок ланцюжка значущий).
type Mode int

const (
	// ModePlainFLV — не роутер: однодоріжковий RTMP-readback читається прямо flv.ReadTags.
	ModePlainFLV Mode = iota
	ModeTrackSelect
	ModeChimeraFixed
	ModeChimeraSelect
	ModeMultitrackSelect
	ModeMultitrackFixed
	ModeEB
)

func (m Mode) String() string {
	switch m {
	case ModePlainFLV:
		return "plainflv"
	case ModeTrackSelect:
		return "trackselect"
	case ModeChimeraFixed:
		return "chimerafixed"
	case ModeChimeraSelect:
		return "chimeraselect"
	case ModeMultitrackSelect:
		return "multitrackselect"
	case ModeMultitrackFixed:
		return "multitrackfixed"
	case ModeEB:
		return "eb"
	}
	return "mode" + strconv.Itoa(int(m))
}

// Source — контракт джерела, від якого залежить режим. Type тут не бере участі
// у виборі (він визначає транспорт readback), але лишається частиною контракту.
type Source struct {
	Type        string
	AudioTracks int
	EB          bool
}

// Platform — контракт виходу; Video < 0 = уся драбина (EB passthrough).
// Невалідне значення з конфіга нормалізує викликач у 0, як int в оригіналі.
type Platform struct {
	Type     string
	VODTrack bool
	Video    int
}

// Plan — режим і те, як під нього налаштувати демуксер.
type Plan struct {
	Mode           Mode
	Video          int
	VideoRole      string
	AllVideoTracks bool
	EB             bool
}

// Select — вибір режиму роутера.
func Select(src Source, plat Platform) Plan {
	video := plat.Video
	ebArm := src.EB && plat.Type == "rtmp" && video < 0
	if video < 0 && !ebArm {
		video = 0 // уся драбина без EB-плеча не має сенсу
	}
	videoRole := "video"
	if src.EB && !ebArm {
		videoRole = "video" + strconv.Itoa(video)
	}

	chimera := plat.Type == "rtmp" && plat.VODTrack
	multitrackSRT := plat.Type == "srt" && src.AudioTracks > 1
	dualTrackRelay := !chimera && !multitrackSRT && src.AudioTracks == 2
	dualTrackChimera := chimera && src.AudioTracks == 2
	multitrackSource := src.AudioTracks > 1 || src.EB

	plan := Plan{Video: video, VideoRole: videoRole, AllVideoTracks: videoRole != "video", EB: ebArm}
	switch {
	case ebArm:
		plan.Mode = ModeEB
		plan.AllVideoTracks = true
	case dualTrackChimera:
		plan.Mode = ModeChimeraSelect
	case chimera:
		plan.Mode = ModeChimeraFixed
	case multitrackSRT:
		plan.Mode = ModeMultitrackSelect
	case dualTrackRelay:
		plan.Mode = ModeTrackSelect
	case multitrackSource:
		plan.Mode = ModeMultitrackFixed
	default:
		plan.Mode = ModePlainFLV
	}
	return plan
}
