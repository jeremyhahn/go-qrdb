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

package retry_test

import (
	"context"
	"fmt"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/retry"
)

// ExampleDo demonstrates basic retry usage with default config.
func ExampleDo() {
	ctx := context.Background()
	cfg := retry.DefaultConfig()

	attempts := 0
	err := retry.Do(ctx, cfg, func(ctx context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New(errors.ErrTimeout, "example.op", "temporary failure")
		}
		return nil
	})
	if err != nil {
		fmt.Printf("Failed after retries: %v\n", err)
		return
	}

	fmt.Printf("Succeeded after %d attempts\n", attempts)
	// Output: Succeeded after 3 attempts
}

// ExampleDoExponential demonstrates exponential backoff retry.
func ExampleDoExponential() {
	ctx := context.Background()

	err := retry.DoExponential(ctx, 3, func(ctx context.Context) error {
		// Simulate an operation that might fail
		return errors.New(errors.ErrUnavailable, "service.call", "service unavailable")
	})
	if err != nil {
		fmt.Printf("Operation failed: %v\n", err)
	}
	// Output: Operation failed: service.call: unavailable: service unavailable
}

// ExampleDo_withOnRetryCallback demonstrates using the OnRetry callback.
func ExampleDo_withOnRetryCallback() {
	ctx := context.Background()

	cfg := &retry.Config{
		MaxAttempts: 3,
		Backoff: &retry.ExponentialBackoff{
			InitialDelay: 100 * time.Millisecond,
			MaxDelay:     10 * time.Second,
			Multiplier:   2.0,
			Jitter:       0, // No jitter for predictable output
		},
		OnRetry: func(attempt int, err error, backoff time.Duration) {
			fmt.Printf("Retry attempt %d after %v: %v\n", attempt+1, backoff, err)
		},
	}

	attempts := 0
	_ = retry.Do(ctx, cfg, func(ctx context.Context) error {
		attempts++
		if attempts < 2 {
			return errors.New(errors.ErrTimeout, "op", "timeout")
		}
		return nil
	})

	// Output:
	// Retry attempt 1 after 100ms: op: timeout: timeout
}

// ExampleDo_withCustomRetryableFunc demonstrates custom retry logic.
func ExampleDo_withCustomRetryableFunc() {
	ctx := context.Background()

	cfg := &retry.Config{
		MaxAttempts: 3,
		Backoff: &retry.ConstantBackoff{
			Delay:  100 * time.Millisecond,
			Jitter: 0,
		},
		RetryableFunc: func(err error) bool {
			// Only retry timeout errors
			return errors.Is(err, errors.ErrTimeout)
		},
	}

	// This will retry (timeout is retryable)
	err1 := retry.Do(ctx, cfg, func(ctx context.Context) error {
		return errors.New(errors.ErrTimeout, "op", "timeout")
	})
	fmt.Printf("Timeout error retried: %v\n", err1 != nil)

	// This won't retry (not found is not retryable per our custom func)
	attempts := 0
	_ = retry.Do(ctx, cfg, func(ctx context.Context) error {
		attempts++
		return errors.New(errors.ErrNotFound, "op", "not found")
	})
	fmt.Printf("Not found error attempts: %d\n", attempts)

	// Output:
	// Timeout error retried: true
	// Not found error attempts: 1
}

// ExampleExponentialBackoff demonstrates exponential backoff calculation.
func ExampleExponentialBackoff() {
	backoff := &retry.ExponentialBackoff{
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     1 * time.Second,
		Multiplier:   2.0,
		Jitter:       0, // No jitter for predictable output
	}

	for i := 0; i < 5; i++ {
		delay := backoff.Next(i)
		fmt.Printf("Attempt %d: %v\n", i, delay)
	}

	// Output:
	// Attempt 0: 100ms
	// Attempt 1: 200ms
	// Attempt 2: 400ms
	// Attempt 3: 800ms
	// Attempt 4: 1s
}

// ExampleLinearBackoff demonstrates linear backoff calculation.
func ExampleLinearBackoff() {
	backoff := &retry.LinearBackoff{
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     500 * time.Millisecond,
		Increment:    100 * time.Millisecond,
		Jitter:       0,
	}

	for i := 0; i < 6; i++ {
		delay := backoff.Next(i)
		fmt.Printf("Attempt %d: %v\n", i, delay)
	}

	// Output:
	// Attempt 0: 100ms
	// Attempt 1: 200ms
	// Attempt 2: 300ms
	// Attempt 3: 400ms
	// Attempt 4: 500ms
	// Attempt 5: 500ms
}

// ExampleConstantBackoff demonstrates constant backoff.
func ExampleConstantBackoff() {
	backoff := &retry.ConstantBackoff{
		Delay:  250 * time.Millisecond,
		Jitter: 0,
	}

	for i := 0; i < 3; i++ {
		delay := backoff.Next(i)
		fmt.Printf("Attempt %d: %v\n", i, delay)
	}

	// Output:
	// Attempt 0: 250ms
	// Attempt 1: 250ms
	// Attempt 2: 250ms
}
