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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// requireOperationError asserts err is an *OperationError for op, and that its
// message names the package, the operation and the cause, in that order.
func requireOperationError(t *testing.T, err error, op string) *OperationError {
	t.Helper()
	var opErr *OperationError
	require.ErrorAs(t, err, &opErr)
	assert.Equal(t, op, opErr.Op)
	assert.Equal(t, "credentials: "+op+": "+opErr.Cause.Error(), opErr.Error())
	return opErr
}

// TestBarrier_CorruptMetaFailsEveryVersionedOperation: the metadata sidecar
// indexes a credential's version history. One that does not decode must fail
// every operation that reads it, rather than being read as "no history" -- a
// rotation would then overwrite the index and orphan in-grace versions, and a
// delete would leave their keys behind.
func TestBarrier_CorruptMetaFailsEveryVersionedOperation(t *testing.T) {
	ctx := context.Background()
	for name, op := range map[string]func(*CredentialService) error{
		"rotate": func(svc *CredentialService) error {
			_, err := svc.RotateCredential(ctx, "k", RotateOptions{NewValue: []byte("v2"), GracePeriod: time.Hour})
			return err
		},
		"retrieve version": func(svc *CredentialService) error {
			_, err := svc.RetrieveCredentialVersionBuffer(ctx, "k", 2)
			return err
		},
		"list versions": func(svc *CredentialService) error {
			_, err := svc.ListCredentialVersions(ctx, "k")
			return err
		},
		"gc": func(svc *CredentialService) error {
			_, err := svc.GCExpiredVersions(ctx, "k")
			return err
		},
		"delete": func(svc *CredentialService) error {
			return svc.DeleteCredential(ctx, "k")
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc := newBarrierService(t)
			require.NoError(t, svc.StoreCredential(ctx, "k", []byte("v1")))
			require.NoError(t, svc.store.Put(ctx, metaKey("k"), []byte("not json")))

			requireOperationError(t, op(svc), "decode meta")

			// Nothing the failed operation touched: the current value is
			// still served.
			got, err := svc.retrieveBytes(ctx, "k")
			require.NoError(t, err)
			assert.Equal(t, []byte("v1"), got)
		})
	}
}

// TestBarrier_SealedWritesFailClosed: a sealed barrier refuses writes, and
// each barrier-strategy operation must surface that as its own typed failure
// carrying seal.ErrSealed, never report success for a write that did not land.
func TestBarrier_SealedWritesFailClosed(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		op   string
		run  func(*CredentialService) error
	}{
		{
			name: "rotate",
			op:   "write current",
			run: func(svc *CredentialService) error {
				_, err := svc.RotateCredential(ctx, "k", RotateOptions{NewValue: []byte("v2")})
				return err
			},
		},
		{
			name: "list",
			op:   "list barrier",
			run: func(svc *CredentialService) error {
				_, err := svc.ListCredentials(ctx)
				return err
			},
		},
		{
			name: "gc",
			op:   "write meta",
			run: func(svc *CredentialService) error {
				_, err := svc.GCExpiredVersions(ctx, "k")
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newBarrierService(t)
			require.NoError(t, svc.StoreCredential(ctx, "k", []byte("v1")))
			require.NoError(t, barrierOf(t, svc).Seal())

			err := tc.run(svc)
			requireOperationError(t, err, tc.op)
			assert.ErrorIs(t, err, seal.ErrSealed)
		})
	}
}

// TestBarrier_SealedStoreIsRefused: a store against a sealed barrier fails with
// the barrier's own sentinel and emits no audit event for a write that never
// happened.
func TestBarrier_SealedStoreIsRefused(t *testing.T) {
	ctx := context.Background()
	svc := newBarrierService(t)
	logger := &fakeAuditLogger{}
	svc.SetAuditLogger(logger)
	require.NoError(t, barrierOf(t, svc).Seal())

	err := svc.StoreCredential(ctx, "k", []byte("v1"))
	assert.ErrorIs(t, err, seal.ErrSealed)
	assert.Empty(t, logger.snapshot(), "a refused store must not be audited as a success")
}

// TestOperationError_MessageNamesTheOperation pins the message format the
// typed error replaced the inline wrapping idiom with.
func TestOperationError_MessageNamesTheOperation(t *testing.T) {
	cause := seal.ErrSealed
	err := &OperationError{Op: "write meta", Cause: cause}
	assert.True(t, strings.HasPrefix(err.Error(), "credentials: write meta: "))
	assert.ErrorIs(t, err, cause)
}
