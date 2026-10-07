// Copyright (c) 2026 Jeremy Hahn
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

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// newTestCredentialService creates a CredentialServicer backed by the
// manual strategy for unit testing.
func newTestCredentialServiceSvc(t *testing.T) CredentialServicer {
	t.Helper()
	cfg := &credentials.Config{Strategy: credentials.StrategyManual}
	inner, err := credentials.NewCredentialService(cfg, nil, testLogger())
	require.NoError(t, err)
	return NewCredentialService(inner)
}

// newTestBarrierCredentialServiceSvc creates a CredentialServicer backed by
// the barrier strategy, over an initialized and unsealed in-memory
// StorageBarrier, for unit testing.
func newTestBarrierCredentialServiceSvc(t *testing.T) CredentialServicer {
	t.Helper()
	barrier, _ := newTestSoftwareBarrier(t)
	ctx := context.Background()
	require.NoError(t, barrier.Initialize(ctx, seal.MustCredentials(ctx, []byte("my-secret"))))
	cfg := &credentials.Config{Strategy: credentials.StrategyBarrier}
	inner, err := credentials.NewCredentialService(cfg, barrier, testLogger())
	require.NoError(t, err)
	return NewCredentialService(inner)
}

// TestCredentialService_CredentialDelete_RemovesAllVersions_Barrier verifies
// the service-layer pass-through for the blocker fix: after a rotation with
// a nonzero grace period, deleting a credential through the
// CredentialServicer interface must make every historical version
// unretrievable, not just the current value. The underlying fix lives in
// pkg/credentials; this test confirms it is visible through the one
// chokepoint every transport (REST, gRPC, QUIC, MCP, embedded, unix) routes
// through.
func TestCredentialService_CredentialDelete_RemovesAllVersions_Barrier(t *testing.T) {
	svc := newTestBarrierCredentialServiceSvc(t)
	ctx := context.Background()

	require.NoError(t, svc.CredentialStore(ctx, "k", []byte("v1")))
	_, err := svc.CredentialRotate(ctx, "k", credentials.RotateOptions{
		NewValue:    []byte("v2"),
		GracePeriod: time.Hour,
	})
	require.NoError(t, err)
	_, err = svc.CredentialRotate(ctx, "k", credentials.RotateOptions{
		NewValue:    []byte("v3"),
		GracePeriod: time.Hour,
	})
	require.NoError(t, err)

	versions, err := svc.CredentialListVersions(ctx, "k")
	require.NoError(t, err)
	require.Len(t, versions, 3, "current v3 plus grace-period v1 and v2")

	require.NoError(t, svc.CredentialDelete(ctx, "k"))

	_, err = svc.CredentialRetrieve(ctx, "k")
	assert.ErrorIs(t, err, credentials.ErrCredentialNotFound)

	for _, v := range []uint64{1, 2, 3} {
		_, err := svc.CredentialRetrieveVersion(ctx, "k", v)
		assert.Error(t, err, "version %d must not be retrievable after delete", v)
	}

	_, err = svc.CredentialListVersions(ctx, "k")
	assert.ErrorIs(t, err, credentials.ErrCredentialNotFound)
}

func TestNewCredentialService(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	assert.NotNil(t, svc)
}

func TestNewCredentialService_InterfaceCompliance(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	iface := CredentialServicer(svc)
	assert.NotNil(t, iface)
}

func TestCredentialService_CredentialStrategy_Manual(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	assert.Equal(t, "manual", svc.CredentialStrategy())
}

func TestCredentialService_CredentialStore_Success(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	ctx := context.Background()

	err := svc.CredentialStore(ctx, "test-cred", []byte("secret-value"))
	require.NoError(t, err)
}

func TestCredentialService_CredentialStore_EmptyName(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	ctx := context.Background()

	err := svc.CredentialStore(ctx, "", []byte("secret-value"))
	require.Error(t, err)
	assert.ErrorIs(t, err, credentials.ErrEmptyCredentialName)
}

func TestCredentialService_CredentialStore_WhitespaceName(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	ctx := context.Background()

	err := svc.CredentialStore(ctx, "   ", []byte("secret-value"))
	require.Error(t, err)
	assert.ErrorIs(t, err, credentials.ErrEmptyCredentialName)
}

func TestCredentialService_CredentialStore_EmptyValue(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	ctx := context.Background()

	err := svc.CredentialStore(ctx, "test-cred", []byte{})
	require.Error(t, err)
	assert.ErrorIs(t, err, credentials.ErrEmptyCredentialValue)
}

func TestCredentialService_CredentialStore_NilValue(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	ctx := context.Background()

	err := svc.CredentialStore(ctx, "test-cred", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, credentials.ErrEmptyCredentialValue)
}

func TestCredentialService_CredentialRetrieve_AfterStore(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	ctx := context.Background()

	err := svc.CredentialStore(ctx, "my-secret", []byte("my-value"))
	require.NoError(t, err)

	val, err := svc.CredentialRetrieve(ctx, "my-secret")
	require.NoError(t, err)
	assert.Equal(t, []byte("my-value"), val)
}

func TestCredentialService_CredentialRetrieve_EmptyName(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	ctx := context.Background()

	_, err := svc.CredentialRetrieve(ctx, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, credentials.ErrEmptyCredentialName)
}

func TestCredentialService_CredentialSubmit_Success(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	ctx := context.Background()

	err := svc.CredentialSubmit(ctx, "submit-cred", []byte("submitted-value"))
	require.NoError(t, err)

	// After submit, the credential should be retrievable.
	val, err := svc.CredentialRetrieve(ctx, "submit-cred")
	require.NoError(t, err)
	assert.Equal(t, []byte("submitted-value"), val)
}

func TestCredentialService_CredentialSubmit_EmptyName(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	ctx := context.Background()

	err := svc.CredentialSubmit(ctx, "", []byte("value"))
	require.Error(t, err)
	assert.ErrorIs(t, err, credentials.ErrEmptyCredentialName)
}

func TestCredentialService_CredentialSubmit_EmptyValue(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	ctx := context.Background()

	err := svc.CredentialSubmit(ctx, "test-cred", []byte{})
	require.Error(t, err)
	assert.ErrorIs(t, err, credentials.ErrEmptyCredentialValue)
}

func TestCredentialService_CredentialSubmit_AlreadySubmitted(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	ctx := context.Background()

	err := svc.CredentialSubmit(ctx, "dup-cred", []byte("first"))
	require.NoError(t, err)

	err = svc.CredentialSubmit(ctx, "dup-cred", []byte("second"))
	require.Error(t, err)
	assert.ErrorIs(t, err, credentials.ErrCredentialAlreadySubmitted)
}

func TestCredentialService_CredentialDelete_Success(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	ctx := context.Background()

	err := svc.CredentialStore(ctx, "delete-me", []byte("value"))
	require.NoError(t, err)

	err = svc.CredentialDelete(ctx, "delete-me")
	require.NoError(t, err)
}

func TestCredentialService_CredentialDelete_NotFound(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	ctx := context.Background()

	err := svc.CredentialDelete(ctx, "nonexistent")
	require.Error(t, err)
	assert.ErrorIs(t, err, credentials.ErrCredentialNotFound)
}

func TestCredentialService_CredentialDelete_EmptyName(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	ctx := context.Background()

	err := svc.CredentialDelete(ctx, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, credentials.ErrEmptyCredentialName)
}

func TestCredentialService_CredentialRetrieve_AfterDelete(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	ctx := context.Background()

	// Store, then delete, then try to retrieve via a blocking call with cancel.
	err := svc.CredentialStore(ctx, "temp-cred", []byte("value"))
	require.NoError(t, err)

	err = svc.CredentialDelete(ctx, "temp-cred")
	require.NoError(t, err)

	// Retrieve on deleted credential should block until context cancels
	// (manual strategy). Use a cancelled context to avoid blocking.
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()

	_, err = svc.CredentialRetrieve(cancelCtx, "temp-cred")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestCredentialService_CredentialSubmit_ThenRetrieve(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	ctx := context.Background()

	// Submit stores the credential and notifies waiters.
	err := svc.CredentialSubmit(ctx, "submit-retrieve", []byte("submitted"))
	require.NoError(t, err)

	val, err := svc.CredentialRetrieve(ctx, "submit-retrieve")
	require.NoError(t, err)
	assert.Equal(t, []byte("submitted"), val)
}

func TestCredentialService_CredentialStore_MultipleDifferentNames(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	ctx := context.Background()

	names := []string{"cred-a", "cred-b", "cred-c"}
	for _, name := range names {
		err := svc.CredentialStore(ctx, name, []byte("value-"+name))
		require.NoError(t, err)
	}

	for _, name := range names {
		val, err := svc.CredentialRetrieve(ctx, name)
		require.NoError(t, err)
		assert.Equal(t, []byte("value-"+name), val)
	}
}

func TestCredentialService_CredentialDelete_DoubleDelete(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	ctx := context.Background()

	err := svc.CredentialStore(ctx, "double-del", []byte("value"))
	require.NoError(t, err)

	err = svc.CredentialDelete(ctx, "double-del")
	require.NoError(t, err)

	err = svc.CredentialDelete(ctx, "double-del")
	require.Error(t, err)
	assert.ErrorIs(t, err, credentials.ErrCredentialNotFound)
}

func TestCredentialService_CredentialRetrieve_CancelledContext(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// Retrieve on a non-existent credential with a cancelled context
	// should return the context error immediately.
	_, err := svc.CredentialRetrieve(cancelCtx, "never-stored")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestCredentialService_CredentialStore_BinaryValue(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	ctx := context.Background()

	binaryVal := []byte{0x00, 0x01, 0xFF, 0xFE, 0xAB, 0xCD}
	err := svc.CredentialStore(ctx, "binary-cred", binaryVal)
	require.NoError(t, err)

	val, err := svc.CredentialRetrieve(ctx, "binary-cred")
	require.NoError(t, err)
	assert.Equal(t, binaryVal, val)
}
