// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: AGPL-3.0-only

package ceremony

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// singleAdminSOFaultEnv is a single-admin ceremony over a storage backend
// whose first write of the pending system SO record fails.
type singleAdminSOFaultEnv struct {
	t       *testing.T
	backend *systemSOWriteFailBackend
	pins    pin.PINBackend
	ca      *mockCA
}

func newSingleAdminSOFaultEnv(t *testing.T) *singleAdminSOFaultEnv {
	t.Helper()
	return &singleAdminSOFaultEnv{
		t:       t,
		backend: &systemSOWriteFailBackend{StorageBackend: seal.NewMemoryBackend(), armed: true},
		pins:    pintest.NewBackend(t),
		ca:      newMockCA(t),
	}
}

// boot builds a fresh barrier and ceremony over the shared backend and PINs,
// as a node (re)start does.
func (e *singleAdminSOFaultEnv) boot() (*seal.StorageBarrier, *CeremonyService) {
	e.t.Helper()
	strategy, err := seal.NewSoftwareStrategy(context.Background(), []byte("test-master-password"))
	require.NoError(e.t, err)
	b, err := seal.NewStorageBarrier(testLogger(), e.backend, seal.BarrierConfig{RootKeyPath: "test/root-key"}, strategy)
	require.NoError(e.t, err)
	svc, err := NewCeremonyService(defaultConfig(), b, testLogger())
	require.NoError(e.t, err)
	svc.SetPINBackend(e.pins)
	svc.SetTrustDomain(testTrustDomain)
	return b, svc
}

func (e *singleAdminSOFaultEnv) opts() *InitializeOptions {
	e.t.Helper()
	adminCSR, _ := generateCSRPEM(e.t, "ignored-cn")
	soCSR, _ := generateCSRPEM(e.t, "ignored-too")
	return &InitializeOptions{
		SOPin:       "test-so-pin",
		UserPin:     "test-user-pin",
		AdminCSRPEM: adminCSR,
		SOUsername:  "secofficer",
		SOCSRPEM:    soCSR,
	}
}

// requireRecoverable asserts the state a failed single-admin SO record
// write leaves: the barrier sealed (so the sealed-window listener, whose
// probe is the barrier, keeps serving), the ceremony awaiting init, and no
// admin or SO handed to the composer.
func requireRecoverable(t *testing.T, b *seal.StorageBarrier, svc *CeremonyService) {
	t.Helper()
	assert.True(t, b.IsSealed(), "the barrier must stay sealed until the SO record is durable")
	assert.Equal(t, StateAwaitingInit, svc.State())
	assert.Nil(t, svc.LastIssuedSystemSO())
	adminPEM, _ := svc.LastIssuedAdminCertPEM()
	assert.Empty(t, adminPEM, "first boot must not be handed an admin without its SO")
	pending, err := svc.PendingSystemSO(context.Background())
	require.Error(t, err, "the SO record is behind the sealed barrier")
	assert.Nil(t, pending)
}

// requireSOHandedOff asserts a successful retry recorded the SO durably.
func requireSOHandedOff(t *testing.T, b *seal.StorageBarrier, svc *CeremonyService, result *InitResult) {
	t.Helper()
	assert.Equal(t, StateOperational, result.State)
	assert.NotEmpty(t, result.SOCertPEM)
	assert.False(t, b.IsSealed())
	assert.Equal(t, StateOperational, svc.State())
	pending, err := svc.PendingSystemSO(context.Background())
	require.NoError(t, err)
	require.NotNil(t, pending, "first boot is handed the SO")
	assert.Equal(t, "secofficer", pending.Username)
	assert.Equal(t, result.SOCertPEM, pending.CertPEM)
}

// TestInitialize_SingleAdminSystemSOPersistFaultStaysSealed pins
// go-qrdb#1129's round-2 finding: a failed SO record write in single-admin
// mode reseals the barrier, so startup does not advance into a first boot
// without the SO, and a retried Initialize records it.
func TestInitialize_SingleAdminSystemSOPersistFaultStaysSealed(t *testing.T) {
	ctx := context.Background()
	e := newSingleAdminSOFaultEnv(t)
	b, svc := e.boot()

	_, err := svc.Initialize(ctx, e.ca, e.opts())
	require.ErrorIs(t, err, errSystemSOWrite)
	require.False(t, e.backend.armed, "the fault hit the SO record write")
	requireRecoverable(t, b, svc)

	result, err := svc.Initialize(ctx, e.ca, e.opts())
	require.NoError(t, err, "the same listener retries the ceremony")
	requireSOHandedOff(t, b, svc, result)
}

// TestInitialize_SingleAdminSystemSOPersistFaultIsRetriableAcrossRestart:
// a process that dies after the fault restarts sealed and awaiting init,
// and the ceremony retried on the restarted node records the SO.
func TestInitialize_SingleAdminSystemSOPersistFaultIsRetriableAcrossRestart(t *testing.T) {
	ctx := context.Background()
	e := newSingleAdminSOFaultEnv(t)
	_, svc := e.boot()

	_, err := svc.Initialize(ctx, e.ca, e.opts())
	require.ErrorIs(t, err, errSystemSOWrite)

	b2, svc2 := e.boot()
	requireRecoverable(t, b2, svc2)

	wrongPIN := e.opts()
	wrongPIN.SOPin = "not-the-so-pin"
	_, err = svc2.Initialize(ctx, e.ca, wrongPIN)
	require.Error(t, err, "the retry must prove the provisioned SO PIN")

	result, err := svc2.Initialize(ctx, e.ca, e.opts())
	require.NoError(t, err, "the restarted node retries the ceremony")
	requireSOHandedOff(t, b2, svc2, result)

	_, err = svc2.Initialize(ctx, e.ca, e.opts())
	assert.ErrorIs(t, err, ErrAlreadyInitialized)
}

// TestInitialize_SingleAdminSystemSOPersistFaultLeavesIntentStanding pins
// go-qrdb#1129's round-5 finding: the single-admin path records a durable SO
// intent before barrier init, so a failed SO record write (or a crash after
// init) leaves it standing for startup to honor, readable while sealed and
// across a restart; the retry that records the SO clears it.
func TestInitialize_SingleAdminSystemSOPersistFaultLeavesIntentStanding(t *testing.T) {
	ctx := context.Background()
	e := newSingleAdminSOFaultEnv(t)
	_, svc := e.boot()

	_, err := svc.Initialize(ctx, e.ca, e.opts())
	require.ErrorIs(t, err, errSystemSOWrite)

	b2, svc2 := e.boot()
	require.True(t, b2.IsSealed())
	pending, err := b2.SystemSOIntent(ctx)
	require.NoError(t, err)
	require.True(t, pending, "the SO intent must survive the failed write and the restart")

	result, err := svc2.Initialize(ctx, e.ca, e.opts())
	require.NoError(t, err)
	requireSOHandedOff(t, b2, svc2, result)
	pending, err = b2.SystemSOIntent(ctx)
	require.NoError(t, err)
	assert.False(t, pending, "a recorded SO clears the intent")
}
