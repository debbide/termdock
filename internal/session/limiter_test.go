package session

import "testing"

func TestLimiter(t *testing.T) {
	limiter := NewLimiter(1)
	if !limiter.Acquire() || limiter.Acquire() {
		t.Fatal("limit not enforced")
	}
	limiter.Release()
	if !limiter.Acquire() {
		t.Fatal("slot was not released")
	}
}
