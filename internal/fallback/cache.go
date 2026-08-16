package fallback

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"restream_go/internal/proc"
)

// Версія рецепта транскоду — входить у ключ кеша: зміна самих команд ffmpeg
// мусить інвалідувати вже готові артефакти (ключ від джерела+параметрів їх не
// розрізняє).
const recipeVersion = 3

// Дефолти під однопроцесорний VPS.
const (
	DefaultMaxConcurrentTranscodes = 1
	DefaultTranscodeThreads        = 1
)

// ActiveTranscode — живий ffmpeg транскоду: pid і файл-джерело (дашборд
// показує їх у рядку fallback-preparer).
type ActiveTranscode struct {
	PID  int
	Name string
}

// sourceIdentity — ідентичність вихідного файла в ключі кеша.
type sourceIdentity struct {
	Path  string
	MTime int64
	Size  int64
}

// Cache — спільний контент-адресований кеш готових заглушок + дедуп воркерів
// . Однакові (джерело+ціль) готуються рівно ОДИН раз.
type Cache struct {
	dir     string
	threads int
	slots   chan struct{}

	mu       sync.Mutex
	inflight map[string]chan struct{}
	active   map[int]string

	// Точка запуску ffmpeg — шов для підміни в тестах (як monkeypatch
	// точка запуску транскоду).
	spawn func(args []string, tmpPath, artifact, meta string, id sourceIdentity, target TargetParams) bool
}

// NewCache — кеш у dir; maxConcurrent — скільки транскодів одночасно,
// threads — скільки потоків кожному (0 — лишити ffmpeg авто).
func NewCache(dir string, maxConcurrent, threads int) *Cache {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("could not create the backup cache directory %s: %v", dir, err)
	}
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	if threads < 0 {
		threads = 0
	}
	c := &Cache{
		dir:      dir,
		threads:  threads,
		slots:    make(chan struct{}, maxConcurrent),
		inflight: map[string]chan struct{}{},
		active:   map[int]string{},
	}
	c.spawn = c.spawnTranscode
	return c
}

// ThreadArgs — вхідний бік: декодер + фільтри (опції перед `-i`).
func (c *Cache) ThreadArgs() []string {
	if c.threads == 0 {
		return nil
	}
	count := strconv.Itoa(c.threads)
	return []string{"-threads", count, "-filter_threads", count, "-filter_complex_threads", count}
}

// EncoderThreadArgs — вихідний бік: САМ КОДЕР. `-threads` перед `-i` налаштовує
// лише декодер, і libx264 усе одно взяв би 1.5*ncpu (89 потоків без обмежень,
// 9 при обмеженні з обох боків).
func (c *Cache) EncoderThreadArgs() []string {
	if c.threads == 0 {
		return nil
	}
	return []string{"-threads", strconv.Itoa(c.threads)}
}

// ActiveTranscodes — pid-и ffmpeg-ів, що зараз транскодять, за зростанням pid.
func (c *Cache) ActiveTranscodes() []ActiveTranscode {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ActiveTranscode, 0, len(c.active))
	for pid, name := range c.active {
		out = append(out, ActiveTranscode{PID: pid, Name: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// GetOrBuild — готовий артефакт для (джерело+ціль), збудований рівно один раз;
// "" — джерело зникло або транскод упав. Кликати ПОЗА локами платформи: може
// блокуюче чекати чужого воркера.
func (c *Cache) GetOrBuild(source string, target TargetParams) string {
	id, ok := identityOf(source)
	if !ok {
		log.Printf("backup source %s is missing -- cannot prepare", source)
		return ""
	}
	key := cacheKey(id, target)
	// Драбина — MPEG-TS (FLV/MP4 кількох відеодоріжок не несе); звичайна ціль — MP4.
	suffix := ".mp4"
	if target.IsLadder() {
		suffix = ".ts"
	}
	artifact := filepath.Join(c.dir, key+suffix)
	meta := filepath.Join(c.dir, key+".json")

	if artifactValid(artifact, meta, id, target) {
		return artifact
	}

	c.mu.Lock()
	if artifactValid(artifact, meta, id, target) {
		c.mu.Unlock()
		return artifact
	}
	done, building := c.inflight[key]
	if !building {
		done = make(chan struct{})
		c.inflight[key] = done
	}
	c.mu.Unlock()

	if building {
		<-done // ПОЗА локом
		if artifactValid(artifact, meta, id, target) {
			return artifact
		}
		return ""
	}

	defer func() {
		c.mu.Lock()
		delete(c.inflight, key)
		c.mu.Unlock()
		close(done)
	}()
	if c.transcode(source, artifact, meta, id, target) {
		return artifact
	}
	return ""
}

func (c *Cache) transcode(source, artifact, meta string, id sourceIdentity, target TargetParams) bool {
	tmpPath := tmpArtifactPath(artifact)
	var args []string
	if target.IsLadder() {
		args = BuildLadderCommand(source, tmpPath, target, c.ThreadArgs(), c.EncoderThreadArgs())
		log.Printf("preparing ladder backup artifact from %s -> %s (%s)",
			source, filepath.Base(artifact), rungSummary(target.Ladder))
	} else {
		args = BuildSingleCommand(source, tmpPath, target, c.ThreadArgs(), c.EncoderThreadArgs())
		log.Printf("preparing backup artifact from %s -> %s (%dx%d@%d, v=%dkbps a=%dkbps)",
			source, filepath.Base(artifact), target.Width, target.Height, target.FPS,
			target.VideoBitrateKbps, target.AudioBitrateKbps)
	}
	// Семафор беремо ПОЗА c.mu (транскод триває секунди-хвилини) і з уже
	// зареєстрованим inflight-каналом: решта охочих до цього ключа чекають на
	// нього, а не крутять другий ffmpeg.
	c.slots <- struct{}{}
	defer func() { <-c.slots }()
	return c.spawn(args, tmpPath, artifact, meta, id, target)
}

func (c *Cache) spawnTranscode(args []string, tmpPath, artifact, meta string,
	id sourceIdentity, target TargetParams) bool {
	cmd := lowPrioCmd(args)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := proc.StartCmd(cmd); err != nil {
		log.Printf("could not start ffmpeg for the backup video: %v", err)
		return false
	}
	pid := cmd.Process.Pid
	applyLowPrio(pid)
	c.mu.Lock()
	c.active[pid] = filepath.Base(id.Path)
	c.mu.Unlock()

	err := cmd.Wait()

	c.mu.Lock()
	delete(c.active, pid)
	c.mu.Unlock()

	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if len(message) > 2000 {
			message = message[len(message)-2000:]
		}
		log.Printf("failed to transcode the backup video: %s", message)
		_ = os.Remove(tmpPath)
		return false
	}
	return publishArtifact(tmpPath, artifact, meta, id, target)
}

// publishArtifact — sidecar ПЕРЕД rename: артефакт без нього не валідний ніколи.
func publishArtifact(tmpPath, artifact, meta string, id sourceIdentity, target TargetParams) bool {
	if err := os.WriteFile(meta, sidecarBytes(id, target), 0o644); err != nil {
		log.Printf("could not write the backup artifact sidecar %s: %v", meta, err)
		return false
	}
	if err := os.Rename(tmpPath, artifact); err != nil {
		log.Printf("could not publish the backup artifact %s: %v", artifact, err)
		return false
	}
	log.Printf("backup artifact ready: %s", artifact)
	return true
}

// --- контент-адресація ---

func identityOf(source string) (sourceIdentity, bool) {
	info, err := os.Stat(source)
	if err != nil {
		return sourceIdentity{}, false
	}
	return sourceIdentity{
		Path:  resolvePath(source),
		MTime: info.ModTime().Unix(),
		Size:  info.Size(),
	}, true
}

// resolvePath — Path.resolve: абсолютний шлях зі знятими симлінками.
func resolvePath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}

func (id sourceIdentity) json(sorted bool) jsonObject {
	if sorted {
		return jsonObject{{"mtime", id.MTime}, {"path", id.Path}, {"size", id.Size}}
	}
	return jsonObject{{"path", id.Path}, {"mtime", id.MTime}, {"size", id.Size}}
}

func (t TargetParams) json(sorted bool) jsonObject {
	if t.IsLadder() {
		rungs := make([]jsonObject, 0, len(t.Ladder))
		for _, r := range t.Ladder {
			if sorted {
				rungs = append(rungs, jsonObject{{"fps", r.FPS}, {"height", r.Height},
					{"video_bitrate_kbps", r.VideoBitrateKbps}, {"width", r.Width}})
			} else {
				rungs = append(rungs, jsonObject{{"width", r.Width}, {"height", r.Height},
					{"fps", r.FPS}, {"video_bitrate_kbps", r.VideoBitrateKbps}})
			}
		}
		if sorted {
			return jsonObject{{"audio_bitrate_kbps", t.AudioBitrateKbps}, {"channels", t.Channels},
				{"ladder", rungs}, {"sample_rate", t.SampleRate}}
		}
		return jsonObject{{"ladder", rungs}, {"channels", t.Channels},
			{"sample_rate", t.SampleRate}, {"audio_bitrate_kbps", t.AudioBitrateKbps}}
	}
	if sorted {
		return jsonObject{{"audio_bitrate_kbps", t.AudioBitrateKbps}, {"channels", t.Channels},
			{"fps", t.FPS}, {"height", t.Height}, {"sample_rate", t.SampleRate},
			{"video_bitrate_kbps", t.VideoBitrateKbps}, {"width", t.Width}}
	}
	return jsonObject{{"width", t.Width}, {"height", t.Height}, {"fps", t.FPS},
		{"channels", t.Channels}, {"sample_rate", t.SampleRate},
		{"video_bitrate_kbps", t.VideoBitrateKbps}, {"audio_bitrate_kbps", t.AudioBitrateKbps}}
}

// equal — порівняння цілей по канонічному вигляду: у драбинному режимі
// геометрійні поля в ціль не входять узагалі.
func (t TargetParams) equal(other TargetParams) bool {
	return pyDumps(t.json(true)) == pyDumps(other.json(true))
}

func keyBlob(id sourceIdentity, target TargetParams) string {
	return pyDumps(jsonObject{
		{"recipe", recipeVersion},
		{"source", id.json(true)},
		{"target", target.json(true)},
	})
}

func cacheKey(id sourceIdentity, target TargetParams) string {
	sum := sha1.Sum([]byte(keyBlob(id, target)))
	return hex.EncodeToString(sum[:])
}

func sidecarBytes(id sourceIdentity, target TargetParams) []byte {
	return []byte(pyDumps(jsonObject{{"source", id.json(false)}, {"target", target.json(false)}}))
}

func tmpArtifactPath(artifact string) string {
	ext := filepath.Ext(artifact)
	return strings.TrimSuffix(artifact, ext) + ".tmp" + ext
}

func artifactValid(artifact, meta string, id sourceIdentity, target TargetParams) bool {
	if _, err := os.Stat(artifact); err != nil {
		return false
	}
	raw, err := os.ReadFile(meta)
	if err != nil {
		return false
	}
	return sidecarMatches(raw, id, target)
}

// sidecarMatches — семантичне порівняння, не
// побайтове: порядок ключів у чужому sidecar на валідність не впливає.
func sidecarMatches(raw []byte, id sourceIdentity, target TargetParams) bool {
	var cached, want map[string]any
	if err := json.Unmarshal(raw, &cached); err != nil {
		return false
	}
	if err := json.Unmarshal(sidecarBytes(id, target), &want); err != nil {
		return false
	}
	return reflect.DeepEqual(cached["source"], want["source"]) &&
		reflect.DeepEqual(cached["target"], want["target"])
}

func rungSummary(rungs []Rung) string {
	parts := make([]string, 0, len(rungs))
	for _, r := range rungs {
		parts = append(parts, fmt.Sprintf("%dx%d@%d/%dk", r.Width, r.Height, r.FPS, r.VideoBitrateKbps))
	}
	return strings.Join(parts, ", ")
}
