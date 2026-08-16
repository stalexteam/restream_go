package fallback

import (
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"restream_go/internal/probe"
)

// Одиночні ролі (по одному файлу). folder-тип додає колекцію FolderFiles +
// Separator; sequence-тип — Loop. Start/End спільні для обох типів.
var singleRoles = []string{"loop", "start", "end", "separator"}

// KindSequence / KindFolder — тип fallback-пресета.
const (
	KindSequence = "sequence"
	KindFolder   = "folder"
)

// LadderRung — сходинка ВИДАНОЇ драбини EB (порядок = trackId); 0 у FPS/
// BitrateKbps = «не оголошено», беремо з live/виміру.
type LadderRung struct {
	Width       int
	Height      int
	FPS         int
	BitrateKbps int
}

// BitrateProvider — виміряний бітрейт живого потоку (switcher.SourceStats).
type BitrateProvider func() (videoKbps, audioKbps int)

// Progress — прогрес підготовки для компонента "fallback-preparer" дашборда.
type Progress struct {
	TotalBytes  int64  `json:"total_bytes"`
	ReadyBytes  int64  `json:"ready_bytes"`
	TotalFiles  int    `json:"total_files"`
	ReadyFiles  int    `json:"ready_files"`
	FailedFiles int    `json:"failed_files"`
	Current     string `json:"current"`
	Started     bool   `json:"started"`
}

// PreparerOptions — сегменти пресета ("" = ролі немає), спільний кеш і те, що
// приходить від платформи: вимір бітрейту, override-и конфіга, ціль минулої сесії.
type PreparerOptions struct {
	Kind        string
	Loop        string
	Start       string
	End         string
	Separator   string
	FolderFiles []string

	Cache      *Cache
	LadderMode bool
	Bitrate    BitrateProvider

	VideoBitrateOverrideKbps int
	AudioBitrateOverrideKbps int

	OnTargetParams func(live LiveParams, target TargetParams)
	PreviousTarget TargetParams

	Clock   Clock
	Sleeper Sleeper
}

// Preparer — per-platform підготовка сегментів заглушки під параметри живого
// потоку: probe live, ціль, делегування в кеш.
type Preparer struct {
	kind        string
	ladderMode  bool
	cache       *Cache
	bitrate     BitrateProvider
	cfgVideo    int
	cfgAudio    int
	onTarget    func(live LiveParams, target TargetParams)
	prevTarget  TargetParams
	clock       Clock
	sleeper     Sleeper
	single      map[string]string
	folderFiles []string

	// normalize — шов для тестів.
	normalize func(src string, live LiveParams, target TargetParams) string

	mu             sync.Mutex
	ladder         []LadderRung
	builtTarget    TargetParams
	hasBuiltTarget bool
	artifacts      map[string]string
	durations      map[string]float64
	folderReady    []string
	lastLive       LiveParams
	hasLastLive    bool
	doneSources    map[string]bool
	failedSources  map[string]bool
	current        string
	planned        bool
	sizes          map[string]int64
}

// NewPreparer — препарер одного плеча платформи.
func NewPreparer(opts PreparerOptions) *Preparer {
	kind := opts.Kind
	if kind == "" {
		kind = KindSequence
	}
	p := &Preparer{
		kind:        kind,
		ladderMode:  opts.LadderMode,
		cache:       opts.Cache,
		bitrate:     opts.Bitrate,
		cfgVideo:    opts.VideoBitrateOverrideKbps,
		cfgAudio:    opts.AudioBitrateOverrideKbps,
		onTarget:    opts.OnTargetParams,
		prevTarget:  opts.PreviousTarget,
		clock:       opts.Clock,
		sleeper:     opts.Sleeper,
		folderFiles: append([]string(nil), opts.FolderFiles...),
		single: map[string]string{
			"loop": opts.Loop, "start": opts.Start, "end": opts.End, "separator": opts.Separator,
		},
		artifacts:     map[string]string{},
		durations:     map[string]float64{},
		doneSources:   map[string]bool{},
		failedSources: map[string]bool{},
		sizes:         map[string]int64{},
	}
	if p.clock == nil {
		p.clock = systemClock{base: time.Now()}
	}
	if p.sleeper == nil {
		p.sleeper = systemSleeper{}
	}
	p.normalize = p.normalizeSegment
	return p
}

// IsFolder — folder-пресет (колекція + separator) чи sequence.
func (p *Preparer) IsFolder() bool { return p.kind == KindFolder }

// SetLadder — сходинки, під які нормалізуємо заглушку EB-плеча.
func (p *Preparer) SetLadder(rungs []LadderRung) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ladder = append([]LadderRung(nil), rungs...)
}

// HasLadder — чи драбина вже відома.
func (p *Preparer) HasLadder() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.ladder) > 0
}

// SegmentSource — готовий артефакт ролі; для "loop" у sequence-режимі — сирий
// файл, поки не готово. Драбині сирий файл не годиться ніколи.
func (p *Preparer) SegmentSource(role string) string {
	p.mu.Lock()
	artifact := p.artifacts[role]
	p.mu.Unlock()
	if artifact != "" {
		return artifact
	}
	if role == "loop" && !p.ladderMode {
		return p.single["loop"]
	}
	return ""
}

// SegmentDuration — тривалість готового сегмента (кешується); потрібна, щоб
// фазувати старт End по keyframe-ах live.
func (p *Preparer) SegmentDuration(role string) (float64, bool) {
	p.mu.Lock()
	artifact := p.artifacts[role]
	if artifact == "" {
		p.mu.Unlock()
		return 0, false
	}
	if cached, ok := p.durations[artifact]; ok {
		p.mu.Unlock()
		return cached, true
	}
	p.mu.Unlock()

	duration, ok := probe.ProbeDurationSec(artifact)
	if !ok {
		return 0, false
	}
	p.mu.Lock()
	p.durations[artifact] = duration
	p.mu.Unlock()
	return duration, true
}

// PlannedSources — вихідні файли, які цей пресет МОЖЕ задіяти.
func (p *Preparer) PlannedSources() []string {
	roles := []string{"loop", "start", "end"}
	if p.kind == KindFolder {
		roles = []string{"start", "end", "separator"}
	}
	var sources []string
	for _, role := range roles {
		if src := p.single[role]; src != "" {
			sources = append(sources, src)
		}
	}
	if p.kind == KindFolder {
		sources = append(sources, p.folderFiles...)
	}
	return sources
}

// Progress — скільки підготовки зроблено, у БАЙТАХ вихідних файлів (розмір
// пропорційніший часу транскоду, ніж лічильник файлів).
func (p *Preparer) Progress() Progress {
	planned := p.PlannedSources()
	p.mu.Lock()
	done := make(map[string]bool, len(p.doneSources))
	for key := range p.doneSources {
		done[key] = true
	}
	failed := make(map[string]bool, len(p.failedSources))
	for key := range p.failedSources {
		failed[key] = true
	}
	progress := Progress{TotalFiles: len(planned), Current: p.current, Started: p.planned}
	p.mu.Unlock()

	for _, src := range planned {
		size := p.fileSize(src)
		progress.TotalBytes += size
		if done[src] {
			progress.ReadyBytes += size
			progress.ReadyFiles++
		}
		if failed[src] {
			progress.FailedFiles++
		}
	}
	return progress
}

func (p *Preparer) fileSize(path string) int64 {
	p.mu.Lock()
	size, ok := p.sizes[path]
	p.mu.Unlock()
	if ok {
		return size
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0 // нуль не кешуємо: файл ще може з'явитись (Q42)
	}
	size = info.Size()
	p.mu.Lock()
	p.sizes[path] = size
	p.mu.Unlock()
	return size
}

// HasReadySegment — чи є ХОЧ ЩОСЬ, що гравець може зараз програти; драбині
// самих лише start/end/separator без тіла не досить.
func (p *Preparer) HasReadySegment() bool {
	roles := singleRoles
	if p.ladderMode {
		roles = []string{"loop"}
	}
	p.mu.Lock()
	for _, role := range roles {
		if p.artifacts[role] != "" {
			p.mu.Unlock()
			return true
		}
	}
	ready := len(p.folderReady) > 0
	p.mu.Unlock()
	if ready {
		return true
	}
	return p.SegmentSource("loop") != ""
}

// FolderReadyFiles — готові (нормалізовані) файли папки для плейлиста гравця.
func (p *Preparer) FolderReadyFiles() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.folderReady...)
}

// LastLiveParams — останні визначені параметри ЖИВОГО потоку (дашборд);
// false — успішного probe ще не було.
func (p *Preparer) LastLiveParams() (LiveParams, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastLive, p.hasLastLive
}

// PrepareAsync — probe живого потоку і підготовка у фоні; ефір це не зупиняє.
func (p *Preparer) PrepareAsync(liveProbeURL string, audioIndex, videoIndex int) {
	go func() {
		live, ok := probe.ProbeStreamParams(liveProbeURL, audioIndex, videoIndex)
		if !ok {
			log.Printf("could not determine live stream parameters -- skipping backup " +
				"check/preparation for this stream start")
			return
		}
		p.buildForLive(live)
	}()
}

// PrepareFromParams — підготовка під ВЖЕ ВІДОМІ параметри (зміна пресета на
// льоту; у FALLBACK джерело для probe недоступне).
func (p *Preparer) PrepareFromParams(live LiveParams) {
	go p.buildForLive(live)
}

// PrepareWarm — прогрів кеша під ЗБЕРЕЖЕНУ ціль минулої сесії, синхронно в
// потоці викликача; abort зупиняє чергу (перша публікація). LastLiveParams не
// чіпаємо: там геометрія ЖИВОГО потоку, а тут лише передбачувана.
func (p *Preparer) PrepareWarm(live LiveParams, target TargetParams, abort <-chan struct{}) {
	// Дедбенд F9 має діяти й між прогрівом і стартом у одному процесі.
	p.mu.Lock()
	p.prevTarget = target
	p.mu.Unlock()
	p.buildAll(live, target, abort)
}

func (p *Preparer) buildForLive(live LiveParams) {
	p.mu.Lock()
	p.lastLive, p.hasLastLive = live, true
	p.mu.Unlock()

	target, ok := p.targetParams(live)
	if !ok {
		p.mu.Lock()
		p.planned = true
		p.mu.Unlock()
		return
	}
	if p.onTarget != nil {
		p.onTarget(live, target)
	}
	p.buildAll(live, target, nil)
}

// targetParams — ціль нормалізації: геометрія з live (або з ВИДАНОЇ драбини) +
// бітрейт цієї платформи. Бітрейт міряємо ОДИН раз на всі сегменти, інакше
// стик дав би розбіжні seq-header.
func (p *Preparer) targetParams(live LiveParams) (TargetParams, bool) {
	vbitrate, abitrate := p.detectTargetBitrates()
	if !p.ladderMode {
		return TargetParams{
			Width: live.Width, Height: live.Height, FPS: live.FPS,
			Channels: live.Channels, SampleRate: live.SampleRate,
			VideoBitrateKbps: vbitrate, AudioBitrateKbps: abitrate,
		}, true
	}
	p.mu.Lock()
	rungs := append([]LadderRung(nil), p.ladder...)
	p.mu.Unlock()
	if len(rungs) == 0 {
		log.Printf("no ladder known yet -- skipping the backup preparation for this stream start")
		return TargetParams{}, false
	}
	target := TargetParams{
		Channels: live.Channels, SampleRate: live.SampleRate, AudioBitrateKbps: abitrate,
	}
	for _, rung := range rungs {
		fps := rung.FPS
		if fps == 0 {
			fps = live.FPS
		}
		kbps := rung.BitrateKbps
		if kbps == 0 {
			kbps = vbitrate
		}
		target.Ladder = append(target.Ladder, Rung{
			Width: rung.Width, Height: rung.Height, FPS: fps, VideoBitrateKbps: kbps})
	}
	return target, true
}

func (p *Preparer) buildAll(live LiveParams, target TargetParams, abort <-chan struct{}) {
	p.mu.Lock()
	p.planned = true
	if p.hasBuiltTarget && !p.builtTarget.equal(target) {
		p.discardArtifactsLocked()
	}
	p.builtTarget, p.hasBuiltTarget = target, true
	p.mu.Unlock()

	if p.kind == KindFolder {
		// Пріоритет: перший файл папки (потрібен у мить входу в FALLBACK), далі
		// Start/End/Separator, тоді решта файлів папки.
		files := p.folderFiles
		if len(files) > 0 && !aborted(abort) {
			p.buildFolderFile(files[0], live, target)
		}
		for _, role := range []string{"start", "end", "separator"} {
			if src := p.single[role]; src != "" && !aborted(abort) {
				p.buildSegment(role, src, live, target)
			}
		}
		for i := 1; i < len(files); i++ {
			if aborted(abort) {
				return
			}
			p.buildFolderFile(files[i], live, target)
		}
		return
	}
	// sequence: Loop першим (потрібен у мить входу в FALLBACK), далі Start/End.
	for _, role := range []string{"loop", "start", "end"} {
		src := p.single[role]
		if src == "" || aborted(abort) {
			continue
		}
		p.buildSegment(role, src, live, target)
	}
}

// discardArtifactsLocked — ціль змінилась: артефакти минулої більше не
// віддаємо (у FALLBACK вони пішли б у канонічний потік із чужою геометрією).
func (p *Preparer) discardArtifactsLocked() {
	for _, role := range singleRoles {
		p.artifacts[role] = ""
	}
	p.folderReady = nil
	p.doneSources = map[string]bool{}
	p.failedSources = map[string]bool{}
	p.durations = map[string]float64{}
	// Хвости минулої цілі в прогресі.
	p.sizes = map[string]int64{}
	p.current = ""
}

func (p *Preparer) buildSegment(role, src string, live LiveParams, target TargetParams) {
	artifact := p.track(src, p.normalize(src, live, target))
	if artifact == "" {
		return
	}
	p.mu.Lock()
	p.artifacts[role] = artifact
	p.mu.Unlock()
}

// track — облік прогресу підготовки (компонент fallback-preparer у дашборді).
func (p *Preparer) track(src, artifact string) string {
	p.mu.Lock()
	if artifact != "" {
		p.doneSources[src] = true
	} else {
		p.failedSources[src] = true
	}
	p.current = ""
	p.mu.Unlock()
	return artifact
}

func (p *Preparer) buildFolderFile(src string, live LiveParams, target TargetParams) {
	artifact := p.track(src, p.normalize(src, live, target))
	if artifact == "" {
		// Битий файл у папці просто пропускаємо (на відміну від sequence, де
		// поганий файл блокує Save).
		log.Printf("backup folder file %s could not be prepared -- skipping", src)
		return
	}
	p.mu.Lock()
	p.folderReady = append(p.folderReady, artifact)
	p.mu.Unlock()
}

func (p *Preparer) normalizeSegment(src string, live LiveParams, target TargetParams) string {
	p.mu.Lock()
	p.current = filepath.Base(src)
	p.mu.Unlock()

	sourceParams, ok := probe.ProbeStreamParams(src, 0, 0)
	if !ok {
		return "" // нечитабельний -> викликач вирішує (skip / raw)
	}
	// Драбину сирий файл задовольнити не може ніколи — він однорендішенний.
	if sourceParams == live && !target.IsLadder() && isSingleTrack(src) {
		return src // оригінал і так підходить під -c copy
	}
	// ПОЗА будь-якими локами — може блокуюче чекати чужого воркера.
	return p.cache.GetOrBuild(src, target)
}

// isSingleTrack — рівно одне відео й одне аудіо (звірка вище дивилась 0/0).
func isSingleTrack(src string) bool {
	counts, ok := probe.ProbeTrackCounts(src)
	return ok && counts.Video == 1 && counts.Audio == 1
}

// detectTargetBitrates — виміряний бітрейт живого потоку, квантований; явний
// override у конфізі має пріоритет, відсутність виміру — дефолт.
func (p *Preparer) detectTargetBitrates() (int, int) {
	var measuredVideo, measuredAudio int
	if p.bitrate != nil {
		measuredVideo, measuredAudio = p.measure()
	}
	p.mu.Lock()
	prev := p.prevTarget
	p.mu.Unlock()
	vbitrate := p.cfgVideo
	if vbitrate == 0 {
		vbitrate = stabilize(
			snapUp(measuredVideo, videoBitrateLadder, videoBitrateStepKbps, defaultVideoBitrateKbps),
			prev.VideoBitrateKbps, videoBitrateLadder, videoBitrateStepKbps)
	}
	abitrate := p.cfgAudio
	if abitrate == 0 {
		abitrate = stabilize(
			snapUp(measuredAudio, audioBitrateLadder, audioBitrateStepKbps, defaultAudioBitrateKbps),
			prev.AudioBitrateKbps, audioBitrateLadder, audioBitrateStepKbps)
	}
	return vbitrate, abitrate
}

// measure — чекаємо перший ненульовий вимір (switcher набирає ~2с семплів
// після старту relay).
func (p *Preparer) measure() (int, int) {
	deadline := p.clock.Now() + bitrateMeasureTimeoutSec
	var video, audio int
	for p.clock.Now() < deadline {
		video, audio = p.bitrate()
		if video != 0 {
			return video, audio
		}
		p.sleeper.Sleep(bitrateMeasurePollSec)
	}
	return video, audio
}

func aborted(abort <-chan struct{}) bool {
	if abort == nil {
		return false
	}
	select {
	case <-abort:
		return true
	default:
		return false
	}
}
