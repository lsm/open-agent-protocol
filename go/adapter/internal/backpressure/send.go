package backpressure

import "time"

const DefaultStall = 30 * time.Second

type Outcome int

const (
	Sent Outcome = iota
	Closed
	Stalled
)

func Send[T any](queue chan<- T, value T, done <-chan struct{}, stall time.Duration) Outcome {
	select {
	case queue <- value:
		return Sent
	default:
	}
	if stall <= 0 {
		stall = DefaultStall
	}
	timer := time.NewTimer(stall)
	defer timer.Stop()
	select {
	case queue <- value:
		return Sent
	case <-done:
		return Closed
	case <-timer.C:
		return Stalled
	}
}
