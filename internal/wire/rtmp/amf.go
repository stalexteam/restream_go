package rtmp

import (
	"encoding/binary"
	"math"
)

// AMF0 — мінімум для connect/publish.

func amfNumber(x float64) []byte {
	out := make([]byte, 9)
	out[0] = 0x00
	binary.BigEndian.PutUint64(out[1:], math.Float64bits(x))
	return out
}

func amfString(s string) []byte {
	out := make([]byte, 0, 3+len(s))
	out = append(out, 0x02, byte(len(s)>>8), byte(len(s)))
	return append(out, s...)
}

func amfNull() []byte { return []byte{0x05} }

func amfStrictArray(items [][]byte) []byte {
	out := []byte{0x0A, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(out[1:], uint32(len(items)))
	for _, it := range items {
		out = append(out, it...)
	}
	return out
}

// amfField/amfObject: python-dict зберігає порядок вставки, тому поля —
// явним упорядкованим слайсом, не map.
type amfField struct {
	key string
	val []byte
}

func amfObject(fields []amfField) []byte {
	out := []byte{0x03}
	for _, f := range fields {
		out = append(out, byte(len(f.key)>>8), byte(len(f.key)))
		out = append(out, f.key...)
		out = append(out, f.val...)
	}
	return append(out, 0x00, 0x00, 0x09)
}

// amfReadValue: number/bool/string/object/ecma-array/null/undefined.
// Рядки клампляться як py-зрізи; number/довжини вимагають повних байтів;
// невідомий тип -> ok=false (ValueError в оригіналі).
func amfReadValue(data []byte, i int) (any, int, bool) {
	if i >= len(data) {
		return nil, 0, false
	}
	t := data[i]
	i++
	switch t {
	case 0x00:
		if i+8 > len(data) {
			return nil, 0, false
		}
		return math.Float64frombits(binary.BigEndian.Uint64(data[i:])), i + 8, true
	case 0x01:
		if i >= len(data) {
			return nil, 0, false
		}
		return data[i] != 0, i + 1, true
	case 0x02:
		if i+2 > len(data) {
			return nil, 0, false
		}
		ln := int(binary.BigEndian.Uint16(data[i:]))
		i += 2
		return string(pySlice(data, i, i+ln)), i + ln, true
	case 0x03, 0x08:
		if t == 0x08 {
			i += 4
		}
		obj := map[string]any{}
		for {
			if i+2 > len(data) {
				return nil, 0, false
			}
			ln := int(binary.BigEndian.Uint16(data[i:]))
			i += 2
			if ln == 0 {
				i++
				break
			}
			key := string(pySlice(data, i, i+ln))
			i += ln
			v, n, ok := amfReadValue(data, i)
			if !ok {
				return nil, 0, false
			}
			obj[key] = v
			i = n
		}
		return obj, i, true
	case 0x05, 0x06:
		return nil, i, true
	}
	return nil, 0, false
}

func amfReadAll(data []byte) []any {
	var vals []any
	for i := 0; i < len(data); {
		v, n, ok := amfReadValue(data, i)
		if !ok {
			break
		}
		vals = append(vals, v)
		i = n
	}
	return vals
}
