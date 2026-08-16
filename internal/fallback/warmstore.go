package fallback

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"restream_go/internal/probe"
)

// LiveParams — параметри живого потоку, під які нормалізується заглушка.
type LiveParams = probe.StreamParams

// WarmEntry — ціль, під яку платформа готувала заглушку минулого разу.
type WarmEntry struct {
	Live   LiveParams
	Target TargetParams
}

// WarmStore — похідні дані (не налаштування) окремим файлом у tmp/: прогрів
// кеша на старті контролера і стабільний бітрейт між сесіями.
type WarmStore struct {
	path  string
	mu    sync.Mutex
	order []string
	data  map[string]WarmEntry
}

// NewWarmStore — читає path; битий/чужий формат = порожній стор.
func NewWarmStore(path string) *WarmStore {
	w := &WarmStore{path: path, data: map[string]WarmEntry{}}
	w.load()
	return w
}

func (w *WarmStore) load() {
	raw, err := os.ReadFile(w.path)
	if err != nil {
		return
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return
	}
	// Порядок вставки (як у python-dict), щоб файл не «дихав» між записами.
	var names []string
	if err := json.Unmarshal(raw, &orderedNames{&names}); err != nil {
		return
	}
	for _, name := range names {
		entry, ok := decodeWarmEntry(doc[name])
		if !ok {
			continue
		}
		w.order = append(w.order, name)
		w.data[name] = entry
	}
}

// Get — ціль платформи; false, якщо запису немає або він неповний.
func (w *WarmStore) Get(name string) (WarmEntry, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	entry, ok := w.data[name]
	return entry, ok
}

// Put — запамʼятати ціль платформи (без запису, якщо вона не змінилась).
func (w *WarmStore) Put(name string, live LiveParams, target TargetParams) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if old, ok := w.data[name]; ok && old.Live == live && old.Target.equal(target) {
		return
	}
	if _, ok := w.data[name]; !ok {
		w.order = append(w.order, name)
	}
	w.data[name] = WarmEntry{Live: live, Target: target}
	w.saveLocked()
}

// Retain — викинути записи платформ, яких уже немає (rename/видалення).
func (w *WarmStore) Retain(names []string) {
	keep := make(map[string]bool, len(names))
	for _, name := range names {
		keep[name] = true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	kept := w.order[:0]
	stale := false
	for _, name := range w.order {
		if keep[name] {
			kept = append(kept, name)
			continue
		}
		stale = true
		delete(w.data, name)
	}
	if !stale {
		return
	}
	w.order = kept
	w.saveLocked()
}

func (w *WarmStore) saveLocked() {
	doc := make(jsonObject, 0, len(w.order))
	for _, name := range w.order {
		entry := w.data[name]
		doc = append(doc, jsonPair{name, jsonObject{
			{"live", liveJSON(entry.Live)},
			{"target", entry.Target.json(false)},
		}})
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, []byte(pyDumps(doc)), "", "  "); err != nil {
		log.Printf("could not persist the backup warm-up state: %v", err)
		return
	}
	tmp := strings.TrimSuffix(w.path, filepath.Ext(w.path)) + ".tmp"
	if err := os.WriteFile(tmp, indented.Bytes(), 0o644); err != nil {
		log.Printf("could not persist the backup warm-up state: %v", err)
		return
	}
	if err := os.Rename(tmp, w.path); err != nil {
		log.Printf("could not persist the backup warm-up state: %v", err)
	}
}

func liveJSON(live LiveParams) jsonObject {
	return jsonObject{
		{"video_codec", live.VideoCodec}, {"width", live.Width}, {"height", live.Height},
		{"fps", live.FPS}, {"audio_codec", live.AudioCodec}, {"channels", live.Channels},
		{"sample_rate", live.SampleRate},
	}
}

type liveDTO struct {
	VideoCodec string `json:"video_codec"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	FPS        int    `json:"fps"`
	AudioCodec string `json:"audio_codec"`
	Channels   int    `json:"channels"`
	SampleRate int    `json:"sample_rate"`
}

type rungDTO struct {
	Width            int `json:"width"`
	Height           int `json:"height"`
	FPS              int `json:"fps"`
	VideoBitrateKbps int `json:"video_bitrate_kbps"`
}

type targetDTO struct {
	Ladder           []rungDTO `json:"ladder"`
	Width            int       `json:"width"`
	Height           int       `json:"height"`
	FPS              int       `json:"fps"`
	Channels         int       `json:"channels"`
	SampleRate       int       `json:"sample_rate"`
	VideoBitrateKbps int       `json:"video_bitrate_kbps"`
	AudioBitrateKbps int       `json:"audio_bitrate_kbps"`
}

func decodeWarmEntry(raw json.RawMessage) (WarmEntry, bool) {
	var dto struct {
		Live   *liveDTO   `json:"live"`
		Target *targetDTO `json:"target"`
	}
	if err := json.Unmarshal(raw, &dto); err != nil || dto.Live == nil || dto.Target == nil {
		return WarmEntry{}, false
	}
	return WarmEntry{
		Live: LiveParams{
			VideoCodec: dto.Live.VideoCodec, Width: dto.Live.Width, Height: dto.Live.Height,
			FPS: dto.Live.FPS, AudioCodec: dto.Live.AudioCodec, Channels: dto.Live.Channels,
			SampleRate: dto.Live.SampleRate,
		},
		Target: dto.Target.params(),
	}, true
}

func (d targetDTO) params() TargetParams {
	target := TargetParams{
		Width: d.Width, Height: d.Height, FPS: d.FPS,
		Channels: d.Channels, SampleRate: d.SampleRate,
		VideoBitrateKbps: d.VideoBitrateKbps, AudioBitrateKbps: d.AudioBitrateKbps,
	}
	for _, rung := range d.Ladder {
		target.Ladder = append(target.Ladder, Rung{rung.Width, rung.Height, rung.FPS, rung.VideoBitrateKbps})
	}
	return target
}

// orderedNames — імена платформ у порядку появи у файлі (json.Unmarshal у map
// його губить).
type orderedNames struct{ into *[]string }

func (o *orderedNames) UnmarshalJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if _, err := decoder.Token(); err != nil { // '{'
		return err
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok {
			return nil
		}
		*o.into = append(*o.into, name)
		var skip json.RawMessage
		if err := decoder.Decode(&skip); err != nil {
			return err
		}
	}
	return nil
}
