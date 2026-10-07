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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// rotateTwiceWithGrace rotates name twice with a nonzero grace period,
// leaving a current version plus two in-grace historical versions behind.
func rotateTwiceWithGrace(t *testing.T, svc *CredentialService, name string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, svc.StoreCredential(ctx, name, []byte("v1")))
	_, err := svc.RotateCredential(ctx, name, RotateOptions{
		NewValue:    []byte("v2"),
		GracePeriod: time.Hour,
	})
	require.NoError(t, err)
	_, err = svc.RotateCredential(ctx, name, RotateOptions{
		NewValue:    []byte("v3"),
		GracePeriod: time.Hour,
	})
	require.NoError(t, err)
}

// assertVersionGone asserts that version is no longer retrievable: the
// service must fail closed with one of the documented "gone" errors, never
// return the historical plaintext.
func assertVersionGone(t *testing.T, svc *CredentialService, name string, version uint64) {
	t.Helper()
	ctx := context.Background()
	_, err := svc.RetrieveCredentialVersion(ctx, name, version)
	if !errors.Is(err, ErrCredentialNotFound) && !errors.Is(err, ErrVersionNotFound) && !errors.Is(err, ErrVersionExpired) {
		t.Fatalf("RetrieveCredentialVersion(%d) = %v, want one of ErrCredentialNotFound/ErrVersionNotFound/ErrVersionExpired", version, err)
	}
}

func TestDeleteCredential_Barrier_RemovesCurrentMetaAndAllVersions(t *testing.T) {
	svc := newBarrierService(t)
	ctx := context.Background()
	rotateTwiceWithGrace(t, svc, "k")

	// Sanity check: before delete, the grace-period history is present.
	versions, err := svc.ListCredentialVersions(ctx, "k")
	require.NoError(t, err)
	require.Len(t, versions, 3, "current v3 plus grace-period v1 and v2")

	require.NoError(t, svc.DeleteCredential(ctx, "k"))

	_, err = svc.RetrieveCredential(ctx, "k")
	assert.ErrorIs(t, err, ErrCredentialNotFound)

	assertVersionGone(t, svc, "k", 1)
	assertVersionGone(t, svc, "k", 2)
	assertVersionGone(t, svc, "k", 3)

	_, err = svc.ListCredentialVersions(ctx, "k")
	assert.ErrorIs(t, err, ErrCredentialNotFound)

	// The defect this guards against: the meta sidecar and every
	// historical version key must actually be gone from the barrier
	// store, not merely unreachable through one lookup path.
	_, err = svc.barrier.Get(ctx, metaKey("k"))
	assert.Error(t, err, "meta sidecar must be deleted")
	_, err = svc.barrier.Get(ctx, versionKey("k", 1))
	assert.Error(t, err, "v1 history key must be deleted")
	_, err = svc.barrier.Get(ctx, versionKey("k", 2))
	assert.Error(t, err, "v2 history key must be deleted")
	_, err = svc.barrier.Get(ctx, credentialPrefix+"k")
	assert.Error(t, err, "current value key must be deleted")
}

func TestDeleteCredential_Barrier_RemovesAllVersionsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	strategy, err := seal.NewSoftwareStrategy(ctx, []byte("test-master-password"))
	require.NoError(t, err)
	cfg := seal.BarrierConfig{RootKeyPath: "test/root-key"}
	base := seal.NewMemoryBackend()
	creds := seal.MustCredentials(ctx, []byte("test-password-123"))

	b1, err := seal.NewStorageBarrier(testLogger(), base, cfg, strategy)
	require.NoError(t, err)
	require.NoError(t, b1.Initialize(ctx, creds))

	svc1, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, b1, testLogger())
	require.NoError(t, err)

	rotateTwiceWithGrace(t, svc1, "k")
	require.NoError(t, svc1.DeleteCredential(ctx, "k"))
	require.NoError(t, b1.Close())

	// Simulate a process restart: a fresh StorageBarrier unsealing the
	// same durable backend, and a fresh CredentialService on top of it.
	b2, err := seal.NewStorageBarrier(testLogger(), base, cfg, strategy)
	require.NoError(t, err)
	require.NoError(t, b2.Unseal(ctx, creds))
	defer func() { _ = b2.Close() }()

	svc2, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, b2, testLogger())
	require.NoError(t, err)

	_, err = svc2.RetrieveCredential(ctx, "k")
	assert.ErrorIs(t, err, ErrCredentialNotFound)
	assertVersionGone(t, svc2, "k", 1)
	assertVersionGone(t, svc2, "k", 2)
	assertVersionGone(t, svc2, "k", 3)

	_, err = b2.Get(ctx, metaKey("k"))
	assert.Error(t, err, "meta sidecar must not survive a restart")
	_, err = b2.Get(ctx, versionKey("k", 1))
	assert.Error(t, err, "v1 history key must not survive a restart")
	_, err = b2.Get(ctx, versionKey("k", 2))
	assert.Error(t, err, "v2 history key must not survive a restart")
}

func TestDeleteCredential_Barrier_SealedFailsClosed(t *testing.T) {
	svc := newBarrierService(t)
	ctx := context.Background()
	require.NoError(t, svc.StoreCredential(ctx, "k", []byte("v1")))

	require.NoError(t, svc.barrier.Seal())

	err := svc.DeleteCredential(ctx, "k")
	require.Error(t, err, "delete on a sealed barrier must fail closed, not silently no-op")

	var opErr *OperationError
	assert.ErrorAs(t, err, &opErr)
	assert.ErrorIs(t, err, seal.ErrSealed)
}

func TestDeleteCredential_Manual_RemovesAllVersionsAndMeta(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	rotateTwiceWithGrace(t, svc, "k")

	versions, err := svc.ListCredentialVersions(ctx, "k")
	require.NoError(t, err)
	require.Len(t, versions, 3)

	require.NoError(t, svc.DeleteCredential(ctx, "k"))

	// After deletion, RetrieveCredential (version 0) blocks on the
	// waiter path rather than returning empty — assert the fail-closed
	// "blocks" behavior with a short timeout instead of hanging forever.
	timeoutCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err = svc.RetrieveCredential(timeoutCtx, "k")
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	assertVersionGone(t, svc, "k", 1)
	assertVersionGone(t, svc, "k", 2)
	assertVersionGone(t, svc, "k", 3)

	_, err = svc.ListCredentialVersions(ctx, "k")
	assert.ErrorIs(t, err, ErrCredentialNotFound)

	svc.mu.RLock()
	_, versionsLeft := svc.manualVersions["k"]
	_, metaLeft := svc.manualMeta["k"]
	svc.mu.RUnlock()
	assert.False(t, versionsLeft, "manualVersions entry must be deleted")
	assert.False(t, metaLeft, "manualMeta entry must be deleted")
}
