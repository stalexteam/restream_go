// Package proc — супервізія довгоживучих процесів: пороги рестарту/флепінгу,
// спавн під захистом від сиріт, /proc-стати.
package proc

import "time"

// RestartBackoff — пауза супервізора між спробами.
const RestartBackoff = 1500 * time.Millisecond

// Поріг флепінгу (смерть одразу після старту) і кількість падінь ПОСПІЛЬ, після
// якої сигналити назовні; до першого успіху досить однієї невдачі.
const (
	FlappingExitThreshold  = 3 * time.Second
	FlappingCountThreshold = 3
)

// EverSucceededThreshold — окремий, значно вищий поріг «справді з'єдналися»:
// Twitch відхиляє невалідний ключ із затримкою впритул до FlappingExitThreshold.
const EverSucceededThreshold = 10 * time.Second
