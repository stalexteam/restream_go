package fallback

// Дискретна драбина цільового бітрейту заглушки: вимір гуляє на сотні
// кбіт між сесіями, а від цього числа залежить ключ кеша.
var (
	videoBitrateLadder = []int{1000, 1500, 2000, 2500, 3000, 3500, 4000, 4500,
		5000, 6000, 7000, 8000, 10000, 12000, 16000, 20000}
	audioBitrateLadder = []int{64, 96, 128, 160, 192, 256, 320}
)

const (
	videoBitrateStepKbps    = 2000
	audioBitrateStepKbps    = 64
	defaultVideoBitrateKbps = 6000
	defaultAudioBitrateKbps = 160

	// Скільки чекати першого ненульового виміру (switcher набирає ~2с семплів
	// після старту relay) і з яким кроком перепитувати.
	bitrateMeasureTimeoutSec = 4.0
	bitrateMeasurePollSec    = 0.3
)

func quantizeUp(kbps, step, def int) int {
	if kbps <= 0 {
		return def
	}
	up := ((kbps + step - 1) / step) * step
	if up < step {
		return step
	}
	return up
}

func snapUp(kbps int, ladder []int, step, def int) int {
	if kbps <= 0 {
		return def
	}
	for _, value := range ladder {
		if kbps <= value {
			return value
		}
	}
	return quantizeUp(kbps, step, def)
}

// stabilize — симетричний дедбенд в одну сходинку: поки вимір не відійшов від
// попереднього значення далі, лишаємось на ньому (інакше стрім біля межі
// чергував би два ключі кеша, тобто два повні транскоди пресета).
func stabilize(snapped, previous int, ladder []int, step int) int {
	if previous == 0 || previous == snapped {
		return snapped
	}
	below := step
	if snapped-step > below {
		below = snapped - step
	}
	for i := len(ladder) - 1; i >= 0; i-- {
		if ladder[i] < snapped {
			below = ladder[i]
			break
		}
	}
	above := snapped + step
	for _, value := range ladder {
		if value > snapped {
			above = value
			break
		}
	}
	if below <= previous && previous <= above {
		return previous
	}
	return snapped
}
