package timeline

import "fmt"

// virtualClock — керований годинник для тестів sink/switcher.
type virtualClock struct{ t float64 }

func (c *virtualClock) Now() float64 { return c.t }

func (c *virtualClock) advance(d float64) { c.t += d }

// aseq/adata/vkey — мінімальні payload-и тегів для черги sink-а.
func aseq(marker byte) []byte { return []byte{0xAF, 0x00, marker, 0x10} }

func adata(marker byte, size int) []byte {
	return append([]byte{0xAF, 0x01}, filled(marker, size)...)
}

func vseq(marker byte) []byte { return []byte{0x17, 0x00, 0, 0, 0, marker} }

func vkey(marker byte, size int) []byte {
	return append([]byte{0x17, 0x01, 0, 0, 0}, filled(marker, size)...)
}

func vframe(marker byte, size int) []byte {
	return append([]byte{0x27, 0x01, 0, 0, 0}, filled(marker, size)...)
}

func filled(marker byte, size int) []byte {
	body := make([]byte, size)
	for i := range body {
		body[i] = marker
	}
	return body
}

// traceRecorder — спільний журнал викликів для sink-заглушок.
type traceRecorder struct{ lines []string }

func (r *traceRecorder) add(line string) { r.lines = append(r.lines, line) }

// recSink — sink, який лише записує, що йому запропонували.
type recSink struct {
	name string
	rec  *traceRecorder
}

func (s *recSink) Name() string { return s.name }

func (s *recSink) Offer(tagType byte, ts int64, payload []byte) {
	s.rec.add(fmt.Sprintf("offer %s %d %d %d", s.name, tagType, ts, len(payload)))
}

func vinter(marker byte, size int) []byte { return vframe(marker, size) }
