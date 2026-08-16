package flv

import (
	"bytes"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// Порт selftest із DEV/tools/: кейси, яких у
// фікстурах нема (довільні tid/fourcc, відмови).
func TestVideoWrapUnwrapIdentity(t *testing.T) {
	cases := []struct {
		pkt    byte
		legacy []byte
	}{
		{0, append([]byte("\x17\x00\x00\x00\x00"), []byte("\x01seqrec")...)},
		{1, append([]byte("\x17\x01\x00\x01\x22"), []byte("\x00\x00\x00\x04nalu")...)}, // CT!=0
		{1, append([]byte("\x27\x01\xff\xff\xff"), []byte("\x00\x00\x00\x04nalu")...)}, // від'ємний CT
		{2, []byte("\x27\x02\x00\x00\x00")},
	}
	for _, fourcc := range [][]byte{[]byte("avc1"), []byte("hvc1"), []byte("av01")} {
		for _, tid := range []byte{0, 1, 5, 7} {
			for _, c := range cases {
				wrapped := WrapMultitrackVideo(tid, fourcc, c.legacy)
				if wrapped[0]&0x80 == 0 || wrapped[0]&0x0F != 6 {
					t.Fatalf("wrap must set Ex|Multitrack: %02x", wrapped[0])
				}
				if wrapped[1] != c.pkt {
					t.Fatalf("wrap must keep packet type in byte1: %02x", wrapped[1])
				}
				if !bytes.Equal(wrapped[2:6], fourcc) || wrapped[6] != tid {
					t.Fatalf("wrap must keep fourcc+tid")
				}
				gtid, gfc, glegacy, ok := UnwrapMultitrackVideo(wrapped)
				if !ok || gtid != tid || !bytes.Equal(gfc, fourcc) || !bytes.Equal(glegacy, c.legacy) {
					t.Fatalf("unwrap(wrap) identity failed: pkt=%d tid=%d fourcc=%s", c.pkt, tid, fourcc)
				}
			}
		}
	}
	// frame type наскрізний
	if WrapMultitrackVideo(1, []byte("avc1"), []byte("\x17\x00\x00\x00\x00\x01"))[0] != 0x96 {
		t.Fatal("key -> 0x96")
	}
	if WrapMultitrackVideo(1, []byte("avc1"), []byte("\x27\x01\x00\x00\x00"))[0] != 0xA6 {
		t.Fatal("inter -> 0xA6")
	}
}

func TestVideoUnwrapRefusals(t *testing.T) {
	for name, payload := range map[string][]byte{
		"legacy":       []byte("\x17\x01\x00\x00\x00data"),
		"Ex single":    []byte("\x91avc1body"),
		"ManyTracks":   []byte("\x96\x11avc1\x01xxx"),
		"CodedFramesX": []byte("\x96\x03avc1\x01xxx"),
		"short":        []byte("\x96\x01avc1"),
	} {
		if _, _, _, ok := UnwrapMultitrackVideo(payload); ok {
			t.Fatalf("%s must be refused", name)
		}
	}
}

func TestIsVideoKeyframe(t *testing.T) {
	for _, c := range []struct {
		payload []byte
		want    bool
	}{
		{[]byte("\x17\x01"), true},          // legacy key
		{[]byte("\x27\x01"), false},         // legacy inter
		{[]byte("\x96\x01avc1\x01"), true},  // Ex multitrack key
		{[]byte("\xa6\x01avc1\x01"), false}, // Ex multitrack inter
		{[]byte("\x91avc1"), true},          // Ex single key
		{nil, false},                        // empty
	} {
		if IsVideoKeyframe(c.payload) != c.want {
			t.Fatalf("IsVideoKeyframe(% x) != %v", c.payload, c.want)
		}
	}
}

func TestIsAVCSeqHeader(t *testing.T) {
	for _, c := range []struct {
		payload []byte
		want    bool
	}{
		{[]byte("\x17\x00\x00\x00\x00"), true},  // legacy seq
		{[]byte("\x17\x01\x00\x00\x00"), false}, // legacy frame
		{[]byte("\x96\x00avc1\x01\x01"), true},  // multitrack seq
		{[]byte("\x96\x01avc1\x01\x00"), false}, // multitrack frame
		{[]byte("\x90avc1\x01"), true},          // Ex-single seq
		{[]byte("\x91avc1"), false},             // Ex-single frame
		{[]byte("\x17"), false},                 // short
	} {
		if IsAVCSeqHeader(c.payload) != c.want {
			t.Fatalf("IsAVCSeqHeader(% x) != %v", c.payload, c.want)
		}
	}
}

func TestAudioWrapUnwrap(t *testing.T) {
	for _, tid := range []byte{0, 1, 5} {
		for _, legacy := range [][]byte{
			[]byte("\xaf\x00\x12\x10"),    // seq header + ASC
			[]byte("\xaf\x01rawaacframe"), // coded frames
		} {
			wrapped := WrapMultitrackAudio(tid, legacy)
			if wrapped[0] != 0x95 || wrapped[1] != legacy[1]&0x0F ||
				!bytes.Equal(wrapped[2:6], []byte("mp4a")) || wrapped[6] != tid {
				t.Fatalf("wrap layout wrong: % x", wrapped[:7])
			}
			gtid, glegacy, ok := UnwrapMultitrackAudio(wrapped)
			if !ok || gtid != tid || !bytes.Equal(glegacy, legacy) {
				t.Fatalf("audio unwrap(wrap) identity failed: tid=%d", tid)
			}
		}
	}
	for name, payload := range map[string][]byte{
		"legacy AF":    []byte("\xaf\x01data"),
		"wrong fourcc": []byte("\x95\x00opus\x01xx"),
		"ManyTracks":   []byte("\x95\x11mp4a\x01xx"),
		"short":        []byte("\x95\x00mp4a"),
	} {
		if _, _, ok := UnwrapMultitrackAudio(payload); ok {
			t.Fatalf("%s must be refused", name)
		}
	}
	if IsMultitrackAudio([]byte("\x95\x00mp4a")) {
		t.Fatal("6 bytes is below wrapper size")
	}
	if !IsMultitrackAudio([]byte("\x95\x00mp4a\x01")) {
		t.Fatal("7-byte 0x95 is multitrack")
	}
}

func TestHeaderKeyAndTrackID(t *testing.T) {
	if HeaderKey(TagVideo, []byte("\x17\x00")) != "video" {
		t.Fatal("legacy video key")
	}
	wrapped := WrapMultitrackVideo(3, []byte("hvc1"), []byte("\x17\x00\x00\x00\x00\x01"))
	if HeaderKey(TagVideo, wrapped) != "video3" {
		t.Fatal("video3 key")
	}
	if !IsSeqHeader(TagVideo, wrapped) {
		t.Fatal("is_seq_header via dispatcher")
	}
	if HeaderKey(TagAudio, []byte("\xaf\x01data")) != "audio" {
		t.Fatal("legacy audio key")
	}
	if HeaderKey(TagAudio, WrapMultitrackAudio(2, []byte("\xaf\x01data"))) != "audio2" {
		t.Fatal("audio2 key")
	}
	if HeaderKey(TagScript, []byte("\x02\x00\x0aonMetaData")) != "meta" {
		t.Fatal("script tag must key as 'meta'")
	}
	if VideoTrackID([]byte("\x17\x01\x00\x00\x00")) != 0 {
		t.Fatal("legacy track id 0")
	}
	if VideoTrackID(WrapMultitrackVideo(5, []byte("avc1"), []byte("\x27\x01\x00\x00\x00nalu"))) != 5 {
		t.Fatal("Ex track id 5")
	}
	if IsSeqHeader(TagScript, []byte("\x02\x00")) {
		t.Fatal("script is never a seq header")
	}
}

func TestOrderedHeaderItems(t *testing.T) {
	hdrs := map[string][]byte{
		"audio2": nil, "video3": nil, "meta": nil, "video": nil, "audio": nil, "video1": nil,
	}
	items := OrderedHeaderItems(hdrs)
	wantKeys := []string{"meta", "video", "video1", "video3", "audio", "audio2"}
	wantTypes := []byte{TagScript, TagVideo, TagVideo, TagVideo, TagAudio, TagAudio}
	if len(items) != len(wantKeys) {
		t.Fatalf("got %d items", len(items))
	}
	for i, it := range items {
		if it.Key != wantKeys[i] || it.TagType != wantTypes[i] {
			t.Fatalf("item %d: (%s,%d) want (%s,%d)", i, it.Key, it.TagType, wantKeys[i], wantTypes[i])
		}
	}
	// поза стелями слотів — ігноруються
	over := map[string]bool{"video8": true, "audio6": true}
	if len(OrderedHeaderItems(over)) != 0 {
		t.Fatal("slots beyond MaxVideoSlots/MaxAudioSlots must be ignored")
	}
}

func TestWriteTagBytes(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteTag(&buf, TagVideo, 0x01234567, []byte("AB")); err != nil {
		t.Fatal(err)
	}
	want := []byte{
		9, 0, 0, 2, // type, size
		0x23, 0x45, 0x67, 0x01, // ts low 3 + extended
		0, 0, 0, // StreamID
		'A', 'B',
		0, 0, 0, 13, // PreviousTagSize
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("got % x want % x", buf.Bytes(), want)
	}
}

type flushCounter struct {
	buf     bytes.Buffer
	flushes int
}

func (f *flushCounter) Write(p []byte) (int, error) { return f.buf.Write(p) }
func (f *flushCounter) Flush() error                { f.flushes++; return nil }

func TestPipeOutputFlushesEveryWrite(t *testing.T) {
	fc := &flushCounter{}
	out := NewPipeOutput(fc)
	if err := out.WriteHeader(); err != nil {
		t.Fatal(err)
	}
	if fc.flushes != 1 || !bytes.Equal(fc.buf.Bytes(), FileHeader) {
		t.Fatalf("header: flushes=%d bytes=% x", fc.flushes, fc.buf.Bytes())
	}
	if err := out.WriteTag(TagAudio, 5, []byte("\xaf\x01xx")); err != nil {
		t.Fatal(err)
	}
	if fc.flushes != 2 {
		t.Fatalf("tag write must flush, flushes=%d", fc.flushes)
	}
	// writer без Flush теж працює
	plain := NewPipeOutput(&bytes.Buffer{})
	if err := plain.WriteHeader(); err != nil {
		t.Fatal(err)
	}
}

type recTag struct {
	typ     byte
	ts      uint32
	payload []byte
}

func collectTags(t *testing.T, stream *bytes.Buffer) []recTag {
	t.Helper()
	var got []recTag
	err := ReadTags(stream, "s", func(source string, tagType byte, ts uint32, payload []byte) {
		got = append(got, recTag{tagType, ts, append([]byte(nil), payload...)})
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestReadTagsSynthetic(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(FileHeader)
	WriteTag(&buf, TagScript, 0, []byte("\x02\x00\x0aonMetaData"))
	WriteTag(&buf, TagVideo, 0, []byte("\x17\x00\x00\x00\x00\x01"))
	WriteTag(&buf, 4, 1, []byte("skipme")) // невідомий тип — не віддається
	WriteTag(&buf, TagVideo, 33, []byte("\x17\x01\x00\x00\x00frame"))
	WriteTag(&buf, TagAudio, 0xFF001234, []byte("\xaf\x01aac")) // ts понад 24 біти
	got := collectTags(t, &buf)
	if len(got) != 4 {
		t.Fatalf("got %d tags", len(got))
	}
	if got[0].typ != TagScript || got[1].typ != TagVideo || got[2].typ != TagVideo || got[3].typ != TagAudio {
		t.Fatalf("tag types: %+v", got)
	}
	if got[3].ts != 0xFF001234 {
		t.Fatalf("extended ts lost: %08x", got[3].ts)
	}
	if !bytes.Equal(got[2].payload, []byte("\x17\x01\x00\x00\x00frame")) {
		t.Fatal("payload mismatch")
	}
}

func TestReadTagsBadHeaderAndTruncation(t *testing.T) {
	var n int
	count := func(string, byte, uint32, []byte) { n++ }

	if err := ReadTags(bytes.NewBufferString("XXX garbage stream"), "s", count, nil); err != nil || n != 0 {
		t.Fatalf("bad magic: err=%v n=%d", err, n)
	}
	if err := ReadTags(&bytes.Buffer{}, "s", count, nil); err != nil || n != 0 {
		t.Fatalf("empty: err=%v n=%d", err, n)
	}

	var buf bytes.Buffer
	buf.Write(FileHeader)
	WriteTag(&buf, TagAudio, 1, []byte("\xaf\x01aa"))
	WriteTag(&buf, TagAudio, 2, []byte("\xaf\x01bb"))
	full := buf.Bytes()
	trunc := bytes.NewBuffer(full[:len(full)-9]) // ріже другий тег посеред payload
	n = 0
	if err := ReadTags(trunc, "s", count, nil); err != nil || n != 1 {
		t.Fatalf("truncated: err=%v n=%d", err, n)
	}
}

func TestReadTagsStallResume(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Pipe deadlines unsupported on Windows")
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()

	var stalls, resumes, tags atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- ReadTags(pr, "s", func(string, byte, uint32, []byte) { tags.Add(1) }, &ReadTagsOptions{
			ReadTimeout: 60 * time.Millisecond,
			OnStall:     func() { stalls.Add(1) },
			OnResume:    func() { resumes.Add(1) },
		})
	}()

	writeTag := func(typ byte, ts uint32, p []byte) {
		var b bytes.Buffer
		WriteTag(&b, typ, int64(ts), p)
		if _, err := pw.Write(b.Bytes()); err != nil {
			t.Error(err)
		}
	}
	pw.Write(FileHeader)
	writeTag(TagVideo, 0, []byte("\x17\x00\x00\x00\x00\x01")) // seq header: таймаут ще не діє
	time.Sleep(200 * time.Millisecond)
	if stalls.Load() != 0 {
		t.Fatal("stall before first keyframe is forbidden")
	}
	writeTag(TagVideo, 33, []byte("\x17\x01\x00\x00\x00frame")) // перший keyframe — таймаут увімкнено
	time.Sleep(250 * time.Millisecond)
	if stalls.Load() != 1 {
		t.Fatalf("expected exactly one stall, got %d", stalls.Load())
	}
	writeTag(TagAudio, 66, []byte("\xaf\x01aac"))
	time.Sleep(100 * time.Millisecond)
	if resumes.Load() != 1 {
		t.Fatalf("expected one resume, got %d", resumes.Load())
	}
	pw.Close()
	if err := <-done; err != nil {
		t.Fatalf("ReadTags: %v", err)
	}
	if tags.Load() != 3 {
		t.Fatalf("tags delivered: %d", tags.Load())
	}
}
