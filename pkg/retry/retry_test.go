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
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestExponentialBackoff_Next(t *testing.T) {
	tests := []struct {
		name    string
		backoff *ExponentialBackoff
		attempt int
		wantMin time.Duration
		wantMax time.Duration
	}{
		{
			name: "first attempt without jitter",
			backoff: &ExponentialBackoff{
				InitialDelay: 100 * time.Millisecond,
				MaxDelay:     10 * time.Second,
				Multiplier:   2.0,
				Jitter:       0,
			},
			attempt: 0,
			wantMin: 100 * time.Millisecond,
			wantMax: 100 * time.Millisecond,
		},
		{
			name: "second attempt without jitter",
			backoff: &ExponentialBackoff{
				InitialDelay: 100 * time.Millisecond,
				MaxDelay:     10 * time.Second,
				Multiplier:   2.0,
				Jitter:       0,
			},
			attempt: 1,
			wantMin: 200 * time.Millisecond,
			wantMax: 200 * time.Millisecond,
		},
		{
			name: "third attempt without jitter",
			backoff: &ExponentialBackoff{
				InitialDelay: 100 * time.Millisecond,
				MaxDelay:     10 * time.Second,
				Multiplier:   2.0,
				Jitter:       0,
			},
			attempt: 2,
			wantMin: 400 * time.Millisecond,
			wantMax: 400 * time.Millisecond,
		},
		{
			name: "max delay reached",
			backoff: &ExponentialBackoff{
				InitialDelay: 100 * time.Millisecond,
				MaxDelay:     500 * time.Millisecond,
				Multiplier:   2.0,
				Jitter:       0,
			},
			attempt: 10,
			wantMin: 500 * time.Millisecond,
			wantMax: 500 * time.Millisecond,
		},
		{
			name: "with jitter",
			backoff: &ExponentialBackoff{
				InitialDelay: 100 * time.Millisecond,
				MaxDelay:     10 * time.Second,
				Multiplier:   2.0,
				Jitter:       0.5, // 50% jitter
			},
			attempt: 0,
			wantMin: 50 * time.Millisecond,  // 100ms * (1 - 0.5)
			wantMax: 100 * time.Millisecond, // 100ms * 1.0
		},
		{
			name: "negative attempt defaults to 0",
			backoff: &ExponentialBackoff{
				InitialDelay: 100 * time.Millisecond,
				MaxDelay:     10 * time.Second,
				Multiplier:   2.0,
				Jitter:       0,
			},
			attempt: -1,
			wantMin: 100 * time.Millisecond,
			wantMax: 100 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Run multiple times to account for jitter randomness
			for i := 0; i < 10; i++ {
				got := tt.backoff.Next(tt.attempt)
				assert.GreaterOrEqual(t, got, tt.wantMin, "backoff duration too small")
				assert.LessOrEqual(t, got, tt.wantMax, "backoff duration too large")
			}
		})
	}
}

func TestLinearBackoff_Next(t *testing.T) {
	tests := []struct {
		name    string
		backoff *LinearBackoff
		attempt int
		wantMin time.Duration
		wantMax time.Duration
	}{
		{
			name: "first attempt without jitter",
			backoff: &LinearBackoff{
				InitialDelay: 100 * time.Millisecond,
				MaxDelay:     5 * time.Second,
				Increment:    50 * time.Millisecond,
				Jitter:       0,
			},
			attempt: 0,
			wantMin: 100 * time.Millisecond,
			wantMax: 100 * time.Millisecond,
		},
		{
			name: "second attempt without jitter",
			backoff: &LinearBackoff{
				InitialDelay: 100 * time.Millisecond,
				MaxDelay:     5 * time.Second,
				Increment:    50 * time.Millisecond,
				Jitter:       0,
			},
			attempt: 1,
			wantMin: 150 * time.Millisecond,
			wantMax: 150 * time.Millisecond,
		},
		{
			name: "max delay reached",
			backoff: &LinearBackoff{
				InitialDelay: 100 * time.Millisecond,
				MaxDelay:     500 * time.Millisecond,
				Increment:    100 * time.Millisecond,
				Jitter:       0,
			},
			attempt: 10,
			wantMin: 500 * time.Millisecond,
			wantMax: 500 * time.Millisecond,
		},
		{
			name: "with jitter",
			backoff: &LinearBackoff{
				InitialDelay: 100 * time.Millisecond,
				MaxDelay:     5 * time.Second,
				Increment:    50 * time.Millisecond,
				Jitter:       0.5,
			},
			attempt: 0,
			wantMin: 50 * time.Millisecond,
			wantMax: 100 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i := 0; i < 10; i++ {
				got := tt.backoff.Next(tt.attempt)
				assert.GreaterOrEqual(t, got, tt.wantMin, "backoff duration too small")
				assert.LessOrEqual(t, got, tt.wantMax, "backoff duration too large")
			}
		})
	}
}

func TestConstantBackoff_Next(t *testing.T) {
	tests := []struct {
		name    string
		backoff *ConstantBackoff
		attempt int
		wantMin time.Duration
		wantMax time.Duration
	}{
		{
			name: "constant delay without jitter",
			backoff: &ConstantBackoff{
				Delay:  200 * time.Millisecond,
				Jitter: 0,
			},
			attempt: 0,
			wantMin: 200 * time.Millisecond,
			wantMax: 200 * time.Millisecond,
		},
		{
			name: "constant delay with jitter",
			backoff: &ConstantBackoff{
				Delay:  200 * time.Millisecond,
				Jitter: 0.5,
			},
			attempt: 5,
			wantMin: 100 * time.Millisecond,
			wantMax: 200 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i := 0; i < 10; i++ {
				got := tt.backoff.Next(tt.attempt)
				assert.GreaterOrEqual(t, got, tt.wantMin, "backoff duration too small")
				assert.LessOrEqual(t, got, tt.wantMax, "backoff duration too large")
			}
		})
	}
}

func TestDo_SuccessFirstAttempt(t *testing.T) {
	ctx := context.Background()
	cfg := DefaultConfig()

	callCount := 0
	op := func(ctx context.Context) error {
		callCount++
		return nil
	}

	err := Do(ctx, cfg, op)
	assert.NoError(t, err)
	assert.Equal(t, 1, callCount, "operation should be called once")
}

func TestDo_SuccessAfterRetries(t *testing.T) {
	ctx := context.Background()
	cfg := &Config{
		MaxAttempts: 3,
		Backoff: &ConstantBackoff{
			Delay:  10 * time.Millisecond,
			Jitter: 0,
		},
	}

	callCount := 0
	op := func(ctx context.Context) error {
		callCount++
		if callCount < 3 {
			return dberrors.New(dberrors.ErrTimeout, "test.op", "temporary failure")
		}
		return nil
	}

	start := time.Now()
	err := Do(ctx, cfg, op)
	elapsed := time.Since(start)

	assert.NoError(t, err)
	assert.Equal(t, 3, callCount, "operation should be called three times")
	// Should have waited ~20ms (2 retries * 10ms)
	assert.GreaterOrEqual(t, elapsed, 20*time.Millisecond, "should wait for backoff")
}

func TestDo_NonRetryableError(t *testing.T) {
	ctx := context.Background()
	cfg := DefaultConfig()

	callCount := 0
	expectedErr := dberrors.New(dberrors.ErrNotFound, "test.op", "resource not found")

	op := func(ctx context.Context) error {
		callCount++
		return expectedErr
	}

	err := Do(ctx, cfg, op)
	assert.Error(t, err)
	assert.Equal(t, 1, callCount, "non-retryable error should not retry")
	assert.True(t, dberrors.Is(err, dberrors.ErrNotFound))
}

// A user update that lost a race is a retryable ErrConflict, so the default
// classifier retries it with no RetryableFunc (go-qrdb#938).
func TestDo_UserConcurrentUpdateIsRetriedByDefault(t *testing.T) {
	cfg := &Config{MaxAttempts: 3, Backoff: &ConstantBackoff{}}
	callCount := 0
	err := Do(context.Background(), cfg, func(ctx context.Context) error {
		callCount++
		if callCount < 3 {
			return &transport.UserConcurrentUpdateError{ID: 7}
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, callCount, "a lost race should be retried until it applies")
}

func TestDo_UserConcurrentUpdateExhaustsAsAConflict(t *testing.T) {
	cfg := &Config{MaxAttempts: 2, Backoff: &ConstantBackoff{}}
	callCount := 0
	err := Do(context.Background(), cfg, func(ctx context.Context) error {
		callCount++
		return &transport.UserConcurrentUpdateError{ID: 7}
	})
	assert.Equal(t, 2, callCount, "every attempt should run")
	assert.True(t, dberrors.Is(err, dberrors.ErrConflict))
	var lost *transport.UserConcurrentUpdateError
	assert.True(t, errors.As(err, &lost), "the final error should still be the lost race, got %v", err)
}

func TestDo_MaxAttemptsExceeded(t *testing.T) {
	ctx := context.Background()
	cfg := &Config{
		MaxAttempts: 3,
		Backoff: &ConstantBackoff{
			Delay:  1 * time.Millisecond,
			Jitter: 0,
		},
	}

	callCount := 0
	expectedErr := dberrors.New(dberrors.ErrTimeout, "test.op", "timeout")

	op := func(ctx context.Context) error {
		callCount++
		return expectedErr
	}

	err := Do(ctx, cfg, op)
	assert.Error(t, err)
	assert.Equal(t, 3, callCount, "should retry up to max attempts")
	assert.True(t, dberrors.Is(err, dberrors.ErrTimeout))
}

// A context cancelled DURING an attempt stops the loop before the next one.
//
// The cancellation is driven from inside the operation rather than by a
// sleeping goroutine, which makes the attempt count exact instead of a race
// the scheduler can win: by the time op returns, ctx is already done, so Do's
// backoff select takes its ctx.Done() branch on the first poll. The previous
// version slept 10ms against a 100ms backoff and asserted "at most 2 attempts",
// which passes for the wrong reasons under load (go-qrdb#88).
func TestDo_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &Config{
		MaxAttempts: 10,
		Backoff: &ConstantBackoff{
			Delay:  100 * time.Millisecond,
			Jitter: 0,
		},
	}

	callCount := 0
	op := func(_ context.Context) error {
		callCount++
		cancel()
		return dberrors.New(dberrors.ErrTimeout, "test.op", "timeout")
	}

	err := Do(ctx, cfg, op)

	require.Error(t, err)
	assert.Equal(t, 1, callCount,
		"no attempt may follow a cancelled context")

	// The returned error carries ErrCanceled -- the code retry.Do passes,
	// saying "I stopped because the context ended, not because attempts ran
	// out" (go-qrdb#187).
	//
	// This block previously asserted the OPPOSITE, with a comment explaining
	// that errors.Wrap discards the code and that "a caller therefore cannot
	// distinguish 'gave up because the context was cancelled' from 'gave up
	// after MaxAttempts' by code alone." That is an exact statement of the
	// defect, pinned as a test rather than filed -- which is how it stayed
	// live. A test written to match observed behaviour freezes it; when the
	// behaviour is wrong, the test is what makes it permanent.
	var qe *dberrors.QRDBError
	require.True(t, dberrors.As(err, &qe))
	assert.Equal(t, "retry.Do", qe.Op, "the giving-up frame must name retry.Do")
	assert.True(t, dberrors.Is(err, dberrors.ErrCanceled),
		"Do must report WHY it stopped, got %v", err)
	assert.False(t, qe.IsRetryable(),
		"a cancellation must not be retryable, got %v", err)

	// The operation's own error is not lost, only no longer what a single Is
	// call answers about: it is still reachable through the chain.
	var inner *dberrors.QRDBError
	require.True(t, errors.As(qe.Unwrap(), &inner))
	assert.Equal(t, dberrors.ErrTimeout, inner.Code,
		"the operation's own classification must survive in the chain")
}

// A real, timer-driven DEADLINE stops the loop.
//
// Three versions of this test have now been wrong in three different ways, so
// the reasoning is worth keeping. It began as a 30ms deadline against a 20ms
// backoff asserting "at most 2 attempts" — ten milliseconds of margin, which a
// loaded runner does not honour. It then became context.WithTimeout plus
// cancel(), which reports context.Canceled and would have passed if deadline
// handling specifically broke. It then became a synthetic context that
// reported DeadlineExceeded from Err while reporting no deadline from
// Deadline — a fake supplying the very value the test asserted, and a
// context.Context contract violation besides (go-qrdb#88).
//
// This one uses a REAL context.WithTimeout, and makes the attempt count
// deterministic by having the operation BLOCK on ctx.Done() rather than by
// racing the timer. Scheduling therefore affects how long the test takes, not
// what it observes: the second attempt cannot return until the deadline has
// genuinely fired, so Do always stops there. The deadline is short enough to
// keep the suite quick and long enough that nothing depends on winning a race.
func TestDo_ContextTimeout(t *testing.T) {
	// The bubble's clock moves only when every goroutine in it is durably
	// blocked, so the deadline cannot expire while attempt 1 or the backoff
	// runs, however slowly the runner schedules them: it expires exactly
	// while attempt 2 waits on it. The deadline is still the context's own
	// timer, so the context ends by DEADLINE.
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		cfg := &Config{
			MaxAttempts: 10,
			Backoff: &ConstantBackoff{
				Delay:  time.Millisecond,
				Jitter: 0,
			},
		}

		var callCount atomic.Int32
		op := func(opCtx context.Context) error {
			if callCount.Add(1) == 2 {
				// Blocks until the bubble's clock reaches the deadline.
				<-opCtx.Done()
			}
			return dberrors.New(dberrors.ErrUnavailable, "test.op", "service unavailable")
		}

		done := make(chan error, 1)
		go func() { done <- Do(ctx, cfg, op) }()

		var err error
		select {
		case err = <-done:
		case <-time.After(returnBound):
			t.Fatalf("Do did not return within %v of a %v deadline expiring",
				returnBound, 50*time.Millisecond)
		}

		require.Error(t, err)
		assert.Equal(t, int32(2), callCount.Load(),
			"the loop must stop on the attempt that saw the deadline expire, "+
				"not continue to MaxAttempts")
		assert.ErrorIs(t, ctx.Err(), context.DeadlineExceeded,
			"the context must have expired by DEADLINE, not by cancellation")

		// See TestDo_ContextCancellation: the wrap now applies retry.Do's own code.
		var qe *dberrors.QRDBError
		require.True(t, dberrors.As(err, &qe))
		assert.Equal(t, "retry.Do", qe.Op)
		assert.True(t, dberrors.Is(err, dberrors.ErrCanceled),
			"Do must report WHY it stopped, got %v", err)
		assert.False(t, qe.IsRetryable(),
			"a deadline-driven stop must not come back retryable, got %v", err)

		// ErrUnavailable IS retryable, so this is the case that matters most: the
		// old inheritance would have marked the whole thing retryable and invited
		// a caller to retry a context that has already expired.
		var inner *dberrors.QRDBError
		require.True(t, errors.As(qe.Unwrap(), &inner))
		assert.Equal(t, dberrors.ErrUnavailable, inner.Code,
			"the operation's own classification must survive in the chain")
		assert.True(t, inner.IsRetryable(),
			"precondition: the inner error must be retryable, or this proves nothing")
	})
}

// A context that is ALREADY done is not attempted at all.
//
// This is Do's pre-attempt guard, distinct from the two cases above: with no
// previous error to wrap it returns ctx.Err() unchanged, and the operation is
// never called.
func TestDo_ContextAlreadyDone_NeverCallsTheOperation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	callCount := 0
	err := Do(ctx, &Config{MaxAttempts: 5, Backoff: &ConstantBackoff{Delay: time.Millisecond}},
		func(_ context.Context) error {
			callCount++
			return nil
		})

	require.Error(t, err)
	assert.Equal(t, 0, callCount, "an already-cancelled context must not be attempted")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestDo_OnRetryCallback(t *testing.T) {
	ctx := context.Background()

	var retryAttempts []int
	var retryErrors []error
	var retryBackoffs []time.Duration

	cfg := &Config{
		MaxAttempts: 3,
		Backoff: &ConstantBackoff{
			Delay:  5 * time.Millisecond,
			Jitter: 0,
		},
		OnRetry: func(attempt int, err error, backoff time.Duration) {
			retryAttempts = append(retryAttempts, attempt)
			retryErrors = append(retryErrors, err)
			retryBackoffs = append(retryBackoffs, backoff)
		},
	}

	callCount := 0
	op := func(ctx context.Context) error {
		callCount++
		if callCount < 3 {
			return dberrors.New(dberrors.ErrTimeout, "test.op", "timeout")
		}
		return nil
	}

	err := Do(ctx, cfg, op)
	assert.NoError(t, err)
	assert.Equal(t, 3, callCount)
	assert.Equal(t, []int{0, 1}, retryAttempts, "callback should be called for each retry")
	assert.Len(t, retryErrors, 2)
	assert.Len(t, retryBackoffs, 2)
}

func TestDo_CustomRetryableFunc(t *testing.T) {
	ctx := context.Background()

	customErr := errors.New("custom error")
	callCount := 0

	cfg := &Config{
		MaxAttempts: 3,
		Backoff: &ConstantBackoff{
			Delay:  1 * time.Millisecond,
			Jitter: 0,
		},
		RetryableFunc: func(err error) bool {
			// Retry only on our custom error.
			return errors.Is(err, customErr)
		},
	}

	op := func(ctx context.Context) error {
		callCount++
		return customErr
	}

	err := Do(ctx, cfg, op)
	assert.Error(t, err)
	assert.Equal(t, 3, callCount, "should retry custom error")
}

func TestDo_CustomRetryableFuncNonRetryable(t *testing.T) {
	ctx := context.Background()

	customErr := errors.New("custom error")
	callCount := 0

	cfg := &Config{
		MaxAttempts: 3,
		Backoff: &ConstantBackoff{
			Delay:  1 * time.Millisecond,
			Jitter: 0,
		},
		RetryableFunc: func(err error) bool {
			return false // Never retry
		},
	}

	op := func(ctx context.Context) error {
		callCount++
		return customErr
	}

	err := Do(ctx, cfg, op)
	assert.Error(t, err)
	assert.Equal(t, 1, callCount, "should not retry when custom func returns false")
}

func TestDo_NilConfig(t *testing.T) {
	ctx := context.Background()

	callCount := 0
	op := func(ctx context.Context) error {
		callCount++
		return nil
	}

	err := Do(ctx, nil, op)
	assert.NoError(t, err)
	assert.Equal(t, 1, callCount, "should use default config")
}

func TestDo_ZeroMaxAttempts(t *testing.T) {
	ctx := context.Background()
	cfg := &Config{
		MaxAttempts: 0, // Invalid, should default to 1
		Backoff:     DefaultConstantBackoff(),
	}

	callCount := 0
	op := func(ctx context.Context) error {
		callCount++
		return nil
	}

	err := Do(ctx, cfg, op)
	assert.NoError(t, err)
	assert.Equal(t, 1, callCount, "zero max attempts should default to 1")
}

func TestDoExponential(t *testing.T) {
	ctx := context.Background()

	callCount := 0
	op := func(ctx context.Context) error {
		callCount++
		if callCount < 2 {
			return dberrors.New(dberrors.ErrTimeout, "test.op", "timeout")
		}
		return nil
	}

	err := DoExponential(ctx, 3, op)
	assert.NoError(t, err)
	assert.Equal(t, 2, callCount)
}

func TestDoLinear(t *testing.T) {
	ctx := context.Background()

	callCount := 0
	op := func(ctx context.Context) error {
		callCount++
		if callCount < 2 {
			return dberrors.New(dberrors.ErrUnavailable, "test.op", "unavailable")
		}
		return nil
	}

	err := DoLinear(ctx, 3, op)
	assert.NoError(t, err)
	assert.Equal(t, 2, callCount)
}

func TestDoConstant(t *testing.T) {
	ctx := context.Background()

	callCount := 0
	op := func(ctx context.Context) error {
		callCount++
		if callCount < 2 {
			return dberrors.New(dberrors.ErrLeaderNotAvailable, "test.op", "no leader")
		}
		return nil
	}

	err := DoConstant(ctx, 3, op)
	assert.NoError(t, err)
	assert.Equal(t, 2, callCount)
}

func TestDefaultBackoffs(t *testing.T) {
	t.Run("DefaultExponentialBackoff", func(t *testing.T) {
		b := DefaultExponentialBackoff()
		require.NotNil(t, b)
		assert.Equal(t, 100*time.Millisecond, b.InitialDelay)
		assert.Equal(t, 10*time.Second, b.MaxDelay)
		assert.Equal(t, 2.0, b.Multiplier)
		assert.Equal(t, 0.1, b.Jitter)
	})

	t.Run("DefaultLinearBackoff", func(t *testing.T) {
		b := DefaultLinearBackoff()
		require.NotNil(t, b)
		assert.Equal(t, 100*time.Millisecond, b.InitialDelay)
		assert.Equal(t, 5*time.Second, b.MaxDelay)
		assert.Equal(t, 200*time.Millisecond, b.Increment)
		assert.Equal(t, 0.1, b.Jitter)
	})

	t.Run("DefaultConstantBackoff", func(t *testing.T) {
		b := DefaultConstantBackoff()
		require.NotNil(t, b)
		assert.Equal(t, 500*time.Millisecond, b.Delay)
		assert.Equal(t, 0.1, b.Jitter)
	})

	t.Run("DefaultConfig", func(t *testing.T) {
		cfg := DefaultConfig()
		require.NotNil(t, cfg)
		assert.Equal(t, 3, cfg.MaxAttempts)
		assert.NotNil(t, cfg.Backoff)
	})
}

// Benchmark tests.
func BenchmarkExponentialBackoff_Next(b *testing.B) {
	backoff := DefaultExponentialBackoff()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		backoff.Next(i % 10)
	}
}

func BenchmarkLinearBackoff_Next(b *testing.B) {
	backoff := DefaultLinearBackoff()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		backoff.Next(i % 10)
	}
}

func BenchmarkConstantBackoff_Next(b *testing.B) {
	backoff := DefaultConstantBackoff()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		backoff.Next(i)
	}
}

func BenchmarkDo_NoRetries(b *testing.B) {
	ctx := context.Background()
	cfg := DefaultConfig()
	op := func(ctx context.Context) error {
		return nil
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Do(ctx, cfg, op)
	}
}

func BenchmarkDo_WithRetries(b *testing.B) {
	ctx := context.Background()
	cfg := &Config{
		MaxAttempts: 3,
		Backoff: &ConstantBackoff{
			Delay:  1 * time.Nanosecond, // Minimal delay for benchmark
			Jitter: 0,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		callCount := 0
		op := func(ctx context.Context) error {
			callCount++
			if callCount < 2 {
				return dberrors.New(dberrors.ErrTimeout, "bench.op", "timeout")
			}
			return nil
		}
		_ = Do(ctx, cfg, op)
	}
}

// Additional edge case tests for 90%+ coverage

func TestDo_ContextCanceledBeforeFirstAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	cfg := DefaultConfig()

	callCount := 0
	op := func(ctx context.Context) error {
		callCount++
		return nil
	}

	err := Do(ctx, cfg, op)
	assert.Error(t, err)
	assert.Equal(t, 0, callCount, "operation should not be called when context is already canceled")
}

// A cancellation arriving around the backoff wait ends it.
//
// Distinct from TestDo_ContextCancellation, where the context is already done
// before Do reaches the select. Here the cancel is issued concurrently with
// Do entering it — whether the select had begun waiting is not synchronised,
// and the guarantee asserted does not need it to be.
//
// Two constraints pull against each other here and both have to hold. The
// backoff must be long enough that a second attempt cannot happen however late
// the cancel lands — otherwise the attempt count is a coin flip, which is the
// flake this issue is about. But a long backoff must not become the test's
// runtime when the behaviour REGRESSES: asserting elapsed time after Do
// returns bounds nothing, because a Do that stopped honouring cancellation
// simply sleeps out the timer first.
//
// So the backoff is long AND Do runs on its own goroutine under a bound this
// test enforces. A regression fails in bounded time with a diagnostic instead
// of tying up a CI runner (go-qrdb#88).
func TestDo_ContextCanceledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &Config{
		MaxAttempts: 10,
		Backoff: &ConstantBackoff{
			Delay:  time.Hour,
			Jitter: 0,
		},
		// Fires immediately before Do enters its backoff select. The cancel
		// therefore lands either just before that select or while it waits —
		// which of the two is not synchronised here, and the assertion does
		// not depend on it: either way a cancelled context must end the
		// backoff instead of sleeping through it.
		OnRetry: func(_ int, _ error, _ time.Duration) {
			go cancel()
		},
	}

	var callCount atomic.Int32
	op := func(_ context.Context) error {
		callCount.Add(1)
		return dberrors.New(dberrors.ErrTimeout, "test.op", "timeout")
	}

	done := make(chan error, 1)
	go func() { done <- Do(ctx, cfg, op) }()

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(returnBound):
		t.Fatalf("Do did not return within %v: it is waiting out the %v backoff "+
			"instead of returning on cancellation", returnBound, time.Hour)
	}

	assert.Equal(t, int32(1), callCount.Load(),
		"a cancelled context must end the backoff instead of sleeping through it")
}

// returnBound is how long a test waits for a call that should return
// immediately. Generous enough that a loaded runner never trips it, and short
// enough that a genuine hang is reported rather than waited out.
const returnBound = 30 * time.Second

func TestDo_NilBackoff(t *testing.T) {
	ctx := context.Background()
	cfg := &Config{
		MaxAttempts: 2,
		Backoff:     nil, // Should use default
	}

	callCount := 0
	op := func(ctx context.Context) error {
		callCount++
		if callCount < 2 {
			return dberrors.New(dberrors.ErrTimeout, "test.op", "timeout")
		}
		return nil
	}

	err := Do(ctx, cfg, op)
	assert.NoError(t, err)
	assert.Equal(t, 2, callCount)
}

func TestDo_NegativeMaxAttempts(t *testing.T) {
	ctx := context.Background()
	cfg := &Config{
		MaxAttempts: -5, // Invalid, should default to 1
		Backoff:     DefaultConstantBackoff(),
	}

	callCount := 0
	op := func(ctx context.Context) error {
		callCount++
		return nil
	}

	err := Do(ctx, cfg, op)
	assert.NoError(t, err)
	assert.Equal(t, 1, callCount, "negative max attempts should default to 1")
}

func TestShouldRetry_NilError(t *testing.T) {
	cfg := DefaultConfig()
	result := shouldRetry(cfg, nil)
	assert.False(t, result, "nil error should not be retryable")
}

func TestShouldRetry_WithCustomFunc(t *testing.T) {
	cfg := &Config{
		RetryableFunc: func(err error) bool {
			return err.Error() == "retry me"
		},
	}

	t.Run("retryable error", func(t *testing.T) {
		err := errors.New("retry me")
		result := shouldRetry(cfg, err)
		assert.True(t, result)
	})

	t.Run("non-retryable error", func(t *testing.T) {
		err := errors.New("don't retry")
		result := shouldRetry(cfg, err)
		assert.False(t, result)
	})
}

func TestShouldRetry_WithQRDBError(t *testing.T) {
	cfg := &Config{
		RetryableFunc: nil, // Use default QRDB error check
	}

	t.Run("retryable QRDB error", func(t *testing.T) {
		err := dberrors.New(dberrors.ErrTimeout, "test", "timeout")
		result := shouldRetry(cfg, err)
		assert.True(t, result)
	})

	t.Run("non-retryable QRDB error", func(t *testing.T) {
		err := dberrors.New(dberrors.ErrInvalidInput, "test", "bad input")
		result := shouldRetry(cfg, err)
		assert.False(t, result)
	})

	t.Run("standard error", func(t *testing.T) {
		err := errors.New("standard error")
		result := shouldRetry(cfg, err)
		assert.False(t, result, "standard errors should not be retryable")
	})
}

func TestExponentialBackoff_EdgeCases(t *testing.T) {
	t.Run("zero jitter", func(t *testing.T) {
		backoff := &ExponentialBackoff{
			InitialDelay: 100 * time.Millisecond,
			MaxDelay:     1 * time.Second,
			Multiplier:   2.0,
			Jitter:       0,
		}

		// Multiple calls should return same value
		d1 := backoff.Next(0)
		d2 := backoff.Next(0)
		assert.Equal(t, d1, d2)
	})

	t.Run("full jitter", func(t *testing.T) {
		backoff := &ExponentialBackoff{
			InitialDelay: 100 * time.Millisecond,
			MaxDelay:     1 * time.Second,
			Multiplier:   2.0,
			Jitter:       1.0, // 100% jitter
		}

		// Should vary between 0 and InitialDelay
		d := backoff.Next(0)
		assert.GreaterOrEqual(t, d, time.Duration(0))
		assert.LessOrEqual(t, d, 100*time.Millisecond)
	})

	t.Run("very high attempt number", func(t *testing.T) {
		backoff := &ExponentialBackoff{
			InitialDelay: 10 * time.Millisecond,
			MaxDelay:     1 * time.Second,
			Multiplier:   2.0,
			Jitter:       0,
		}

		// Should cap at MaxDelay
		d := backoff.Next(100)
		assert.Equal(t, 1*time.Second, d)
	})

	t.Run("multiplier of 1", func(t *testing.T) {
		backoff := &ExponentialBackoff{
			InitialDelay: 100 * time.Millisecond,
			MaxDelay:     1 * time.Second,
			Multiplier:   1.0,
		}

		// Should stay constant
		d1 := backoff.Next(0)
		d2 := backoff.Next(5)
		d3 := backoff.Next(10)
		assert.Equal(t, d1, d2)
		assert.Equal(t, d2, d3)
	})
}

func TestLinearBackoff_EdgeCases(t *testing.T) {
	t.Run("zero increment", func(t *testing.T) {
		backoff := &LinearBackoff{
			InitialDelay: 100 * time.Millisecond,
			MaxDelay:     1 * time.Second,
			Increment:    0,
			Jitter:       0,
		}

		// Should stay at InitialDelay
		d1 := backoff.Next(0)
		d2 := backoff.Next(5)
		assert.Equal(t, 100*time.Millisecond, d1)
		assert.Equal(t, 100*time.Millisecond, d2)
	})

	t.Run("negative attempt", func(t *testing.T) {
		backoff := &LinearBackoff{
			InitialDelay: 100 * time.Millisecond,
			MaxDelay:     1 * time.Second,
			Increment:    50 * time.Millisecond,
			Jitter:       0,
		}

		d := backoff.Next(-5)
		assert.Equal(t, 100*time.Millisecond, d)
	})

	t.Run("large increment quickly reaches max", func(t *testing.T) {
		backoff := &LinearBackoff{
			InitialDelay: 100 * time.Millisecond,
			MaxDelay:     500 * time.Millisecond,
			Increment:    200 * time.Millisecond,
			Jitter:       0,
		}

		d0 := backoff.Next(0)
		d1 := backoff.Next(1)
		d2 := backoff.Next(2)
		d10 := backoff.Next(10)

		assert.Equal(t, 100*time.Millisecond, d0)
		assert.Equal(t, 300*time.Millisecond, d1)
		assert.Equal(t, 500*time.Millisecond, d2)  // Capped
		assert.Equal(t, 500*time.Millisecond, d10) // Still capped
	})
}

func TestConstantBackoff_EdgeCases(t *testing.T) {
	t.Run("zero delay", func(t *testing.T) {
		backoff := &ConstantBackoff{
			Delay:  0,
			Jitter: 0,
		}

		d := backoff.Next(5)
		assert.Equal(t, time.Duration(0), d)
	})

	t.Run("jitter with zero delay", func(t *testing.T) {
		backoff := &ConstantBackoff{
			Delay:  0,
			Jitter: 0.5,
		}

		d := backoff.Next(5)
		assert.Equal(t, time.Duration(0), d)
	})

	t.Run("different attempt numbers same delay", func(t *testing.T) {
		backoff := &ConstantBackoff{
			Delay:  100 * time.Millisecond,
			Jitter: 0,
		}

		d1 := backoff.Next(0)
		d2 := backoff.Next(100)
		assert.Equal(t, d1, d2)
	})
}

func TestDoWithBackoff(t *testing.T) {
	ctx := context.Background()

	callCount := 0
	op := func(ctx context.Context) error {
		callCount++
		if callCount < 2 {
			return dberrors.New(dberrors.ErrTimeout, "test.op", "timeout")
		}
		return nil
	}

	backoff := &ConstantBackoff{
		Delay:  1 * time.Millisecond,
		Jitter: 0,
	}

	err := DoWithBackoff(ctx, 3, backoff, op)
	assert.NoError(t, err)
	assert.Equal(t, 2, callCount)
}

func TestDo_OnRetryNotCalledOnSuccess(t *testing.T) {
	ctx := context.Background()

	onRetryCalled := false
	cfg := &Config{
		MaxAttempts: 3,
		Backoff:     DefaultConstantBackoff(),
		OnRetry: func(attempt int, err error, backoff time.Duration) {
			onRetryCalled = true
		},
	}

	op := func(ctx context.Context) error {
		return nil // Immediate success
	}

	err := Do(ctx, cfg, op)
	assert.NoError(t, err)
	assert.False(t, onRetryCalled, "OnRetry should not be called on immediate success")
}

func TestDo_OnRetryCalledCorrectly(t *testing.T) {
	ctx := context.Background()

	attempts := []int{}
	errs := []error{}
	backoffs := []time.Duration{}

	cfg := &Config{
		MaxAttempts: 4,
		Backoff: &ConstantBackoff{
			Delay:  5 * time.Millisecond,
			Jitter: 0,
		},
		OnRetry: func(attempt int, err error, backoff time.Duration) {
			attempts = append(attempts, attempt)
			errs = append(errs, err)
			backoffs = append(backoffs, backoff)
		},
	}

	callCount := 0
	expectedErr := dberrors.New(dberrors.ErrTimeout, "test.op", "timeout")

	op := func(ctx context.Context) error {
		callCount++
		if callCount < 3 {
			return expectedErr
		}
		return nil
	}

	err := Do(ctx, cfg, op)
	assert.NoError(t, err)
	assert.Equal(t, 3, callCount)

	// OnRetry should be called twice (after 1st and 2nd failures)
	assert.Equal(t, []int{0, 1}, attempts)
	assert.Len(t, errs, 2)
	assert.Len(t, backoffs, 2)

	for _, b := range backoffs {
		assert.Equal(t, 5*time.Millisecond, b)
	}
}
