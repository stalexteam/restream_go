package flv

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"time"
)

// TagFunc отримує кожен аудіо/відео/script-тег (тип 8/9/18).
type TagFunc func(source string, tagType byte, timestamp uint32, payload []byte)

// ReadTagsOptions — детектор стагнації relay ("Read timeout" з дашборда).
type ReadTagsOptions struct {
	ReadTimeout time.Duration // 0 = без таймауту
	OnStall     func()
	OnResume    func()
}

type deadlineReader interface{ SetReadDeadline(time.Time) error }

// ReadTags читає FLV-теги зі stream до EOF (nil на EOF/невалідному
// заголовку, помилка лише на I/O-збої) і кличе onTag для тегів 8/9/18.
// Таймаут діє від першого keyframe (до нього легітимне очікування буває
// довшим) і лише якщо stream підтримує SetReadDeadline.
func ReadTags(stream io.Reader, source string, onTag TagFunc, opts *ReadTagsOptions) error {
	var dr deadlineReader
	var timeout time.Duration
	var onStall, onResume func()
	if opts != nil && opts.ReadTimeout > 0 {
		if d, ok := stream.(deadlineReader); ok && d.SetReadDeadline(time.Time{}) == nil {
			dr, timeout = d, opts.ReadTimeout
			onStall, onResume = opts.OnStall, opts.OnResume
		}
	}
	br := bufio.NewReader(stream)

	header := make([]byte, len(FileHeader)-prevTagSizeSize)
	if err := readExact(br, header); err != nil {
		return finish(err)
	}
	if !bytes.Equal(header[:3], []byte("FLV")) {
		return nil
	}
	prev := make([]byte, prevTagSizeSize)
	if err := readExact(br, prev); err != nil { // PreviousTagSize0
		return finish(err)
	}

	firstTag := true
	stalled := false
	tagHeader := make([]byte, tagHeaderSize)
	for {
		if dr != nil && !firstTag {
			b, err := waitFirstByte(br, dr, timeout, &stalled, onStall)
			if err != nil {
				return finish(err)
			}
			if stalled {
				stalled = false
				if onResume != nil {
					onResume()
				}
			}
			tagHeader[0] = b
			if err := readExact(br, tagHeader[1:]); err != nil {
				return finish(err)
			}
		} else if err := readExact(br, tagHeader); err != nil {
			return finish(err)
		}
		tagType := tagHeader[0]
		dataSize := int(tagHeader[1])<<16 | int(tagHeader[2])<<8 | int(tagHeader[3])
		ts := uint32(tagHeader[4])<<16 | uint32(tagHeader[5])<<8 | uint32(tagHeader[6]) |
			uint32(tagHeader[7])<<24
		payload := make([]byte, dataSize)
		if err := readExact(br, payload); err != nil {
			return finish(err)
		}
		if err := readExact(br, prev); err != nil {
			return finish(err)
		}
		if firstTag && tagType == TagVideo && IsVideoKeyframe(payload) && !IsAVCSeqHeader(payload) {
			firstTag = false
		}
		if tagType == TagAudio || tagType == TagVideo || tagType == TagScript {
			onTag(source, tagType, ts, payload)
		}
	}
}

// waitFirstByte чекає перший байт тега під дедлайном; стагнація -> onStall
// (один раз на паузу).
func waitFirstByte(br *bufio.Reader, dr deadlineReader, timeout time.Duration, stalled *bool, onStall func()) (byte, error) {
	for {
		_ = dr.SetReadDeadline(time.Now().Add(timeout))
		b, err := br.ReadByte()
		if err == nil {
			_ = dr.SetReadDeadline(time.Time{})
			return b, nil
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			if !*stalled {
				*stalled = true
				if onStall != nil {
					onStall()
				}
			}
			continue
		}
		_ = dr.SetReadDeadline(time.Time{})
		return 0, err
	}
}

// readExact дочитує buf цілком. Частковий EOF -> io.ErrUnexpectedEOF;
// хвостову дедлайн-помилку bufio (дедлайн уже знято) ретраїть блокуюче.
func readExact(r io.Reader, buf []byte) error {
	n := 0
	for n < len(buf) {
		m, err := r.Read(buf[n:])
		n += m
		if n == len(buf) {
			return nil
		}
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}
			if err == io.EOF && n > 0 {
				return io.ErrUnexpectedEOF
			}
			return err
		}
	}
	return nil
}

// finish: EOF (і частковий) — штатне завершення, як у python-версії.
func finish(err error) error {
	if err == nil || err == io.EOF || err == io.ErrUnexpectedEOF {
		return nil
	}
	return err
}
