package platform

import (
	"fmt"
	"log"
	"sync/atomic"

	"restream_go/internal/eb"
	"restream_go/internal/fallback"
)

// EBSessionMaxAgeSec — EB_SESSION_MAX_AGE_SEC: скільки живе виданий go-live-ключ
// у межах однієї публікації source.
const EBSessionMaxAgeSec = 300.0

// EBManagerDeps — половина go-live-обміну, що живе в Manager.
type EBManagerDeps struct {
	// ProbeGen — Manager.source_probe_gen: покоління публікації source.
	ProbeGen func() int
	// ObservedLadder — Source.video_manifest; ok=false = невідомий source.
	ObservedLadder func() ([]eb.Rung, bool)
	// Fetch — шов замість eb.FetchSession (nil = прод).
	Fetch func(streamKey string, rungs []eb.Rung, vodTrackAudio bool) (*eb.Session, error)
}

// ebDeps — усе, що потрібно плечу: половина Manager-а плюс те, чим володіє конвеєр.
type ebDeps struct {
	EBManagerDeps
	name      string
	vodTrack  bool
	streamKey func() string
	emit      func(level, text string)
	// ladderMinted — _ensure_ladder_backup; стейт-машина додає перевірку OFFLINE.
	ladderMinted func()
	clock        Clock
}

type ebSession struct {
	url      string
	rungs    []eb.GrantedRung
	gen      int
	mintedAt float64
}

// EBArm — go-live-сесія EB-плеча: кеш ключа на одну публікацію source + TTL.
// Кличеться з супервізорного потоку push-клієнта поза локами, тож стан —
// один атомарний вказівник (як присвоєння кортежу під GIL в оригіналі).
type EBArm struct {
	deps    ebDeps
	session atomic.Pointer[ebSession]
}

func newEBArm(deps ebDeps) *EBArm {
	if deps.Fetch == nil {
		deps.Fetch = eb.FetchSession
	}
	return &EBArm{deps: deps}
}

// PushURL — URL для (пере)підключення плеча. Порожній
// рядок = невдале підключення для супервізора; причину користувач бачить тостом.
func (a *EBArm) PushURL() string {
	generation := a.deps.ProbeGen()
	if cached := a.session.Load(); cached != nil && cached.gen == generation &&
		a.deps.clock.Now()-cached.mintedAt < EBSessionMaxAgeSec {
		return cached.url
	}
	url, rungs, errText := a.exchange()
	if errText != "" {
		log.Printf("[%s] enhanced broadcasting go-live failed: %s", a.deps.name, errText)
		a.deps.emit("error", errText)
		a.session.Store(nil)
		return ""
	}
	a.session.Store(&ebSession{url: url, rungs: rungs, gen: generation, mintedAt: a.deps.clock.Now()})
	if a.deps.ladderMinted != nil {
		a.deps.ladderMinted()
	}
	return url
}

// EnsureSession — змінтувати сесію ЗАРАЗ (валідація контракту source), не
// чекаючи першого підключення push-клієнта.
func (a *EBArm) EnsureSession() bool { return a.PushURL() != "" }

// Rungs — видана драбина поточної сесії.
func (a *EBArm) Rungs() []eb.GrantedRung {
	cached := a.session.Load()
	if cached == nil {
		return nil
	}
	return append([]eb.GrantedRung(nil), cached.rungs...)
}

// LadderRungs — та сама драбина у формі, яку чекає препарер заглушки.
func (a *EBArm) LadderRungs() []fallback.LadderRung {
	granted := a.Rungs()
	rungs := make([]fallback.LadderRung, len(granted))
	for i, r := range granted {
		rungs[i] = fallback.LadderRung{
			Width: r.Width, Height: r.Height, FPS: r.Fps, BitrateKbps: r.BitrateKbps,
		}
	}
	return rungs
}

// exchange — порт Manager.eb_push_url: обмін плюс ОБОВ'ЯЗКОВА позиційна звірка
// виданої драбини зі спостереженою.
func (a *EBArm) exchange() (string, []eb.GrantedRung, string) {
	observed, ok := a.deps.ObservedLadder()
	if !ok {
		return "", nil, a.deps.name + ": unknown source"
	}
	session, err := a.deps.Fetch(a.deps.streamKey(), observed, a.deps.vodTrack)
	if err != nil {
		return "", nil, a.deps.name + ": " + err.Error()
	}
	if mismatch := eb.CompareLadder(observed, session.Rungs); mismatch != "" {
		return "", nil, fmt.Sprintf(
			"%s: the ladder Twitch granted does not match the source -- %s. Not pushing.",
			a.deps.name, mismatch)
	}
	log.Printf("[%s] enhanced broadcasting session minted for %s",
		a.deps.name, eb.FormatRungs(session.Rungs))
	return session.URL, session.Rungs, ""
}
