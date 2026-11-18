// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: AGPL-3.0-only

package ceremony

import (
	"context"
	"crypto/x509"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
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

// An explicit unseal after a failed single-admin SO persist must not close
// the sealed window: the standing SO intent keeps the handoff pending until
// a retried ceremony makes the SO record durable.
func TestInitialize_SingleAdminStandingSOIntentHoldsSealedWindowAfterUnseal(t *testing.T) {
	ctx := context.Background()
	e := newSingleAdminSOFaultEnv(t)
	b, svc := e.boot()

	_, err := svc.Initialize(ctx, e.ca, e.opts())
	require.ErrorIs(t, err, errSystemSOWrite)
	require.True(t, b.IsSealed())

	require.NoError(t, b.Unseal(ctx, seal.Credentials{}))
	require.False(t, b.IsSealed())
	assert.True(t, svc.HoldsSealedWindow(ctx), "a standing SO intent holds the sealed window")
	assert.True(t, svc.SystemSOHandoffPending(ctx), "a standing SO intent keeps the handoff pending")

	result, err := svc.Initialize(ctx, e.ca, e.opts())
	require.NoError(t, err, "the retry persists the SO")
	requireSOHandedOff(t, b, svc, result)
	assert.False(t, svc.HoldsSealedWindow(ctx), "a durable SO closes the sealed window")
	assert.False(t, svc.SystemSOHandoffPending(ctx), "a durable SO ends the handoff")
}

// The boot listener's gate answers sealed while HoldsSealedWindow holds, so
// after an explicit unseal between the failed SO persist and the retry, the
// Initialize retry must still pass sealedwindow.Decide as the listener
// builds the request; once the SO is durable the gate refuses it again.
func TestInitialize_SingleAdminStandingSOIntentRetryPassesListenerGateAfterUnseal(t *testing.T) {
	ctx := context.Background()
	e := newSingleAdminSOFaultEnv(t)
	b, svc := e.boot()

	_, err := svc.Initialize(ctx, e.ca, e.opts())
	require.ErrorIs(t, err, errSystemSOWrite)
	require.NoError(t, b.Unseal(ctx, seal.Credentials{}))
	require.False(t, b.IsSealed())

	gate := func() sealedwindow.Request {
		return sealedwindow.Request{
			Sealed:             svc.HoldsSealedWindow(ctx), // the listener probe's IsSealed
			Listener:           true,
			Allowlisted:        true,
			PreOperationalOnly: sealedwindow.PreOperationalOnly(sealedwindow.VerbCeremonyInitialize),
			EnrollmentOpen:     svc.EnrollmentOpen(ctx),
		}
	}
	require.Equal(t, sealedwindow.Serve, sealedwindow.Decide(gate()), "the listener gate admits the retry")

	result, err := svc.Initialize(ctx, e.ca, e.opts())
	require.NoError(t, err, "the retry persists the SO")
	requireSOHandedOff(t, b, svc, result)

	assert.False(t, b.IsSealed())
	assert.False(t, svc.EnrollmentOpen(ctx), "an unsealed barrier without the SO intent closes enrollment")
	assert.NotEqual(t, sealedwindow.Serve, sealedwindow.Decide(gate()), "a durable SO closes the listener gate")
}

// signCountingCA counts every certificate the ceremony asks the CA to sign.
type signCountingCA struct {
	*mockCA
	signs atomic.Int64
}

func (c *signCountingCA) SignCSR(ctx context.Context, csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, error) {
	c.signs.Add(1)
	return c.mockCA.SignCSR(ctx, csrPEM, opts)
}

func (c *signCountingCA) IssueCertificate(ctx context.Context, req *IssueCertRequest) (*IssuedCert, error) {
	c.signs.Add(1)
	return c.mockCA.IssueCertificate(ctx, req)
}

// TestInitialize_SingleAdminRetryVerifiesPINsBeforeSigning pins
// go-qrdb#1129's round-2 P1: a retry after a failed single-admin init
// presents PINs that are already provisioned, and a wrong or locked-out SO
// PIN is refused before the CA signs anything.
func TestInitialize_SingleAdminRetryVerifiesPINsBeforeSigning(t *testing.T) {
	ctx := context.Background()
	failOnce := func(t *testing.T, pins pin.PINBackend) (*singleAdminSOFaultEnv, *seal.StorageBarrier, *CeremonyService, *signCountingCA) {
		t.Helper()
		e := newSingleAdminSOFaultEnv(t)
		e.pins = pins
		b, svc := e.boot()
		ca := &signCountingCA{mockCA: e.ca}
		_, err := svc.Initialize(ctx, ca, e.opts())
		require.ErrorIs(t, err, errSystemSOWrite)
		ca.signs.Store(0)
		return e, b, svc, ca
	}

	t.Run("wrong SO PIN", func(t *testing.T) {
		e, b, svc, ca := failOnce(t, pintest.NewBackend(t))
		wrong := e.opts()
		wrong.SOPin = "not-the-so-pin"
		_, err := svc.Initialize(ctx, ca, wrong)
		require.ErrorIs(t, err, pin.ErrPINInvalid)
		assert.Zero(t, ca.signs.Load(), "a wrong SO PIN must not cost a CA signing")
		requireRecoverable(t, b, svc)

		result, err := svc.Initialize(ctx, ca, e.opts())
		require.NoError(t, err, "the right PIN still completes the ceremony")
		requireSOHandedOff(t, b, svc, result)
	})

	t.Run("wrong user PIN", func(t *testing.T) {
		e, _, svc, ca := failOnce(t, pintest.NewBackend(t))
		wrong := e.opts()
		wrong.UserPin = "not-the-user-pin"
		_, err := svc.Initialize(ctx, ca, wrong)
		require.ErrorIs(t, err, pin.ErrPINInvalid)
		assert.Zero(t, ca.signs.Load(), "a wrong user PIN must not cost a CA signing")
	})

	t.Run("locked-out SO PIN", func(t *testing.T) {
		e, _, svc, ca := failOnce(t, pin.NewLockoutBackend(pintest.NewBackend(t)))
		wrong := e.opts()
		wrong.SOPin = "not-the-so-pin"
		var err error
		for i := 0; i < 32 && !errors.Is(err, pin.ErrPINLocked); i++ {
			_, err = svc.Initialize(ctx, ca, wrong)
			require.Error(t, err)
		}
		require.ErrorIs(t, err, pin.ErrPINLocked, "repeated wrong SO PINs lock the counter out")
		_, err = svc.Initialize(ctx, ca, e.opts())
		require.ErrorIs(t, err, pin.ErrPINLocked, "a locked-out counter refuses even the right PIN")
		assert.Zero(t, ca.signs.Load(), "a locked-out SO PIN must not cost a CA signing")
	})
}

var errSystemSOIntentClear = errors.New("simulated system SO intent clear fault")

// intentClearFailBackend fails the first removal of the barrier's system SO
// intent marker, after the SO record itself has been written.
type intentClearFailBackend struct {
	seal.StorageBackend
	armed bool
}

func (b *intentClearFailBackend) Delete(ctx context.Context, key string) error {
	if b.armed && strings.HasSuffix(key, ".system-so-intent") {
		b.armed = false
		return errSystemSOIntentClear
	}
	return b.StorageBackend.Delete(ctx, key)
}

// TestInitialize_SingleAdminSOLessRetryDropsTheAbandonedSORecord pins
// go-qrdb#1129's Codex r1 P1: an attempt whose SO record write landed but
// whose intent clear failed leaves that record behind; a retry that names no
// SO must remove it, so first boot is handed no SO this initialization never
// issued.
func TestInitialize_SingleAdminSOLessRetryDropsTheAbandonedSORecord(t *testing.T) {
	ctx := context.Background()
	backend := &intentClearFailBackend{StorageBackend: seal.NewMemoryBackend(), armed: true}
	e := newSingleAdminSOFaultEnv(t)
	e.backend.StorageBackend = backend
	e.backend.armed = false
	b, svc := e.boot()

	_, err := svc.Initialize(ctx, e.ca, e.opts())
	require.ErrorIs(t, err, errSystemSOIntentClear)
	require.False(t, backend.armed, "the fault hit the intent clear")

	soLess := e.opts()
	soLess.SOUsername, soLess.SOCSRPEM = "", nil
	result, err := svc.Initialize(ctx, e.ca, soLess)
	require.NoError(t, err, "an SO-less retry completes the ceremony")
	assert.Equal(t, StateOperational, result.State)
	assert.Empty(t, result.SOCertPEM)
	assert.False(t, b.IsSealed())
	assert.Nil(t, svc.LastIssuedSystemSO())
	pending, err := svc.PendingSystemSO(ctx)
	require.NoError(t, err)
	assert.Nil(t, pending, "first boot must enrol no SO the retry did not issue")
	intent, err := b.SystemSOIntent(ctx)
	require.NoError(t, err)
	assert.False(t, intent)
}
