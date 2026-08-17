//go:build ignore

// Робить resource_windows_amd64.syso з restreamd.ico. З кореня модуля:
// go run internal/assets/mkicon.go
//
// Свій генератор, бо cvtres кладе абсолютні символи (@comp.id, @feat.00), і
// лінкер Go падає на них із "sectnum < 0!".
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
)

const (
	rtIcon      = 3
	rtGroupIcon = 14
	langID      = 0x0409

	machineAMD64  = 0x8664
	relAMD64Addr32NB = 0x0003
	scnData       = 0x40000040 // INITIALIZED_DATA | MEM_READ
	symClassStatic   = 3
)

// icoEntry — запис каталогу .ico (16 байт на диску).
type icoEntry struct {
	width, height, colors, reserved byte
	planes, bitCount                uint16
	size, offset                    uint32
}

func main() {
	raw, err := os.ReadFile("internal/assets/restreamd.ico")
	if err != nil {
		fail(err)
	}
	entries, err := parseICO(raw)
	if err != nil {
		fail(err)
	}
	section, relocs := buildRsrc(raw, entries)
	if err := os.WriteFile("resource_windows_amd64.syso", buildCOFF(section, relocs), 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("resource_windows_amd64.syso: %d icons, %d bytes\n", len(entries), len(section))
}

func parseICO(raw []byte) ([]icoEntry, error) {
	if len(raw) < 6 || binary.LittleEndian.Uint16(raw[2:]) != 1 {
		return nil, fmt.Errorf("not an .ico file")
	}
	count := int(binary.LittleEndian.Uint16(raw[4:]))
	if len(raw) < 6+count*16 {
		return nil, fmt.Errorf("truncated icon directory")
	}
	out := make([]icoEntry, count)
	for i := range out {
		b := raw[6+i*16:]
		out[i] = icoEntry{
			width: b[0], height: b[1], colors: b[2], reserved: b[3],
			planes:   binary.LittleEndian.Uint16(b[4:]),
			bitCount: binary.LittleEndian.Uint16(b[6:]),
			size:     binary.LittleEndian.Uint32(b[8:]),
			offset:   binary.LittleEndian.Uint32(b[12:]),
		}
	}
	return out, nil
}

// buildRsrc збирає секцію .rsrc: дерево каталогів, записи даних, самі байти.
// Повертає ще й зсуви полів OffsetToData — на них потрібні релокації.
func buildRsrc(raw []byte, entries []icoEntry) ([]byte, []uint32) {
	n := len(entries)
	// Порядок розкладки фіксований, тож усі зсуви рахуються наперед.
	rootSize := 16 + 2*8
	iconDirSize := 16 + n*8
	groupDirSize := 16 + 1*8
	langDirSize := 16 + 8

	langBase := uint32(rootSize + iconDirSize + groupDirSize)
	dataBase := langBase + uint32((n+1)*langDirSize)
	payloadBase := dataBase + uint32((n+1)*16)

	var buf bytes.Buffer
	// Рівень 1: типи (RT_ICON < RT_GROUP_ICON — каталоги мусять бути впорядковані).
	writeDir(&buf, 2)
	writeDirEntry(&buf, rtIcon, uint32(rootSize), true)
	writeDirEntry(&buf, rtGroupIcon, uint32(rootSize+iconDirSize), true)

	// Рівень 2: імена ресурсів.
	writeDir(&buf, n)
	for i := 0; i < n; i++ {
		writeDirEntry(&buf, uint32(i+1), langBase+uint32(i*langDirSize), true)
	}
	writeDir(&buf, 1)
	writeDirEntry(&buf, 1, langBase+uint32(n*langDirSize), true)

	// Рівень 3: мови -> записи даних.
	for i := 0; i <= n; i++ {
		writeDir(&buf, 1)
		writeDirEntry(&buf, langID, dataBase+uint32(i*16), false)
	}

	group := groupIconDir(entries)
	sizes := make([]uint32, 0, n+1)
	for _, e := range entries {
		sizes = append(sizes, e.size)
	}
	sizes = append(sizes, uint32(len(group)))

	// Записи даних; зсуви полів OffsetToData йдуть у релокації.
	var relocs []uint32
	offset := payloadBase
	for _, size := range sizes {
		relocs = append(relocs, uint32(buf.Len()))
		binary.Write(&buf, binary.LittleEndian, offset)
		binary.Write(&buf, binary.LittleEndian, size)
		binary.Write(&buf, binary.LittleEndian, uint32(0)) // CodePage
		binary.Write(&buf, binary.LittleEndian, uint32(0)) // Reserved
		offset += align8(size)
	}

	for _, e := range entries {
		buf.Write(raw[e.offset : e.offset+e.size])
		pad(&buf, e.size)
	}
	buf.Write(group)
	pad(&buf, uint32(len(group)))
	return buf.Bytes(), relocs
}

// groupIconDir — GRPICONDIR: як каталог .ico, але замість зсуву 2-байтовий id.
func groupIconDir(entries []icoEntry) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint16(0))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint16(len(entries)))
	for i, e := range entries {
		buf.Write([]byte{e.width, e.height, e.colors, e.reserved})
		binary.Write(&buf, binary.LittleEndian, e.planes)
		binary.Write(&buf, binary.LittleEndian, e.bitCount)
		binary.Write(&buf, binary.LittleEndian, e.size)
		binary.Write(&buf, binary.LittleEndian, uint16(i+1))
	}
	return buf.Bytes()
}

func buildCOFF(section []byte, relocs []uint32) []byte {
	const headerSize, sectionHeaderSize, relocSize, symSize = 20, 40, 10, 18
	dataPos := headerSize + sectionHeaderSize
	relocPos := dataPos + len(section)
	symPos := relocPos + len(relocs)*relocSize

	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint16(machineAMD64))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint32(0))
	binary.Write(&buf, binary.LittleEndian, uint32(symPos))
	binary.Write(&buf, binary.LittleEndian, uint32(1))
	binary.Write(&buf, binary.LittleEndian, uint16(0))
	binary.Write(&buf, binary.LittleEndian, uint16(0))

	buf.Write(padName(".rsrc"))
	binary.Write(&buf, binary.LittleEndian, uint32(0)) // VirtualSize
	binary.Write(&buf, binary.LittleEndian, uint32(0)) // VirtualAddress
	binary.Write(&buf, binary.LittleEndian, uint32(len(section)))
	binary.Write(&buf, binary.LittleEndian, uint32(dataPos))
	binary.Write(&buf, binary.LittleEndian, uint32(relocPos))
	binary.Write(&buf, binary.LittleEndian, uint32(0)) // PointerToLinenumbers
	binary.Write(&buf, binary.LittleEndian, uint16(len(relocs)))
	binary.Write(&buf, binary.LittleEndian, uint16(0))
	binary.Write(&buf, binary.LittleEndian, uint32(scnData))

	buf.Write(section)
	for _, at := range relocs {
		binary.Write(&buf, binary.LittleEndian, at)
		binary.Write(&buf, binary.LittleEndian, uint32(0)) // символ секції
		binary.Write(&buf, binary.LittleEndian, uint16(relAMD64Addr32NB))
	}

	buf.Write(padName(".rsrc"))
	binary.Write(&buf, binary.LittleEndian, uint32(0)) // Value
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // SectionNumber
	binary.Write(&buf, binary.LittleEndian, uint16(0)) // Type
	buf.WriteByte(symClassStatic)
	buf.WriteByte(0) // без aux
	binary.Write(&buf, binary.LittleEndian, uint32(4)) // порожня таблиця рядків
	return buf.Bytes()
}

func writeDir(buf *bytes.Buffer, idEntries int) {
	binary.Write(buf, binary.LittleEndian, uint32(0)) // Characteristics
	binary.Write(buf, binary.LittleEndian, uint32(0)) // TimeDateStamp
	binary.Write(buf, binary.LittleEndian, uint16(0)) // MajorVersion
	binary.Write(buf, binary.LittleEndian, uint16(0)) // MinorVersion
	binary.Write(buf, binary.LittleEndian, uint16(0)) // NumberOfNamedEntries
	binary.Write(buf, binary.LittleEndian, uint16(idEntries))
}

func writeDirEntry(buf *bytes.Buffer, id, offset uint32, isDir bool) {
	binary.Write(buf, binary.LittleEndian, id)
	if isDir {
		offset |= 0x80000000
	}
	binary.Write(buf, binary.LittleEndian, offset)
}

func align8(n uint32) uint32 { return (n + 7) &^ 7 }

func pad(buf *bytes.Buffer, written uint32) {
	buf.Write(make([]byte, align8(written)-written))
}

func padName(name string) []byte {
	out := make([]byte, 8)
	copy(out, name)
	return out
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "mkicon:", err)
	os.Exit(1)
}
