package control

import (
	"log"
	"strconv"

	"restream_go/internal/platform"
)

const (
	stateOffline  = platform.StateOffline
	stateLive     = platform.StateLive
	stateFallback = platform.StateFallback
)

func (m *Manager) setSessionState(s platform.State) { m.sessionState.Store(int32(s)) }

func (m *Manager) isDefaultSourceLocked(sourceName string) bool {
	src, ok := m.sources.Get(sourceName)
	return ok && src.isDefault
}

// --- сигнали платформ (у клоні — синхронно з горутини-власника машини) ---

// OnPlatformStalled — relay платформи застояв: для дефолтного source це
// відкриває сесійне вікно очікування.
func (m *Manager) OnPlatformStalled(platformName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.platforms.Get(platformName)
	if !ok || !m.isDefaultSourceLocked(e.sourceName) {
		return
	}
	if m.sessionState.Load() == int32(stateLive) {
		m.setSessionState(stateFallback)
		m.scheduleSessionTimeoutLocked()
		m.notify()
	}
}

// OnPlatformRecovered — платформа безшовно повернулась на relay.
func (m *Manager) OnPlatformRecovered(platformName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.platforms.Get(platformName)
	if !ok || !m.isDefaultSourceLocked(e.sourceName) {
		return
	}
	if m.sessionState.Load() == int32(stateFallback) {
		m.setSessionState(stateLive)
		m.cancelSessionTimeoutLocked()
		m.notify()
	}
}

// OnPlatformGaveUp — платформа заглушила себе; стоп OBS лише коли не лишилось
// жодної живої платформи (P7,:2046).
func (m *Manager) OnPlatformGaveUp(platformName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.anyPlatformAliveLocked() {
		log.Printf("no platform is left alive -- asking OBS to stop the whole stream")
		m.emitEvent("error",
			"No enabled platform could be reached -- check the URLs/keys in Settings. "+
				"Broadcast stopped, and a stop command was sent to the OBS browser-source. "+
				"If OBS is still streaming, set its Page permission to \"Full access to OBS\".")
		m.requestStopStreamingInOBS()
		return
	}
	log.Printf("platform %s gave up -- other platforms keep streaming, OBS not touched", platformName)
	m.emitEvent("warning", platformName+": couldn't connect -- this platform stopped. "+
		"The broadcast keeps going on the others.")
}

// anyPlatformAliveLocked — «жива» = обидва гейти відкриті, не failed, не OFFLINE.
func (m *Manager) anyPlatformAliveLocked() bool {
	for _, e := range m.platforms.Values() {
		if e.rt.EffectiveEnabled() && !e.rt.Failed() && e.rt.State() != stateOffline {
			return true
		}
	}
	return false
}

func (m *Manager) anyPlatformActiveLocked() bool {
	for _, e := range m.platforms.Values() {
		if e.rt.State() != stateOffline {
			return true
		}
	}
	return false
}

// --- сигнали OBS / латч / детектор штатного стопу ---

// OnManualStop — штатний стоп OBS з obs-source.html.
func (m *Manager) OnManualStop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastStartedObsID = ""
	m.lastHaltedObsID = ""
	now := m.clock.Now()
	m.gracefulStopAt.Store(&now)
	wasActive := m.sessionState.Load() != int32(stateOffline)
	m.setSessionState(stateOffline)
	m.cancelSessionTimeoutLocked()
	defaultName := ""
	if src := m.defaultSource.Load(); src != nil {
		defaultName = src.name
	}
	anyStopped := false
	for _, e := range m.platforms.Values() {
		if e.sourceName == defaultName {
			if e.rt.Halt() {
				anyStopped = true
			}
		} else {
			e.rt.GracefulStopIfFallback()
		}
	}
	if wasActive || anyStopped {
		log.Printf("OBS reports streaming stopped -> ending the broadcast")
		m.emitEvent("info", "Broadcast ended")
	}
	m.notify()
}

// OnDashboardHalt — ручний HALT із дашборда: стоп усього + латч сесії.
func (m *Manager) OnDashboardHalt() {
	m.mu.Lock()
	defer m.mu.Unlock()
	anyActive := m.sessionState.Load() != int32(stateOffline)
	for _, e := range m.platforms.Values() {
		if e.rt.Halt() {
			anyActive = true
		}
	}
	m.setSessionState(stateOffline)
	m.cancelSessionTimeoutLocked()
	if !anyActive {
		return
	}
	log.Printf("HALT requested from the dashboard -> stopping everything and asking OBS to stop")
	m.emitEvent("warning", "Broadcast halted from the dashboard")
	m.lastHaltedObsID = m.lastStartedObsID
	m.requestStopStreamingInOBS()
}

// ReportOBSSession — obs-source.html доповів id поточної сесії стриму.
func (m *Manager) ReportOBSSession(obsID string) {
	if obsID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastStartedObsID = obsID
}

// IsSessionHalted — чи саме ця сесія OBS заглушена.
func (m *Manager) IsSessionHalted(obsID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return obsID != "" && obsID == m.lastHaltedObsID
}

func (m *Manager) isCurrentSessionHaltedLocked() bool {
	return m.lastHaltedObsID != "" && m.lastStartedObsID == m.lastHaltedObsID
}

// OnOBSStreamingStarted — obs-source.html повідомив про старт стриму.
func (m *Manager) OnOBSStreamingStarted() {
	log.Printf("OBS reports streaming started (obs-source.html)")
}

// OnMediaMTXConnectTimeout — MediaMTX закрив з'єднання по readTimeout, не
// дочекавшись публікації; попередження глобальне.
func (m *Manager) OnMediaMTXConnectTimeout() {
	m.mu.Lock()
	if m.anyPlatformActiveLocked() || m.sessionState.Load() != int32(stateOffline) {
		m.mu.Unlock()
		return
	}
	now := m.clock.Now()
	if now-m.lastConnectTimeoutToastAt < ConnectTimeoutToastCooldownSec {
		m.mu.Unlock()
		return
	}
	m.lastConnectTimeoutToastAt = now
	m.mu.Unlock()

	log.Printf("a source failed to finish connecting to MediaMTX within the connect timeout -- " +
		"consider raising it in Settings")
	m.emitEvent("warning",
		"The source didn't finish connecting in time -- try raising the connect timeout in Settings")
}

// --- сесійний offline-таймер ---

func (m *Manager) scheduleSessionTimeoutLocked() {
	m.cancelSessionTimeoutLocked()
	timeoutSec := intOr(m.config, "offline_timeout_sec", DefaultOfflineTimeoutSec)
	deadline := m.wall() + float64(timeoutSec)
	m.fallbackDeadline = &deadline
	m.timeoutTimer = m.timers("session", floatSeconds(float64(timeoutSec)), m.onSessionTimeout)
}

func (m *Manager) cancelSessionTimeoutLocked() {
	if m.timeoutTimer != nil {
		m.timeoutTimer.Stop()
		m.timeoutTimer = nil
	}
	m.fallbackDeadline = nil
}

// onSessionTimeout — source не повернувся у вікні: гасимо всі платформи.
func (m *Manager) onSessionTimeout() {
	m.mu.Lock()
	if m.sessionState.Load() != int32(stateFallback) {
		m.mu.Unlock()
		return
	}
	m.cancelSessionTimeoutLocked()
	m.setSessionState(stateOffline)
	log.Printf("gave up waiting for the source to recover after %s s -> ending the broadcast (all platforms)",
		strconv.Itoa(intOr(m.config, "offline_timeout_sec", DefaultOfflineTimeoutSec)))
	for _, e := range m.platforms.Values() {
		e.rt.Halt()
	}
	m.notify()
	m.mu.Unlock()
	m.emitEvent("warning", "Broadcast ended -- the source did not reconnect in time")
}
