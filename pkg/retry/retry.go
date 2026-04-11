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

package retry

import (
	"context"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
)

// Operation is a function that can be retried.
// It should return an error if the operation fails.
type Operation func(ctx context.Context) error

// Config configures retry behavior.
type Config struct {
	// MaxAttempts is the maximum number of attempts (including the initial attempt).
	// Must be >= 1. Default is 3.
	MaxAttempts int

	// Backoff strategy to use between retries.
	// If nil, DefaultExponentialBackoff is used.
	Backoff Backoff

	// OnRetry is an optional callback invoked before each retry attempt.
	// The first parameter is the attempt number (0-indexed), the second is the error
	// that caused the retry, and the third is the backoff duration before retry.
	OnRetry func(attempt int, err error, backoff time.Duration)

	// RetryableFunc is an optional function to determine if an error is retryable.
	// If nil, uses the QRDBError's IsRetryable() method.
	// Return true to retry, false to fail immediately.
	RetryableFunc func(err error) bool
}

// DefaultConfig returns a retry config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		MaxAttempts: 3,
		Backoff:     DefaultExponentialBackoff(),
	}
}

// Do executes the operation with retry logic according to the config.
// The operation is retried only if:
// - The error is retryable (according to RetryableFunc or QRDBError.IsRetryable())
// - MaxAttempts has not been exceeded
// - The context has not been canceled.
func Do(ctx context.Context, cfg *Config, op Operation) error {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 1
	}

	if cfg.Backoff == nil {
		cfg.Backoff = DefaultExponentialBackoff()
	}

	var lastErr error

	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		// Check context before attempting operation
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return errors.Wrap(lastErr, errors.ErrCanceled, "retry.Do")
			}
			return ctx.Err()
		default:
		}

		// Execute the operation
		err := op(ctx)
		if err == nil {
			// Success!
			return nil
		}

		lastErr = err

		// Check if we should retry
		if !shouldRetry(cfg, err) {
			// Error is not retryable
			return err
		}

		// Check if we have more attempts left
		if attempt+1 >= cfg.MaxAttempts {
			// No more attempts
			return err
		}

		// Calculate backoff duration
		backoffDuration := cfg.Backoff.Next(attempt)

		// Call retry callback if configured
		if cfg.OnRetry != nil {
			cfg.OnRetry(attempt, err, backoffDuration)
		}

		// Wait for backoff period or context cancellation
		select {
		case <-ctx.Done():
			return errors.Wrap(lastErr, errors.ErrCanceled, "retry.Do")
		case <-time.After(backoffDuration):
			// Continue to next attempt
		}
	}

	// This should not be reached, but return last error as fallback
	return lastErr
}

// shouldRetry determines if an error is retryable based on the config.
func shouldRetry(cfg *Config, err error) bool {
	if err == nil {
		return false
	}

	// Use custom retry function if provided
	if cfg.RetryableFunc != nil {
		return cfg.RetryableFunc(err)
	}

	// Default: use QRDB error's IsRetryable method
	return errors.IsRetryable(err)
}

// DoWithBackoff is a convenience function that retries an operation with the specified
// max attempts and backoff strategy.
func DoWithBackoff(ctx context.Context, maxAttempts int, backoff Backoff, op Operation) error {
	cfg := &Config{
		MaxAttempts: maxAttempts,
		Backoff:     backoff,
	}
	return Do(ctx, cfg, op)
}

// DoExponential is a convenience function that retries an operation with exponential backoff.
func DoExponential(ctx context.Context, maxAttempts int, op Operation) error {
	return DoWithBackoff(ctx, maxAttempts, DefaultExponentialBackoff(), op)
}

// DoLinear is a convenience function that retries an operation with linear backoff.
func DoLinear(ctx context.Context, maxAttempts int, op Operation) error {
	return DoWithBackoff(ctx, maxAttempts, DefaultLinearBackoff(), op)
}

// DoConstant is a convenience function that retries an operation with constant backoff.
func DoConstant(ctx context.Context, maxAttempts int, op Operation) error {
	return DoWithBackoff(ctx, maxAttempts, DefaultConstantBackoff(), op)
}
