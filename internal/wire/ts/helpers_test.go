package ts

import (
	"bytes"
	"testing"

	"restream_go/internal/wire/flv"
)

// recTag — тег, що вийшов із демуксера.
type recTag struct {
	role string
	typ  byte
	ts   int64
	size int
}

func recordTag(into *[]recTag) TagFunc {
	return func(role string, tagType byte, ts int64, payload []byte) {
		*into = append(*into, recTag{role: role, typ: tagType, ts: ts, size: len(payload)})
	}
}

func compareTags(t *testing.T, what string, got, want []recTag) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d tags, want %d", what, len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: tag %d = %+v, want %+v", what, i, got[i], want[i])
		}
	}
}

// demuxAll — прогін готового TS через демуксер із Flush у кінці.
func demuxAll(t *testing.T, data []byte, allVideoTracks, measureBitrate bool) ([]recTag, [][]byte) {
	t.Helper()
	var tags []recTag
	var payloads [][]byte
	d := NewDemuxer(func(role string, tagType byte, ts int64, payload []byte) {
		tags = append(tags, recTag{role: role, typ: tagType, ts: ts, size: len(payload)})
		payloads = append(payloads, append([]byte(nil), payload...))
	}, allVideoTracks, measureBitrate)
	d.Feed(data)
	d.Flush()
	return tags, payloads
}

// synthTS — секунда відео+аудіо власним муксером: самодостатня заміна фікстур.
func synthTS(t *testing.T, frames int) []byte {
	t.Helper()
	sps := []byte{0x67, 0x64, 0x00, 0x1F, 0xAC, 0xD9}
	pps := []byte{0x68, 0xEB, 0xE3, 0xCB}
	videoSeq := append([]byte{0x17, 0x00, 0, 0, 0}, buildAVCConfig(sps, pps)...)
	audioSeq := append([]byte{0xAF, 0x00}, buildAudioSpecificConfig(2, 3, 2)...)

	var out bytes.Buffer
	mux := NewMuxOutput(&out)
	mustWrite(t, mux.WriteHeader())
	mustWrite(t, mux.WriteTag(flv.TagVideo, 0, videoSeq))
	mustWrite(t, mux.WriteTag(flv.TagAudio, 0, audioSeq))
	for i := 0; i < frames; i++ {
		ts := pyRound(float64(i*1000) / 60)
		nalFirst, frameByte := byte(0x41), byte(0x27)
		if i%30 == 0 {
			nalFirst, frameByte = 0x65, 0x17
		}
		nal := append([]byte{nalFirst}, make([]byte, 200)...)
		vp := []byte{frameByte, 0x01, 0, 0, 0, 0, 0, byte(len(nal) >> 8), byte(len(nal))}
		vp = append(vp, nal...)
		mustWrite(t, mux.WriteTag(flv.TagVideo, ts, vp))
		mustWrite(t, mux.WriteTag(flv.TagAudio, ts, append([]byte{0xAF, 0x01}, repBytes(byte(i), 32)...)))
	}
	return out.Bytes()
}

func repBytes(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}
