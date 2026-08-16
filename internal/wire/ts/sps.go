package ts

import (
	"bytes"
	"math"
)

var nalStartCode = []byte{0, 0, 1}

// NALUnits — Annex-B (00 00 01 / 00 00 00 01 start codes) -> NAL-и без
// start code.
func NALUnits(data []byte) [][]byte {
	var out [][]byte
	start := bytes.Index(data, nalStartCode)
	for start != -1 {
		start += 3
		nxt := -1
		if rel := bytes.Index(data[start:], nalStartCode); rel != -1 {
			nxt = start + rel
		}
		end := len(data)
		if nxt != -1 {
			end = nxt
		}
		nal := data[start:end]
		for len(nal) > 0 && nal[len(nal)-1] == 0 { // хвостові нулі 4-байтного start code
			nal = nal[:len(nal)-1]
		}
		if len(nal) > 0 {
			out = append(out, nal)
		}
		start = nxt
	}
	return out
}

// pyRound — round-half-to-even, як убудований round у Python.
func pyRound(x float64) int64 {
	f := math.Floor(x)
	switch d := x - f; {
	case d > 0.5:
		return int64(f) + 1
	case d < 0.5:
		return int64(f)
	default:
		i := int64(f)
		if i%2 != 0 {
			i++
		}
		return i
	}
}

// bitReader — бітовий читач RBSP + Exp-Golomb (лише для SPS). Помилка
// липка: далі читається 0, результат відкидається в кінці парсу.
type bitReader struct {
	data []byte
	pos  int
	err  bool
}

func (r *bitReader) bit() int {
	i, off := r.pos/8, r.pos%8
	if i >= len(r.data) {
		r.err = true
		return 0
	}
	r.pos++
	return int(r.data[i]>>(7-off)) & 1
}

func (r *bitReader) bits(n int) int64 {
	var v int64
	for j := 0; j < n; j++ {
		v = v<<1 | int64(r.bit())
	}
	return v
}

func (r *bitReader) ue() int64 {
	zeros := 0
	for {
		b := r.bit()
		if r.err {
			return 0
		}
		if b != 0 {
			break
		}
		zeros++
		if zeros > 32 {
			r.err = true
			return 0
		}
	}
	if zeros == 0 {
		return 0
	}
	return 1<<zeros - 1 + r.bits(zeros)
}

func (r *bitReader) se() int64 {
	k := r.ue()
	if k&1 != 0 {
		return (k + 1) / 2
	}
	return -(k / 2)
}

// rbsp прибирає emulation-prevention байти (00 00 03 -> 00 00).
func rbsp(data []byte) []byte {
	out := make([]byte, 0, len(data))
	for i, n := 0, len(data); i < n; {
		if i+2 < n && data[i] == 0 && data[i+1] == 0 && data[i+2] == 3 {
			out = append(out, data[i:i+2]...)
			i += 3
		} else {
			out = append(out, data[i])
			i++
		}
	}
	return out
}

// Профілі, SPS яких несе chroma_format_idc і матриці масштабування.
var spsExtendedProfiles = map[int64]bool{
	100: true, 110: true, 122: true, 244: true, 44: true, 83: true,
	86: true, 118: true, 128: true, 138: true, 139: true, 134: true, 135: true,
}

func skipScalingLists(r *bitReader, count int) {
	for i := 0; i < count; i++ {
		if r.bit() == 0 {
			continue
		}
		size := 64
		if i < 6 {
			size = 16
		}
		last, next := int64(8), int64(8)
		for j := 0; j < size; j++ {
			if next != 0 {
				next = ((last+r.se()+256)%256 + 256) % 256
			}
			if next != 0 {
				last = next
			}
		}
	}
}

func vuiFPS(r *bitReader) int {
	if r.bit() != 0 && r.bits(8) == 255 { // aspect_ratio_info_present, Extended_SAR
		r.bits(16)
		r.bits(16)
	}
	if r.bit() != 0 { // overscan_info_present
		r.bit()
	}
	if r.bit() != 0 { // video_signal_type_present
		r.bits(3)
		r.bit()
		if r.bit() != 0 { // colour_description_present
			r.bits(24)
		}
	}
	if r.bit() != 0 { // chroma_loc_info_present
		r.ue()
		r.ue()
	}
	if r.bit() != 0 { // timing_info_present
		numUnitsInTick := r.bits(32)
		timeScale := r.bits(32)
		if numUnitsInTick != 0 {
			return int(pyRound(float64(timeScale) / float64(2*numUnitsInTick)))
		}
	}
	return 0
}

// SPSGeometry — результат ParseH264SPS (FPS 0, якщо VUI без timing info).
type SPSGeometry struct {
	Width, Height, FPS int
}

// ParseH264SPS парсить width/height/fps зі SPS-NAL; nil на нерозбірливому
// SPS.
func ParseH264SPS(nal []byte) *SPSGeometry {
	var body []byte
	if len(nal) > 0 {
		body = nal[1:]
	}
	r := &bitReader{data: rbsp(body)}
	profileIDC := r.bits(8)
	r.bits(16) // constraint flags + level_idc
	r.ue()     // seq_parameter_set_id
	chromaFormatIDC := int64(1)
	if spsExtendedProfiles[profileIDC] {
		chromaFormatIDC = r.ue()
		if chromaFormatIDC == 3 {
			r.bit() // separate_colour_plane_flag
		}
		r.ue()            // bit_depth_luma_minus8
		r.ue()            // bit_depth_chroma_minus8
		r.bit()           // qpprime_y_zero_transform_bypass_flag
		if r.bit() != 0 { // seq_scaling_matrix_present_flag
			n := 8
			if chromaFormatIDC == 3 {
				n = 12
			}
			skipScalingLists(r, n)
		}
	}
	r.ue() // log2_max_frame_num_minus4
	pocType := r.ue()
	if pocType == 0 {
		r.ue()
	} else if pocType == 1 {
		r.bit()
		r.se()
		r.se()
		cnt := r.ue()
		for i := int64(0); i < cnt && !r.err; i++ {
			r.se()
		}
	}
	r.ue()  // max_num_ref_frames
	r.bit() // gaps_in_frame_num_value_allowed_flag
	widthMBs := r.ue() + 1
	heightMapUnits := r.ue() + 1
	frameMbsOnly := int64(r.bit())
	if frameMbsOnly == 0 {
		r.bit() // mb_adaptive_frame_field_flag
	}
	r.bit() // direct_8x8_inference_flag
	var crop [4]int64
	if r.bit() != 0 {
		for i := range crop {
			crop[i] = r.ue()
		}
	}
	fps := 0
	if r.bit() != 0 {
		fps = vuiFPS(r)
	}
	if r.err {
		return nil
	}

	// CropUnitX/Y за 7.4.2.1.1: для 4:2:0 -- 2 і 2*(2-frame_mbs_only).
	subW, subH := int64(1), int64(1)
	if chromaFormatIDC == 1 || chromaFormatIDC == 2 {
		subW = 2
	}
	if chromaFormatIDC == 1 {
		subH = 2
	}
	if chromaFormatIDC == 0 {
		subW, subH = 1, 1
	}
	frameMult := 2 - frameMbsOnly
	width := widthMBs*16 - (crop[0]+crop[1])*subW
	height := heightMapUnits*16*frameMult - (crop[2]+crop[3])*subH*frameMult
	if width <= 0 || height <= 0 {
		return nil
	}
	return &SPSGeometry{Width: int(width), Height: int(height), FPS: fps}
}
