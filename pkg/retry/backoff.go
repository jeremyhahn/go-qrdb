// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

// Package retry provides retry logic with configurable backoff strategies.
package retry

import (
	"math"
	"math/rand"
	"time"
)

// Backoff defines the interface for backoff strategies.
type Backoff interface {
	// Next returns the next backoff duration for the given attempt number (0-indexed).
	Next(attempt int) time.Duration
}

// ExponentialBackoff implements exponential backoff with jitter.
type ExponentialBackoff struct {
	// InitialDelay is the delay for the first retry attempt
	InitialDelay time.Duration

	// MaxDelay is the maximum delay between retries
	MaxDelay time.Duration

	// Multiplier is the factor by which the delay increases (typically 2.0)
	Multiplier float64

	// Jitter adds randomization to prevent thundering herd.
	// Value between 0.0 (no jitter) and 1.0 (full jitter).
	Jitter float64
}

// Next calculates the next backoff duration with exponential growth and optional jitter.
func (b *ExponentialBackoff) Next(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}

	// Calculate exponential delay: initialDelay * multiplier^attempt
	delay := float64(b.InitialDelay) * math.Pow(b.Multiplier, float64(attempt))

	// Cap at max delay
	if delay > float64(b.MaxDelay) {
		delay = float64(b.MaxDelay)
	}

	// Apply jitter if configured
	if b.Jitter > 0 {
		// Jitter factor between (1 - jitter) and 1.0
		// This reduces the delay by up to jitter percentage
		jitterFactor := 1.0 - (b.Jitter * rand.Float64()) //#nosec G404 -- math/rand is acceptable for jitter, not security-sensitive
		delay *= jitterFactor
	}

	return time.Duration(delay)
}

// LinearBackoff implements linear backoff with optional jitter.
type LinearBackoff struct {
	// InitialDelay is the delay for the first retry attempt
	InitialDelay time.Duration

	// MaxDelay is the maximum delay between retries
	MaxDelay time.Duration

	// Increment is added to the delay on each attempt
	Increment time.Duration

	// Jitter adds randomization to prevent thundering herd.
	// Value between 0.0 (no jitter) and 1.0 (full jitter).
	Jitter float64
}

// Next calculates the next backoff duration with linear growth and optional jitter.
func (b *LinearBackoff) Next(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}

	// Calculate linear delay: initialDelay + (increment * attempt)
	delay := b.InitialDelay + (b.Increment * time.Duration(attempt))

	// Cap at max delay
	if delay > b.MaxDelay {
		delay = b.MaxDelay
	}

	// Apply jitter if configured
	if b.Jitter > 0 {
		jitterFactor := 1.0 - (b.Jitter * rand.Float64()) //#nosec G404 -- math/rand is acceptable for jitter, not security-sensitive
		delay = time.Duration(float64(delay) * jitterFactor)
	}

	return delay
}

// ConstantBackoff implements constant backoff with optional jitter.
type ConstantBackoff struct {
	// Delay is the constant delay between retries
	Delay time.Duration

	// Jitter adds randomization to prevent thundering herd.
	// Value between 0.0 (no jitter) and 1.0 (full jitter).
	Jitter float64
}

// Next returns the constant backoff duration with optional jitter.
func (b *ConstantBackoff) Next(attempt int) time.Duration {
	delay := b.Delay

	// Apply jitter if configured
	if b.Jitter > 0 {
		jitterFactor := 1.0 - (b.Jitter * rand.Float64()) //#nosec G404 -- math/rand is acceptable for jitter, not security-sensitive
		delay = time.Duration(float64(delay) * jitterFactor)
	}

	return delay
}

// DefaultExponentialBackoff returns a preconfigured exponential backoff
// with sensible defaults: 100ms initial, 10s max, 2x multiplier, 0.1 jitter.
func DefaultExponentialBackoff() *ExponentialBackoff {
	return &ExponentialBackoff{
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     10 * time.Second,
		Multiplier:   2.0,
		Jitter:       0.1,
	}
}

// DefaultLinearBackoff returns a preconfigured linear backoff
// with sensible defaults: 100ms initial, 5s max, 200ms increment, 0.1 jitter.
func DefaultLinearBackoff() *LinearBackoff {
	return &LinearBackoff{
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     5 * time.Second,
		Increment:    200 * time.Millisecond,
		Jitter:       0.1,
	}
}

// DefaultConstantBackoff returns a preconfigured constant backoff
// with sensible defaults: 500ms delay, 0.1 jitter.
func DefaultConstantBackoff() *ConstantBackoff {
	return &ConstantBackoff{
		Delay:  500 * time.Millisecond,
		Jitter: 0.1,
	}
}
