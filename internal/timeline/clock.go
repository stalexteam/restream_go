package timeline

import "time"

// Clock — монотонний час у секундах).
type Clock interface {
	Now() float64
}

type systemClock struct{ base time.Time }

func (c systemClock) Now() float64 { return time.Since(c.base).Seconds() }

// SystemClock — прод-годинник: секунди від моменту створення.
func SystemClock() Clock { return systemClock{base: time.Now()} }
