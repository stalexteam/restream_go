package control

import (
	"log"
	"strconv"
)

// AddSource — новий source на автопризначеному шляху.
func (m *Manager) AddSource(name, stype string, audioTracks int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sources.Has(name) {
		return
	}
	if !sourceTypes[stype] {
		stype = "rtmp"
	}
	scfg := D(
		"name", name,
		"is_default", false,
		"type", stype,
		"live_path", m.assignPath(name),
	)
	probe := scfg.Clone()
	probe.Set("audio_tracks", int64(audioTracks))
	scfg.Set("audio_tracks", int64(SourceTrackCount(probe)))
	source := m.instantiateSource(scfg)
	log.Printf("added source %s on auto-assigned path %s", name, source.livePath)
	m.persistLocked()
}

// UpdateSource — rename/тип/лічильник доріжок/VOD Track/EB. Повертає
// текст помилки або "".
func (m *Manager) UpdateSource(name, newName, stype string, audioTracks int,
	vodTrack, enhancedBroadcasting bool, videoTracks int) string {

	m.mu.Lock()
	source, ok := m.sources.Get(name)
	if !ok {
		m.mu.Unlock()
		return "unknown source"
	}
	newType := stype
	if !sourceTypes[newType] {
		newType = source.stype
	}
	newVOD := vodTrack && newType == "rtmp"
	newEB := enhancedBroadcasting && newType == "rtmp"
	newTracks := SourceTrackCount(D(
		"type", newType, "audio_tracks", int64(audioTracks), "vod_track", newVOD))
	newVideoTracks := SourceVideoTracks(D(
		"type", newType, "enhanced_broadcasting", newEB, "video_tracks", int64(videoTracks)))
	contractChanged := newType != source.stype || newTracks != source.audioTracks ||
		newEB != source.enhancedBroadcasting
	if contractChanged && source.available {
		m.mu.Unlock()
		return "stop publishing to this source before changing its type or track count"
	}
	if newName != "" && newName != name {
		m.sources.Pop(name)
		source.name = newName
		source.scfg.Set("name", newName)
		m.sources.Set(newName, source)
		for _, e := range m.platforms.Values() {
			if e.sourceName == name {
				e.sourceName = newName
				e.spec.SourceName = newName
				e.cfg.Set("source", newName)
			}
		}
	}
	declarationChanged := newVideoTracks != source.videoTracks
	source.stype = newType
	source.scfg.Set("type", newType)
	source.vodTrack = newVOD
	source.scfg.Set("vod_track", newVOD)
	source.enhancedBroadcasting = newEB
	source.scfg.Set("enhanced_broadcasting", newEB)
	source.videoTracks = newVideoTracks
	source.scfg.Set("video_tracks", int64(newVideoTracks))
	source.audioTracks = newTracks
	source.scfg.Set("audio_tracks", int64(newTracks))
	source.publish()
	if contractChanged {
		// Wiring транспорту зафіксовано конструкцією -- залежні платформи
		// перестворюються.
		for _, e := range m.platformsOf(source.name) {
			pcfg := e.toConfig()
			rt := e.rt
			m.blockingUnlocked(rt.Shutdown)
			m.platforms.Pop(e.name)
			m.instantiatePlatform(pcfg)
		}
	}
	log.Printf("updated source %s (type=%s, audio_tracks=%d, eb=%s, video_tracks=%s)",
		source.name, source.stype, source.audioTracks, pyBool(source.enhancedBroadcasting),
		videoTracksLabel(source.videoTracks))
	m.persistLocked()
	revalidate := declarationChanged && source.available && !contractChanged
	gen := source.probeGenValue
	m.mu.Unlock()

	if revalidate {
		// Твердження про кількість сходинок змінилось під час ефіру:
		// платформи не чіпаємо, лише перевіряємо вміст заново.
		m.spawn(func() { m.revalidateContract(source, gen) })
	}
	return ""
}

// RemoveSource — видалення (дефолтний незнищенний, посилання блокують).
func (m *Manager) RemoveSource(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	source, ok := m.sources.Get(name)
	if !ok || source.isDefault {
		return
	}
	if len(m.platformsOf(name)) > 0 {
		return // guard: викликач показує toast зі списком платформ
	}
	m.sources.Pop(name)
	m.byPath.Pop(source.livePath)
	log.Printf("removed source %s", name)
	m.persistLocked()
}

// PlatformsReferencingSource — імена платформ цього source.
func (m *Manager) PlatformsReferencingSource(name string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []string{}
	for _, e := range m.platformsOf(name) {
		out = append(out, e.name)
	}
	return out
}

func pyBool(v bool) string {
	if v {
		return "True"
	}
	return "False"
}

// videoTracksLabel — `source.video_tracks or "auto"` у лог-рядку.
func videoTracksLabel(n int) string {
	if n == 0 {
		return "auto"
	}
	return strconv.Itoa(n)
}
