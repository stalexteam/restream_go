// Package probe визначає вміст джерела двома шляхами: ffprobe (codec/geometry
// одного треку, лічильники доріжок, наявність відео/аудіо, тривалість файлу)
// і власним TS-транспортом (ProbeTSManifest — паспорт кожної відеодоріжки
// драбини без ffprobe-analyzeduration, R13).
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"restream_go/internal/proc"
)

const probeTimeout = 15 * time.Second

// ffprobe за замовч. аналізує MPEG-TS/SRT-вхід до 5с; 2.5с/3МБ — емпіричний
// надійний поріг (нижче ~2с іноді не знаходить відеодоріжку) з запасом над
// keyframe-interval 2с. Для RTMP нешкідливо.
var liveProbeArgs = []string{"-analyzeduration", "2500000", "-probesize", "3000000"}

// StreamParams — codec/геометрія обраного відео- та аудіотреку джерела.
type StreamParams struct {
	VideoCodec string
	Width      int
	Height     int
	FPS        int
	AudioCodec string
	Channels   int
	SampleRate int
}

// MediaTracks — наявність відео/аудіодоріжок у джерелі.
type MediaTracks struct {
	Video bool
	Audio bool
}

// TrackCounts — кількість доріжок за типом.
type TrackCounts struct {
	Video int
	Audio int
}

// BuildStreamParamsArgs — argv ffprobe для ProbeStreamParams (probe_stream_params).
func BuildStreamParamsArgs(target string) []string {
	args := []string{"ffprobe", "-v", "error"}
	args = append(args, liveProbeArgs...)
	args = append(args, "-show_entries",
		"stream=codec_type,codec_name,width,height,r_frame_rate,channels,sample_rate",
		"-of", "json", target)
	return args
}

// BuildMediaTracksArgs — argv ffprobe для ProbeMediaTracks (probe_media_tracks).
func BuildMediaTracksArgs(target string) []string {
	return []string{"ffprobe", "-v", "error", "-show_entries", "stream=codec_type", "-of", "json", target}
}

// BuildDurationArgs — argv ffprobe для ProbeDurationSec (probe_duration_sec).
func BuildDurationArgs(target string) []string {
	return []string{"ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", target}
}

// BuildTrackCountsArgs — argv ffprobe для ProbeTrackCounts (probe_track_counts).
func BuildTrackCountsArgs(target string) []string {
	args := []string{"ffprobe", "-v", "error"}
	args = append(args, liveProbeArgs...)
	args = append(args, "-show_entries", "stream=codec_type", "-of", "json", target)
	return args
}

// runProbe спавнить ffprobe з таймаутом; err — лише збій спавну/таймаут,
// ненульовий код виходу повертається окремо в exitCode без помилки.
func runProbe(args []string) (stdout []byte, exitCode int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var out bytes.Buffer
	cmd.Stdout = &out
	runErr := proc.StartCmd(cmd)
	if runErr == nil {
		runErr = cmd.Wait()
	}
	if ctx.Err() == context.DeadlineExceeded {
		return out.Bytes(), -1, fmt.Errorf("timed out after %s", probeTimeout)
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return out.Bytes(), -1, runErr
		}
		return out.Bytes(), exitErr.ExitCode(), nil
	}
	return out.Bytes(), 0, nil
}

func decodeStreams(stdout []byte) ([]map[string]any, error) {
	var doc struct {
		Streams []map[string]any `json:"streams"`
	}
	if err := json.Unmarshal(stdout, &doc); err != nil {
		return nil, err
	}
	return doc.Streams, nil
}

func codecType(s map[string]any) string {
	v, _ := s["codec_type"].(string)
	return v
}

func reqString(m map[string]any, key string) (string, error) {
	v, ok := m[key]
	if !ok {
		return "", fmt.Errorf("missing %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("field %q is not a string", key)
	}
	return s, nil
}

// reqInt — int(m[key]) py-семантикою: приймає і JSON-число, і рядок-число
// (ffprobe місцями віддає числа рядками, напр. sample_rate).
func reqInt(m map[string]any, key string) (int, error) {
	v, ok := m[key]
	if !ok {
		return 0, fmt.Errorf("missing %q", key)
	}
	switch x := v.(type) {
	case float64:
		return int(x), nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return 0, err
		}
		return n, nil
	default:
		return 0, fmt.Errorf("field %q has unsupported type %T", key, v)
	}
}

// computeFPS — round(int(num)/int(den)) py-семантикою: `int(den or 0)`
// коротко-замикає обчислення num при порожньому/нульовому den (уникає ділення
// на нуль, не парсить num узагалі в цьому випадку).
func computeFPS(rFrameRate string) (int, error) {
	numStr, denStr, _ := strings.Cut(rFrameRate, "/")
	den := 0
	if denStr != "" {
		d, err := strconv.Atoi(denStr)
		if err != nil {
			return 0, err
		}
		den = d
	}
	if den == 0 {
		return 0, nil
	}
	num, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, err
	}
	return int(math.RoundToEven(float64(num) / float64(den))), nil
}

// ProbeStreamParams — {video_codec, width, height, fps, audio_codec, channels,
// sample_rate} для обраних відео/аудіотреків target (файл або URL); ok=false,
// якщо не вдалось визначити. video_index не 0 лише для EB-драбини.
// Кодек — навмисно частина порівняння (не лише геометрія): -c copy вимагає
// збіжного кодека, інакше "вже відповідний" за розміром файл з іншим кодеком
// ніколи не перекодувався б і -c copy впав би.
func ProbeStreamParams(target string, audioIndex, videoIndex int) (StreamParams, bool) {
	params, err := probeStreamParams(target, audioIndex, videoIndex)
	if err != nil {
		log.Printf("probe: could not determine parameters for '%s': %v", target, err)
		return StreamParams{}, false
	}
	if params == nil {
		return StreamParams{}, false
	}
	return *params, true
}

func probeStreamParams(target string, audioIndex, videoIndex int) (*StreamParams, error) {
	stdout, exitCode, err := runProbe(BuildStreamParamsArgs(target))
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("ffprobe exited with %d", exitCode)
	}
	streams, err := decodeStreams(stdout)
	if err != nil {
		return nil, err
	}
	var vstreams, astreams []map[string]any
	for _, s := range streams {
		switch codecType(s) {
		case "video":
			vstreams = append(vstreams, s)
		case "audio":
			astreams = append(astreams, s)
		}
	}
	if videoIndex >= len(vstreams) || audioIndex >= len(astreams) {
		return nil, nil
	}
	v, a := vstreams[videoIndex], astreams[audioIndex]

	videoCodec, err := reqString(v, "codec_name")
	if err != nil {
		return nil, err
	}
	width, err := reqInt(v, "width")
	if err != nil {
		return nil, err
	}
	height, err := reqInt(v, "height")
	if err != nil {
		return nil, err
	}
	rFrameRate, err := reqString(v, "r_frame_rate")
	if err != nil {
		return nil, err
	}
	fps, err := computeFPS(rFrameRate)
	if err != nil {
		return nil, err
	}
	audioCodec, err := reqString(a, "codec_name")
	if err != nil {
		return nil, err
	}
	channels, err := reqInt(a, "channels")
	if err != nil {
		return nil, err
	}
	sampleRate, err := reqInt(a, "sample_rate")
	if err != nil {
		return nil, err
	}
	return &StreamParams{videoCodec, width, height, fps, audioCodec, channels, sampleRate}, nil
}

// ProbeMediaTracks — наявність відео/аудіодоріжок у target; ok=false, якщо
// ffprobe не зміг ВІДКРИТИ джерело (на відміну від «читається, але без
// потрібної доріжки» — MediaTracks{} з ok=true).
func ProbeMediaTracks(target string) (MediaTracks, bool) {
	stdout, exitCode, err := runProbe(BuildMediaTracksArgs(target))
	if err != nil {
		log.Printf("probe: could not probe tracks for '%s': %v", target, err)
		return MediaTracks{}, false
	}
	if exitCode != 0 {
		return MediaTracks{}, false
	}
	streams, err := decodeStreams(stdout)
	if err != nil {
		log.Printf("probe: could not probe tracks for '%s': %v", target, err)
		return MediaTracks{}, false
	}
	var mt MediaTracks
	for _, s := range streams {
		switch codecType(s) {
		case "video":
			mt.Video = true
		case "audio":
			mt.Audio = true
		}
	}
	return mt, true
}

// ProbeDurationSec — тривалість локального файла в секундах; ok=false, якщо
// не вдалось прочитати.
func ProbeDurationSec(target string) (float64, bool) {
	stdout, exitCode, err := runProbe(BuildDurationArgs(target))
	if err != nil {
		log.Printf("probe: could not probe duration for '%s': %v", target, err)
		return 0, false
	}
	value := strings.TrimSpace(string(stdout))
	if exitCode != 0 || value == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(value, 64)
	if err != nil {
		log.Printf("probe: could not probe duration for '%s': %v", target, err)
		return 0, false
	}
	return v, true
}

// ProbeTrackCounts — кількість доріжок за типом; ok=false, якщо джерело не
// відкрилось. Для звірки контракту source (декларація типу/кількості
// аудіодоріжок проти фактичного вмісту публікації).
func ProbeTrackCounts(target string) (TrackCounts, bool) {
	stdout, exitCode, err := runProbe(BuildTrackCountsArgs(target))
	if err != nil {
		log.Printf("probe: could not probe track counts for '%s': %v", target, err)
		return TrackCounts{}, false
	}
	if exitCode != 0 {
		return TrackCounts{}, false
	}
	streams, err := decodeStreams(stdout)
	if err != nil {
		log.Printf("probe: could not probe track counts for '%s': %v", target, err)
		return TrackCounts{}, false
	}
	var tc TrackCounts
	for _, s := range streams {
		switch codecType(s) {
		case "video":
			tc.Video++
		case "audio":
			tc.Audio++
		}
	}
	return tc, true
}
