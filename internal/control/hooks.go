package control

import (
	"log"
	"strings"
)

// SourceForPath — хук MediaMTX -> source. Точний збіг, а для EB-source-ів
// ще й збіг по хвосту `_<slug>`: розбирати шлях по `_` не можна (підкреслення
// легальне і в самому slug), тож виграє НАЙДОВШИЙ slug.
func (m *Manager) sourceForPathLocked(path string) *Source {
	if path == "" {
		return nil
	}
	if source, ok := m.byPath.Get(path); ok {
		return source
	}
	var best *Source
	bestLen := -1
	for _, knownPath := range m.byPath.Keys() {
		candidate, _ := m.byPath.Get(knownPath)
		if !candidate.enhancedBroadcasting {
			continue
		}
		slug := knownPath
		if i := strings.Index(knownPath, "/"); i >= 0 {
			slug = knownPath[i+1:]
		}
		// Префікс переписаного ключа мусить бути непорожній: "live/_<slug>"
		// публікує хтось чужий.
		tail := "_" + slug
		if strings.HasPrefix(path, "live/") && strings.HasSuffix(path, tail) &&
			len(path)-len(tail) > len("live/") {
			if best == nil || len(slug) > bestLen {
				best, bestLen = candidate, len(slug)
			}
		}
	}
	return best
}

// OnAvailable — хук runOnAvailable MediaMTX.
func (m *Manager) OnAvailable(path string) {
	m.mu.Lock()
	if m.stopping.Load() {
		// Probe після shutdown лишив би сироту.
		log.Printf("available hook for %s after shutdown -- ignoring", pyRepr(path))
		m.mu.Unlock()
		return
	}
	source := m.sourceForPathLocked(path)
	if source == nil {
		log.Printf("available hook for unknown path %s -- ignoring", pyRepr(path))
		m.mu.Unlock()
		return
	}
	if m.isCurrentSessionHaltedLocked() {
		log.Printf("a source is publishing (path=%s), but this session was halted from the dashboard "+
			"-> ignoring (not restarting the broadcast)", path)
		m.mu.Unlock()
		return
	}
	m.abortWarm()
	source.available = true
	source.activePath = path
	source.contractError = ""
	source.availableSince = m.wall()
	source.hasAvailableSince = true
	source.probeGenValue++
	source.publish()
	gen := source.probeGenValue
	m.mu.Unlock()

	// Контракт перевіряємо у фоні (probe readback займає секунди); платформи
	// стартують лише після успішної валідації.
	m.spawn(func() { m.validateAndStart(source, gen) })
}

// OnUnavailable — хук runOnUnavailable MediaMTX.
func (m *Manager) OnUnavailable(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopping.Load() {
		// Сесійний таймер на знесеній машині більше нікого не розбудить.
		log.Printf("unavailable hook for %s after shutdown -- ignoring", pyRepr(path))
		return
	}
	source := m.sourceForPathLocked(path)
	if source == nil {
		log.Printf("unavailable hook for unknown path %s -- ignoring", pyRepr(path))
		return
	}
	source.available = false
	source.validated = false
	source.hasAvailableSince = false
	source.availableSince = 0
	source.activePath = ""
	source.probeGenValue++
	source.publish()
	for _, e := range m.platformsOf(source.name) {
		e.rt.OnSourceUnavailable()
	}
	// Сесію веде дефолтний source: обрив під час LIVE відкриває вікно
	// очікування повернення.
	if source.isDefault && m.sessionState.Load() == int32(stateLive) {
		m.setSessionState(stateFallback)
		m.scheduleSessionTimeoutLocked()
	}
	m.notify()
}

func (m *Manager) abortWarm() {
	m.abortOnce.Do(func() { close(m.warmAbort) })
}

// pyRepr — python %r для рядка/None у текстах логів роутингу хуків.
func pyRepr(path string) string {
	if path == "" {
		return "None"
	}
	return "'" + path + "'"
}
