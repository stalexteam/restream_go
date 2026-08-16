package route

import (
	"crypto/sha1"
	"encoding/hex"
	"testing"
)

// recTag — один тег, що дійшов до виходу роутера.
type recTag struct {
	stream  string
	tagType byte
	tsMS    int64
	size    int
	sha     string
}

func recorder(into *[]recTag) Emit {
	return func(stream string, tagType byte, ts int64, payload []byte) {
		*into = append(*into, recTag{stream, tagType, ts, len(payload), shaOf(payload)})
	}
}

func shaOf(payload []byte) string {
	sum := sha1.Sum(payload)
	return hex.EncodeToString(sum[:])
}

func compareTags(t *testing.T, what string, got, want []recTag) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d tags, want %d\ngot  %+v\nwant %+v", what, len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: tag %d = %+v, want %+v", what, i, got[i], want[i])
		}
	}
}

// vframe — легасі-відеотег заданого розміру.
func vframe(key bool, size int) []byte {
	head := byte(0x27)
	if key {
		head = 0x17
	}
	return append([]byte{head, 0x01, 0, 0, 0}, make([]byte, size)...)
}

// aseq/adata/vkey — мінімальні аудіо/відео payload-и з розпізнаваним тілом.
func aseq(marker byte) []byte { return []byte{0xAF, 0x00, marker, 0x10} }

func adata(marker byte, size int) []byte {
	return append([]byte{0xAF, 0x01}, filled(marker, size)...)
}

func vkey(marker byte, size int) []byte {
	return append([]byte{0x17, 0x01, 0, 0, 0}, filled(marker, size)...)
}

func filled(marker byte, size int) []byte {
	body := make([]byte, size)
	for i := range body {
		body[i] = marker
	}
	return body
}
