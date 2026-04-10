// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package credentials

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testLogger returns a silent logger for testing.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestBarrier creates an initialized and unsealed StorageBarrier
// backed by an in-memory storage backend for testing.
func newTestBarrier(t *testing.T) *seal.StorageBarrier {
	t.Helper()

	strategy, err := seal.NewSoftwareStrategy([]byte("test-master-password"))
	require.NoError(t, err, "NewSoftwareStrategy")

	cfg := seal.BarrierConfig{
		RootKeyPath: "test/root-key",
	}
	b, err := seal.NewStorageBarrier(testLogger(), seal.NewMemoryBackend(), cfg, strategy)
	require.NoError(t, err, "NewStorageBarrier")

	ctx := context.Background()
	err = b.Initialize(ctx, seal.Credentials{Secret: "test-password-123"})
	require.NoError(t, err, "barrier Initialize")

	return b
}

// --- Constructor tests ---

func TestNewCredentialService_NilConfig_DefaultsToManual(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)
	assert.Equal(t, StrategyManual, svc.Strategy())
}

func TestNewCredentialService_EmptyConfig_DefaultsToManual(t *testing.T) {
	svc, err := NewCredentialService(&Config{}, nil, testLogger())
	require.NoError(t, err)
	assert.Equal(t, StrategyManual, svc.Strategy())
}

func TestNewCredentialService_BarrierStrategy_Success(t *testing.T) {
	barrier := newTestBarrier(t)
	defer func() {
		if err := barrier.Close(); err != nil {
			t.Errorf("barrier.Close: %v", err)
		}
	}()

	svc, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, barrier, testLogger())
	require.NoError(t, err)
	assert.Equal(t, StrategyBarrier, svc.Strategy())
}

func TestNewCredentialService_BarrierStrategy_NilBarrier(t *testing.T) {
	_, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, nil, testLogger())
	assert.ErrorIs(t, err, ErrNilBarrier)
}

func TestNewCredentialService_InvalidStrategy(t *testing.T) {
	_, err := NewCredentialService(&Config{Strategy: "bogus"}, nil, testLogger())
	assert.ErrorIs(t, err, ErrInvalidStrategy)
}

func TestNewCredentialService_NilLogger(t *testing.T) {
	_, err := NewCredentialService(nil, nil, nil)
	assert.ErrorIs(t, err, ErrNilLogger)
}

// --- StoreCredential tests (manual) ---

func TestStoreCredential_Manual_Success(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	err = svc.StoreCredential(ctx, "api-key", []byte("secret-value"))
	require.NoError(t, err)

	got, err := svc.RetrieveCredential(ctx, "api-key")
	require.NoError(t, err)
	assert.Equal(t, []byte("secret-value"), got)
}

func TestStoreCredential_Manual_EmptyName(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	err = svc.StoreCredential(context.Background(), "", []byte("value"))
	assert.ErrorIs(t, err, ErrEmptyCredentialName)
}

func TestStoreCredential_Manual_WhitespaceName(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	err = svc.StoreCredential(context.Background(), "   ", []byte("value"))
	assert.ErrorIs(t, err, ErrEmptyCredentialName)
}

func TestStoreCredential_Manual_EmptyValue(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	err = svc.StoreCredential(context.Background(), "key", []byte{})
	assert.ErrorIs(t, err, ErrEmptyCredentialValue)
}

func TestStoreCredential_Manual_NilValue(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	err = svc.StoreCredential(context.Background(), "key", nil)
	assert.ErrorIs(t, err, ErrEmptyCredentialValue)
}

func TestStoreCredential_Manual_Overwrite(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, svc.StoreCredential(ctx, "key", []byte("v1")))
	require.NoError(t, svc.StoreCredential(ctx, "key", []byte("v2")))

	got, err := svc.RetrieveCredential(ctx, "key")
	require.NoError(t, err)
	assert.Equal(t, []byte("v2"), got)
}

func TestStoreCredential_Manual_ValueIsolation(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	original := []byte("original-value")
	require.NoError(t, svc.StoreCredential(ctx, "key", original))

	// Mutate the original slice after storing.
	original[0] = 'X'

	got, err := svc.RetrieveCredential(ctx, "key")
	require.NoError(t, err)
	assert.Equal(t, []byte("original-value"), got, "stored value should be isolated from caller mutations")
}

// --- StoreCredential tests (barrier) ---

func TestStoreCredential_Barrier_Success(t *testing.T) {
	barrier := newTestBarrier(t)
	defer func() {
		if err := barrier.Close(); err != nil {
			t.Errorf("barrier.Close: %v", err)
		}
	}()

	svc, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, barrier, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	err = svc.StoreCredential(ctx, "db-password", []byte("s3cret"))
	require.NoError(t, err)

	got, err := svc.RetrieveCredential(ctx, "db-password")
	require.NoError(t, err)
	assert.Equal(t, []byte("s3cret"), got)
}

// --- RetrieveCredential tests (manual) ---

func TestRetrieveCredential_Manual_AlreadyStored(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, svc.StoreCredential(ctx, "key", []byte("val")))

	got, err := svc.RetrieveCredential(ctx, "key")
	require.NoError(t, err)
	assert.Equal(t, []byte("val"), got)
}

func TestRetrieveCredential_Manual_BlocksUntilSubmitted(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	var wg sync.WaitGroup
	var got []byte
	var retrieveErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		got, retrieveErr = svc.RetrieveCredential(ctx, "deferred")
	}()

	// Give the goroutine time to register as a waiter.
	time.Sleep(20 * time.Millisecond)

	err = svc.SubmitCredential(ctx, "deferred", []byte("delayed-value"))
	require.NoError(t, err)

	wg.Wait()

	require.NoError(t, retrieveErr)
	assert.Equal(t, []byte("delayed-value"), got)
}

func TestRetrieveCredential_Manual_ContextCancellation(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	var retrieveErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, retrieveErr = svc.RetrieveCredential(ctx, "never-submitted")
	}()

	// Give the goroutine time to register as a waiter.
	time.Sleep(20 * time.Millisecond)
	cancel()

	wg.Wait()

	assert.ErrorIs(t, retrieveErr, context.Canceled)
}

func TestRetrieveCredential_Manual_EmptyName(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	_, err = svc.RetrieveCredential(context.Background(), "")
	assert.ErrorIs(t, err, ErrEmptyCredentialName)
}

func TestRetrieveCredential_Manual_ReturnsIsolatedCopy(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, svc.StoreCredential(ctx, "key", []byte("value")))

	got1, err := svc.RetrieveCredential(ctx, "key")
	require.NoError(t, err)

	// Mutate the returned slice.
	got1[0] = 'X'

	got2, err := svc.RetrieveCredential(ctx, "key")
	require.NoError(t, err)
	assert.Equal(t, []byte("value"), got2, "subsequent retrieval must return unmodified copy")
}

// --- RetrieveCredential tests (barrier) ---

func TestRetrieveCredential_Barrier_Success(t *testing.T) {
	barrier := newTestBarrier(t)
	defer func() {
		if err := barrier.Close(); err != nil {
			t.Errorf("barrier.Close: %v", err)
		}
	}()

	svc, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, barrier, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, svc.StoreCredential(ctx, "token", []byte("abc123")))

	got, err := svc.RetrieveCredential(ctx, "token")
	require.NoError(t, err)
	assert.Equal(t, []byte("abc123"), got)
}

func TestRetrieveCredential_Barrier_NotFound(t *testing.T) {
	barrier := newTestBarrier(t)
	defer func() {
		if err := barrier.Close(); err != nil {
			t.Errorf("barrier.Close: %v", err)
		}
	}()

	svc, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, barrier, testLogger())
	require.NoError(t, err)

	_, err = svc.RetrieveCredential(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, ErrCredentialNotFound)
}

// --- SubmitCredential tests (manual) ---

func TestSubmitCredential_Manual_Success(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	err = svc.SubmitCredential(ctx, "key", []byte("value"))
	require.NoError(t, err)

	got, err := svc.RetrieveCredential(ctx, "key")
	require.NoError(t, err)
	assert.Equal(t, []byte("value"), got)
}

func TestSubmitCredential_Manual_UnblocksWaiter(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	results := make(chan []byte, 1)
	errs := make(chan error, 1)

	go func() {
		val, err := svc.RetrieveCredential(ctx, "pending")
		errs <- err
		results <- val
	}()

	// Give the goroutine time to register as a waiter.
	time.Sleep(20 * time.Millisecond)

	require.NoError(t, svc.SubmitCredential(ctx, "pending", []byte("submitted")))

	assert.NoError(t, <-errs)
	assert.Equal(t, []byte("submitted"), <-results)
}

func TestSubmitCredential_Manual_AlreadySubmitted(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, svc.SubmitCredential(ctx, "key", []byte("v1")))

	err = svc.SubmitCredential(ctx, "key", []byte("v2"))
	assert.ErrorIs(t, err, ErrCredentialAlreadySubmitted)
}

func TestSubmitCredential_Manual_EmptyName(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	err = svc.SubmitCredential(context.Background(), "", []byte("value"))
	assert.ErrorIs(t, err, ErrEmptyCredentialName)
}

func TestSubmitCredential_Manual_EmptyValue(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	err = svc.SubmitCredential(context.Background(), "key", []byte{})
	assert.ErrorIs(t, err, ErrEmptyCredentialValue)
}

func TestSubmitCredential_Manual_NilValue(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	err = svc.SubmitCredential(context.Background(), "key", nil)
	assert.ErrorIs(t, err, ErrEmptyCredentialValue)
}

// --- DeleteCredential tests (manual) ---

func TestDeleteCredential_Manual_Success(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, svc.StoreCredential(ctx, "key", []byte("value")))
	require.NoError(t, svc.DeleteCredential(ctx, "key"))

	// After deletion, a retrieve should block (no credential exists).
	// Use a short timeout context to verify deletion.
	timeoutCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	_, err = svc.RetrieveCredential(timeoutCtx, "key")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestDeleteCredential_Manual_NotFound(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	err = svc.DeleteCredential(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, ErrCredentialNotFound)
}

func TestDeleteCredential_Manual_EmptyName(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	err = svc.DeleteCredential(context.Background(), "")
	assert.ErrorIs(t, err, ErrEmptyCredentialName)
}

// --- DeleteCredential tests (barrier) ---

func TestDeleteCredential_Barrier_Success(t *testing.T) {
	barrier := newTestBarrier(t)
	defer func() {
		if err := barrier.Close(); err != nil {
			t.Errorf("barrier.Close: %v", err)
		}
	}()

	svc, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, barrier, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, svc.StoreCredential(ctx, "key", []byte("value")))
	require.NoError(t, svc.DeleteCredential(ctx, "key"))

	_, err = svc.RetrieveCredential(ctx, "key")
	assert.ErrorIs(t, err, ErrCredentialNotFound)
}

// --- Strategy / AutoUnsealAvailable tests ---

func TestStrategy_Manual(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)
	assert.Equal(t, "manual", svc.Strategy())
}

func TestStrategy_Barrier(t *testing.T) {
	barrier := newTestBarrier(t)
	defer func() {
		if err := barrier.Close(); err != nil {
			t.Errorf("barrier.Close: %v", err)
		}
	}()

	svc, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, barrier, testLogger())
	require.NoError(t, err)
	assert.Equal(t, "barrier", svc.Strategy())
}

func TestAutoUnsealAvailable_Manual(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)
	assert.False(t, svc.AutoUnsealAvailable())
}

func TestAutoUnsealAvailable_Barrier(t *testing.T) {
	barrier := newTestBarrier(t)
	defer func() {
		if err := barrier.Close(); err != nil {
			t.Errorf("barrier.Close: %v", err)
		}
	}()

	svc, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, barrier, testLogger())
	require.NoError(t, err)
	assert.True(t, svc.AutoUnsealAvailable())
}

// --- Validation helper tests ---

func TestValidateName_Valid(t *testing.T) {
	assert.NoError(t, validateName("valid-name"))
}

func TestValidateName_Empty(t *testing.T) {
	assert.ErrorIs(t, validateName(""), ErrEmptyCredentialName)
}

func TestValidateName_Whitespace(t *testing.T) {
	assert.ErrorIs(t, validateName("  \t  "), ErrEmptyCredentialName)
}

func TestValidateValue_Valid(t *testing.T) {
	assert.NoError(t, validateValue([]byte("data")))
}

func TestValidateValue_Empty(t *testing.T) {
	assert.ErrorIs(t, validateValue([]byte{}), ErrEmptyCredentialValue)
}

func TestValidateValue_Nil(t *testing.T) {
	assert.ErrorIs(t, validateValue(nil), ErrEmptyCredentialValue)
}

// --- Concurrent access tests ---

func TestStoreCredential_Manual_ConcurrentAccess(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(n int) {
			defer wg.Done()
			name := "key"
			value := []byte("value")
			_ = svc.StoreCredential(ctx, name, value)
			_, _ = svc.RetrieveCredential(ctx, name)
			_ = n
		}(i)
	}

	wg.Wait()
}

func TestSubmitCredential_Manual_MultipleWaiters(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	const numWaiters = 5
	results := make([][]byte, numWaiters)
	errs := make([]error, numWaiters)
	var wg sync.WaitGroup
	wg.Add(numWaiters)

	for i := range numWaiters {
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = svc.RetrieveCredential(ctx, "shared")
		}(i)
	}

	// Give goroutines time to register as waiters.
	time.Sleep(30 * time.Millisecond)

	require.NoError(t, svc.SubmitCredential(ctx, "shared", []byte("broadcast")))

	wg.Wait()

	for i := range numWaiters {
		assert.NoError(t, errs[i], "waiter %d error", i)
		assert.Equal(t, []byte("broadcast"), results[i], "waiter %d value", i)
	}
}
