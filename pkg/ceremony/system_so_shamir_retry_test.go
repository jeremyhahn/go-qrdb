// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: AGPL-3.0-only

package ceremony

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
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

	// Two of three holders claim their envelopes; the ceremony stays enrolling.
	officers, err := e.svc.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, err)
	for _, o := range officers {
		for _, h := range []*testHolder{alice, bob} {
			if o.Username == h.username {
				h.thumb = o.CertThumbprint
			}
		}
	}
	e.claim(seal.DomainUnseal, alice)
	e.claim(seal.DomainUnseal, bob)

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
	assert.Equal(t, StateEnrolling, result.State)
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
