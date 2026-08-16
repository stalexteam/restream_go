package route

import (
	"bytes"
	"testing"

	"restream_go/internal/wire/flv"
)

// Ланцюжок вибору режиму: перевіряються перетини умов, де порядок гілок вирішує.
func TestSelectMode(t *testing.T) {
	cases := []struct {
		name string
		src  Source
		plat Platform
		want Plan
	}{
		{"rtmp single track", Source{"rtmp", 1, false}, Platform{"rtmp", false, 0},
			Plan{ModePlainFLV, 0, "video", false, false}},
		{"rtmp dual track", Source{"rtmp", 2, false}, Platform{"rtmp", false, 0},
			Plan{ModeTrackSelect, 0, "video", false, false}},
		{"rtmp three tracks", Source{"rtmp", 3, false}, Platform{"rtmp", false, 0},
			Plan{ModeMultitrackFixed, 0, "video", false, false}},
		{"chimera single track", Source{"rtmp", 1, false}, Platform{"rtmp", true, 0},
			Plan{ModeChimeraFixed, 0, "video", false, false}},
		{"chimera dual track", Source{"rtmp", 2, false}, Platform{"rtmp", true, 0},
			Plan{ModeChimeraSelect, 0, "video", false, false}},
		{"srt single track", Source{"srt", 1, false}, Platform{"srt", false, 0},
			Plan{ModePlainFLV, 0, "video", false, false}},
		{"srt dual track", Source{"srt", 2, false}, Platform{"srt", false, 0},
			Plan{ModeMultitrackSelect, 0, "video", false, false}},
		{"srt three tracks", Source{"srt", 3, false}, Platform{"srt", false, 0},
			Plan{ModeMultitrackSelect, 0, "video", false, false}},
		{"vod track ignored on srt", Source{"srt", 2, false}, Platform{"srt", true, 0},
			Plan{ModeMultitrackSelect, 0, "video", false, false}},
		{"negative video without eb source", Source{"rtmp", 2, false}, Platform{"rtmp", false, -1},
			Plan{ModeTrackSelect, 0, "video", false, false}},
		{"eb arm", Source{"srt", 1, true}, Platform{"rtmp", false, -1},
			Plan{ModeEB, -1, "video", true, true}},
		{"eb arm wins over chimera", Source{"srt", 2, true}, Platform{"rtmp", true, -1},
			Plan{ModeEB, -1, "video", true, true}},
		{"eb source, whole ladder on srt platform", Source{"srt", 1, true}, Platform{"srt", false, -1},
			Plan{ModeMultitrackFixed, 0, "video0", true, false}},
		{"eb source, one rung", Source{"srt", 1, true}, Platform{"rtmp", false, 2},
			Plan{ModeMultitrackFixed, 2, "video2", true, false}},
		{"eb source, one rung to chimera", Source{"srt", 2, true}, Platform{"rtmp", true, 2},
			Plan{ModeChimeraSelect, 2, "video2", true, false}},
		{"eb source, one rung to srt slots", Source{"srt", 2, true}, Platform{"srt", false, 1},
			Plan{ModeMultitrackSelect, 1, "video1", true, false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Select(c.src, c.plat); got != c.want {
				t.Fatalf("got %+v want %+v", got, c.want)
			}
		})
	}
}

func TestWrapRung(t *testing.T) {
	legacy := vframe(true, 7)
	if got := WrapRung(0, legacy); !bytes.Equal(got, legacy) {
		t.Fatalf("rung 0 must stay legacy: %x", got)
	}
	wrapped := WrapRung(3, legacy)
	id, fourcc, back, ok := flv.UnwrapMultitrackVideo(wrapped)
	if !ok || id != 3 || string(fourcc) != "avc1" || !bytes.Equal(back, legacy) {
		t.Fatalf("rung 3 round-trip: ok=%v id=%d fourcc=%s back=%x", ok, id, fourcc, back)
	}
}

// Стик перемикання track-select: переоголошений seq не оновлює монотонний
// guard, а сам guard спільний на вихідний потік і не скидається.
func TestTrackSelectSwitchSeam(t *testing.T) {
	var got []recTag
	sel := 0
	r := NewTrackSelect(recorder(&got), func() int { return sel }, "video")

	r.Route("audio0", flv.TagAudio, 0, aseq(0x10))
	r.Route("audio1", flv.TagAudio, 0, aseq(0x11))
	r.Route("audio0", flv.TagAudio, 100, adata(0xA0, 4))
	sel = 1
	r.Route("audio1", flv.TagAudio, 90, adata(0xB1, 4))  // seq під ts=90, дані під guard-ом
	r.Route("audio1", flv.TagAudio, 110, adata(0xC1, 4)) // guard пройдено

	want := []recTag{
		{"relay", flv.TagAudio, 0, 4, shaOf(aseq(0x10))},
		{"relay", flv.TagAudio, 100, 6, shaOf(adata(0xA0, 4))},
		{"relay", flv.TagAudio, 90, 4, shaOf(aseq(0x11))},
		{"relay", flv.TagAudio, 110, 6, shaOf(adata(0xC1, 4))},
	}
	compareTags(t, "switch seam", got, want)
}

// Хімера/EB guard-ів не мають: після свопу немонотонний тег проходить.
func TestChimeraSelectHasNoGuard(t *testing.T) {
	var got []recTag
	live, vod := 0, 1
	r := NewChimeraSelect(recorder(&got), func() int { return live }, func() int { return vod }, "video")

	r.Route("audio0", flv.TagAudio, 100, adata(0xA0, 4))
	live, vod = 1, 0
	r.Route("audio0", flv.TagAudio, 90, adata(0xA1, 4))

	if len(got) != 2 {
		t.Fatalf("got %d tags, want 2: %+v", len(got), got)
	}
	if got[1].tsMS != 90 || got[1].size != 11 {
		t.Fatalf("VOD-wrapped tag expected at ts 90: %+v", got[1])
	}
}

// Порожня мапа заглушки віддає слот 0, а не мовчить.
func TestMultitrackBackupEmptyMap(t *testing.T) {
	var got []recTag
	timeline := MultitrackBackupTimeline(recorder(&got), func() []int { return []int{Unmapped, Unmapped} })
	timeline("seg", flv.TagAudio, 5, adata(0xA0, 4))
	if len(got) != 1 || got[0].sha != shaOf(flv.WrapMultitrackAudio(0, adata(0xA0, 4))) {
		t.Fatalf("empty map must fall back to slot 0: %+v", got)
	}
}
