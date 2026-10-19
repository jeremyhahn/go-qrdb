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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// errInjectedFault is the storage failure faultBackend returns for an armed
// key.
var errInjectedFault = errors.New("credentials test: injected storage fault")

// faultBackend is an in-memory seal.StorageBackend whose Put and Delete fail
// for the one key suffix they are armed with, so a test can fail a single
// step of a multi-key barrier operation while every other step succeeds.
type faultBackend struct {
	*seal.MemoryBackend
	failPut    atomic.Pointer[string]
	failDelete atomic.Pointer[string]
}

var _ seal.StorageBackend = (*faultBackend)(nil)

// Put fails with errInjectedFault when key ends with the armed Put suffix.
func (f *faultBackend) Put(ctx context.Context, key string, data []byte) error {
	if s := f.failPut.Load(); s != nil && strings.HasSuffix(key, *s) {
		return errInjectedFault
	}
	return f.MemoryBackend.Put(ctx, key, data)
}

// Delete fails with errInjectedFault when key ends with the armed Delete
// suffix.
func (f *faultBackend) Delete(ctx context.Context, key string) error {
	if s := f.failDelete.Load(); s != nil && strings.HasSuffix(key, *s) {
		return errInjectedFault
	}
	return f.MemoryBackend.Delete(ctx, key)
}

// newFaultBarrierService builds a barrier-strategy service over a
// faultBackend, initialized and unsealed, with no fault armed.
func newFaultBarrierService(t *testing.T) (*CredentialService, *faultBackend) {
	t.Helper()
	ctx := context.Background()
	strategy, err := seal.NewSoftwareStrategy(ctx, []byte("test-master-password"))
	require.NoError(t, err)
	backend := &faultBackend{MemoryBackend: seal.NewMemoryBackend()}
	barrier, err := seal.NewStorageBarrier(testLogger(), backend, seal.BarrierConfig{RootKeyPath: "test/root-key"}, strategy)
	require.NoError(t, err)
	require.NoError(t, barrier.Initialize(ctx, seal.MustCredentials(ctx, []byte("test-password-123"))))
	t.Cleanup(func() { _ = barrier.Close() })
	svc, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, barrier, testLogger())
	require.NoError(t, err)
	return svc, backend
}

// seedGraceVersion stores v1 under name and rotates to v2 with a grace
// period, leaving v1 as a historical version key indexed by the meta sidecar.
func seedGraceVersion(t *testing.T, svc *CredentialService, name string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, svc.StoreCredential(ctx, name, []byte("v1")))
	_, err := svc.RotateCredential(ctx, name, RotateOptions{NewValue: []byte("v2"), GracePeriod: time.Hour})
	require.NoError(t, err)
}

func arm(p *atomic.Pointer[string], suffix string) {
	p.Store(&suffix)
}

// TestBarrierFault_RotateWriteFailures: each write a barrier rotation makes
// surfaces its own typed failure carrying the storage cause.
func TestBarrierFault_RotateWriteFailures(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		suffix string
		op     string
	}{
		{name: "grace version", suffix: versionPrefix + "1", op: "write grace version"},
		{name: "meta", suffix: metaSuffix, op: "write meta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, backend := newFaultBarrierService(t)
			require.NoError(t, svc.StoreCredential(ctx, "k", []byte("v1")))
			arm(&backend.failPut, tc.suffix)

			_, err := svc.RotateCredential(ctx, "k", RotateOptions{NewValue: []byte("v2"), GracePeriod: time.Hour})
			requireOperationError(t, err, tc.op)
			assert.ErrorIs(t, err, errInjectedFault)
		})
	}
}

// TestBarrierFault_GCDeleteFailure: a GC that cannot delete an expired
// version key fails with that version's operation and leaves the meta
// sidecar indexing it, so a later GC retries the reap.
func TestBarrierFault_GCDeleteFailure(t *testing.T) {
	ctx := context.Background()
	svc, backend := newFaultBarrierService(t)
	seedGraceVersion(t, svc, "k")
	expireBarrierGrace(t, svc, "k")
	arm(&backend.failDelete, versionKey("k", 1))

	reaped, err := svc.GCExpiredVersions(ctx, "k")
	requireOperationError(t, err, "delete v1")
	assert.ErrorIs(t, err, errInjectedFault)
	assert.Zero(t, reaped)

	meta, err := svc.loadBarrierMeta(ctx, "k")
	require.NoError(t, err)
	require.Len(t, meta.History, 1, "the unreaped version must stay indexed")

	backend.failDelete.Store(nil)
	reaped, err = svc.GCExpiredVersions(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, 1, reaped)
}

// TestBarrierFault_DeleteFailures: a barrier delete that cannot remove a
// version key or the current value fails with the step that failed, and
// DeleteCredential reports it rather than success.
func TestBarrierFault_DeleteFailures(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		suffix string
		op     string
	}{
		{name: "version", suffix: versionKey("k", 1), op: "delete v1"},
		{name: "current", suffix: credentialPrefix + "k", op: "delete current"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, backend := newFaultBarrierService(t)
			seedGraceVersion(t, svc, "k")
			arm(&backend.failDelete, tc.suffix)

			err := svc.DeleteCredential(ctx, "k")
			requireOperationError(t, err, tc.op)
			assert.ErrorIs(t, err, errInjectedFault)
		})
	}
}

// TestRetrieveCredentialVersion_Barrier_CurrentValueMissing: the meta
// sidecar names a current version whose value key is gone; asking for that
// version by number is not-found, not an empty value.
func TestRetrieveCredentialVersion_Barrier_CurrentValueMissing(t *testing.T) {
	ctx := context.Background()
	svc, backend := newFaultBarrierService(t)
	seedGraceVersion(t, svc, "k")
	require.NoError(t, backend.MemoryBackend.Delete(ctx, credentialPrefix+"k"))

	_, err := svc.RetrieveCredentialVersionBuffer(ctx, "k", 2)
	assert.ErrorIs(t, err, ErrCredentialNotFound)
}

// TestCredentialService_InvalidStrategyMutations: a service whose strategy
// is not one it implements refuses store and delete.
func TestCredentialService_InvalidStrategyMutations(t *testing.T) {
	ctx := context.Background()
	svc := newManualService(t)
	svc.strategy = "bogus"

	assert.ErrorIs(t, svc.StoreCredential(ctx, "k", []byte("v")), ErrInvalidStrategy)
	assert.ErrorIs(t, svc.DeleteCredential(ctx, "k"), ErrInvalidStrategy)
}

// TestGCExpiredVersions_ManualReapsLastVersion: reaping a manual
// credential's only grace version drops its now-empty version map.
func TestGCExpiredVersions_ManualReapsLastVersion(t *testing.T) {
	ctx := context.Background()
	svc := newManualService(t)
	require.NoError(t, svc.StoreCredential(ctx, "k", []byte("v1")))
	_, err := svc.RotateCredential(ctx, "k", RotateOptions{NewValue: []byte("v2"), GracePeriod: time.Hour})
	require.NoError(t, err)
	expireGrace(svc, "k")

	reaped, err := svc.GCExpiredVersions(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, 1, reaped)
	svc.mu.RLock()
	_, kept := svc.manualVersions["k"]
	svc.mu.RUnlock()
	assert.False(t, kept, "an emptied version map must be dropped")
}
