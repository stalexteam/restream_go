package fallback

import (
	"fmt"
	"strconv"
	"strings"

	"restream_go/internal/jsonwire"
)

// json.dumps Python-сумісно (сепаратори ", "/": ", ensure_ascii, заданий
// порядок ключів): від цих байтів залежить ключ кеша й сумісність sidecar-ів.

type jsonPair struct {
	key string
	val any
}

type jsonObject []jsonPair

func pyDumps(v any) string {
	var b strings.Builder
	writeJSONValue(&b, v)
	return b.String()
}

func writeJSONValue(b *strings.Builder, v any) {
	switch x := v.(type) {
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case string:
		jsonwire.WriteString(b, x)
	case jsonObject:
		b.WriteByte('{')
		for i, pair := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			jsonwire.WriteString(b, pair.key)
			b.WriteString(": ")
			writeJSONValue(b, pair.val)
		}
		b.WriteByte('}')
	case []jsonObject:
		b.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			writeJSONValue(b, item)
		}
		b.WriteByte(']')
	default:
		panic(fmt.Sprintf("pyDumps: unsupported type %T", v))
	}
}
