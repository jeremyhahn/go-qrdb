// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: AGPL-3.0-only

package ceremony

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/stretchr/testify/require"
)

// ShamirSOPersistFault is a 2-of-3 ceremony whose system SO record write
// failed, restarted and unsealed by two holders: Service is enrolling, its
// barrier is unsealed and it holds no SO record, the state a retried
// Initialize must repair.
type ShamirSOPersistFault struct {
	Service *CeremonyService
	CA      CeremonyCA
	// Options returns the ceremony's original options, the ones a retry
	// repeats.
	Options func() *InitializeOptions
	// Reopen builds a fresh service over the same storage.
	Reopen func() *CeremonyService
}

// NewShamirSOPersistFault drives the ceremony to that state for tests
// outside the package (the composed service in pkg/service).
func NewShamirSOPersistFault(t *testing.T) *ShamirSOPersistFault {
	t.Helper()
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
	_, err = e.submit(svc2, b2, seal.DomainUnseal, seal.OpUnseal, alice, "")
	require.NoError(t, err)
	_, err = e.submit(svc2, b2, seal.DomainUnseal, seal.OpUnseal, bob, "")
	require.NoError(t, err)
	require.False(t, b2.IsSealed())

	return &ShamirSOPersistFault{
		Service: svc2,
		CA:      e.ca,
		Options: opts,
		Reopen:  func() *CeremonyService { return e.newService(b2) },
	}
}
