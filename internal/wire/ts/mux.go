package ts

import (
	"encoding/binary"
	"io"
	"sort"

	"restream_go/internal/wire/flv"
)

const (
	pidPAT       = 0x0000
	pidPMT       = 0x1000
	pidVideo     = 0x0100
	pidAudioBase = 0x0101 // слот i (0..MaxAudioSlots-1) -> pidAudioBase + i
)

const (
	streamIDVideo = 0xE0
	streamIDAudio = 0xC0
)

// Максимальний інтервал повторного PAT/PMT (мс, за шкалою вихідних тегів):
// приймач без періодичного PSI вважає PMT застарілим і рве з'єднання.
const patPMTIntervalMS = 100

// crc32MPEG — CRC-32/MPEG-2: poly 0x04C11DB7, init 0xFFFFFFFF, без
// фінального XOR/рефлексії (НЕ zlib.crc32).
func crc32MPEG(data []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, b := range data {
		crc ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// psiSection: body — усе між last_section_number і CRC (не включно).
func psiSection(tableID byte, tableIDExt uint16, body []byte, version int) []byte {
	versionByte := byte(0xC0 | (version&0x1F)<<1 | 1) // reserved'11'+version(5)+current_next=1
	core := make([]byte, 0, 5+len(body))
	core = append(core, byte(tableIDExt>>8), byte(tableIDExt), versionByte, 0x00, 0x00)
	core = append(core, body...)
	sectionLength := (len(core) + 4) & 0x0FFF // +CRC32
	out := make([]byte, 0, 3+len(core)+4)
	out = append(out, tableID, byte(0xB0|sectionLength>>8), byte(sectionLength))
	out = append(out, core...)
	crc := crc32MPEG(out)
	return append(out, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

func buildPAT(pmtPID int) []byte {
	body := []byte{0x00, 0x01, byte(0xE0 | pmtPID>>8&0x1F), byte(pmtPID)}
	return psiSection(0x00, 1, body, 0)
}

var patSection = buildPAT(pidPMT) // константа -- не перераховуємо CRC щоразу

type pmtStream struct {
	streamType byte
	pid        int
}

func buildPMT(pcrPID int, streams []pmtStream, version int) []byte {
	body := make([]byte, 0, 4+5*len(streams))
	body = append(body, byte(0xE0|pcrPID>>8&0x1F), byte(pcrPID), 0xF0, 0x00)
	for _, s := range streams {
		body = append(body, s.streamType, byte(0xE0|s.pid>>8&0x1F), byte(s.pid), 0xF0, 0x00)
	}
	return psiSection(0x02, 1, body, version)
}

func psiPacket(pid int, section []byte, cc int) []byte {
	pkt := make([]byte, 0, PacketSize)
	pkt = append(pkt, syncByte, byte(0x40|pid>>8&0x1F), byte(pid), byte(0x10|cc&0xF))
	pkt = append(pkt, 0x00) // pointer_field=0
	pkt = append(pkt, section...)
	for len(pkt) < PacketSize {
		pkt = append(pkt, 0xFF)
	}
	return pkt
}

func encodePCR(pcr90k int64) []byte {
	base := uint64(pcr90k) & uint64(pts33Mask)
	v := base<<15 | 0x3F<<9 // extension(9 bits)=0, reserved(6 bits)=all-1
	out := make([]byte, 6)
	for i := 5; i >= 0; i-- {
		out[i] = byte(v)
		v >>= 8
	}
	return out
}

func writeTS90k(prefix4 byte, value int64) []byte {
	v := uint64(value) & uint64(pts33Mask)
	return []byte{
		prefix4<<4 | byte(v>>30&0x07)<<1 | 1,
		byte(v >> 22),
		byte(v>>15&0x7F)<<1 | 1,
		byte(v >> 7),
		byte(v&0x7F)<<1 | 1,
	}
}

func buildPES(streamID byte, esPayload []byte, pts90k, dts90k int64, hasDTS bool) []byte {
	var flags byte
	var tsField []byte
	if hasDTS && dts90k != pts90k {
		flags = 0xC0 // PTS_DTS_flags='11'
		tsField = append(writeTS90k(0b0011, pts90k), writeTS90k(0b0001, dts90k)...)
	} else {
		flags = 0x80 // PTS_DTS_flags='10'
		tsField = writeTS90k(0b0010, pts90k)
	}
	optional := make([]byte, 0, 3+len(tsField))
	optional = append(optional, 0x80, flags, byte(len(tsField)))
	optional = append(optional, tsField...)
	bodyLen := len(optional) + len(esPayload)
	// PES_packet_length=0 ("unbounded") -- стандартна практика для відео в
	// TS; аудіо-фрейми завжди малі, тож несемо точну довжину.
	pesLen := 0
	if streamID == streamIDAudio && bodyLen <= 0xFFFF {
		pesLen = bodyLen
	}
	out := make([]byte, 0, 6+len(optional)+len(esPayload))
	out = append(out, 0x00, 0x00, 0x01, streamID, byte(pesLen>>8), byte(pesLen))
	out = append(out, optional...)
	return append(out, esPayload...)
}

func buildTSPacket(pid, cc int, pusi bool, chunk []byte, pcr int64, hasPCR bool) []byte {
	var pusiBit byte
	if pusi {
		pusiBit = 0x40
	}
	if !hasPCR && len(chunk) >= 184 {
		out := make([]byte, 0, PacketSize)
		out = append(out, syncByte, pusiBit|byte(pid>>8&0x1F), byte(pid), byte(0x10|cc&0xF))
		return append(out, chunk...)
	}
	afLen := 183 - len(chunk)
	var body []byte
	var flags byte
	if hasPCR {
		body = encodePCR(pcr)
		flags = 0x10 // PCR_flag
	}
	stuff := afLen - 1 - len(body)
	if stuff < 0 {
		stuff = 0
	}
	out := make([]byte, 0, 6+afLen+len(chunk))
	out = append(out, syncByte, pusiBit|byte(pid>>8&0x1F), byte(pid), byte(0x30|cc&0xF))
	out = append(out, byte(afLen))
	// af_len=0 -- сам байт довжини, без прапорців.
	if afLen > 0 {
		out = append(out, flags)
		out = append(out, body...)
		for i := 0; i < stuff; i++ {
			out = append(out, 0xFF)
		}
	}
	return append(out, chunk...)
}

func pesToPackets(pid int, pesBytes []byte, cc int, pcr int64, hasPCR bool) ([][]byte, int) {
	var packets [][]byte
	offset, n := 0, len(pesBytes)
	first := true
	for offset < n {
		usePCR := hasPCR && first
		avail := 184
		if usePCR {
			avail = 176 // 184 - (1+1+6) af overhead для PCR
		}
		end := offset + avail
		if end > n {
			end = n
		}
		chunk := pesBytes[offset:end]
		offset = end
		packets = append(packets, buildTSPacket(pid, cc, first, chunk, pcr, usePCR))
		cc = (cc + 1) & 0xF
		first = false
	}
	return packets, cc
}

// parseAVCConfig — SPS/PPS з AVCDecoderConfigurationRecord (інверсія
// buildAVCConfig); зрізи клампляться, як у python.
func parseAVCConfig(cfg []byte) (sps, pps []byte) {
	if len(cfg) < 6 {
		return nil, nil
	}
	idx := 6
	numSPS := int(cfg[5] & 0x1F)
	for i := 0; i < numSPS; i++ {
		if idx+2 > len(cfg) {
			break
		}
		length := int(cfg[idx])<<8 | int(cfg[idx+1])
		idx += 2
		sps = pySlice(cfg, idx, idx+length)
		idx += length
	}
	if idx >= len(cfg) {
		return sps, nil
	}
	numPPS := int(cfg[idx])
	idx++
	for i := 0; i < numPPS; i++ {
		if idx+2 > len(cfg) {
			break
		}
		length := int(cfg[idx])<<8 | int(cfg[idx+1])
		idx += 2
		pps = pySlice(cfg, idx, idx+length)
		idx += length
	}
	return sps, pps
}

func pySlice(b []byte, lo, hi int) []byte {
	n := len(b)
	if lo > n {
		lo = n
	}
	if hi > n {
		hi = n
	}
	if hi < lo {
		hi = lo
	}
	return b[lo:hi]
}

func avccNALs(data []byte) [][]byte {
	var out [][]byte
	i, n := 0, len(data)
	for i+4 <= n {
		length := int(binary.BigEndian.Uint32(data[i:]))
		i += 4
		if length > n-i {
			return out
		}
		out = append(out, data[i:i+length])
		i += length
	}
	return out
}

func parseAudioSpecificConfig(asc []byte) (aot, sfi, chanConfig int, ok bool) {
	if len(asc) < 2 {
		return 0, 0, 0, false
	}
	v := int(asc[0])<<8 | int(asc[1])
	return v >> 11 & 0x1F, v >> 7 & 0x0F, v >> 3 & 0x0F, true
}

func buildADTSHeader(aot, sfi, chanConfig, frameLength int) []byte {
	profile := aot - 1
	if profile < 0 {
		profile = 0
	}
	profile &= 0x03
	fullLen := (frameLength + 7) & 0x1FFF
	return []byte{
		0xFF,
		0xF1, // MPEG-4, layer=00, protection_absent=1 (no CRC)
		byte(profile<<6 | (sfi&0x0F)<<2 | chanConfig>>2&0x01),
		byte((chanConfig&0x03)<<6 | fullLen>>11&0x03),
		byte(fullLen >> 3),
		byte((fullLen&0x07)<<5 | 0x1F), // buffer fullness = VBR (all-1)
		0xFC,                           // buffer fullness cont. + numRawDataBlocksInFrame-1 = 0
	}
}

type flusher interface{ Flush() error }

// MuxOutput — порт TsMuxOutput: OutputSink-сумісний sink (WriteHeader/
// WriteTag), FLV-теги (video AVCC + до 6 audio-слотів legacy/0x95) -> сирі
// MPEG-TS-байти. Один інстанс -- одне (пере)підключення виходу.
// Аудіо-слот стає частиною PMT з першого свого тега; версія PMT
// інкрементується лише при реальній зміні набору PID-ів.
type MuxOutput struct {
	w io.Writer
	f flusher

	cc              map[int]int
	sps, pps        []byte
	audioCfg        map[int][3]int
	knownAudioSlots map[int]bool
	pmtVersion      int
	pmtDirty        bool
	lastPMTTS       int64
	hasLastPMT      bool
	pmtSection      []byte // кэш секції до зміни набору PID
}

// NewMuxOutput — sink поверх w; якщо w буферизований (має Flush), flush
// після кожного PSI/PES-запису.
func NewMuxOutput(w io.Writer) *MuxOutput {
	m := &MuxOutput{w: w}
	if f, ok := w.(flusher); ok {
		m.f = f
	}
	m.reset()
	return m
}

func (m *MuxOutput) reset() {
	m.cc = map[int]int{}
	m.sps, m.pps = nil, nil
	m.audioCfg = map[int][3]int{}
	m.knownAudioSlots = map[int]bool{}
	m.pmtVersion = 0
	m.pmtDirty = true
	m.hasLastPMT = false
	m.pmtSection = nil
}

func (m *MuxOutput) WriteHeader() error {
	m.reset()
	return nil
}

// WriteTag приймає ts БЕЗ 32-бітної маски — як.
func (m *MuxOutput) WriteTag(tagType byte, ts int64, payload []byte) error {
	switch tagType {
	case flv.TagVideo:
		return m.writeVideo(ts, payload)
	case flv.TagAudio:
		return m.writeAudio(ts, payload)
	}
	return nil // script (onMetaData) не має TS-аналога
}

func (m *MuxOutput) writeVideo(ts int64, payload []byte) error {
	if len(payload) < 5 {
		return nil
	}
	if flv.IsAVCSeqHeader(payload) {
		sps, pps := parseAVCConfig(payload[5:])
		if len(sps) > 0 {
			m.sps = append([]byte(nil), sps...)
		}
		if len(pps) > 0 {
			m.pps = append([]byte(nil), pps...)
		}
		return nil
	}
	isKey := payload[0]>>4 == 1
	cts := int64(payload[2])<<16 | int64(payload[3])<<8 | int64(payload[4])
	var annexb []byte
	if isKey && len(m.sps) > 0 && len(m.pps) > 0 {
		annexb = append(annexb, 0, 0, 0, 1)
		annexb = append(annexb, m.sps...)
		annexb = append(annexb, 0, 0, 0, 1)
		annexb = append(annexb, m.pps...)
	}
	for _, nal := range avccNALs(payload[5:]) {
		annexb = append(annexb, 0, 0, 0, 1)
		annexb = append(annexb, nal...)
	}
	if len(annexb) == 0 {
		return nil
	}
	if m.pmtDirty || !m.hasLastPMT || ts-m.lastPMTTS >= patPMTIntervalMS {
		if err := m.emitPATPMT(ts); err != nil {
			return err
		}
	}
	dts90k := ts * 90
	pts90k := (ts + cts) * 90
	pes := buildPES(streamIDVideo, annexb, pts90k, dts90k, true)
	return m.emitPES(pidVideo, pes, dts90k, true)
}

func (m *MuxOutput) writeAudio(ts int64, payload []byte) error {
	slot, raw := 0, payload
	if id, legacy, ok := flv.UnwrapMultitrackAudio(payload); ok {
		slot, raw = int(id), legacy
	}
	if slot >= flv.MaxAudioSlots || len(raw) < 2 {
		return nil
	}
	if flv.IsAACSeqHeader(raw) {
		if aot, sfi, ch, ok := parseAudioSpecificConfig(raw[2:]); ok {
			m.audioCfg[slot] = [3]int{aot, sfi, ch}
		}
		m.registerSlot(slot)
		return nil
	}
	cfg, ok := m.audioCfg[slot]
	if !ok {
		return nil // ще не бачили ASC для цього слота -- не можемо синтезувати ADTS
	}
	m.registerSlot(slot)
	if m.pmtDirty {
		if err := m.emitPATPMT(ts); err != nil {
			return err
		}
	}
	rawAAC := raw[2:]
	adts := append(buildADTSHeader(cfg[0], cfg[1], cfg[2], len(rawAAC)), rawAAC...)
	pes := buildPES(streamIDAudio, adts, ts*90, 0, false)
	return m.emitPES(pidAudioBase+slot, pes, 0, false)
}

func (m *MuxOutput) registerSlot(slot int) {
	if m.knownAudioSlots[slot] {
		return
	}
	m.knownAudioSlots[slot] = true
	m.pmtVersion = (m.pmtVersion + 1) & 0x1F
	m.pmtDirty = true
	m.pmtSection = nil
}

func (m *MuxOutput) emitPATPMT(ts int64) error {
	m.pmtDirty = false
	m.lastPMTTS = ts
	m.hasLastPMT = true
	if m.pmtSection == nil {
		streams := []pmtStream{{streamTypeH264, pidVideo}}
		slots := make([]int, 0, len(m.knownAudioSlots))
		for s := range m.knownAudioSlots {
			slots = append(slots, s)
		}
		sort.Ints(slots)
		for _, s := range slots {
			streams = append(streams, pmtStream{streamTypeAACADTS, pidAudioBase + s})
		}
		m.pmtSection = buildPMT(pidVideo, streams, m.pmtVersion)
	}
	out := append(psiPacket(pidPAT, patSection, m.nextCC(pidPAT)),
		psiPacket(pidPMT, m.pmtSection, m.nextCC(pidPMT))...)
	return m.write(out)
}

func (m *MuxOutput) nextCC(pid int) int {
	cc := m.cc[pid]
	m.cc[pid] = (cc + 1) & 0xF
	return cc
}

func (m *MuxOutput) emitPES(pid int, pes []byte, pcr int64, hasPCR bool) error {
	packets, cc := pesToPackets(pid, pes, m.cc[pid], pcr, hasPCR)
	m.cc[pid] = cc
	total := 0
	for _, p := range packets {
		total += len(p)
	}
	// Один write+flush на весь PES, а не на кожен 188-байтний пакет.
	out := make([]byte, 0, total)
	for _, p := range packets {
		out = append(out, p...)
	}
	return m.write(out)
}

func (m *MuxOutput) write(data []byte) error {
	if _, err := m.w.Write(data); err != nil {
		return err
	}
	if m.f != nil {
		return m.f.Flush()
	}
	return nil
}
