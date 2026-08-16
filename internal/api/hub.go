package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"restream_go/internal/control"
	"restream_go/internal/proc"
)

// TickSec — період фонового освіження знімку.
const TickSec = time.Second

// Source — те, що hub читає з менеджера (`manager.status`/`fallback_progress`).
type Source interface {
	Status() *control.Dict
	FallbackProgress() control.FallbackProgress
}

// ManagerSource робить типізовані знімки Manager-а впорядкованими словниками:
// hub мутує їх на місці.
type ManagerSource struct{ M *control.Manager }

// Status — знімок Manager-а у вигляді Dict (порядок ключів зберігається).
func (s ManagerSource) Status() *control.Dict {
	raw, err := json.Marshal(s.M.Status())
	if err != nil {
		log.Printf("api: could not encode status: %v", err)
		return control.NewDict()
	}
	d, err := control.Loads(raw)
	if err != nil {
		log.Printf("api: could not decode status: %v", err)
		return control.NewDict()
	}
	return d
}

// FallbackProgress — зведений прогрес підготовки заглушок.
func (s ManagerSource) FallbackProgress() control.FallbackProgress {
	return s.M.FallbackProgress()
}

// snapshot — знімок плюс побайтові значення верхніх ключів для дельти.
type snapshot struct {
	dict *control.Dict
	raw  map[string][]byte
}

func newSnapshot(d *control.Dict) *snapshot {
	s := &snapshot{dict: d, raw: make(map[string][]byte, d.Len())}
	for _, k := range d.Keys() {
		v, _ := d.Get(k)
		s.raw[k] = pyMarshal(v)
	}
	return s
}

// wireMessage — {"type":..., "data":...} саме в цьому порядку.
type wireMessage struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

type eventMessage struct {
	Type  string `json:"type"`
	Level string `json:"level"`
	Text  string `json:"text"`
}

type controlMessage struct {
	Type   string `json:"type"`
	Action string `json:"action"`
}

// Hub — серверна сторона push-каналу `/ws`.
type Hub struct {
	src     Source
	baseDir string
	sampler *proc.Sampler

	mu          sync.Mutex
	conns       []*wsConn
	sourceConns map[*wsConn]bool
	last        *snapshot

	// sourceCount дублює len(sourceConns) для читання поза локом.
	sourceCount atomic.Int64

	// Шви golden-сверки; прод бере дефолти з NewHub.
	sample func(pid int) (proc.Stats, bool)
	alive  func(pid int) bool
	ownPID func() int
	mtxPID func() *int

	event chan struct{}
	stop  chan struct{}
	done  chan struct{}
}

// NewHub піднімає фонову петлю push-у.
func NewHub(src Source, baseDir string) *Hub {
	h := newHub(src, baseDir)
	go h.run()
	return h
}

func newHub(src Source, baseDir string) *Hub {
	h := &Hub{
		src:         src,
		baseDir:     baseDir,
		sampler:     proc.NewSampler(),
		sourceConns: map[*wsConn]bool{},
		alive:       pidAlive,
		ownPID:      os.Getpid,
		event:       make(chan struct{}, 1),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
	h.sample = func(pid int) (proc.Stats, bool) { return h.sampler.Sample(pid, true) }
	h.mtxPID = h.mediamtxPID
	return h
}

// Notify — розбудити петлю негайно (`Manager.on_change`).
func (h *Hub) Notify() {
	select {
	case h.event <- struct{}{}:
	default:
	}
}

// Close зупиняє фонову петлю.
func (h *Hub) Close() {
	select {
	case <-h.stop:
	default:
		close(h.stop)
	}
	<-h.done
}

// PushEvent — toast клієнтам (разова подія, не частина знімку).
func (h *Hub) PushEvent(level, text string) {
	h.broadcastRaw(pyMarshal(eventMessage{Type: "event", Level: level, Text: text}))
}

// PushControl — команда всім клієнтам; єдиний споживач — obs-source.html.
func (h *Hub) PushControl(action string) {
	h.mu.Lock()
	count := len(h.conns)
	h.mu.Unlock()
	log.Printf("dashboard: pushing control action=%s to %d connected /ws client(s)", action, count)
	h.broadcastRaw(pyMarshal(controlMessage{Type: "control", Action: action}))
}

// PushMessage — готове повідомлення всім клієнтам (добрані тривалості файлів).
func (h *Hub) PushMessage(message any) {
	h.broadcastRaw(pyMarshal(message))
}

func (h *Hub) broadcastRaw(text []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sendAllLocked(text)
}

// sendAllLocked шле всім і прибирає мертві з'єднання.
func (h *Hub) sendAllLocked(text []byte) {
	live := h.conns[:0]
	for _, c := range h.conns {
		if h.sendRaw(c, text) {
			live = append(live, c)
			continue
		}
		h.dropSourceLocked(c)
	}
	for i := len(live); i < len(h.conns); i++ {
		h.conns[i] = nil
	}
	h.conns = live
}

func (h *Hub) sendRaw(c *wsConn, text []byte) bool {
	if err := c.sendText(text); err != nil {
		log.Printf("dashboard: dropping unresponsive /ws connection")
		return false
	}
	return true
}

// Register — новий клієнт: повний знімок будується ПОЗА локом хаба.
func (h *Hub) Register(c *wsConn) {
	snap := h.buildSnapshot()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.last = snap
	h.conns = append(h.conns, c)
	// Мертвий сокет тут не знімається — його прибере перша дельта.
	h.sendRaw(c, pyMarshal(wireMessage{Type: "full", Data: snap.dict}))
}

// MarkSource — з'єднання представилось як obs-source.html (індикатор Source).
func (h *Hub) MarkSource(c *wsConn) {
	h.mu.Lock()
	h.sourceConns[c] = true
	h.sourceCount.Store(int64(len(h.sourceConns)))
	h.mu.Unlock()
	h.Notify()
}

// Unregister — ідемпотентне зняття з'єднання.
func (h *Hub) Unregister(c *wsConn) {
	h.mu.Lock()
	for i, existing := range h.conns {
		if existing == c {
			h.conns = append(h.conns[:i], h.conns[i+1:]...)
			break
		}
	}
	wasSource := h.sourceConns[c]
	h.dropSourceLocked(c)
	h.mu.Unlock()
	if wasSource {
		h.Notify()
	}
}

func (h *Hub) dropSourceLocked(c *wsConn) {
	if _, ok := h.sourceConns[c]; ok {
		delete(h.sourceConns, c)
		h.sourceCount.Store(int64(len(h.sourceConns)))
	}
}

func (h *Hub) run() {
	defer close(h.done)
	for {
		timer := time.NewTimer(TickSec)
		select {
		case <-h.event:
		case <-timer.C:
		case <-h.stop:
			timer.Stop()
			return
		}
		timer.Stop()
		h.broadcast()
	}
}

func (h *Hub) broadcast() {
	snap := h.buildSnapshot()
	h.mu.Lock()
	defer h.mu.Unlock()
	delta := diffSnapshots(h.last, snap)
	h.last = snap
	if delta.Len() == 0 || len(h.conns) == 0 {
		return
	}
	h.sendAllLocked(pyMarshal(wireMessage{Type: "delta", Data: delta}))
}

// diffSnapshots — верхні ключі, значення яких змінились.
func diffSnapshots(previous, current *snapshot) *control.Dict {
	delta := control.NewDict()
	for _, k := range current.dict.Keys() {
		if previous != nil {
			prev, ok := previous.raw[k]
			if !ok {
				prev = []byte("null")
			}
			if bytes.Equal(prev, current.raw[k]) {
				continue
			}
		}
		v, _ := current.dict.Get(k)
		delta.Set(k, v)
	}
	return delta
}

func (h *Hub) buildSnapshot() *snapshot {
	status := h.src.Status()
	components := control.NewDict()
	components.Set("mediamtx", h.component(h.mtxPID()))
	pid := h.ownPID()
	components.Set("controller", h.component(&pid))
	components.Set("fallback-preparer", h.fallbackComponent())

	for _, item := range dictList(status, "platforms") {
		name := "?"
		if v, ok := item.Get("name"); ok {
			name = pyText(v)
		}
		if v, ok := item.Get("relay_pid"); ok {
			item.Pop("relay_pid")
			components.Set("relay:"+name, h.component(pidOf(v)))
		}
		if v, ok := item.Get("backup_pid"); ok {
			item.Pop("backup_pid")
			components.Set("backup:"+name, h.component(pidOf(v)))
		}
		out, ok := item.GetOr("output", nil).(*control.Dict)
		if !ok {
			// Без цього cpu/rss писались би в тимчасовий dict і гинули.
			out = control.NewDict()
			item.Set("output", out)
		}
		outPID := pidOf(out.GetOr("pid", nil))
		var stats *proc.Stats
		if outPID != nil && h.alive(*outPID) {
			if s, ok := h.sample(*outPID); ok {
				stats = &s
			}
		}
		out.Set("cpu_percent", optCPU(stats))
		out.Set("rss_mb", optRSS(stats))
	}
	status.Set("components", components)
	status.Set("obs_source_connected", h.sourceCount.Load() > 0)
	return newSnapshot(status)
}

// component — running/pid/cpu/rss одного процесу.
func (h *Hub) component(pid *int) *control.Dict {
	running := pid != nil && h.alive(*pid)
	var stats *proc.Stats
	if running {
		if s, ok := h.sample(*pid); ok {
			stats = &s
		}
	}
	out := control.NewDict()
	out.Set("running", running)
	if running {
		out.Set("pid", int64(*pid))
	} else {
		out.Set("pid", nil)
	}
	out.Set("cpu_percent", optCPU(stats))
	out.Set("rss_mb", optRSS(stats))
	return out
}

// fallbackComponent — колонка Status для підготовки заглушок: частка готових
// байтів плюс живі ffmpeg-и транскоду.
func (h *Hub) fallbackComponent() *control.Dict {
	progress := h.src.FallbackProgress()
	total, ready := progress.TotalBytes, progress.ReadyBytes
	transcodes := progress.Transcodes

	state := ""
	switch {
	case progress.TotalFiles == 0:
		state = "–"
	case !progress.Started:
		state = "idle"
	case ready >= total:
		state = "ready"
	default:
		state = fmt.Sprintf("%d%%", int64(float64(ready*100)/float64(total)))
	}

	detail := fmt.Sprintf("%d/%d file(s) ready (%d of %d MB across %d platform(s))",
		progress.ReadyFiles, progress.TotalFiles, mb(ready), mb(total), progress.Platforms)
	if progress.FailedFiles != 0 {
		detail += fmt.Sprintf("\n%d file(s) could not be prepared -- see the log", progress.FailedFiles)
	}
	if len(progress.Converting) > 0 {
		detail += "\nconverting: " + strings.Join(progress.Converting, ", ")
	} else if !progress.Started && progress.TotalFiles != 0 {
		detail += "\nnothing prepared yet -- targets are known once a broadcast starts"
	}

	cpu, rss, alive := 0.0, 0.0, false
	names := make([]string, 0, len(transcodes))
	for _, entry := range transcodes {
		pid, name := transcodeEntry(entry)
		names = append(names, fmt.Sprintf("%s (pid %d)", name, pid))
		if s, ok := h.sample(pid); ok {
			cpu += s.CPUPercent
			rss += s.RSSMB
			alive = true
		}
	}
	if len(transcodes) > 1 {
		detail += "\nffmpeg: " + strings.Join(names, ", ")
	}

	out := control.NewDict()
	out.Set("running", len(transcodes) > 0)
	if len(transcodes) == 1 {
		pid, _ := transcodeEntry(transcodes[0])
		out.Set("pid", int64(pid))
	} else {
		out.Set("pid", nil)
	}
	if alive {
		out.Set("cpu_percent", pyRound1(cpu))
		out.Set("rss_mb", pyRound1(rss))
	} else {
		out.Set("cpu_percent", nil)
		out.Set("rss_mb", nil)
	}
	out.Set("status", state)
	out.Set("detail", detail)
	return out
}

func (h *Hub) mediamtxPID() *int {
	raw, err := os.ReadFile(filepath.Join(h.baseDir, "tmp", ".mediamtx.pid"))
	if err != nil {
		return nil
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil
	}
	return &pid
}

func transcodeEntry(entry []any) (pid int, name string) {
	if len(entry) > 0 {
		if p := pidOf(entry[0]); p != nil {
			pid = *p
		}
	}
	if len(entry) > 1 {
		name = pyText(entry[1])
	}
	return pid, name
}

func optCPU(s *proc.Stats) any {
	if s == nil {
		return nil
	}
	return s.CPUPercent
}

func optRSS(s *proc.Stats) any {
	if s == nil {
		return nil
	}
	return s.RSSMB
}

// mb — round(bytes / 1MiB) з банківським округленням python.
func mb(sizeBytes int64) int64 {
	return int64(math.RoundToEven(float64(sizeBytes) / (1024 * 1024)))
}

// pyRound1 — round(x, 1) python-семантики (десятковий half-to-even, ST3).
func pyRound1(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	scaled := new(big.Rat).Mul(new(big.Rat).SetFloat64(x), big.NewRat(10, 1))
	rounded := new(big.Int)
	rem := new(big.Int)
	rounded.QuoRem(scaled.Num(), scaled.Denom(), rem)
	rem.Abs(rem)
	twice := new(big.Int).Lsh(rem, 1)
	switch cmp := twice.Cmp(scaled.Denom()); {
	case cmp > 0 || (cmp == 0 && rounded.Bit(0) == 1):
		if scaled.Sign() < 0 {
			rounded.Sub(rounded, big.NewInt(1))
		} else {
			rounded.Add(rounded, big.NewInt(1))
		}
	}
	value, _ := new(big.Rat).SetFrac(rounded, big.NewInt(10)).Float64()
	return value
}

// dictList — список вкладених словників під ключем (пропускає чужі типи).
func dictList(d *control.Dict, key string) []*control.Dict {
	items, ok := d.GetOr(key, nil).([]any)
	if !ok {
		return nil
	}
	out := make([]*control.Dict, 0, len(items))
	for _, item := range items {
		if nested, ok := item.(*control.Dict); ok {
			out = append(out, nested)
		}
	}
	return out
}

func pidOf(v any) *int {
	switch n := v.(type) {
	case int64:
		pid := int(n)
		return &pid
	case float64:
		pid := int(n)
		return &pid
	}
	return nil
}

func pyText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
