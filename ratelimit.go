package main

import (
	"context"
	"sync"
	"time"
)

// RateLimiter implements a sliding window rate limiter
// Allows max N requests within a time window
type RateLimiter struct {
	maxRequests int
	window      time.Duration
	requests    []time.Time
	mu          sync.Mutex
}

// NewRateLimiter creates a new rate limiter
// maxRequests: maximum number of requests allowed in the window
// window: time window duration
func NewRateLimiter(maxRequests int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		maxRequests: maxRequests,
		window:      window,
		requests:    make([]time.Time, 0, maxRequests),
	}
}

// DefaultRateLimiter creates a rate limiter with Telegram's recommended limits
// 30 requests per 30 seconds
func DefaultRateLimiter() *RateLimiter {
	return NewRateLimiter(30, 30*time.Second)
}

// Wait blocks until a request can be made within rate limits
// Returns error if context is cancelled
func (rl *RateLimiter) Wait(ctx context.Context) error {
	for {
		rl.mu.Lock()
		now := time.Now()

		// Remove old requests outside the window
		cutoff := now.Add(-rl.window)
		newRequests := make([]time.Time, 0, len(rl.requests))
		for _, t := range rl.requests {
			if t.After(cutoff) {
				newRequests = append(newRequests, t)
			}
		}
		rl.requests = newRequests

		// Check if we can make a request
		if len(rl.requests) < rl.maxRequests {
			rl.requests = append(rl.requests, now)
			rl.mu.Unlock()
			return nil
		}

		// Calculate how long to wait until the oldest request expires
		oldestRequest := rl.requests[0]
		waitDuration := oldestRequest.Add(rl.window).Sub(now)
		rl.mu.Unlock()

		// Add small buffer to avoid race conditions
		waitDuration += 10 * time.Millisecond

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitDuration):
			// Continue loop to try again
		}
	}
}

// TryAcquire attempts to acquire a slot without blocking
// Returns true if successful, false if rate limited
func (rl *RateLimiter) TryAcquire() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()

	// Remove old requests outside the window
	cutoff := now.Add(-rl.window)
	newRequests := make([]time.Time, 0, len(rl.requests))
	for _, t := range rl.requests {
		if t.After(cutoff) {
			newRequests = append(newRequests, t)
		}
	}
	rl.requests = newRequests

	// Check if we can make a request
	if len(rl.requests) < rl.maxRequests {
		rl.requests = append(rl.requests, now)
		return true
	}

	return false
}

// Available returns the number of requests that can be made immediately
func (rl *RateLimiter) Available() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()

	// Count requests within the window
	cutoff := now.Add(-rl.window)
	count := 0
	for _, t := range rl.requests {
		if t.After(cutoff) {
			count++
		}
	}

	return rl.maxRequests - count
}
