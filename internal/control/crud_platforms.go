package control

import (
	"log"

	"restream_go/internal/platform"
)

// PlatformFields — підмножина полів update_platform; nil = поле не задане
// (python `field in fields and fields[field] is not None`).
type PlatformFields struct {
	NewName      string
	Type         *string
	VODTrack     *bool
	Server       *string
	Key          *string
	StreamID     *string
	Passphrase   *string
	Source       *string
	Group        *string
	BackupPreset *string
	Audio        *int
	AudioVOD     *int
	Video        *int
	AudioMap     *[]any
}

// toConfig — фіксований порядок полів персисту (Platform.to_config:1306).
func (e *platformEntry) toConfig() *Dict {
	cfg := D(
		"name", e.name,
		"type", e.ptype,
		"vod_track", e.vodTrack,
		"enabled", e.enabled,
		"group", e.groupID,
		"source", e.sourceName,
		"audio", int64(e.audio),
		"audio_vod", int64(e.audioVOD),
		"video", int64(e.video),
		"server", e.server,
		"key", e.key,
		"streamid", e.streamID,
		"passphrase", e.passphrase,
		"backup_preset", e.backupPreset,
	)
	if e.ptype == "srt" {
		cfg.Set("audio_map", append([]any(nil), e.audioMap...))
	}
	return cfg
}

// AddPlatform — нова платформа з дефолтами форми.
func (m *Manager) AddPlatform(name, ptype string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.platforms.Has(name) {
		return
	}
	defaultSource := ""
	if src := m.defaultSource.Load(); src != nil {
		defaultSource = src.name
	}
	if !platformTypes[ptype] {
		ptype = "rtmp"
	}
	pcfg := D(
		"name", name,
		"type", ptype,
		"vod_track", false,
		"enabled", false,
		"group", pyStr(m.defaultGroupLocked().GetOr("id", "")),
		"source", defaultSource,
		"audio", int64(0),
		"audio_vod", int64(1),
		"video", int64(0),
		"server", "", "key", "", "streamid", "", "passphrase", "",
		"backup_preset", m.defaultPresetIDLocked(),
	)
	e := m.instantiatePlatform(pcfg)
	m.joinRunningSourceLocked(e)
	log.Printf("added platform %s (%s)", name, ptype)
	m.persistLocked()
}

// joinRunningSourceLocked — платформа (пере)створена під час активної публікації:
// підключаємо одразу, хук available повторно не прийде (S6,:2375).
func (m *Manager) joinRunningSourceLocked(e *platformEntry) {
	src, ok := m.sources.Get(e.sourceName)
	if !ok || !(src.available && src.validated) {
		return
	}
	m.gateFallbackLocked(e)
	if e.spec.EB {
		// Драбину вже спостережено, тож мінтимо ЗАРАЗ: від неї залежить і URL,
		// і підготовка пер-сходинкової заглушки. Мережа -- отже окремою
		// горутиною: сюди заходять під Manager.lock.
		m.spawn(func() { e.rt.EnsureEBSession() })
	}
	e.rt.OnSourceAvailable()
}

// UpdatePlatform — зміна топології перестворює платформу, решта — live-apply
func (m *Manager) UpdatePlatform(name string, fields PlatformFields) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.platforms.Get(name)
	if !ok {
		return
	}
	oldCfg := old.toConfig()
	newCfg := oldCfg.Clone()
	if fields.NewName != "" {
		newCfg.Set("name", fields.NewName)
	}
	setIfGiven(newCfg, "type", fields.Type)
	setIfGiven(newCfg, "server", fields.Server)
	setIfGiven(newCfg, "key", fields.Key)
	setIfGiven(newCfg, "streamid", fields.StreamID)
	setIfGiven(newCfg, "passphrase", fields.Passphrase)
	setIfGiven(newCfg, "source", fields.Source)
	setIfGiven(newCfg, "group", fields.Group)
	setIfGiven(newCfg, "backup_preset", fields.BackupPreset)
	if fields.VODTrack != nil {
		newCfg.Set("vod_track", *fields.VODTrack)
	}
	if fields.Audio != nil {
		newCfg.Set("audio", int64(max(*fields.Audio, 0)))
	}
	if fields.AudioVOD != nil {
		newCfg.Set("audio_vod", int64(max(*fields.AudioVOD, 0)))
	}
	if fields.Video != nil {
		newCfg.Set("video", int64(max(*fields.Video, -1)))
	}
	if fields.AudioMap != nil {
		newCfg.Set("audio_map", clampAudioMap(*fields.AudioMap))
	}
	if pyStr(newCfg.GetOr("type", "")) != "rtmp" {
		// VOD Track і passthrough драбини -- лише для rtmp (ті самі
		// інваріанти, що в normalize_platforms).
		newCfg.Set("vod_track", false)
		newCfg.Set("video", int64(max(cfgInt(newCfg, "video", 0), 0)))
	} else {
		// audio_map немає в to_config rtmp-платформи -- інакше він мовчки
		// перебивав би audio.
		newCfg.Pop("audio_map")
	}

	newSpec := m.specForConfigLocked(newCfg)
	if problem := chimeraProblem(newSpec); problem != "" {
		m.emitEvent("warning", name+": "+problem)
		return
	}

	if m.topologyChangedLocked(old, oldCfg, newCfg, newSpec) {
		rt := old.rt
		m.blockingUnlocked(rt.Shutdown)
		m.platforms.Pop(name)
		e := m.instantiatePlatform(newCfg)
		m.joinRunningSourceLocked(e)
		log.Printf("recreated platform %s -> %s", name, pyStr(newCfg.GetOr("name", "")))
		m.persistLocked()
		return
	}

	server := pyStr(newCfg.GetOr("server", ""))
	key := pyStr(newCfg.GetOr("key", ""))
	streamID := pyStr(newCfg.GetOr("streamid", ""))
	passphrase := pyStr(newCfg.GetOr("passphrase", ""))
	if server != old.server || key != old.key || streamID != old.streamID || passphrase != old.passphrase {
		old.server, old.key, old.streamID, old.passphrase = server, key, streamID, passphrase
		old.cfg.Set("server", server)
		old.cfg.Set("key", key)
		old.cfg.Set("streamid", streamID)
		old.cfg.Set("passphrase", passphrase)
		old.rt.UpdateCredentials(server, key, streamID, passphrase)
	}
	if group := pyStr(newCfg.GetOr("group", "")); group != old.groupID {
		m.setPlatformGroupLocked(old, group)
	}
	if preset := pyStr(newCfg.GetOr("backup_preset", "")); preset != old.backupPreset {
		presetID := preset
		if presetID == "" {
			presetID = m.defaultPresetIDLocked()
		}
		m.applyPresetLocked(old, presetID, m.segmentsForPreset(presetID))
	}
	newAudio, newAudioVOD := cfgInt(newCfg, "audio", 0), cfgInt(newCfg, "audio_vod", 0)
	if old.ptype != "srt" && (newAudio != old.audio || newAudioVOD != old.audioVOD) {
		old.audio, old.audioVOD = newAudio, newAudioVOD
		old.cfg.Set("audio", int64(newAudio))
		old.cfg.Set("audio_vod", int64(newAudioVOD))
		old.rt.UpdateTracks(newAudio, newAudioVOD)
	}
	newMap, hasNewMap := newCfg.Get("audio_map")
	oldMap, hasOldMap := oldCfg.Get("audio_map")
	if hasNewMap != hasOldMap || !pyEqual(newMap, oldMap) {
		clamped := clampAudioMap(newMap)
		old.audioMap = clamped
		old.audio = firstMappedTrack(clamped)
		old.cfg.Set("audio_map", clamped)
		old.rt.UpdateAudioMap(audioMapTracks(clamped))
	}
	log.Printf("updated platform %s", name)
	m.persistLocked()
}

// chimeraProblem — чому VOD Track не працює з цим source, або "".
func chimeraProblem(spec platform.Spec) string {
	if spec.Chimera && !spec.MultitrackSource {
		return "VOD Track needs a source with 2 audio tracks -- this one has a single track"
	}
	return ""
}

// specForConfigLocked — Spec, який дала б ця конфігурація платформи.
func (m *Manager) specForConfigLocked(cfg *Dict) platform.Spec {
	src, _ := m.sources.Get(pyStr(cfg.GetOr("source", "")))
	return platform.NewSpec(platform.Config{
		Name:       pyStr(cfg.GetOr("name", "")),
		Type:       pyStr(cfg.GetOr("type", "rtmp")),
		VODTrack:   pyTruthy(cfg.GetOr("vod_track", false)),
		SourceName: pyStr(cfg.GetOr("source", "")),
		Video:      cfgInt(cfg, "video", 0),
	}, m.sourceContract(src))
}

// topologyChangedLocked — рішення «перестворити чи live-apply»: топологічні поля
// плюс будь-яка різниця зібраного Spec.
func (m *Manager) topologyChangedLocked(old *platformEntry, oldCfg, newCfg *Dict, newSpec platform.Spec) bool {
	// video звіряє лише Spec нижче: сире -1 з форми ніколи не дорівнює
	// нормалізованому 0 з to_config.
	fields := []string{"name", "type", "vod_track", "source", "audio", "audio_vod"}
	if old.spec.DualTrackRelay || old.spec.DualTrackChimera || old.ptype == "srt" {
		// audio/audio_vod живуть на кожному тезі, а для srt їх замінює
		// audio_map -- дефолтний "audio": 0 з UI не має тригерити recreate.
		fields = []string{"name", "type", "vod_track", "source"}
	}
	for _, field := range fields {
		newValue, _ := newCfg.Get(field)
		oldValue, _ := oldCfg.Get(field)
		if !pyEqual(newValue, oldValue) {
			return true
		}
	}
	return newSpec != old.spec
}

// RemovePlatform — знести платформу.
func (m *Manager) RemovePlatform(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.platforms.Get(name)
	if !ok {
		return
	}
	rt := e.rt
	m.blockingUnlocked(rt.Shutdown)
	m.platforms.Pop(name)
	log.Printf("removed platform %s", name)
	m.persistLocked()
}

// EnablePlatform — увімкнути платформу; невалідний контракт source або
// непридатний fallback-пресет блокує.
func (m *Manager) EnablePlatform(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.platforms.Get(name)
	if !ok {
		return
	}
	if src, ok := m.sources.Get(e.sourceName); ok && src.available && !src.validated {
		m.emitEvent("warning", name+": its source failed the content check -- fix the source first")
		return
	}
	if problem := m.fallbackPresetProblemLocked(e.backupPreset); problem != "" {
		m.emitEvent("warning", name+": "+problem)
		return
	}
	if problem := chimeraProblem(e.spec); problem != "" {
		m.emitEvent("warning", name+": "+problem)
		return
	}
	e.enabled = true
	e.cfg.Set("enabled", true)
	e.rt.SetEnabled(true)
	m.persistLocked()
}

// fallbackPresetProblemLocked — чому пресет не дасть заглушки, або "".
func (m *Manager) fallbackPresetProblemLocked(presetID string) string {
	p := m.presetByIDLocked(presetID)
	if pyStrOr(p.GetOr("type", "sequence"), "sequence") == "folder" {
		raw := presetText(p.GetOr("folder", nil))
		if raw == "" {
			return "fallback folder is not set -- configure it in Settings first"
		}
		folder := m.resolvePresetPath(p.GetOr("folder", nil))
		if folder == "" || !isDir(folder) {
			return "fallback folder not found: " + raw
		}
		if len(listFolderPaths(folder)) == 0 {
			return "fallback folder has no video files: " + raw
		}
		return ""
	}
	raw := presetText(p.GetOr("loop_file", nil))
	if raw == "" {
		return "its fallback preset has no loop video set -- configure it in Settings first"
	}
	loop := m.resolvePresetPath(p.GetOr("loop_file", nil))
	if loop == "" || !isFile(loop) {
		return "fallback loop video not found: " + raw
	}
	return ""
}

// gateFallbackLocked — гейт перед виходом в ефір; персист на викликачеві.
func (m *Manager) gateFallbackLocked(e *platformEntry) bool {
	if !e.enabled {
		return false
	}
	problem := m.fallbackPresetProblemLocked(e.backupPreset)
	if problem == "" {
		problem = chimeraProblem(e.spec)
	}
	if problem == "" {
		return false
	}
	m.switchOffForFallbackLocked(e, problem)
	return true
}

func (m *Manager) switchOffForFallbackLocked(e *platformEntry, problem string) {
	e.enabled = false
	e.cfg.Set("enabled", false)
	e.rt.SetEnabled(false)
	log.Printf("platform %s switched off: %s", e.name, problem)
	m.emitEvent("warning", e.name+": "+problem+" -- the platform was switched off")
}

// OnPlatformFallbackUnusable — заглушка знадобилась просто зараз, але пресет
// непридатний: платформа вже знята з ефіру машиною, лишається галочка.
func (m *Manager) OnPlatformFallbackUnusable(platformName, problem string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.platforms.Get(platformName)
	if !ok || !e.enabled {
		return
	}
	m.switchOffForFallbackLocked(e, problem)
	m.persistLocked()
	m.notify()
}

// fallbackPresetProblem — перевірка пресета з горутини платформи.
func (m *Manager) fallbackPresetProblem(e *platformEntry) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fallbackPresetProblemLocked(e.backupPreset)
}

// DisablePlatform — вимкнути платформу.
func (m *Manager) DisablePlatform(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.platforms.Get(name)
	if !ok {
		return
	}
	e.enabled = false
	e.cfg.Set("enabled", false)
	e.rt.SetEnabled(false)
	m.persistLocked()
}

func (m *Manager) setPlatformGroupLocked(e *platformEntry, groupID string) {
	e.groupID = groupID
	e.cfg.Set("group", groupID)
	e.gate = m.groupEnabledLocked(groupID)
	e.rt.SetGroup(groupID, e.gate)
}

// applyPresetLocked — джерело сегментів оновлюється тим самим викликом.
func (m *Manager) applyPresetLocked(e *platformEntry, presetID string, seg Segments) {
	e.backupPreset = presetID
	e.cfg.Set("backup_preset", presetID)
	e.segments = seg
	e.rt.ApplyPreset(presetID, seg)
}

func setIfGiven(cfg *Dict, key string, value *string) {
	if value != nil {
		cfg.Set(key, *value)
	}
}
