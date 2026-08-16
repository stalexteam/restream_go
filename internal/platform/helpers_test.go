package platform

// virtualClock — керований годинник для тестів.
type virtualClock struct{ t float64 }

func (c *virtualClock) Now() float64 { return c.t }
