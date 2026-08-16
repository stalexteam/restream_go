package proc

import (
	"math"
	"math/big"
	"os"
	"sync"
	"time"
)

// Stats — CPU%/RSS одного семпла процесу (порт dict {cpu_percent, rss_mb}
// із proc_stats.sample).
type Stats struct {
	CPUPercent float64
	RSSMB      float64
}

type prevSample struct {
	t     float64 // Sampler.now() у момент семпла
	units int64   // сумарний CPU-час на той момент (Linux: тіки; Windows: 100нс)
}

// rawSample — платформо-специфічний зріз одного виміру перед спільною
// математикою: сирі одиниці CPU-часу (не секунди — точна дельта потребує
// цілочисельного віднімання ДО ділення) і RSS. age — лінива: вік процесу
// потрібен лише коли попереднього семпла нема.
type rawSample struct {
	cpuUnits    int64
	unitsPerSec float64
	rssRaw      int64
	rssDivisor  float64
	age         func() (float64, bool)
}

// Sampler кешує попередній семпл на pid (порт module-level _prev_samples у
// ). Перший семпл нового pid не має з чим порахувати дельту —
// береться середнє за весь вік процесу: короткоживучі транскод-процеси
// (1-2с) інакше показували б рівно 0.0%.
// Мапа захищена мьютексом —
// конкурентний доступ до мапи в Go, на відміну від Python, панікує.
type Sampler struct {
	mu       sync.Mutex
	prev     map[int]prevSample
	now      func() float64
	readFile func(name string) ([]byte, error)
}

// NewSampler створює семплер із системним годинником і реальним /proc.
func NewSampler() *Sampler {
	base := time.Now()
	return &Sampler{
		prev:     make(map[int]prevSample),
		now:      func() float64 { return time.Since(base).Seconds() },
		readFile: os.ReadFile,
	}
}

// Sample — CPU%/RSS для pid; ok=false, якщо pid відсутній (havePID=false) або
// процес не читається (завершився) — тоді застарілий кеш для цього pid
// прибирається разом із ним.
func (s *Sampler) Sample(pid int, havePID bool) (Stats, bool) {
	if !havePID {
		return Stats{}, false
	}
	raw, ok := s.readRaw(pid)
	if !ok {
		s.mu.Lock()
		delete(s.prev, pid)
		s.mu.Unlock()
		return Stats{}, false
	}
	return s.compute(pid, raw), true
}

func (s *Sampler) compute(pid int, raw rawSample) Stats {
	now := s.now()
	s.mu.Lock()
	prev, hadPrev := s.prev[pid]
	s.prev[pid] = prevSample{t: now, units: raw.cpuUnits}
	s.mu.Unlock()

	cpuPercent := 0.0
	if hadPrev {
		if elapsed := now - prev.t; elapsed > 0 {
			delta := raw.cpuUnits - prev.units
			cpuPercent = math.Max(0.0, float64(delta)/raw.unitsPerSec/elapsed*100)
		}
	} else if raw.age != nil {
		if age, ok := raw.age(); ok && age > 0 {
			cpuPercent = math.Max(0.0, float64(raw.cpuUnits)/raw.unitsPerSec/age*100)
		}
	}

	return Stats{
		CPUPercent: pyRound1(cpuPercent),
		RSSMB:      pyRound1(float64(raw.rssRaw) / raw.rssDivisor),
	}
}

// pyRound1 — round(x, 1) з Python: коректно округлене ДЕСЯТКОВЕ значення до 1
// знака після коми, half-to-even, за ТОЧНИМ раціональним значенням x (не за
// похибкою від x*10 у float64 — на відміну від pyRound у wire/ts, тут крок
// округлення НЕ 1, і множення на 10 у float саме собі не точне). x тут завжди
// ≥0 (cpu_percent затиснутий у max(0,...), rss — з невід'ємного /proc).
func pyRound1(x float64) float64 {
	r := new(big.Rat).SetFloat64(x)
	if r == nil { // NaN/Inf — недосяжно для cpu_percent/rss_mb
		return x
	}
	num := new(big.Int).Mul(r.Num(), big.NewInt(10))
	rem := new(big.Int)
	q, rem := new(big.Int).QuoRem(num, r.Denom(), rem)
	twice := new(big.Int).Lsh(rem, 1)
	switch cmp := twice.Cmp(r.Denom()); {
	case cmp > 0:
		q.Add(q, big.NewInt(1))
	case cmp == 0 && q.Bit(0) == 1:
		q.Add(q, big.NewInt(1))
	}
	f, _ := new(big.Rat).SetFrac(q, big.NewInt(10)).Float64()
	return f
}
