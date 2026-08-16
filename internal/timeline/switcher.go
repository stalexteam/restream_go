// Package timeline формує ЄДИНИЙ канонічний FLV-таймлайн з активного джерела
// (live/backup) і роздає його всім виходам без розриву їхніх з'єднань.
// Порт controller/, семантика 1:1.
package timeline

import (
	"bytes"
	"math"
	"sort"
	"sync"
	"sync/atomic"

	"restream_go/internal/wire/flv"
)

const switchTSStepMS = 33

// Вікно накопичення байтів для бітрейту джерела і поріг «дані з OBS течуть».
const (
	bitrateWindowSec = 2.0
	dataPresentSec   = 1.0
)

// Правдоподібний інтервал keyframe-ів і глибина вибірки для медіани GOP.
const (
	gopMinSec     = 0.3
	gopMaxSec     = 10.0
	gopSampleMax  = 9
	noneSourceKey = "" // сентинел `source: str | None`
)

// SourceStats — метрики джерела для OBS-індикатора дашборда.
type SourceStats struct {
	Flowing   bool
	VideoKbps int
	AudioKbps int
}

type byteSample struct {
	at      float64
	tagType byte
	size    int
}

type headerTag struct {
	tagType byte
	payload []byte
}

// Switcher — канонічний таймлайн платформи.
type Switcher struct {
	clock Clock

	activeMu            sync.Mutex
	activeSource        string
	pendingSource       string
	pendingCallback     func(paramsChanged bool)
	pendingNotBefore    *float64
	pendingPriorHeaders map[string][]byte

	timelineMu   sync.Mutex
	sinks        []Sink
	outputSource string
	lastOutTS    int64
	offset       int64
	waitKeyframe bool
	// Доріжки драбини, що вже почались у цьому відрізку таймлайну (після
	// кожного перемикання джерела — заново).
	startedTracks map[byte]bool
	// Останній ВІДДАНИЙ out_ts кожної доріжки — НЕ скидається на перемиканні:
	// саме проти нього перевіряються перші теги нового джерела.
	trackOutTS  map[string]int64
	switchOutTS int64

	// В оригіналі мапа заголовків мутується без лока; тут — власний leaf-лок.
	headersMu  sync.Mutex
	seqHeaders map[string]map[string][]byte

	// Міряємо GOP relay ЗАВЖДИ (і поки relay не активний): по ньому Platform
	// фазує аутро. Публіковані значення — атомарні (лок-фрі читання, K3).
	gopMu           sync.Mutex
	gopSamples      []float64
	relayKeyframeAt atomic.Uint64
	relayGOPSec     atomic.Uint64

	statsMu         sync.Mutex
	byteSamples     []byteSample
	lastRelayDataAt atomic.Uint64
	hasRelayData    atomic.Bool
}

// NewSwitcher створює таймлайн; clk == nil — системний годинник.
func NewSwitcher(clk Clock) *Switcher {
	if clk == nil {
		clk = SystemClock()
	}
	return &Switcher{
		clock:         clk,
		startedTracks: map[byte]bool{},
		trackOutTS:    map[string]int64{},
		seqHeaders:    map[string]map[string][]byte{},
	}
}

// --- керування виходами ---

func (s *Switcher) RegisterSink(sink Sink) {
	s.timelineMu.Lock()
	defer s.timelineMu.Unlock()
	name := sink.Name()
	for i, existing := range s.sinks {
		if existing.Name() == name {
			s.sinks[i] = sink
			return
		}
	}
	s.sinks = append(s.sinks, sink)
}

func (s *Switcher) UnregisterSink(name string) {
	s.timelineMu.Lock()
	defer s.timelineMu.Unlock()
	for i, sink := range s.sinks {
		if sink.Name() == name {
			s.sinks = append(s.sinks[:i], s.sinks[i+1:]...)
			return
		}
	}
}

// CurrentHeaders — снімок seq-header джерела, що зараз іде у ВИХІД (для
// сідування нового sink при attach).
func (s *Switcher) CurrentHeaders() map[string][]byte {
	s.timelineMu.Lock()
	defer s.timelineMu.Unlock()
	s.headersMu.Lock()
	defer s.headersMu.Unlock()
	return copyHeaders(s.seqHeaders[s.outputSource])
}

// --- перемикання джерела ---

// SetActive — негайне перемикання; скасовує будь-який очікуваний RequestSwitch
// (разом із його колбеком). Порожній рядок = `set_active(None)`.
func (s *Switcher) SetActive(source string) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	s.activeSource = source
	s.pendingSource = noneSourceKey
	s.pendingCallback = nil
	s.pendingNotBefore = nil
	s.pendingPriorHeaders = nil
}

// RequestSwitch — відкладене перемикання: source стає активним лише на його
// першому ready-keyframe. notBefore (nil = немає) — keyframe-и РАНІШЕ цієї
// миті ігноруються, озброєння лишається чинним.
func (s *Switcher) RequestSwitch(source string, onSwitched func(paramsChanged bool), notBefore *float64) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	s.pendingSource = source
	s.pendingCallback = onSwitched
	s.pendingNotBefore = nil
	if notBefore != nil {
		v := *notBefore
		s.pendingNotBefore = &v
	}
	s.headersMu.Lock()
	s.pendingPriorHeaders = copyHeaders(s.seqHeaders[source])
	s.headersMu.Unlock()
}

func (s *Switcher) PendingSource() string {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	return s.pendingSource
}

// --- keyframe-и live: коли був останній і як часто вони йдуть ---

// RelayKeyframeAt — момент останнього keyframe від relay (0 — ще не було).
func (s *Switcher) RelayKeyframeAt() float64 {
	return math.Float64frombits(s.relayKeyframeAt.Load())
}

// RelayGOPSec — спостережений інтервал keyframe-ів live; ok=false — ще не міряно.
func (s *Switcher) RelayGOPSec() (float64, bool) {
	bits := s.relayGOPSec.Load()
	if bits == 0 {
		return 0, false
	}
	return math.Float64frombits(bits), true
}

// --- метрики джерела ---

func (s *Switcher) SourceStats() SourceStats {
	now := s.clock.Now()
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	cutoff := now - bitrateWindowSec
	drop := 0
	for drop < len(s.byteSamples) && s.byteSamples[drop].at < cutoff {
		drop++
	}
	s.byteSamples = s.byteSamples[drop:]
	var vbytes, abytes int
	for _, sample := range s.byteSamples {
		switch sample.tagType {
		case flv.TagVideo:
			vbytes += sample.size
		case flv.TagAudio:
			abytes += sample.size
		}
	}
	flowing := false
	if last, ok := s.lastRelayData(); ok {
		flowing = (now - last) < dataPresentSec
	}
	return SourceStats{Flowing: flowing, VideoKbps: kbps(vbytes), AudioKbps: kbps(abytes)}
}

// SecondsSinceRelayData — скільки вже мовчить живе джерело (дозволене
// надолуження для гравця заглушки).
func (s *Switcher) SecondsSinceRelayData() float64 {
	last, ok := s.lastRelayData()
	if !ok {
		return 0
	}
	return math.Max(0, s.clock.Now()-last)
}

func (s *Switcher) lastRelayData() (float64, bool) {
	if !s.hasRelayData.Load() {
		return 0, false
	}
	return math.Float64frombits(s.lastRelayDataAt.Load()), true
}

// kbps — round Python (half-to-even) над байтами вікна.
func kbps(size int) int {
	return int(math.RoundToEven(float64(size*8) / bitrateWindowSec / 1000))
}

func (s *Switcher) recordRelayBitrate(tagType byte, size int) {
	now := s.clock.Now()
	s.statsMu.Lock()
	s.byteSamples = append(s.byteSamples, byteSample{now, tagType, size})
	s.statsMu.Unlock()
	s.lastRelayDataAt.Store(math.Float64bits(now))
	s.hasRelayData.Store(true)
}

func (s *Switcher) recordRelayKeyframe() {
	now := s.clock.Now()
	s.gopMu.Lock()
	defer s.gopMu.Unlock()
	previous := math.Float64frombits(s.relayKeyframeAt.Load())
	s.relayKeyframeAt.Store(math.Float64bits(now))
	if previous == 0 {
		return
	}
	// Реконнект relay дає довільну паузу — вона не є GOP.
	interval := now - previous
	if interval < gopMinSec || interval > gopMaxSec {
		return
	}
	// МЕДІАНА кількох останніх, не останній інтервал: джиттер прибуття давав
	// оцінку 2.1с там, де OBS шле 2.0с.
	s.gopSamples = append(s.gopSamples, interval)
	if len(s.gopSamples) > gopSampleMax {
		s.gopSamples = s.gopSamples[1:]
	}
	ordered := append([]float64(nil), s.gopSamples...)
	sort.Float64s(ordered)
	s.relayGOPSec.Store(math.Float64bits(ordered[len(ordered)/2]))
}

func (s *Switcher) storeHeader(source, key string, payload []byte) {
	s.headersMu.Lock()
	defer s.headersMu.Unlock()
	hdrs := s.seqHeaders[source]
	if hdrs == nil {
		hdrs = map[string][]byte{}
		s.seqHeaders[source] = hdrs
	}
	hdrs[key] = payload
}

func (s *Switcher) sourceHeaders(source string) []headerTag {
	s.headersMu.Lock()
	defer s.headersMu.Unlock()
	hdrs := s.seqHeaders[source]
	out := make([]headerTag, 0, len(hdrs))
	for _, item := range flv.OrderedHeaderItems(hdrs) {
		out = append(out, headerTag{item.TagType, hdrs[item.Key]})
	}
	return out
}

// --- основний конвеєр ---

// Process — один тег від джерела: канонізація шкали і роздача по сінках.
func (s *Switcher) Process(source string, tagType byte, ts int64, payload []byte) {
	seqHeader := flv.IsSeqHeader(tagType, payload)
	isMeta := tagType == flv.TagScript
	if seqHeader {
		s.storeHeader(source, flv.HeaderKey(tagType, payload), payload)
	} else if isMeta {
		s.storeHeader(source, "meta", payload)
	}

	// Бітрейт/присутність — по relay (потік від OBS), незалежно від того,
	// активний він зараз чи pending. Track 1 хімери (0x95) не рахуємо: він
	// подвоїв би audio_kbps і автодетект бітрейту заглушки.
	if source == "relay" && !seqHeader && !isMeta && (len(payload) == 0 || payload[0] != 0x95) {
		s.recordRelayBitrate(tagType, len(payload))
	}

	// Анкер перемикання — лише КАНОНІЧНА доріжка (legacy, track 0): сходинки
	// драбини йдуть за нею, кожна на власному keyframe.
	isReadyKeyframe := !seqHeader && tagType == flv.TagVideo &&
		flv.IsVideoKeyframe(payload) && flv.VideoTrackID(payload) == 0

	if isReadyKeyframe && source == "relay" {
		s.recordRelayKeyframe()
	}

	var callback func(bool)
	paramsChanged := false
	s.activeMu.Lock()
	notBefore := s.pendingNotBefore
	if s.pendingSource != noneSourceKey && source == s.pendingSource && isReadyKeyframe &&
		(notBefore == nil || s.clock.Now() >= *notBefore) {
		s.headersMu.Lock()
		// Порівнюємо лише кодек-заголовки: "meta" (onMetaData) ffmpeg генерує
		// не byte-стабільно навіть для незмінного джерела.
		paramsChanged = len(s.pendingPriorHeaders) > 0 &&
			headersDiffer(s.pendingPriorHeaders, s.seqHeaders[source])
		s.headersMu.Unlock()
		if !paramsChanged {
			s.activeSource = s.pendingSource
		}
		s.pendingSource = noneSourceKey
		s.pendingNotBefore = nil
		s.pendingPriorHeaders = nil
		callback = s.pendingCallback
		s.pendingCallback = nil
	}
	active := s.activeSource
	s.activeMu.Unlock()

	if callback != nil {
		callback(paramsChanged)
	}

	if source != active {
		return
	}

	s.timelineMu.Lock()
	defer s.timelineMu.Unlock()

	if s.outputSource != source {
		s.outputSource = source
		s.lastOutTS += switchTSStepMS
		s.switchOutTS = s.lastOutTS
		s.waitKeyframe = true
	}

	if s.waitKeyframe {
		if seqHeader || isMeta || !isReadyKeyframe {
			return
		}
		s.waitKeyframe = false
		s.startedTracks = map[byte]bool{0: true}
		s.offset = s.lastOutTS - ts + switchTSStepMS
		// Заголовки штовхаємо ЩОЙНО тут, впритул перед keyframe: раніше вихід
		// оголосив би нову конфігурацію без жодних даних за нею.
		for _, h := range s.sourceHeaders(source) {
			s.emit(h.tagType, s.switchOutTS, h.payload)
		}
	}

	if !seqHeader && tagType == flv.TagVideo {
		// Кожна сходинка драбини входить у нову ділянку на ВЛАСНОМУ keyframe —
		// інакше приймач дістав би биту доріжку при здоровому треку 0.
		trackID := flv.VideoTrackID(payload)
		if !s.startedTracks[trackID] {
			if !flv.IsVideoKeyframe(payload) {
				return
			}
			s.startedTracks[trackID] = true
		}
	}

	outTS := ts + s.offset
	if outTS < 0 {
		outTS = 0
	}
	if !seqHeader && !isMeta {
		// Offset заякорено на КАДРІ, а аудіо нового джерела в цю мить відстає
		// від нього на свій буфер (виміряно −154мс на fallback->live): цей
		// відрізок часу вже відданий попереднім джерелом, тож дропаємо самі.
		// По ДОРІЖЦІ: спільний лічильник підтягував би аудіо до відеочасу.
		key := flv.HeaderKey(tagType, payload)
		if last, ok := s.trackOutTS[key]; ok && outTS < last {
			return
		}
		s.trackOutTS[key] = outTS
	}
	if outTS > s.lastOutTS {
		s.lastOutTS = outTS
	}
	s.emit(tagType, outTS, payload)
}

// emit викликається під timelineMu: Offer неблокуючий, тримати лок безпечно.
func (s *Switcher) emit(tagType byte, ts int64, payload []byte) {
	for _, sink := range s.sinks {
		sink.Offer(tagType, ts, payload)
	}
}

// headersDiffer — ключі prior ∪ current без "meta"; відсутній ключ не дорівнює
// присутньому (py `dict.get` → None).
func headersDiffer(prior, current map[string][]byte) bool {
	for key := range prior {
		if key != "meta" && !sameHeader(prior, current, key) {
			return true
		}
	}
	for key := range current {
		if key != "meta" && !sameHeader(prior, current, key) {
			return true
		}
	}
	return false
}

func sameHeader(a, b map[string][]byte, key string) bool {
	av, aok := a[key]
	bv, bok := b[key]
	return aok == bok && bytes.Equal(av, bv)
}
