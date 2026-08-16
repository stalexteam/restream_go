// Package api — HTTP/WS-шар контролера: маршрути (“), власний
// WebSocket (“) і push-хаб дашборда (“).
package api

import (
	"bytes"
	"encoding/json"
	"log"

	"restream_go/internal/jsonwire"
)

// pyMarshal — байти в тій самій формі, що python `json.dumps`: сепаратори
// ", "/": " і ensure_ascii. Форма проводу заморожена саме цими байтами,
// тож `encoding/json` напряму не годиться.
func pyMarshal(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		log.Printf("api: could not encode message: %v", err)
		return []byte("null")
	}
	return pyReformat(bytes.TrimRight(buf.Bytes(), "\n"))
}

// pyReformat переписує компактний JSON у python-форму: рядки перекодовуються
// (ensure_ascii, без HTML-екранування), після ',' і ':' додається пробіл.
func pyReformat(src []byte) []byte {
	out := make([]byte, 0, len(src)+len(src)/8)
	for i := 0; i < len(src); {
		switch c := src[i]; {
		case c == '"':
			end := jsonStringEnd(src, i)
			var s string
			if err := json.Unmarshal(src[i:end], &s); err != nil {
				out = append(out, src[i:end]...)
			} else {
				out = jsonwire.AppendString(out, s)
			}
			i = end
		case c == ',' || c == ':':
			out = append(out, c, ' ')
			i++
		default:
			out = append(out, c)
			i++
		}
	}
	return out
}

// jsonStringEnd — індекс ЗА закривальною лапкою рядка, що починається на i.
func jsonStringEnd(src []byte, i int) int {
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return len(src)
}

const hexDigits = "0123456789abcdef"

// appendPyString — рядок у формі `json.dumps(..., ensure_ascii=True)`.
