package fallback

import (
	"fmt"
	"strconv"
	"strings"
)

// Keyframe-інтервал заглушки (с) — той самий, що Twitch оголошує для сходинок
// драбини і що ми просимо в OBS.
const keyframeIntervalSec = 2

// Rung — сходинка драбини в ЦІЛІ нормалізації (порядок = trackId).
type Rung struct {
	Width            int
	Height           int
	FPS              int
	VideoBitrateKbps int
}

// TargetParams — ціль нормалізації заглушки. Непорожній Ladder = EB-плече:
// геометрія береться зі сходинок, і поля Width/Height/FPS/VideoBitrateKbps у
// ключі кеша та в команді участі не беруть.
type TargetParams struct {
	Ladder           []Rung
	Width            int
	Height           int
	FPS              int
	Channels         int
	SampleRate       int
	VideoBitrateKbps int
	AudioBitrateKbps int
}

// IsLadder — чи це драбинна ціль (N рендішенів в один артефакт).
func (t TargetParams) IsLadder() bool { return len(t.Ladder) > 0 }

// BuildLadderCommand — один ffmpeg -> MPEG-TS з N відеорендішенами драбини +
// аудіо: сходинки мусять бути keyframe-вирівняні і йти по ОДНІЙ шкалі
// часу, інакше це N плеєрів, які треба синхронізувати.
func BuildLadderCommand(source, out string, target TargetParams,
	threadArgs, encoderThreadArgs []string) []string {
	rungs := target.Ladder
	var splits strings.Builder
	for i := range rungs {
		fmt.Fprintf(&splits, "[s%d]", i)
	}
	chains := []string{fmt.Sprintf("[0:v]split=%d%s", len(rungs), splits.String())}
	for i, rung := range rungs {
		chains = append(chains, fmt.Sprintf(
			"[s%d]scale=%d:%d:force_original_aspect_ratio=decrease,"+
				"pad=%d:%d:(ow-iw)/2:(oh-ih)/2,fps=%d[v%d]",
			i, rung.Width, rung.Height, rung.Width, rung.Height, rung.FPS, i))
	}

	cmd := []string{"ffmpeg", "-y", "-hide_banner", "-loglevel", "warning"}
	cmd = append(cmd, threadArgs...)
	cmd = append(cmd, "-i", source, "-filter_complex", strings.Join(chains, ";"))
	for i := range rungs {
		cmd = append(cmd, "-map", fmt.Sprintf("[v%d]", i))
	}
	cmd = append(cmd, "-map", "0:a:0")
	// -bf 0: без B-кадрів — decode-порядок заглушки збігається з показом.
	cmd = append(cmd, "-c:v", "libx264", "-preset", "veryfast", "-bf", "0")
	for i, rung := range rungs {
		bitrate := rung.VideoBitrateKbps
		gop := rung.FPS * keyframeIntervalSec
		if gop < 1 {
			gop = 1
		}
		cmd = append(cmd,
			fmt.Sprintf("-b:v:%d", i), fmt.Sprintf("%dk", bitrate),
			fmt.Sprintf("-maxrate:v:%d", i), fmt.Sprintf("%dk", bitrate),
			fmt.Sprintf("-bufsize:v:%d", i), fmt.Sprintf("%dk", bitrate*2),
			fmt.Sprintf("-g:v:%d", i), strconv.Itoa(gop),
			fmt.Sprintf("-keyint_min:v:%d", i), strconv.Itoa(gop),
			// Спільний вираз на всі сходинки -> keyframes падають на ті самі
			// медіа-часи навіть при різному fps.
			fmt.Sprintf("-force_key_frames:v:%d", i),
			fmt.Sprintf("expr:gte(t,n_forced*%d)", keyframeIntervalSec))
	}
	cmd = append(cmd, encoderThreadArgs...)
	cmd = append(cmd,
		"-c:a", "aac", "-b:a", fmt.Sprintf("%dk", target.AudioBitrateKbps),
		"-ac", strconv.Itoa(target.Channels), "-ar", strconv.Itoa(target.SampleRate),
		// Дефолтний muxdelay mpegts — 0.7с: відео лягає у файл поперед аудіо, і
		// гравець віддає теги в порядку файла (аудіо клумпами по 400-700мс).
		"-muxdelay", "0", "-muxpreload", "0",
		"-f", "mpegts", out)
	return cmd
}

// BuildSingleCommand — звичайна (не драбинна) ціль: одна геометрія в MP4.
func BuildSingleCommand(source, out string, target TargetParams,
	threadArgs, encoderThreadArgs []string) []string {
	w, h, fps := target.Width, target.Height, target.FPS
	vbitrate := target.VideoBitrateKbps

	cmd := []string{"ffmpeg", "-y", "-hide_banner", "-loglevel", "warning"}
	cmd = append(cmd, threadArgs...)
	cmd = append(cmd,
		"-i", source,
		"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,"+
			"pad=%d:%d:(ow-iw)/2:(oh-ih)/2,fps=%d", w, h, w, h, fps),
		"-ac", strconv.Itoa(target.Channels), "-ar", strconv.Itoa(target.SampleRate),
		"-c:v", "libx264", "-preset", "veryfast", "-bf", "0",
		"-g", strconv.Itoa(fps*2), "-keyint_min", strconv.Itoa(fps*2),
		"-b:v", fmt.Sprintf("%dk", vbitrate), "-maxrate", fmt.Sprintf("%dk", vbitrate),
		"-bufsize", fmt.Sprintf("%dk", vbitrate*2),
		"-c:a", "aac", "-b:a", fmt.Sprintf("%dk", target.AudioBitrateKbps))
	cmd = append(cmd, encoderThreadArgs...)
	return append(cmd, out)
}
