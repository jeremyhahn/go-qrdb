// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !integration

package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// warmupReadIndex: immediate success on Get returning data
// ---------------------------------------------------------------------------

func TestWarmupReadIndex_ImmediateSuccess(t *testing.T) {
	var callCount atomic.Int32
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			callCount.Add(1)
			return []byte("value"), nil
		},
	}

	warmupReadIndex(context.Background(), mock)
	// Should return after the first successful Get.
	assert.Equal(t, int32(1), callCount.Load())
}

// ---------------------------------------------------------------------------
// warmupReadIndex: success on KeyNotFoundError (pipeline is operational)
// ---------------------------------------------------------------------------

func TestWarmupReadIndex_KeyNotFoundIsSuccess(t *testing.T) {
	var callCount atomic.Int32
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			callCount.Add(1)
			return nil, &service.KeyNotFoundError{Key: "__sys:warmup"}
		},
	}

	warmupReadIndex(context.Background(), mock)
	// KeyNotFoundError means the ReadIndex pipeline worked.
	assert.Equal(t, int32(1), callCount.Load())
}

// ---------------------------------------------------------------------------
// warmupReadIndex: retries on transient errors then succeeds
// ---------------------------------------------------------------------------

func TestWarmupReadIndex_RetriesThenSucceeds(t *testing.T) {
	var callCount atomic.Int32
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			n := callCount.Add(1)
			if n < 3 {
				return nil, errors.New("not ready")
			}
			return nil, &service.KeyNotFoundError{Key: "__sys:warmup"}
		},
	}

	warmupReadIndex(context.Background(), mock)
	assert.Equal(t, int32(3), callCount.Load())
}

// ---------------------------------------------------------------------------
// warmupReadIndex: exhausts all attempts without blocking
// ---------------------------------------------------------------------------

func TestWarmupReadIndex_ExhaustsAttempts(t *testing.T) {
	var callCount atomic.Int32
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			callCount.Add(1)
			return nil, errors.New("persistent failure")
		},
	}

	warmupReadIndex(context.Background(), mock)
	// Should try maxAttempts (5) times then return.
	assert.Equal(t, int32(5), callCount.Load())
}

// ---------------------------------------------------------------------------
// warmupReadIndex: context canceled stops retries
// ---------------------------------------------------------------------------

func TestWarmupReadIndex_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var callCount atomic.Int32
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			n := callCount.Add(1)
			if n == 1 {
				cancel() // Cancel after first attempt.
			}
			return nil, errors.New("not ready")
		},
	}

	warmupReadIndex(ctx, mock)
	// Should stop after detecting context cancellation.
	assert.LessOrEqual(t, callCount.Load(), int32(2))
}

// ---------------------------------------------------------------------------
// warmupReadIndex: context already canceled returns immediately
// ---------------------------------------------------------------------------

func TestWarmupReadIndex_ContextAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel before calling.

	var callCount atomic.Int32
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			callCount.Add(1)
			return nil, errors.New("should not reach here many times")
		},
	}

	warmupReadIndex(ctx, mock)
	// With an already-canceled context, the function should return quickly.
	// The first Get call may still happen (context checked after Get),
	// but no retries should occur.
	assert.LessOrEqual(t, callCount.Load(), int32(1))
}

// ---------------------------------------------------------------------------
// warmupReadIndex: context deadline expires during backoff sleep
//
// Uses a short deadline so that Get returns quickly (error), ctx.Err()
// is nil on line 1060 (deadline not yet expired), but during the 200ms
// backoff the deadline expires and <-ctx.Done() fires in the select.
// This covers line 1069-1070 of start.go.
// ---------------------------------------------------------------------------

func TestWarmupReadIndex_ContextExpiresInBackoff(t *testing.T) {
	// Deadline is 50ms: enough for Get to return but too short for
	// the 200ms backoff.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var callCount atomic.Int32
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			callCount.Add(1)
			// Return immediately with an error (not KeyNotFound).
			return nil, errors.New("not ready")
		},
	}

	warmupReadIndex(ctx, mock)
	// Should have called Get at least once, then the context deadline
	// expires during the backoff sleep.
	assert.GreaterOrEqual(t, callCount.Load(), int32(1))
}
