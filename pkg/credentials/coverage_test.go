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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStoreCredential_Barrier_EmptyName verifies that the barrier strategy
// rejects an empty credential name.
func TestStoreCredential_Barrier_EmptyName(t *testing.T) {
	barrier := newTestBarrier(t)
	defer barrier.Close() //nolint:errcheck

	svc, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, barrier, testLogger())
	require.NoError(t, err)

	err = svc.StoreCredential(context.Background(), "", []byte("value"))
	assert.ErrorIs(t, err, ErrEmptyCredentialName)
}

// TestStoreCredential_Barrier_EmptyValue verifies that the barrier strategy
// rejects an empty credential value.
func TestStoreCredential_Barrier_EmptyValue(t *testing.T) {
	barrier := newTestBarrier(t)
	defer barrier.Close() //nolint:errcheck

	svc, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, barrier, testLogger())
	require.NoError(t, err)

	err = svc.StoreCredential(context.Background(), "key", nil)
	assert.ErrorIs(t, err, ErrEmptyCredentialValue)
}

// TestRetrieveCredential_Barrier_EmptyName verifies that the barrier strategy
// rejects an empty credential name on retrieval.
func TestRetrieveCredential_Barrier_EmptyName(t *testing.T) {
	barrier := newTestBarrier(t)
	defer barrier.Close() //nolint:errcheck

	svc, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, barrier, testLogger())
	require.NoError(t, err)

	_, err = svc.retrieveBytes(context.Background(), "")
	assert.ErrorIs(t, err, ErrEmptyCredentialName)
}

// TestDeleteCredential_Barrier_EmptyName verifies that the barrier strategy
// rejects an empty credential name on deletion.
func TestDeleteCredential_Barrier_EmptyName(t *testing.T) {
	barrier := newTestBarrier(t)
	defer barrier.Close() //nolint:errcheck

	svc, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, barrier, testLogger())
	require.NoError(t, err)

	err = svc.DeleteCredential(context.Background(), "")
	assert.ErrorIs(t, err, ErrEmptyCredentialName)
}

// TestSubmitCredential_Barrier_StoresDelegation verifies that SubmitCredential
// for the barrier strategy delegates to StoreCredential.
func TestSubmitCredential_Barrier_StoresDelegation(t *testing.T) {
	barrier := newTestBarrier(t)
	defer barrier.Close() //nolint:errcheck

	svc, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, barrier, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	err = svc.SubmitCredential(ctx, "api-token", []byte("tok123"))
	require.NoError(t, err)

	got, err := svc.retrieveBytes(ctx, "api-token")
	require.NoError(t, err)
	assert.Equal(t, []byte("tok123"), got)
}

// TestRetrieveCredential_Manual_BlocksAndContextCancel tests that a retrieval
// waiting on an unsubmitted credential is properly cancelled when context is
// cancelled, exercising the context.Done() branch in retrieveManual.
func TestRetrieveCredential_Manual_BlocksAndTimesOut(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err = svc.retrieveBytes(ctx, "never-stored-timeout")
	assert.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

// TestDeleteCredential_Manual_AfterDelete_RetrieveBlocks confirms that after
// deleting a manual credential, a new retrieval blocks (no cached value).
func TestDeleteCredential_Manual_AfterDelete_RetrieveBlocks(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, svc.StoreCredential(ctx, "transient", []byte("val")))
	require.NoError(t, svc.DeleteCredential(ctx, "transient"))

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()

	_, err = svc.retrieveBytes(timeoutCtx, "transient")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}
