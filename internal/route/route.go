// Package route — табличний track-router: ролі demux-подій розкладаються по
// вихідних слотах канонічного потоку ("relay"/"seg") з обгортками режиму.
// Порт,,, і
// inline-роуту одним двигуном.
package route

import (
	"strconv"
	"strings"

	"restream_go/internal/wire/flv"
	"restream_go/internal/wire/ts"
)

// Emit — вихід роутера: канонічний потік ("relay"/"seg").
type Emit func(stream string, tagType byte, ts int64, payload []byte)

// Unmapped — слот вимкнено (None в audio_map оригіналу).
const Unmapped = -1

// VOD-доріжка хімери/EB на проводі — 0x95 track 1; legacy-AAC-тег починається з 0xAF.
const (
	vodTrackID    = 1
	legacyAACByte = 0xAF
)

var fourCCAVC = []byte("avc1")

// audioPolicy — сімейство аудіо-поведінки; чотири ридери-предки відрізняються
// лише ним (кожен наступний — узагальнення попереднього).
type audioPolicy int

const (
	// policyFixed — без кешу seq, без переоголошень, без guard-ів.
	policyFixed audioPolicy = iota
	// policySelect — seq іде в замаплені слоти і обриває тег; переоголошуються
	// лише слоти зі зміненим джерелом; пер-слотовий монотонний guard.
	policySelect
	// policyChimera — seq лише кешується й тече далі; будь-яка зміна мапи
	// переоголошує ВСІ замаплені слоти; guard-ів немає.
	policyChimera
)

// slotWrap — форма слота на проводі.
type slotWrap func(slot int, payload []byte) []byte

func slotRaw(_ int, payload []byte) []byte { return payload }

func slotMultitrack(slot int, payload []byte) []byte {
	return flv.WrapMultitrackAudio(byte(slot), payload)
}

// Хімера/EB: слот 0 — legacy Live-доріжка, слот 1 — VOD track 1.
func slotVODPair(slot int, payload []byte) []byte {
	if slot == 0 {
		return payload
	}
	return flv.WrapMultitrackAudio(vodTrackID, payload)
}

// WrapRung — сходинка драбини у формі проводу: trackId 0 legacy як є, решта — Ex.
func WrapRung(trackID int, payload []byte) []byte {
	if trackID == 0 {
		return payload
	}
	return flv.WrapMultitrackVideo(byte(trackID), fourCCAVC, payload)
}

// Router — синхронний роутер одного режиму: приймає demux-події, емітить
// канонічні теги. Потокобезпека селекторів і виходу — справа викликача.
type Router struct {
	out       Emit
	stream    string
	videoRole string
	rungs     bool // EB: WrapRung на всіх відеоролях
	mapping   func() []int
	wrap      slotWrap
	policy    audioPolicy

	lastSeq map[int][]byte
	lastTS  map[int]int64
	prior   []int
}

func newRouter(out Emit, stream string, policy audioPolicy, mapping func() []int, wrap slotWrap) *Router {
	r := &Router{
		out: out, stream: stream, policy: policy, mapping: mapping, wrap: wrap,
		lastSeq: make(map[int][]byte), lastTS: make(map[int]int64),
	}
	if policy != policyFixed {
		r.prior = append([]int(nil), mapping()...)
	}
	return r
}

// TagFunc — вхід роутера у формі колбека демуксера.
func (r *Router) TagFunc() ts.TagFunc { return r.Route }

// Route — одна demux-подія.
func (r *Router) Route(role string, tagType byte, tsMS int64, payload []byte) {
	if r.rungs {
		if strings.HasPrefix(role, "video") {
			if id := trackIndex(role, "video"); id >= 0 {
				r.out(r.stream, tagType, tsMS, WrapRung(id, payload))
			}
			return
		}
	} else if role == r.videoRole {
		r.out(r.stream, tagType, tsMS, payload)
		return
	}
	if !strings.HasPrefix(role, "audio") {
		return
	}
	if track := trackIndex(role, "audio"); track >= 0 {
		r.audio(track, tagType, tsMS, payload)
	}
}

func (r *Router) audio(track int, tagType byte, tsMS int64, payload []byte) {
	if r.policy != policyFixed && flv.IsSeqHeader(tagType, payload) {
		r.lastSeq[track] = payload
		if r.policy == policySelect {
			for slot, mapped := range r.mapping() {
				if mapped == track {
					r.out(r.stream, tagType, tsMS, r.wrap(slot, payload))
				}
			}
			return
		}
	}

	cur := r.mapping()
	if r.policy != policyFixed && !sameMapping(cur, r.prior) {
		r.redeclare(cur, tsMS)
		r.prior = append([]int(nil), cur...)
	}

	for slot, mapped := range cur {
		if mapped != track {
			continue
		}
		if r.policy == policySelect {
			last, ok := r.lastTS[slot]
			if !ok {
				last = -1 // дефолт guard-а: ts <= -1 не проходить
			}
			if tsMS <= last {
				continue
			}
			r.lastTS[slot] = tsMS
		}
		r.out(r.stream, tagType, tsMS, r.wrap(slot, payload))
	}
}

// redeclare — переемісія закешованих seq-header під новою мапою поточним ts;
// пер-слотові guard-и вона свідомо не чіпає.
func (r *Router) redeclare(cur []int, tsMS int64) {
	for slot, mapped := range cur {
		if mapped < 0 {
			continue
		}
		if r.policy == policySelect {
			prior := Unmapped
			if slot < len(r.prior) {
				prior = r.prior[slot]
			}
			if mapped == prior {
				continue
			}
		}
		if cached, ok := r.lastSeq[mapped]; ok {
			r.out(r.stream, flv.TagAudio, tsMS, r.wrap(slot, cached))
		}
	}
}

func sameMapping(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// trackIndex — індекс доріжки з ролі demux-події; -1 = не число (у Python тут
// ValueError, тобто смерть ридера).
func trackIndex(role, prefix string) int {
	n, err := strconv.Atoi(role[len(prefix):])
	if err != nil {
		return -1
	}
	return n
}

func constMapping(slots ...int) func() []int {
	return func() []int { return slots }
}

// NewTrackSelect — дводоріжковий селектор: у relay йде
// legacy-аудіо поточно вибраної доріжки.
func NewTrackSelect(out Emit, getSelected func() int, videoRole string) *Router {
	r := newRouter(out, "relay", policySelect,
		func() []int { return []int{getSelected()} }, slotRaw)
	r.videoRole = videoRole
	return r
}

// NewMultitrackSelect — N-слотовий селектор: кожен слот
// завжди 0x95-обгорнутий, включно зі слотом 0.
func NewMultitrackSelect(out Emit, getTrackMap func() []int, videoRole string) *Router {
	r := newRouter(out, "relay", policySelect, getTrackMap, slotMultitrack)
	r.videoRole = videoRole
	return r
}

// NewChimeraSelect — хімера RTMP+VOD над dual-track source
// (chimera.run_chimera_select_reader): ролі Live/VOD міняються без реконнекту.
func NewChimeraSelect(out Emit, getAudio, getAudioVOD func() int, videoRole string) *Router {
	r := newRouter(out, "relay", policyChimera,
		func() []int { return []int{getAudio(), getAudioVOD()} }, slotVODPair)
	r.videoRole = videoRole
	return r
}

// NewChimeraFixed — хімера з фіксованими доріжками (chimera.run_chimera_reader);
// audioIdx == audioVODIdx свідомо шле той самий тег в обидві ролі.
func NewChimeraFixed(out Emit, audioIdx, audioVODIdx int, videoRole string) *Router {
	r := newRouter(out, "relay", policyFixed, constMapping(audioIdx, audioVODIdx), slotVODPair)
	r.videoRole = videoRole
	return r
}

// NewMultitrackFixed — одна фіксована доріжка мультитрекового source;
// inline-роут із (_make_multitrack_reader_hook).
func NewMultitrackFixed(out Emit, audioIdx int, videoRole string) *Router {
	r := newRouter(out, "relay", policyFixed, constMapping(audioIdx), slotRaw)
	r.videoRole = videoRole
	return r
}

// NewEB — EB-плече (eb_relay.run_eb_reader): уся драбина через WrapRung, аудіо —
// семантика хімери; getAudioVOD == nil означає вихід без VOD-доріжки.
func NewEB(out Emit, getAudio, getAudioVOD func() int) *Router {
	mapping := func() []int {
		vod := Unmapped
		if getAudioVOD != nil {
			vod = getAudioVOD()
		}
		return []int{getAudio(), vod}
	}
	r := newRouter(out, "relay", policyChimera, mapping, slotVODPair)
	r.rungs = true
	return r
}

// NewEBBackup — заглушка EB-плеча (eb_relay.run_eb_backup_reader): драбина і
// РІВНО audio0 у потік "seg".
func NewEBBackup(out Emit) *Router {
	r := newRouter(out, "seg", policyFixed, constMapping(0), slotRaw)
	r.rungs = true
	return r
}

// ChimeraBackupTimeline — фасад process-колбека таймлайна заглушки RTMP+VOD:
// кожен legacy-аудіотег дублюється в VOD track 1 тим самим ts.
func ChimeraBackupTimeline(next Emit) Emit {
	return func(stream string, tagType byte, tsMS int64, payload []byte) {
		next(stream, tagType, tsMS, payload)
		if tagType == flv.TagAudio && len(payload) > 0 && payload[0] == legacyAACByte {
			next(stream, tagType, tsMS, flv.WrapMultitrackAudio(vodTrackID, payload))
		}
	}
}

// MultitrackBackupTimeline — те саме для мультитрекової SRT-заглушки: legacy-тег
// іде в УСІ зараз замаплені слоти; порожня мапа — у слот 0.
func MultitrackBackupTimeline(next Emit, getAudioMap func() []int) Emit {
	return func(stream string, tagType byte, tsMS int64, payload []byte) {
		if tagType == flv.TagAudio && len(payload) > 0 && payload[0] == legacyAACByte {
			active := false
			for slot, mapped := range getAudioMap() {
				if mapped == Unmapped {
					continue
				}
				active = true
				next(stream, tagType, tsMS, flv.WrapMultitrackAudio(byte(slot), payload))
			}
			if !active {
				next(stream, tagType, tsMS, flv.WrapMultitrackAudio(0, payload))
			}
			return
		}
		next(stream, tagType, tsMS, payload)
	}
}
