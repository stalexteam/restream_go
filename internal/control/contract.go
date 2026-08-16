package control

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"restream_go/internal/wire/ts"
)

// contractInfo — паспорт вмісту, який віддає перевірка контракту.
type contractInfo struct {
	audioActual int
	video       []ts.VideoTrack
	audioTracks []ts.AudioTrack
}

// checkContract — probe readback проти декларації source; "" = контракт зійшовся.
// Кличеться ПОЗА локом.
func (m *Manager) checkContract(source *Source) (string, contractInfo) {
	url := source.ReadbackURL()
	if source.enhancedBroadcasting {
		return m.checkEBContract(source, url)
	}
	if strings.HasPrefix(url, "srt://") {
		manifest, ok := m.probes.TSManifest(url)
		if !ok {
			return "could not probe the incoming stream", contractInfo{}
		}
		if len(manifest.Video) == 0 {
			return "no video track", contractInfo{}
		}
		if manifest.Audio != source.audioTracks {
			return declaredAudioMismatch(source.audioTracks, manifest.Audio), contractInfo{}
		}
		return "", contractInfo{audioActual: manifest.Audio, audioTracks: manifest.AudioTracks}
	}
	counts, ok := m.probes.TrackCounts(url)
	if !ok {
		return "could not probe the incoming stream", contractInfo{}
	}
	if counts.Video < 1 {
		return "no video track", contractInfo{}
	}
	if counts.Audio != source.audioTracks {
		return declaredAudioMismatch(source.audioTracks, counts.Audio), contractInfo{}
	}
	return "", contractInfo{audioActual: counts.Audio}
}

// checkEBContract — контракт EB-драбини: усі відеотреки H.264 І однакове
// співвідношення сторін, плюс лічильники проти декларації.
func (m *Manager) checkEBContract(source *Source, url string) (string, contractInfo) {
	manifest, ok := m.probes.TSManifest(url)
	if !ok {
		return "could not probe the incoming stream", contractInfo{}
	}
	video := manifest.Video
	if len(video) == 0 {
		return "no video track", contractInfo{}
	}
	if len(manifest.Unsupported) > 0 {
		types := make([]string, 0, len(manifest.Unsupported))
		for _, t := range manifest.Unsupported {
			types = append(types, fmt.Sprintf("0x%02X", t))
		}
		return "unsupported stream type(s) on the wire: " + strings.Join(types, ", "), contractInfo{}
	}
	for _, t := range video {
		if t.Codec != "h264" {
			return fmt.Sprintf(
				"video track #%d is %s; Enhanced Broadcasting support is limited to all-H.264 ladders",
				t.Index+1, strings.ToUpper(t.Codec)), contractInfo{}
		}
	}
	if source.videoTracks != 0 && len(video) != source.videoTracks {
		return fmt.Sprintf(
			"expected %d video track(s), source carries %d -- check Maximum Video Tracks in OBS",
			source.videoTracks, len(video)), contractInfo{}
	}
	for _, t := range video {
		if t.Width == 0 || t.Height == 0 {
			return "could not read the geometry of every video track", contractInfo{}
		}
	}
	minRatio, maxRatio := ratioOf(video[0]), ratioOf(video[0])
	for _, t := range video[1:] {
		r := ratioOf(t)
		if r < minRatio {
			minRatio = r
		}
		if r > maxRatio {
			maxRatio = r
		}
	}
	if maxRatio-minRatio > AspectTolerance*maxRatio {
		shapes := make([]string, 0, len(video))
		for _, t := range video {
			shapes = append(shapes, strconv.Itoa(t.Width)+"x"+strconv.Itoa(t.Height))
		}
		return "video tracks have different aspect ratios (" + strings.Join(shapes, ", ") + ")", contractInfo{}
	}
	if manifest.Audio != source.audioTracks {
		return declaredAudioMismatch(source.audioTracks, manifest.Audio), contractInfo{}
	}
	return "", contractInfo{audioActual: manifest.Audio, video: video, audioTracks: manifest.AudioTracks}
}

func orEmptyVideo(v []ts.VideoTrack) []ts.VideoTrack {
	if v == nil {
		return []ts.VideoTrack{}
	}
	return v
}

func orEmptyAudio(a []ts.AudioTrack) []ts.AudioTrack {
	if a == nil {
		return []ts.AudioTrack{}
	}
	return a
}

func ratioOf(t ts.VideoTrack) float64 { return float64(t.Width) / float64(t.Height) }

func declaredAudioMismatch(declared, got int) string {
	return fmt.Sprintf("declared %d audio track(s), got %d", declared, got)
}

// validateAndStart — фонова валідація контракту й старт платформ.
func (m *Manager) validateAndStart(source *Source, gen int) {
	reason, info := m.checkContract(source)
	if m.stopping.Load() {
		// Контролер зупиняється, а probe секундний: без цієї перевірки relay
		// піднявся б ПІСЛЯ shutdown і лишився сиротою.
		return
	}
	if reason == "" {
		// Паспорт фіксуємо ДО go-live-обміну (обмін читає драбину звідти), а
		// сам обмін — ДО старту платформ і поза локом.
		m.mu.Lock()
		if gen != source.probeGenValue || !source.available {
			m.mu.Unlock()
			return
		}
		m.acceptContractLocked(source, info)
		var ebPlatforms []*platformEntry
		for _, e := range m.platformsOf(source.name) {
			if e.spec.EB {
				ebPlatforms = append(ebPlatforms, e)
			}
		}
		m.mu.Unlock()
		for _, e := range ebPlatforms {
			e.rt.EnsureEBSession()
		}
	}

	m.mu.Lock()
	if gen != source.probeGenValue || !source.available {
		m.mu.Unlock()
		return // публікація вже зникла/нова -- цей probe застарів
	}
	if reason != "" {
		source.validated = false
		source.contractError = reason
		log.Printf("source %s failed the content check: %s", source.name, reason)
		m.emitEvent("error", "Source "+source.name+": "+reason+
			" -- check the source type/track count in Settings")
		// Активні залежні платформи -> не даємо стартувати ефір із
		// неправильним вмістом: глушимо стрім в OBS.
		for _, e := range m.platformsOf(source.name) {
			if e.rt.EffectiveEnabled() {
				m.requestStopStreamingInOBS()
				break
			}
		}
		m.notify()
		m.mu.Unlock()
		return
	}
	log.Printf("source %s is live on %s (%s, %d audio track(s)%s)",
		source.name, source.path(), source.stype, source.audioTracks, ladderSuffix(info.video))
	if source.isDefault {
		m.cancelSessionTimeoutLocked()
		if m.sessionState.Load() == int32(stateOffline) {
			m.emitEvent("info", "Broadcast started")
		}
		m.setSessionState(stateLive)
	}
	switchedOff := false
	for _, e := range m.platformsOf(source.name) {
		switchedOff = m.gateFallbackLocked(e) || switchedOff
		e.rt.OnSourceAvailable()
	}
	if switchedOff {
		m.persistLocked()
	}
	m.notify()
	m.mu.Unlock()

	// Паспорт потоку для дашборда -- поза локом, ефір уже стартував.
	params, ok := m.probes.StreamParams(source.ReadbackURL(), 0, int(source.videoIdx.Load()))
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if gen != source.probeGenValue || !source.available {
		return
	}
	source.media = &SourceMedia{
		VideoCodec:        params.VideoCodec,
		Width:             params.Width,
		Height:            params.Height,
		FPS:               params.FPS,
		AudioCodec:        params.AudioCodec,
		Channels:          params.Channels,
		SampleRate:        params.SampleRate,
		AudioTracksActual: info.audioActual,
		VideoTracksActual: source.videoManifest,
		AudioTracksDetail: source.audioManifest,
	}
	m.notify()
}

// ladderSuffix — хвіст ", video: 1920x1080@60/6000k,..." лог-рядка.
func ladderSuffix(video []ts.VideoTrack) string {
	if len(video) == 0 {
		return ""
	}
	parts := make([]string, 0, len(video))
	for _, t := range video {
		fps := "?"
		if t.FPS != 0 {
			fps = strconv.Itoa(t.FPS)
		}
		part := strconv.Itoa(t.Width) + "x" + strconv.Itoa(t.Height) + "@" + fps
		if t.BitrateKbps != 0 {
			part += "/" + strconv.Itoa(t.BitrateKbps) + "k"
		}
		parts = append(parts, part)
	}
	return ", video: " + strings.Join(parts, ", ")
}

// acceptContractLocked — контракт зійшовся: фіксуємо паспорт драбини.
func (m *Manager) acceptContractLocked(source *Source, info contractInfo) {
	source.validated = true
	source.contractError = ""
	source.audioManifest = orEmptyAudio(info.audioTracks)
	source.videoManifest = orEmptyVideo(info.video)
	source.primaryVideoIndex = 0
	for _, t := range source.videoManifest {
		if t.Codec == "h264" {
			source.primaryVideoIndex = t.Index
			break
		}
	}
	source.publish()
}

// revalidateContract — перевірка контракту БЕЗ (пере)старту платформ.
func (m *Manager) revalidateContract(source *Source, gen int) {
	reason, info := m.checkContract(source)
	if m.stopping.Load() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if gen != source.probeGenValue || !source.available {
		return
	}
	if reason == "" {
		m.acceptContractLocked(source, info)
		log.Printf("source %s still matches its declaration", source.name)
	} else {
		source.validated = false
		source.contractError = reason
		log.Printf("source %s no longer matches its declaration: %s", source.name, reason)
		m.emitEvent("error", "Source "+source.name+": "+reason)
	}
	m.notify()
}
