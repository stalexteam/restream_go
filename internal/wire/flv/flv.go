// Package flv — мінімальний парсер/писар FLV-тегів + Enhanced-RTMP
// обгортки (0x95-аудіо, Ex-відео).
package flv

import (
	"bytes"
	"strconv"
)

const (
	TagAudio  byte = 8
	TagVideo  byte = 9
	TagScript byte = 18 // onMetaData
)

// FLV-заголовок файла + PreviousTagSize0.
var FileHeader = []byte("FLV\x01\x05\x00\x00\x00\x09\x00\x00\x00\x00")

const (
	tagHeaderSize   = 11
	prevTagSizeSize = 4
)

// Enhanced-RTMP відео: байт 0 — [IsExVideoHeader:1][frameType:3][packetType:4];
// legacy тримає біт 7 порожнім (frameType:4|codecId:4). packetType 6 = Multitrack.
const (
	exVideoHeaderBit  = 0x80
	multitrackVideoPT = 6
)

// Enhanced-RTMP multitrack audio (хімера Twitch VOD Track): 0x95 (ExHeader +
// Multitrack) | byte[1] hi=OneTrack lo=packetType | FourCC mp4a | trackId.
const (
	multitrackAudioByte = 0x95
	wrapperSize         = 7
)

var multitrackFourCC = []byte("mp4a")

func IsVideoKeyframe(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	b0 := payload[0]
	if b0&exVideoHeaderBit != 0 {
		return (b0>>4)&0x07 == 1
	}
	return b0>>4 == 1
}

func IsMultitrackAudio(payload []byte) bool {
	return len(payload) >= wrapperSize && payload[0] == multitrackAudioByte
}

func multitrackAudioID(payload []byte) (byte, bool) {
	if !IsMultitrackAudio(payload) {
		return 0, false
	}
	if payload[1]>>4 != 0 || !bytes.Equal(payload[2:6], multitrackFourCC) {
		return 0, false
	}
	return payload[6], true
}

// UnwrapMultitrackAudio: 0x95 OneTrack mp4a -> (trackID, legacy AF-payload);
// ok=false, якщо формат інший.
func UnwrapMultitrackAudio(payload []byte) (trackID byte, legacy []byte, ok bool) {
	id, ok := multitrackAudioID(payload)
	if !ok {
		return 0, nil, false
	}
	legacy = make([]byte, 0, 2+len(payload)-wrapperSize)
	legacy = append(legacy, 0xAF, payload[1]&0x0F)
	legacy = append(legacy, payload[wrapperSize:]...)
	return id, legacy, true
}

// WrapMultitrackAudio: legacy AF-payload -> 0x95 OneTrack mp4a.
// Lossless-обернене до unwrap.
func WrapMultitrackAudio(trackID byte, legacy []byte) []byte {
	out := make([]byte, 0, wrapperSize+len(legacy)-2)
	out = append(out, multitrackAudioByte, legacy[1]&0x0F)
	out = append(out, multitrackFourCC...)
	out = append(out, trackID)
	out = append(out, legacy[2:]...)
	return out
}

// Multitrack-відео — та сама 7-байтна обгортка, що й 0x95-аудіо, але FourCC
// наскрізний (avc1/hvc1/...), не фіксований, і frameType живе в byte[0].

func IsMultitrackVideo(payload []byte) bool {
	return len(payload) >= wrapperSize &&
		payload[0]&exVideoHeaderBit != 0 &&
		payload[0]&0x0F == multitrackVideoPT
}

// UnwrapMultitrackVideo: Ex OneTrack -> (trackID, fourcc, legacy-payload);
// ok=false, якщо формат інший. Body CodedFrames уже несе [CT:3] як legacy;
// SequenceStart/End у Ex-формі CT не мають, legacy — має (нульовий), тож він
// додається тут.
func UnwrapMultitrackVideo(payload []byte) (trackID byte, fourcc, legacy []byte, ok bool) {
	if !IsMultitrackVideo(payload) {
		return 0, nil, nil, false
	}
	pkt := payload[1]
	if pkt>>4 != 0 || pkt > 2 { // лише OneTrack; лише SequenceStart/CodedFrames/End
		return 0, nil, nil, false
	}
	body := payload[wrapperSize:]
	frameType := (payload[0] >> 4) & 0x07
	n := 2 + len(body)
	if pkt != 1 {
		n += 3
	}
	legacy = make([]byte, 0, n)
	legacy = append(legacy, frameType<<4|7, pkt)
	if pkt != 1 {
		legacy = append(legacy, 0, 0, 0)
	}
	legacy = append(legacy, body...)
	fourcc = append([]byte(nil), payload[2:6]...)
	return payload[6], fourcc, legacy, true
}

// WrapMultitrackVideo: legacy-байтова відеоформа -> Ex OneTrack.
// Lossless-обернене до unwrap.
func WrapMultitrackVideo(trackID byte, fourcc, legacy []byte) []byte {
	pkt := legacy[1]
	body := legacy[2:]
	if pkt != 1 {
		if len(body) >= 3 {
			body = body[3:]
		} else {
			body = nil
		}
	}
	frameType := (legacy[0] >> 4) & 0x07
	out := make([]byte, 0, 2+len(fourcc)+1+len(body))
	out = append(out, exVideoHeaderBit|frameType<<4|multitrackVideoPT, pkt)
	out = append(out, fourcc...)
	out = append(out, trackID)
	out = append(out, body...)
	return out
}

func IsAVCSeqHeader(payload []byte) bool {
	if len(payload) < 2 {
		return false
	}
	b0 := payload[0]
	if b0&exVideoHeaderBit != 0 {
		if b0&0x0F == multitrackVideoPT {
			return payload[1]&0x0F == 0 // справжній packetType — молодший нібл byte[1]
		}
		return b0&0x0F == 0
	}
	return payload[1] == 0
}

func IsAACSeqHeader(payload []byte) bool {
	return len(payload) >= 2 && payload[1] == 0
}

// IsSeqHeader: payload[1]==0 покриває і legacy (AF 00), і 0x95-обгортку
// (byte[1]=0x00 = OneTrack + SequenceStart).
func IsSeqHeader(tagType byte, payload []byte) bool {
	switch tagType {
	case TagVideo:
		return IsAVCSeqHeader(payload)
	case TagAudio:
		return IsAACSeqHeader(payload)
	}
	return false
}

// VideoTrackID: 0 для legacy-відеотегу, trackId — для Ex-мультитрекового.
func VideoTrackID(payload []byte) byte {
	if IsMultitrackVideo(payload) {
		return payload[6]
	}
	return 0
}

// HeaderKey — ключ кешу seq-header: video / videoN (Ex track N) /
// audio (legacy) / audioN (0x95 track N).
func HeaderKey(tagType byte, payload []byte) string {
	if tagType == TagScript {
		return "meta" // той самий ключ, під який onMetaData кладуть викликачі
	}
	if tagType == TagVideo {
		if IsMultitrackVideo(payload) {
			return "video" + strconv.Itoa(int(payload[6]))
		}
		return "video"
	}
	if id, ok := multitrackAudioID(payload); ok {
		return "audio" + strconv.Itoa(int(id))
	}
	return "audio"
}

const (
	// MaxAudioSlots — максимум output-слотів мультитрекового SRT-виходу.
	MaxAudioSlots = 6
	// MaxVideoSlots — стеля відеослотів EB-драбини: 5 сходинок на канвас,
	// 7 при двох канвасах.
	MaxVideoSlots = 8
)

// HeaderItem — (ключ, тип тега) для (пере)оголошення заголовків.
type HeaderItem struct {
	Key     string
	TagType byte
}

// OrderedHeaderItems — порядок (пере)оголошення заголовків: meta -> video ->
// video0..video7 -> audio -> audio0..audio5, лише присутні в headers ключі.
func OrderedHeaderItems[V any](headers map[string]V) []HeaderItem {
	order := make([]HeaderItem, 0, len(headers))
	if _, ok := headers["meta"]; ok {
		order = append(order, HeaderItem{"meta", TagScript})
	}
	if _, ok := headers["video"]; ok {
		order = append(order, HeaderItem{"video", TagVideo})
	}
	for i := 0; i < MaxVideoSlots; i++ {
		key := "video" + strconv.Itoa(i)
		if _, ok := headers[key]; ok {
			order = append(order, HeaderItem{key, TagVideo})
		}
	}
	if _, ok := headers["audio"]; ok {
		order = append(order, HeaderItem{"audio", TagAudio})
	}
	for i := 0; i < MaxAudioSlots; i++ {
		key := "audio" + strconv.Itoa(i)
		if _, ok := headers[key]; ok {
			order = append(order, HeaderItem{key, TagAudio})
		}
	}
	return order
}
