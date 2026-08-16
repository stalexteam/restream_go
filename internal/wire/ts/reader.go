package ts

import (
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"restream_go/internal/wire/flv"
)

// ReadTagsOptions — режими демукса + детектор стагнації (як у flv.ReadTags).
type ReadTagsOptions struct {
	ReadTimeout    time.Duration // 0 = без таймауту
	OnStall        func()
	OnResume       func()
	AllVideoTracks bool
	MeasureBitrate bool
}

type deadlineReader interface{ SetReadDeadline(time.Time) error }

// ReadTags читає сирий MPEG-TS зі stream до EOF; onTag
// кличеться per-track ("video"/"audio0"...). Таймаут діє від першого
// РЕАЛЬНОГО keyframe (PAT/PMT-очікування + mid-GOP join) і лише якщо stream
// підтримує SetReadDeadline.
func ReadTags(stream io.Reader, onTag TagFunc, opts *ReadTagsOptions) error {
	var timeout time.Duration
	var onStall, onResume func()
	var allVideo, measure bool
	if opts != nil {
		timeout = opts.ReadTimeout
		onStall, onResume = opts.OnStall, opts.OnResume
		allVideo, measure = opts.AllVideoTracks, opts.MeasureBitrate
	}
	var dr deadlineReader
	if timeout > 0 {
		if d, ok := stream.(deadlineReader); ok && d.SetReadDeadline(time.Time{}) == nil {
			dr = d
		}
	}

	firstTag := true
	wrapped := func(role string, tagType byte, ts int64, payload []byte) {
		if firstTag && strings.HasPrefix(role, "video") && tagType == flv.TagVideo &&
			flv.IsVideoKeyframe(payload) && !flv.IsAVCSeqHeader(payload) {
			firstTag = false
		}
		onTag(role, tagType, ts, payload)
	}
	demuxer := NewDemuxer(wrapped, allVideo, measure)

	stalled := false
	buf := make([]byte, 65536)
	for {
		useDeadline := dr != nil && !firstTag
		if useDeadline {
			_ = dr.SetReadDeadline(time.Now().Add(timeout))
		}
		n, err := stream.Read(buf)
		if useDeadline {
			_ = dr.SetReadDeadline(time.Time{})
		}
		if n > 0 {
			if stalled {
				stalled = false
				if onResume != nil {
					onResume()
				}
			}
			demuxer.Feed(buf[:n])
		}
		if err != nil {
			if useDeadline && errors.Is(err, os.ErrDeadlineExceeded) {
				if !stalled {
					stalled = true
					if onStall != nil {
						onStall()
					}
				}
				continue
			}
			if err == io.EOF {
				demuxer.Flush()
				return nil
			}
			return err
		}
	}
}
