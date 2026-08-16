package ts

import (
	"bytes"
	"testing"

	"restream_go/internal/wire/flv"
)

func mustWrite(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func pidOf(pkt []byte) int {
	return int(pkt[1]&0x1F)<<8 | int(pkt[2])
}

// Дзеркало test_roundtrip зі + перевірка CTS.
func TestMuxDemuxRoundtrip(t *testing.T) {
	sps := []byte{0x67, 0x64, 0x00, 0x1F, 0xAC, 0xD9}
	pps := []byte{0x68, 0xEB, 0xE3, 0xCB}
	videoSeq := append([]byte{0x17, 0x00, 0, 0, 0}, buildAVCConfig(sps, pps)...)
	audioSeq := append([]byte{0xAF, 0x00}, buildAudioSpecificConfig(2, 3, 2)...) // AAC-LC 48kHz stereo

	var out bytes.Buffer
	mux := NewMuxOutput(&out)
	mustWrite(t, mux.WriteHeader())
	mustWrite(t, mux.WriteTag(flv.TagVideo, 0, videoSeq))
	mustWrite(t, mux.WriteTag(flv.TagAudio, 0, flv.WrapMultitrackAudio(0, audioSeq)))
	mustWrite(t, mux.WriteTag(flv.TagAudio, 0, flv.WrapMultitrackAudio(2, audioSeq)))

	var sentAAC [][]byte
	var wantCTS []int64
	for i := 0; i < 60; i++ {
		ts := pyRound(float64(i*1000) / 60)
		key := i%30 == 0
		nalFirst, frameByte := byte(0x41), byte(0x27)
		if key {
			nalFirst, frameByte = 0x65, 0x17
		}
		nal := append([]byte{nalFirst}, make([]byte, 200)...)
		cts := int64(i%4) * 33
		wantCTS = append(wantCTS, cts)
		vp := []byte{frameByte, 0x01, byte(cts >> 16), byte(cts >> 8), byte(cts)}
		vp = append(vp, 0, 0, byte(len(nal)>>8), byte(len(nal)))
		vp = append(vp, nal...)
		mustWrite(t, mux.WriteTag(flv.TagVideo, ts, vp))
		raw := append([]byte{0xAF, 0x01}, repBytes(byte(i), 32)...)
		sentAAC = append(sentAAC, raw)
		mustWrite(t, mux.WriteTag(flv.TagAudio, ts, flv.WrapMultitrackAudio(0, raw)))
		mustWrite(t, mux.WriteTag(flv.TagAudio, ts, flv.WrapMultitrackAudio(2, raw)))
	}

	data := out.Bytes()
	if len(data) == 0 || len(data)%PacketSize != 0 {
		t.Fatalf("mux output not 188-aligned: %d bytes", len(data))
	}
	patCount := 0
	versions := map[int]bool{}
	for i := 0; i < len(data); i += PacketSize {
		pkt := data[i : i+PacketSize]
		if pkt[0] != syncByte {
			t.Fatalf("packet %d: bad sync byte", i/PacketSize)
		}
		switch pidOf(pkt) {
		case pidPAT:
			patCount++
		case pidPMT:
			sec := pkt[5:] // 4 header + pointer_field
			versions[int(sec[5]>>1&0x1F)] = true
		}
	}
	if patCount < 9 || patCount > 12 {
		t.Fatalf("periodic PAT/PMT: want ~10 per 1s, got %d", patCount)
	}
	if len(versions) != 1 {
		t.Fatalf("PMT version must be stable across resends, got %v", versions)
	}

	var tags []recTag
	var payloads [][]byte
	d := NewDemuxer(func(role string, tagType byte, ts int64, payload []byte) {
		s := recTag{role: role, typ: tagType, ts: ts, size: len(payload)}
		tags = append(tags, s)
		payloads = append(payloads, append([]byte(nil), payload...))
	}, false, false)
	d.Feed(data)
	d.Flush()

	roles := map[string]bool{}
	for _, tag := range tags {
		roles[tag.role] = true
	}
	if len(roles) != 3 || !roles["video"] || !roles["audio0"] || !roles["audio1"] {
		t.Fatalf("roundtrip roles: want video+audio0+audio1 (positional by PMT order), got %v", roles)
	}

	var vframes [][]byte
	keyframes := 0
	for i, tag := range tags {
		if tag.role == "video" && !flv.IsAVCSeqHeader(payloads[i]) {
			vframes = append(vframes, payloads[i])
			if flv.IsVideoKeyframe(payloads[i]) {
				keyframes++
			}
		}
	}
	if len(vframes) != 60 {
		t.Fatalf("video frames after roundtrip: %d != 60", len(vframes))
	}
	if keyframes != 2 {
		t.Fatalf("keyframes after roundtrip: %d != 2", keyframes)
	}
	for i, vf := range vframes {
		cts := int64(vf[2])<<16 | int64(vf[3])<<8 | int64(vf[4])
		if cts != wantCTS[i] {
			t.Fatalf("frame %d: CTS %d != %d", i, cts, wantCTS[i])
		}
	}
	for _, role := range []string{"audio0", "audio1"} {
		var frames [][]byte
		for i, tag := range tags {
			if tag.role == role && !flv.IsAACSeqHeader(payloads[i]) {
				frames = append(frames, payloads[i])
			}
		}
		if len(frames) != len(sentAAC) {
			t.Fatalf("%s: %d frames != %d sent", role, len(frames), len(sentAAC))
		}
		for i := range frames {
			if !bytes.Equal(frames[i], sentAAC[i]) {
				t.Fatalf("%s frame %d: payload mismatch after roundtrip", role, i)
			}
		}
	}
	t.Logf("roundtrip: %d packets, %d tags, PAT resends=%d, CTS survived on all 60 frames",
		len(data)/PacketSize, len(tags), patCount)
}

// Новий слот посеред ефіру -> рівно один інкремент версії PMT.
func TestPMTVersionBumpsOnceOnNewSlot(t *testing.T) {
	sps := []byte{0x67, 0x64, 0x00, 0x1F, 0xAC, 0xD9}
	pps := []byte{0x68, 0xEB, 0xE3, 0xCB}
	videoSeq := append([]byte{0x17, 0x00, 0, 0, 0}, buildAVCConfig(sps, pps)...)
	audioSeq := append([]byte{0xAF, 0x00}, buildAudioSpecificConfig(2, 3, 2)...)

	var out bytes.Buffer
	mux := NewMuxOutput(&out)
	mustWrite(t, mux.WriteHeader())
	mustWrite(t, mux.WriteTag(flv.TagVideo, 0, videoSeq))
	mustWrite(t, mux.WriteTag(flv.TagAudio, 0, flv.WrapMultitrackAudio(0, audioSeq)))
	for i := 0; i < 30; i++ {
		ts := pyRound(float64(i*1000) / 60)
		nalFirst, frameByte := byte(0x41), byte(0x27)
		if i == 0 {
			nalFirst, frameByte = 0x65, 0x17
		}
		nal := append([]byte{nalFirst}, make([]byte, 50)...)
		vp := []byte{frameByte, 0x01, 0, 0, 0}
		vp = append(vp, 0, 0, byte(len(nal)>>8), byte(len(nal)))
		vp = append(vp, nal...)
		mustWrite(t, mux.WriteTag(flv.TagVideo, ts, vp))
		if i == 15 {
			mustWrite(t, mux.WriteTag(flv.TagAudio, ts, flv.WrapMultitrackAudio(1, audioSeq)))
		}
		mustWrite(t, mux.WriteTag(flv.TagAudio, ts, flv.WrapMultitrackAudio(0, []byte{0xAF, 0x01, byte(i)})))
	}

	data := out.Bytes()
	var versions []int
	for i := 0; i < len(data); i += PacketSize {
		pkt := data[i : i+PacketSize]
		if pidOf(pkt) == pidPMT {
			v := int(pkt[5+5] >> 1 & 0x1F)
			if len(versions) == 0 || versions[len(versions)-1] != v {
				versions = append(versions, v)
			}
		}
	}
	if len(versions) != 2 || versions[0] != 1 || versions[1] != 2 {
		t.Fatalf("PMT versions: want [1 2], got %v", versions)
	}
}

// Дзеркало eb_stage2_check: аудіо-only PMT -> жодних відеодоріжок, геометрія
// тривіально повна.
func TestAudioOnlyPMT(t *testing.T) {
	pmt := buildPMT(0x0110, []pmtStream{{streamTypeAACADTS, 0x0110}}, 0)
	stream := append(psiPacket(pidPAT, patSection, 0), psiPacket(pidPMT, pmt, 0)...)
	d := NewDemuxer(func(string, byte, int64, []byte) {}, true, false)
	d.Feed(stream)
	if m := d.Manifest(); len(m.Video) != 0 {
		t.Fatalf("audio-only PMT: video tracks %+v", m.Video)
	}
	if !d.VideoGeometryComplete() {
		t.Fatal("audio-only PMT: geometry must be trivially complete")
	}
}

func TestParseSPSMalformed(t *testing.T) {
	if g := ParseH264SPS([]byte{0x67}); g != nil {
		t.Fatalf("truncated SPS: want nil, got %+v", g)
	}
	garbage := append([]byte{0x67}, repBytes(0xFF, 40)...)
	_ = ParseH264SPS(garbage) // не панікує; результат може бути будь-яким
	if g := ParseH264SPS(nil); g != nil {
		t.Fatalf("empty SPS: want nil, got %+v", g)
	}
}

// Q8: хвостовий чанк рівно 183 байти (af_len=0) не сміє давати 189-байтовий
// "пакет" -- 188-вирівнювання ламалось би на всьому потоці.
func TestPacketsStay188OnEveryChunkSize(t *testing.T) {
	for size := 1; size <= 400; size++ {
		packets, _ := pesToPackets(0x100, make([]byte, size), 0, 0, false)
		for i, pkt := range packets {
			if len(pkt) != PacketSize {
				t.Fatalf("pes %d bytes: packet %d is %d bytes, want %d", size, i, len(pkt), PacketSize)
			}
			if pkt[0] != syncByte {
				t.Fatalf("pes %d bytes: packet %d lost its sync byte", size, i)
			}
		}
	}
	// Той самий інваріант із PCR у першому пакеті.
	for size := 1; size <= 400; size++ {
		packets, _ := pesToPackets(0x100, make([]byte, size), 0, 900000, true)
		for i, pkt := range packets {
			if len(pkt) != PacketSize {
				t.Fatalf("pcr pes %d bytes: packet %d is %d bytes", size, i, len(pkt))
			}
		}
	}
	// Чанк рівно 183 байти: adaptation_field -- лише байт довжини 0.
	pkt := buildTSPacket(0x100, 0, true, make([]byte, 183), 0, false)
	if len(pkt) != PacketSize || pkt[3]&0x30 != 0x30 || pkt[4] != 0 {
		t.Fatalf("183-byte chunk: len=%d flags=%02x afLen=%d", len(pkt), pkt[3], pkt[4])
	}
}
