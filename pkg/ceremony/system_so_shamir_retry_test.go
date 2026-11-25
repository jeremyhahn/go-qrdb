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

// TestInitialize_ShamirSystemSOWriteFaultRollsBackTheInitialization pins
// PR #1579's review finding: the M-of-N system SO record is written inside
// Distribute, before the barrier commits the Shamir initialization, so a
// failed write fails the whole initialization. Nothing is committed: the
// barrier stays uninitialized, a restart straight after the failure (no
// share claimed) loses nothing, and the retried Initialize completes with
// the SO record readable after unseal.
func TestInitialize_ShamirSystemSOWriteFaultRollsBackTheInitialization(t *testing.T) {
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
	assert.Equal(t, StateAwaitingInit, e.svc.State(), "the initialization rolled back: nothing is committed")
	assert.True(t, e.barrier.IsSealed(), "the barrier rolled back to sealed")
	assert.True(t, e.svc.HoldsSealedWindow(ctx), "the sealed window stays up for the retry")

	// A restart straight after the failure, before any share is claimed.
	b2, svc2 := e.restart(2, 3)
	require.Equal(t, StateAwaitingInit, svc2.State(), "the restarted barrier is not initialized")
	e.barrier, e.svc = b2, svc2

	result, err := svc2.Initialize(ctx, e.ca, opts())
	require.NoError(t, err, "the retried Initialize completes")
	assert.Equal(t, StateEnrolling, result.State)
	assert.NotEmpty(t, result.SOCertPEM)
	assert.NotEmpty(t, result.AdminCertPEM, "the retry returns the admin credential")
	require.Len(t, result.Holders, 3, "the retry enrolls every officer")
	officers, err := svc2.ListOfficers(ctx, seal.DomainUnseal)
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
	assert.Equal(t, StateOperational, svc2.State(), "every envelope claimed: the SO record is already durable")

	// After another restart the barrier unseals and first boot reads the SO.
	b3, svc3 := e.restart(2, 3)
	_, err = e.submit(svc3, b3, seal.DomainUnseal, seal.OpUnseal, alice, "")
	require.NoError(t, err)
	_, err = e.submit(svc3, b3, seal.DomainUnseal, seal.OpUnseal, bob, "")
	require.NoError(t, err)
	require.False(t, b3.IsSealed())
	pending, err := e.newService(b3).PendingSystemSO(ctx)
	require.NoError(t, err)
	require.NotNil(t, pending, "first boot is handed the SO")
	assert.Equal(t, "secofficer", pending.Username)
	assert.Equal(t, result.SOCertPEM, pending.CertPEM)
}
