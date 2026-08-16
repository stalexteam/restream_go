package flv

import "io"

// WriteTag серіалізує один FLV-тег (11-байтний header + payload +
// PreviousTagSize) одним записом. Маска шкали до 32 біт — тут.
func WriteTag(w io.Writer, tagType byte, ts int64, payload []byte) error {
	timestamp := uint32(ts)
	buf := make([]byte, 0, tagHeaderSize+len(payload)+prevTagSizeSize)
	n := len(payload)
	buf = append(buf,
		tagType,
		byte(n>>16), byte(n>>8), byte(n),
		byte(timestamp>>16), byte(timestamp>>8), byte(timestamp),
		byte(timestamp>>24), // TimestampExtended
		0, 0, 0,             // StreamID, завжди 0
	)
	buf = append(buf, payload...)
	prev := uint32(tagHeaderSize + n)
	buf = append(buf, byte(prev>>24), byte(prev>>16), byte(prev>>8), byte(prev))
	_, err := w.Write(buf)
	return err
}

type flusher interface{ Flush() error }

// PipeOutput — FLV-байтовий вихід у pipe (stdin push-ffmpeg) під інтерфейс
// OutputSink. Кожен header/tag іде негайно: якщо writer буферизований
// (має Flush) — flush після КОЖНОГО запису.
type PipeOutput struct {
	w io.Writer
	f flusher
}

func NewPipeOutput(w io.Writer) *PipeOutput {
	p := &PipeOutput{w: w}
	if f, ok := w.(flusher); ok {
		p.f = f
	}
	return p
}

func (p *PipeOutput) WriteHeader() error {
	if _, err := p.w.Write(FileHeader); err != nil {
		return err
	}
	return p.flush()
}

func (p *PipeOutput) WriteTag(tagType byte, ts int64, payload []byte) error {
	if err := WriteTag(p.w, tagType, ts, payload); err != nil {
		return err
	}
	return p.flush()
}

func (p *PipeOutput) flush() error {
	if p.f != nil {
		return p.f.Flush()
	}
	return nil
}
