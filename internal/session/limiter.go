package session

import "sync"

type Limiter struct {
	mu              sync.Mutex
	active, maximum int
}

func NewLimiter(maximum int) *Limiter { return &Limiter{maximum: maximum} }
func (limiter *Limiter) Acquire() bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.active >= limiter.maximum {
		return false
	}
	limiter.active++
	return true
}
func (limiter *Limiter) Release() {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.active > 0 {
		limiter.active--
	}
}
func (limiter *Limiter) Active() int {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	return limiter.active
}
