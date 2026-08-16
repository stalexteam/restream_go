// Package jsonwire — спільне JSON-екранування для форматів, байти яких
// заморожені контрактом: конфіг на диску, повідомлення дашборда, ключі кеша.
package jsonwire

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const hexDigits = "0123456789abcdef"

// WriteString пише рядок у лапках з ASCII-екрануванням: керівні символи —
// короткими послідовностями, все поза ASCII — \uXXXX (над BMP — сурогатною
// парою), биті рунці — U+FFFD.
func WriteString(b *strings.Builder, s string) {
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
			case r > utf8.MaxRune || (r >= 0xD800 && r <= 0xDFFF):
				writeEscape(b, 0xFFFD)
			case r > 0xFFFF:
				hi, lo := utf16.EncodeRune(r)
				writeEscape(b, hi)
				writeEscape(b, lo)
			default:
				writeEscape(b, r)
			}
		}
	}
	b.WriteByte('"')
}

// AppendString — та сама форма для байтових буферів.
func AppendString(out []byte, s string) []byte {
	var b strings.Builder
	WriteString(&b, s)
	return append(out, b.String()...)
}

func writeEscape(b *strings.Builder, r rune) {
	b.WriteString(`\u`)
	b.WriteByte(hexDigits[(r>>12)&0xF])
	b.WriteByte(hexDigits[(r>>8)&0xF])
	b.WriteByte(hexDigits[(r>>4)&0xF])
	b.WriteByte(hexDigits[r&0xF])
}
