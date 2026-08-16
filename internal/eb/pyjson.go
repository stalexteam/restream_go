package eb

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
)

// json.dumps Python-сумісно (сепаратори ", "/": ", ensure_ascii, заданий
// порядок ключів) — від цих байтів залежить golden-сверка HTTP body.

type jPair struct {
	key string
	val any
}

type jObj []jPair

type jArr []any

func pyDumps(v any) string {
	var b strings.Builder
	writeJSONValue(&b, v)
	return b.String()
}

func writeJSONValue(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case int:
		b.WriteString(strconv.Itoa(x))
	case string:
		writeJSONString(b, x)
	case jObj:
		b.WriteByte('{')
		for i, pair := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			writeJSONString(b, pair.key)
			b.WriteString(": ")
			writeJSONValue(b, pair.val)
		}
		b.WriteByte('}')
	case jArr:
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

func writeJSONString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r >= 0x20 && r < 0x7f:
				b.WriteRune(r)
			case r > 0xFFFF:
				hi, lo := utf16.EncodeRune(r)
				fmt.Fprintf(b, "\\u%04x\\u%04x", hi, lo)
			default:
				fmt.Fprintf(b, "\\u%04x", r)
			}
		}
	}
	b.WriteByte('"')
}
