// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: AGPL-3.0-only

package ceremony

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

var errSystemSOWrite = errors.New("simulated system SO record write fault")

// systemSOWriteFailBackend fails exactly one write: the first write of the
// pending system SO record. Every other read and write, including a later
// write of that same record, reaches the real backend.
type systemSOWriteFailBackend struct {
	seal.StorageBackend
	armed bool
}

func (b *systemSOWriteFailBackend) Put(ctx context.Context, key string, value []byte) error {
	if b.armed && strings.HasSuffix(key, pendingSystemSOKey) {
		b.armed = false
		return errSystemSOWrite
	}
	return b.StorageBackend.Put(ctx, key, value)
}

// TestInitialize_ShamirSystemSOPersistFaultIsRetriableAcrossRestart pins
// go-qrdb#1129's round-1 finding: when persisting the system SO fails after
// InitializeShamir committed the barrier and the holder envelopes, the
// ceremony is left enrolling, and after a restart and an unseal a retried
// Initialize persists the SO (and only the SO), so strict first boot is
// handed it.
func TestInitialize_ShamirSystemSOPersistFaultIsRetriableAcrossRestart(t *testing.T) {
	ctx := context.Background()
	backend := &systemSOWriteFailBackend{StorageBackend: seal.NewMemoryBackend(), armed: true}
	e := &holderEnv{t: t, backend: backend, ca: testCA(t), pins: pintest.NewBackend(t), factors: testFactors(t)}
	e.barrier = shamirBarrier(t, e.backend, 2, 3)
	e.svc = e.newService(e.barrier)

	alice := newHolder(t, "alice", seal.RoleSecurityOfficer)
	bob := newHolder(t, "bob", seal.RoleSecurityOfficer)
	carol := newHolder(t, "carol", seal.RoleSecurityOfficer)
	soCSR, _ := generateCSRPEM(t, "ignored-cn")
	opts := func() *InitializeOptions {
		o := initOpts(2, alice, bob, carol)
		o.SOUsername = "secofficer"
		o.SOCSRPEM = soCSR
		return o
	}

	_, err := e.svc.Initialize(ctx, e.ca, opts())
	require.ErrorIs(t, err, errSystemSOWrite)
	require.False(t, backend.armed, "the fault hit the SO record write")
	assert.Equal(t, StateEnrolling, e.svc.State(), "the barrier is initialized: the ceremony is enrolling, as a restart restores it")

	// Every holder claims its envelope. The SO record is still missing, so
	// the ceremony stays enrolling: an operational one would refuse the retry.
	officers, err := e.svc.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, err)
	for _, o := range officers {
		for _, h := range []*testHolder{alice, bob, carol} {
			if o.Username == h.username {
				h.thumb = o.CertThumbprint
			}
		}
	}
	e.claim(seal.DomainUnseal, alice)
	e.claim(seal.DomainUnseal, bob)
	e.claim(seal.DomainUnseal, carol)
	assert.Equal(t, StateEnrolling, e.svc.State(), "no operational ceremony without the SO record")

	b2, svc2 := e.restart(2, 3)
	require.Equal(t, StateEnrolling, svc2.State())
	_, err = svc2.Initialize(ctx, e.ca, opts())
	require.Error(t, err, "the SO record cannot be written behind a sealed barrier")

	_, err = e.submit(svc2, b2, seal.DomainUnseal, seal.OpUnseal, alice, "")
	require.NoError(t, err)
	_, err = e.submit(svc2, b2, seal.DomainUnseal, seal.OpUnseal, bob, "")
	require.NoError(t, err)
	require.False(t, b2.IsSealed())

	wrongPIN := opts()
	wrongPIN.SOPin = "not-the-so-pin"
	_, err = svc2.Initialize(ctx, e.ca, wrongPIN)
	require.Error(t, err, "the retry must prove the provisioned SO PIN")

	result, err := svc2.Initialize(ctx, e.ca, opts())
	require.NoError(t, err, "the retried ceremony persists the SO")
	assert.Equal(t, StateEnrolling, result.State, "claims are held in memory: a restarted ceremony has none")
	assert.NotEmpty(t, result.SOCertPEM)
	assert.Empty(t, result.Holders, "the retry enrolls no officer")
	assert.Equal(t, StateEnrolling, svc2.State())

	// A fresh service over the same storage reads the SO from the barrier.
	pending, err := e.newService(b2).PendingSystemSO(ctx)
	require.NoError(t, err)
	require.NotNil(t, pending, "first boot is handed the SO")
	assert.Equal(t, "secofficer", pending.Username)
	assert.Equal(t, result.SOCertPEM, pending.CertPEM)

	_, err = svc2.Initialize(ctx, e.ca, opts())
	assert.ErrorIs(t, err, ErrAlreadyInitialized, "once the SO is recorded the ceremony refuses a replay")
}

// TestInitialize_ShamirSystemSOPersistFaultHoldsEnrollingAcrossRestart
// covers claims made after a restart: the carried result is gone, but the
// barrier's write-ahead marker still holds the ceremony enrolling until a
// retried Initialize writes the SO record.
func TestInitialize_ShamirSystemSOPersistFaultHoldsEnrollingAcrossRestart(t *testing.T) {
	ctx := context.Background()
	backend := &systemSOWriteFailBackend{StorageBackend: seal.NewMemoryBackend(), armed: true}
	e := &holderEnv{t: t, backend: backend, ca: testCA(t), pins: pintest.NewBackend(t), factors: testFactors(t)}
	e.barrier = shamirBarrier(t, e.backend, 2, 3)
	e.svc = e.newService(e.barrier)

	alice := newHolder(t, "alice", seal.RoleSecurityOfficer)
	bob := newHolder(t, "bob", seal.RoleSecurityOfficer)
	carol := newHolder(t, "carol", seal.RoleSecurityOfficer)
	soCSR, _ := generateCSRPEM(t, "ignored-cn")
	opts := func() *InitializeOptions {
		o := initOpts(2, alice, bob, carol)
		o.SOUsername = "secofficer"
		o.SOCSRPEM = soCSR
		return o
	}

	_, err := e.svc.Initialize(ctx, e.ca, opts())
	require.ErrorIs(t, err, errSystemSOWrite)
	require.False(t, backend.armed, "the fault hit the SO record write")
	officers, err := e.svc.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, err)
	for _, o := range officers {
		for _, h := range []*testHolder{alice, bob, carol} {
			if o.Username == h.username {
				h.thumb = o.CertThumbprint
			}
		}
	}
	e.claim(seal.DomainUnseal, alice)
	e.claim(seal.DomainUnseal, bob)

	b2, svc2 := e.restart(2, 3)
	require.Equal(t, StateEnrolling, svc2.State())
	_, err = e.submit(svc2, b2, seal.DomainUnseal, seal.OpUnseal, alice, "")
	require.NoError(t, err)
	_, err = e.submit(svc2, b2, seal.DomainUnseal, seal.OpUnseal, bob, "")
	require.NoError(t, err)
	require.False(t, b2.IsSealed())
	e.barrier, e.svc = b2, svc2

	// A fresh distribution after the restart hands every holder a new
	// envelope; claiming them all must not make the ceremony operational
	// while its SO record is unwritten.
	roster, err := svc2.PrepareRoster(ctx, seal.DomainUnseal, 2, "", nil)
	require.NoError(t, err)
	key, err := secure.NewBufferFromBytes(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	defer key.Destroy()
	_, err = svc2.Distribute(ctx, key, roster, []byte("digest-"+roster.Distribution))
	require.NoError(t, err)
	require.NoError(t, svc2.Committed(ctx, roster, nil))
	e.claim(seal.DomainUnseal, alice)
	e.claim(seal.DomainUnseal, bob)
	e.claim(seal.DomainUnseal, carol)
	assert.Equal(t, StateEnrolling, svc2.State(), "no operational ceremony without the SO record, restart or not")

	result, err := svc2.Initialize(ctx, e.ca, opts())
	require.NoError(t, err, "the retried ceremony persists the SO")
	assert.NotEmpty(t, result.SOCertPEM)
	assert.Equal(t, StateOperational, result.State, "the retry completes the claims' transition")
	assert.Equal(t, StateOperational, svc2.State())
	_, err = b2.Get(ctx, systemSOWriteOutstandingKey)
	assert.ErrorIs(t, err, seal.ErrSecretNotFound, "the written record retires its marker")
}

// An M-of-N Initialize whose SO record write failed returned none of what
// it committed. Its retry on the same service must hand all of it back
// with the SO it records: the holders, the share count, the TLS
// certificate and SPKI pin, and the generated bootstrap admin credential,
// whose private key exists nowhere else.
func TestInitialize_ShamirSystemSOPersistRetryCarriesTheFailedCallsCredentials(t *testing.T) {
	ctx := context.Background()
	backend := &systemSOWriteFailBackend{StorageBackend: seal.NewMemoryBackend(), armed: true}
	e := &holderEnv{t: t, backend: backend, ca: testCA(t), pins: pintest.NewBackend(t), factors: testFactors(t)}
	e.barrier = shamirBarrier(t, e.backend, 2, 3)
	e.svc = e.newService(e.barrier)

	alice := newHolder(t, "alice", seal.RoleSecurityOfficer)
	bob := newHolder(t, "bob", seal.RoleSecurityOfficer)
	carol := newHolder(t, "carol", seal.RoleSecurityOfficer)
	soCSR, _ := generateCSRPEM(t, "ignored-cn")
	opts := func() *InitializeOptions {
		o := initOpts(2, alice, bob, carol)
		o.SOUsername = "secofficer"
		o.SOCSRPEM = soCSR
		return o
	}

	_, err := e.svc.Initialize(ctx, e.ca, opts())
	require.ErrorIs(t, err, errSystemSOWrite)
	require.False(t, backend.armed, "the fault hit the SO record write")
	adminPEM, _ := e.svc.LastIssuedAdminCertPEM()
	require.NotEmpty(t, adminPEM, "the failed call issued the admin credential")

	// Every holder claims its envelope before the retry. The SO record is
	// still missing, so the ceremony must stay enrolling: an operational
	// one refuses Initialize and the SO could never be recorded.
	officers, err := e.svc.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, err)
	for _, o := range officers {
		for _, h := range []*testHolder{alice, bob, carol} {
			if o.Username == h.username {
				h.thumb = o.CertThumbprint
			}
		}
	}
	for _, h := range []*testHolder{alice, bob, carol} {
		e.claim(seal.DomainUnseal, h)
	}
	require.Equal(t, StateEnrolling, e.svc.State(), "no operational ceremony without the SO record")

	result, err := e.svc.Initialize(ctx, e.ca, opts())
	require.NoError(t, err, "the retry records the SO")
	assert.Equal(t, StateOperational, result.State, "recording the SO completes the fully claimed ceremony")
	assert.Equal(t, StateOperational, e.svc.State())
	assert.NotEmpty(t, result.SOCertPEM)
	assert.Len(t, result.Holders, 3, "the failed call's holders")
	assert.Equal(t, 3, result.ShareCount)
	assert.NotEmpty(t, result.TLSCertPEM)
	assert.NotEmpty(t, result.SPKIPin)
	assert.NotEmpty(t, result.CACertPEM)
	assert.Equal(t, adminPEM, result.AdminCertPEM, "the admin certificate the failed call issued")
	assert.NotEmpty(t, result.AdminKeyPEM, "the generated admin key the failed call could not return")
	assert.Equal(t, AdminCredentialGenerated, result.AdminCredentialSource)

	pending, err := e.newService(e.barrier).PendingSystemSO(ctx)
	require.NoError(t, err)
	require.NotNil(t, pending)
	assert.Equal(t, result.SOCertPEM, pending.CertPEM, "the result names the SO the record holds")

	_, err = e.svc.Initialize(ctx, e.ca, opts())
	assert.ErrorIs(t, err, ErrAlreadyInitialized, "the carried result is handed back once")
}

// TestInitialize_ShamirSystemSOPersistRetryRefusesADifferentRoster pins
// go-qrdb#1579 Codex round 1 P1 (1): the carried result's holders belong to
// the failed call's roster, so a same-process retry naming fewer, other or
// reordered officers, or another threshold, is refused before it records
// anything, and the carried result survives for the matching retry, whose
// holders carry their own usernames.
func TestInitialize_ShamirSystemSOPersistRetryRefusesADifferentRoster(t *testing.T) {
	ctx := context.Background()
	backend := &systemSOWriteFailBackend{StorageBackend: seal.NewMemoryBackend(), armed: true}
	e := &holderEnv{t: t, backend: backend, ca: testCA(t), pins: pintest.NewBackend(t), factors: testFactors(t)}
	e.barrier = shamirBarrier(t, e.backend, 2, 3)
	e.svc = e.newService(e.barrier)

	alice := newHolder(t, "alice", seal.RoleSecurityOfficer)
	bob := newHolder(t, "bob", seal.RoleSecurityOfficer)
	carol := newHolder(t, "carol", seal.RoleSecurityOfficer)
	soCSR, _ := generateCSRPEM(t, "ignored-cn")
	opts := func(threshold int, holders ...*testHolder) *InitializeOptions {
		o := initOpts(threshold, holders...)
		o.SOUsername = "secofficer"
		o.SOCSRPEM = soCSR
		return o
	}

	_, err := e.svc.Initialize(ctx, e.ca, opts(2, alice, bob, carol))
	require.ErrorIs(t, err, errSystemSOWrite)

	for name, retry := range map[string]*InitializeOptions{
		"fewer officers":     opts(2, alice, bob),
		"reordered officers": opts(2, bob, alice, carol),
		"other threshold":    opts(3, alice, bob, carol),
	} {
		_, err := e.svc.Initialize(ctx, e.ca, retry)
		require.ErrorIs(t, err, ErrInitializeRetryRoster, name)
		pending, perr := e.newService(e.barrier).PendingSystemSO(ctx)
		require.NoError(t, perr, name)
		require.Nil(t, pending, "%s: a refused retry records no SO", name)
		require.Equal(t, StateEnrolling, e.svc.State(), name)
	}

	result, err := e.svc.Initialize(ctx, e.ca, opts(2, alice, bob, carol))
	require.NoError(t, err, "the matching retry records the SO")
	require.Len(t, result.Holders, 3, "the carried result survived the refusals")
	for i, want := range []string{"alice", "bob", "carol"} {
		assert.Equal(t, want, result.Holders[i].Username, "holders carry their own usernames")
	}
	assert.NotEmpty(t, result.AdminKeyPEM, "the carried admin key survived the refusals")
}
