// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: AGPL-3.0-only

package ceremony

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// ShamirSOWriteFaultForTest returns a ceremony core whose 2-of-3
// Initialize issued a system SO and failed the SO record write after
// everything else committed, and retry, which repeats that Initialize. For
// the external tests that drive first boot against such a core.
func ShamirSOWriteFaultForTest(t *testing.T) (core *CeremonyService, retry func() (*InitResult, error)) {
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
	return e.svc, func() (*InitResult, error) { return e.svc.Initialize(ctx, e.ca, opts()) }
}
