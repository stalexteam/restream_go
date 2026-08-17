// Package ts — мінімальний MPEG-TS демукс/мукс. Демукс: SRT-readback без
// ffmpeg (сплеск analyzeduration); мукс: N-аудіо TS-вихід, який ffmpeg
// зібрати не вміє. Формат тегів ідентичний flv.ReadTags (AVCC H.264, raw-AAC).
package ts

import (
	"bytes"
	"fmt"
	"log"
	"strconv"

	"restream_go/internal/wire/flv"
)

// PacketSize — розмір TS-пакета.
const PacketSize = 188

const (
	syncByte = 0x47
	nullPID  = 0x1FFF
)

const (
	streamTypeH264    = 0x1B
	streamTypeAACADTS = 0x0F
	streamTypeAACLATM = 0x11
)

// Відеотипи, які вміємо НАЗВАТИ (для контракту source), навіть якщо не
// вміємо демуксувати: HEVC-топ EB-драбини треба відхилити з чесною причиною.
var videoStreamTypes = map[int]string{
	streamTypeH264: "h264",
	0x24:           "hevc",
	0x10:           "mpeg4video",
	0x02:           "mpeg2video",
	0x01:           "mpeg1video",
}

var aacSampleRates = [...]int{
	96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050,
	16000, 12000, 11025, 8000, 7350,
}

const (
	pts33Mask = int64(1)<<33 - 1
	pts33Half = int64(1) << 32
)

func readTS90k(b []byte) int64 {
	return int64(b[0]>>1&0x07)<<30 | int64(b[1])<<22 | int64(b[2]>>1)<<15 |
		int64(b[3])<<7 | int64(b[4]>>1&0x7F)
}

// ptsUnwrapper — 33-бітні PTS/DTS wraps (~26.5г) і дрібні "назадні" стрибки
// PTS від B-frame reordering розрізняються модульною відстанню.
type ptsUnwrapper struct {
	hasPrev       bool
	prevRaw       int64
	prevUnwrapped int64
}

func (u *ptsUnwrapper) unwrap(raw int64) int64 {
	if !u.hasPrev {
		u.prevUnwrapped = raw
		u.hasPrev = true
	} else {
		delta := (raw - u.prevRaw) & pts33Mask
		if delta > pts33Half {
			delta -= pts33Mask + 1
		}
		u.prevUnwrapped += delta
	}
	u.prevRaw = raw
	return u.prevUnwrapped
}

func buildAVCConfig(sps, pps []byte) []byte {
	out := make([]byte, 0, 11+len(sps)+len(pps))
	out = append(out, 1, sps[1], sps[2], sps[3], 0xFF, 0xE1)
	out = append(out, byte(len(sps)>>8), byte(len(sps)))
	out = append(out, sps...)
	out = append(out, 1)
	out = append(out, byte(len(pps)>>8), byte(len(pps)))
	out = append(out, pps...)
	return out
}

func flvVideoSeqHeader(avcConfig []byte) []byte {
	return append([]byte{0x17, 0x00, 0, 0, 0}, avcConfig...)
}

func flvVideoFrame(isKey bool, ctsMS int64, avccNals []byte) []byte {
	frameType := byte(2)
	if isKey {
		frameType = 1
	}
	cts := ctsMS
	if cts < 0 {
		cts = 0
	}
	cts &= 0xFFFFFF
	out := make([]byte, 0, 5+len(avccNals))
	out = append(out, frameType<<4|7, 0x01, byte(cts>>16), byte(cts>>8), byte(cts))
	return append(out, avccNals...)
}

type adtsHeader struct {
	aot, sfi, chanConfig, frameLength, headerLen int
}

func parseADTSHeader(data []byte, offset int) (adtsHeader, bool) {
	if offset+7 > len(data) {
		return adtsHeader{}, false
	}
	if data[offset] != 0xFF || data[offset+1]&0xF0 != 0xF0 {
		return adtsHeader{}, false
	}
	protectionAbsent := data[offset+1] & 0x01
	profile := int(data[offset+2] >> 6 & 0x03) // ADTS profile 0..3 -> AOT = profile+1
	sfi := int(data[offset+2] >> 2 & 0x0F)
	chanConfig := int(data[offset+2]&0x01)<<2 | int(data[offset+3]>>6&0x03)
	frameLength := int(data[offset+3]&0x03)<<11 | int(data[offset+4])<<3 |
		int(data[offset+5]>>5&0x07)
	headerLen := 9
	if protectionAbsent != 0 {
		headerLen = 7
	}
	return adtsHeader{profile + 1, sfi, chanConfig, frameLength, headerLen}, true
}

func buildAudioSpecificConfig(aot, sfi, chanConfig int) []byte {
	v := (aot&0x1F)<<11 | (sfi&0x0F)<<7 | (chanConfig&0x0F)<<3
	return []byte{byte(v >> 8), byte(v)}
}

func flvAudioSeqHeader(asc []byte) []byte {
	return append([]byte{0xAF, 0x00}, asc...)
}

func flvAudioFrame(rawAAC []byte) []byte {
	return append([]byte{0xAF, 0x01}, rawAAC...)
}

type pesAssembler struct {
	buf []byte
}

// TagFunc отримує кожен зібраний FLV-тег; role — "video" (або "videoN" у
// режимі all-video) чи "audio0".."audioN-1" за порядком доріжок у PMT.
type TagFunc func(role string, tagType byte, ts int64, payload []byte)

// VideoTrack — паспорт відеодоріжки PMT.
type VideoTrack struct {
	Index       int    `json:"index"`
	PID         int    `json:"pid"`
	Codec       string `json:"codec"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	FPS         int    `json:"fps"`
	BitrateKbps int    `json:"bitrate_kbps"`
}

// AudioTrack — паспорт аудіодоріжки PMT.
type AudioTrack struct {
	Index       int    `json:"index"`
	PID         int    `json:"pid"`
	Codec       string `json:"codec"`
	SampleRate  int    `json:"sample_rate"`
	Channels    int    `json:"channels"`
	BitrateKbps int    `json:"bitrate_kbps"`
}

// Manifest — що реально несе потік.
type Manifest struct {
	Video       []VideoTrack `json:"video"`
	Audio       int          `json:"audio"`
	AudioTracks []AudioTrack `json:"audio_tracks"`
	Unsupported []int        `json:"unsupported"`
}

type unwrapKey struct {
	kind byte
	pid  int
	sub  byte
}

// Demuxer — годувати сирими TS-байтами через Feed (будь-якими
// шматками), на кожен готовий FLV-тег кличе onTag. PAT/PMT парсяться в
// припущенні «уміщаються в один TS-пакет» (продюсер — MediaMTX SRT readback);
// крос-пакетне склеювання секцій не реалізоване.
type Demuxer struct {
	onTag          TagFunc
	allVideo       bool
	measureBitrate bool // лічильники потрібні лише паспорту (probe)
	buf            []byte

	patPMTPID   int // -1, поки PAT не розібрано
	pmtParsed   bool
	videoPIDs   []int
	videoRole   map[int]string
	videoTracks []*VideoTrack
	trackByPID  map[int]*VideoTrack
	unsupported []int
	audioPIDs   []int
	audioTracks []*AudioTrack
	audioByPID  map[int]*AudioTrack

	pes      map[int]*pesAssembler
	pesOrder []int // порядок вставки, як у python-dict (визначає порядок Flush)

	sps       map[int][]byte
	pps       map[int][]byte
	aacParams map[int][3]int

	ptsUnwrap map[unwrapKey]*ptsUnwrapper
	base90k   int64
	hasBase   bool
	byteSpan  map[int]*[3]int64 // pid -> [перший ts, останній ts, байтів]

	cc map[int]int
}

// NewDemuxer створює демуксер; allVideoTracks — ролі video0..videoN за
// позицією в PMT і всі H.264-доріжки (EB-драбина), інакше одна роль "video"
// = перша H.264-доріжка.
func NewDemuxer(onTag TagFunc, allVideoTracks, measureBitrate bool) *Demuxer {
	return &Demuxer{
		onTag:          onTag,
		allVideo:       allVideoTracks,
		measureBitrate: measureBitrate,
		patPMTPID:      -1,
		videoRole:      map[int]string{},
		trackByPID:     map[int]*VideoTrack{},
		audioByPID:     map[int]*AudioTrack{},
		pes:            map[int]*pesAssembler{},
		sps:            map[int][]byte{},
		pps:            map[int][]byte{},
		aacParams:      map[int][3]int{},
		ptsUnwrap:      map[unwrapKey]*ptsUnwrapper{},
		byteSpan:       map[int]*[3]int64{},
		cc:             map[int]int{},
	}
}

// Manifest — копія паспорта потоку; заповнюється по мірі читання.
func (d *Demuxer) Manifest() Manifest {
	m := Manifest{
		Video:       make([]VideoTrack, len(d.videoTracks)),
		Audio:       len(d.audioPIDs),
		AudioTracks: make([]AudioTrack, len(d.audioTracks)),
		Unsupported: append([]int{}, d.unsupported...),
	}
	for i, t := range d.videoTracks {
		m.Video[i] = *t
	}
	for i, t := range d.audioTracks {
		m.AudioTracks[i] = *t
	}
	return m
}

// VideoGeometryComplete — чи відома геометрія кожної доріжки, яку вміємо демуксувати.
func (d *Demuxer) VideoGeometryComplete() bool {
	if !d.pmtParsed {
		return false
	}
	for _, t := range d.videoTracks {
		if t.Codec == "h264" && t.Width == 0 {
			return false
		}
	}
	return true
}

// Feed буферизує до кратності 188 і роутить пакети.
func (d *Demuxer) Feed(data []byte) {
	d.buf = append(d.buf, data...)
	n := len(d.buf) / PacketSize * PacketSize
	if n == 0 {
		return
	}
	for i := 0; i < n; i += PacketSize {
		d.feedPacket(d.buf[i : i+PacketSize])
	}
	d.buf = append(d.buf[:0], d.buf[n:]...)
}

// Flush закриває незавершені PES (PES закривається лише наступним PUSI, тож
// без Flush остання доріжка втрачає хвіст).
func (d *Demuxer) Flush() {
	order := append([]int(nil), d.pesOrder...)
	for _, pid := range order {
		d.finalizePES(pid)
	}
}

func (d *Demuxer) feedPacket(pkt []byte) {
	if pkt[0] != syncByte {
		return // ресинк не реалізовано -- транспорт байти не губить/не зсуває
	}
	pid := int(pkt[1]&0x1F)<<8 | int(pkt[2])
	if pid == nullPID {
		return
	}
	pusi := pkt[1]&0x40 != 0
	afc := pkt[3] >> 4 & 0x3
	cc := int(pkt[3] & 0xF)
	if last, ok := d.cc[pid]; ok && (afc == 1 || afc == 3) {
		if cc != (last+1)&0xF {
			log.Printf("ts_demux: continuity counter gap on PID %d (%d -> %d)", pid, last, cc)
		}
	}
	d.cc[pid] = cc

	payloadStart := 4
	if afc == 2 || afc == 3 {
		payloadStart = 5 + int(pkt[4])
	}
	if afc == 0 || afc == 2 || payloadStart >= 188 {
		return
	}
	payload := pkt[payloadStart:188]

	if pid == 0 {
		d.feedPAT(payload, pusi)
	} else if d.patPMTPID != -1 && pid == d.patPMTPID {
		d.feedPMT(payload, pusi)
	} else if _, isVideo := d.videoRole[pid]; isVideo || d.audioByPID[pid] != nil {
		d.feedPES(pid, payload, pusi)
	}
}

func psiSectionOf(payload []byte, pusi bool) []byte {
	if !pusi || len(payload) == 0 {
		return nil
	}
	pointer := int(payload[0])
	if 1+pointer > len(payload) {
		return nil
	}
	sec := payload[1+pointer:]
	if len(sec) < 8 {
		return nil
	}
	return sec
}

func (d *Demuxer) feedPAT(payload []byte, pusi bool) {
	if d.patPMTPID != -1 {
		return
	}
	sec := psiSectionOf(payload, pusi)
	if sec == nil {
		return
	}
	sectionLength := int(sec[1]&0x0F)<<8 | int(sec[2])
	end := 3 + sectionLength - 4 // -4: CRC32
	for i := 8; i+4 <= end && i+4 <= len(sec); i += 4 {
		programNumber := int(sec[i])<<8 | int(sec[i+1])
		pid := int(sec[i+2]&0x1F)<<8 | int(sec[i+3])
		if programNumber != 0 {
			d.patPMTPID = pid
			return
		}
	}
}

func (d *Demuxer) feedPMT(payload []byte, pusi bool) {
	if d.pmtParsed {
		return
	}
	sec := psiSectionOf(payload, pusi)
	if sec == nil || len(sec) < 12 {
		return
	}
	sectionLength := int(sec[1]&0x0F)<<8 | int(sec[2])
	end := 3 + sectionLength - 4
	programInfoLength := int(sec[10]&0x0F)<<8 | int(sec[11])
	for i := 12 + programInfoLength; i+5 <= end && i+5 <= len(sec); {
		streamType := int(sec[i])
		epid := int(sec[i+1]&0x1F)<<8 | int(sec[i+2])
		esInfoLength := int(sec[i+3]&0x0F)<<8 | int(sec[i+4])
		if codec, isVideo := videoStreamTypes[streamType]; isVideo {
			track := &VideoTrack{Index: len(d.videoTracks), PID: epid, Codec: codec}
			d.videoTracks = append(d.videoTracks, track)
			d.trackByPID[epid] = track
			// Демуксуємо лише H.264; у одно-трековому режимі -- лише ПЕРШУ таку доріжку.
			if codec == "h264" && (d.allVideo || len(d.videoPIDs) == 0) {
				d.videoPIDs = append(d.videoPIDs, epid)
				if d.allVideo {
					d.videoRole[epid] = "video" + strconv.Itoa(track.Index)
				} else {
					d.videoRole[epid] = "video"
				}
			}
		} else if streamType == streamTypeAACADTS || streamType == streamTypeAACLATM {
			track := &AudioTrack{Index: len(d.audioPIDs), PID: epid, Codec: "aac"}
			d.audioPIDs = append(d.audioPIDs, epid)
			d.audioTracks = append(d.audioTracks, track)
			d.audioByPID[epid] = track
		} else {
			d.unsupported = append(d.unsupported, streamType)
		}
		i += 5 + esInfoLength
	}
	d.pmtParsed = true
	var vdesc []string
	for _, t := range d.videoTracks {
		vdesc = append(vdesc, fmt.Sprintf("(%d, %s)", t.PID, t.Codec))
	}
	log.Printf("ts_demux: PMT parsed -- video=%v audio_pids=%v unsupported=%v",
		vdesc, d.audioPIDs, d.unsupported)
}

func (d *Demuxer) setPES(pid int, asm *pesAssembler) {
	if _, ok := d.pes[pid]; !ok {
		d.pesOrder = append(d.pesOrder, pid)
	}
	d.pes[pid] = asm
}

func (d *Demuxer) removePES(pid int) *pesAssembler {
	asm, ok := d.pes[pid]
	if !ok {
		return nil
	}
	delete(d.pes, pid)
	for i, p := range d.pesOrder {
		if p == pid {
			d.pesOrder = append(d.pesOrder[:i], d.pesOrder[i+1:]...)
			break
		}
	}
	return asm
}

func (d *Demuxer) feedPES(pid int, payload []byte, pusi bool) {
	if pusi {
		if prev := d.pes[pid]; prev != nil && len(prev.buf) > 0 {
			d.finalizePES(pid)
		}
		asm := &pesAssembler{}
		asm.buf = append(asm.buf, payload...)
		d.setPES(pid, asm)
	} else if asm := d.pes[pid]; asm != nil {
		asm.buf = append(asm.buf, payload...)
	}
}

func (d *Demuxer) finalizePES(pid int) {
	asm := d.removePES(pid)
	if asm == nil || len(asm.buf) < 9 {
		return
	}
	data := asm.buf
	if data[0] != 0 || data[1] != 0 || data[2] != 1 {
		return
	}
	ptsDtsFlags := data[7] >> 6 & 0x3
	hdrEnd := 9 + int(data[8])
	if hdrEnd > len(data) {
		return
	}
	var ptsRaw, dtsRaw int64 = -1, -1
	if (ptsDtsFlags == 2 || ptsDtsFlags == 3) && len(data) >= 14 {
		ptsRaw = readTS90k(data[9:14])
	}
	if ptsDtsFlags == 3 && len(data) >= 19 {
		dtsRaw = readTS90k(data[14:19])
	} else if ptsRaw != -1 {
		dtsRaw = ptsRaw
	}
	if ptsRaw == -1 {
		return
	}
	d.handleES(pid, data[hdrEnd:], ptsRaw, dtsRaw)
}

func (d *Demuxer) unwrap(key unwrapKey, raw int64) int64 {
	u := d.ptsUnwrap[key]
	if u == nil {
		u = &ptsUnwrapper{}
		d.ptsUnwrap[key] = u
	}
	return u.unwrap(raw)
}

func (d *Demuxer) toMS(unwrapped90k int64) int64 {
	if !d.hasBase {
		d.base90k = unwrapped90k
		d.hasBase = true
	}
	return pyRound(float64(unwrapped90k-d.base90k) / 90)
}

func (d *Demuxer) handleES(pid int, esPayload []byte, ptsRaw, dtsRaw int64) {
	if _, ok := d.videoRole[pid]; ok {
		d.handleVideo(pid, esPayload, ptsRaw, dtsRaw)
		return
	}
	for idx, apid := range d.audioPIDs {
		if apid == pid {
			d.handleAudio(pid, idx, esPayload, ptsRaw)
			return
		}
	}
}

func (d *Demuxer) handleVideo(pid int, esPayload []byte, ptsRaw, dtsRaw int64) {
	pts := d.unwrap(unwrapKey{'v', pid, 'p'}, ptsRaw)
	dts := d.unwrap(unwrapKey{'v', pid, 'd'}, dtsRaw)
	dtsMS := d.toMS(dts)
	ctsMS := d.toMS(pts) - dtsMS
	role := d.videoRole[pid]

	var sps, pps []byte
	var vcl [][]byte
	isKey := false
	for _, nal := range NALUnits(esPayload) {
		switch nal[0] & 0x1F {
		case 7:
			sps = nal
		case 8:
			pps = nal
		case 1, 5:
			vcl = append(vcl, nal)
			if nal[0]&0x1F == 5 {
				isKey = true
			}
		}
	}

	if sps != nil && pps != nil &&
		(!bytes.Equal(d.sps[pid], sps) || !bytes.Equal(d.pps[pid], pps)) {
		d.sps[pid] = append([]byte(nil), sps...)
		d.pps[pid] = append([]byte(nil), pps...)
		d.recordGeometry(pid, sps)
		d.onTag(role, flv.TagVideo, dtsMS, flvVideoSeqHeader(buildAVCConfig(sps, pps)))
	}

	if len(vcl) == 0 {
		return
	}
	size := 0
	for _, n := range vcl {
		size += 4 + len(n)
	}
	avcc := make([]byte, 0, size)
	for _, n := range vcl {
		l := len(n)
		avcc = append(avcc, byte(l>>24), byte(l>>16), byte(l>>8), byte(l))
		avcc = append(avcc, n...)
	}
	d.measure(pid, dtsMS, len(avcc))
	d.onTag(role, flv.TagVideo, dtsMS, flvVideoFrame(isKey, ctsMS, avcc))
}

func (d *Demuxer) recordGeometry(pid int, sps []byte) {
	track := d.trackByPID[pid]
	if track == nil {
		return
	}
	if g := ParseH264SPS(sps); g != nil {
		track.Width, track.Height, track.FPS = g.Width, g.Height, g.FPS
	}
}

// measure — бітрейт доріжки за МЕДІА-часом (не wall-clock), лише в режимі
// паспорта (probe).
func (d *Demuxer) measure(pid int, tsMS int64, size int) {
	if !d.measureBitrate {
		return
	}
	var bitrate *int
	if t := d.trackByPID[pid]; t != nil {
		bitrate = &t.BitrateKbps
	} else if t := d.audioByPID[pid]; t != nil {
		bitrate = &t.BitrateKbps
	} else {
		return
	}
	span := d.byteSpan[pid]
	if span == nil {
		// Байти першого кадру не рахуємо: інтервалів на один менше, ніж кадрів.
		d.byteSpan[pid] = &[3]int64{tsMS, tsMS, 0}
		return
	}
	span[1] = tsMS
	span[2] += int64(size)
	if elapsed := span[1] - span[0]; elapsed > 0 {
		*bitrate = int(pyRound(float64(span[2]*8) / float64(elapsed)))
	}
}

// ADTS channel_configuration -> кількість каналів (0 = задано в самому потоці).
var adtsChannels = [...]int{0, 1, 2, 3, 4, 5, 6, 8}

func (d *Demuxer) recordAudioParams(pid, sampleRate, chanConfig int) {
	track := d.audioByPID[pid]
	if track == nil {
		return
	}
	track.SampleRate = sampleRate
	if chanConfig < len(adtsChannels) {
		track.Channels = adtsChannels[chanConfig]
	} else {
		track.Channels = 0
	}
}

func (d *Demuxer) handleAudio(pid, idx int, esPayload []byte, ptsRaw int64) {
	pts := d.unwrap(unwrapKey{'a', pid, 0}, ptsRaw)
	baseMS := d.toMS(pts)
	role := "audio" + strconv.Itoa(idx)

	offset, frameNo := 0, 0
	n := len(esPayload)
	for offset < n {
		hdr, ok := parseADTSHeader(esPayload, offset)
		if !ok {
			break
		}
		if hdr.frameLength < hdr.headerLen || offset+hdr.frameLength > n {
			break
		}
		sampleRate := 48000
		if hdr.sfi >= 0 && hdr.sfi < len(aacSampleRates) {
			sampleRate = aacSampleRates[hdr.sfi]
		}
		frameTS := baseMS + pyRound(float64(frameNo*1024*1000)/float64(sampleRate))

		params := [3]int{hdr.aot, hdr.sfi, hdr.chanConfig}
		if d.aacParams[pid] != params {
			d.aacParams[pid] = params
			d.recordAudioParams(pid, sampleRate, hdr.chanConfig)
			asc := buildAudioSpecificConfig(hdr.aot, hdr.sfi, hdr.chanConfig)
			d.onTag(role, flv.TagAudio, frameTS, flvAudioSeqHeader(asc))
		}

		rawAAC := esPayload[offset+hdr.headerLen : offset+hdr.frameLength]
		d.measure(pid, frameTS, len(rawAAC))
		d.onTag(role, flv.TagAudio, frameTS, flvAudioFrame(rawAAC))

		offset += hdr.frameLength
		frameNo++
	}
}
