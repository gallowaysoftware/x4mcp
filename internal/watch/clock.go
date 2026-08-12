package watch

import "time"

// clock is the seam that makes the poll loop testable without waiting for real
// seconds. Only the two operations the loop needs are on it: what time is it,
// and wake me in d.
type clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
