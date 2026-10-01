// Copyright 2026 Jeremy Hahn
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

package ceremony

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// registerAnonymouslyAfterInit returns the env of an initialized-then-sealed
// barrier's fresh ceremony -- a restart while sealed -- and an anonymous
// context as the boot-time sealed-window listener marks one.
func registerAnonymouslyAfterInit(t *testing.T) (*holderEnv, context.Context) {
	t.Helper()
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	barrier := testBarrier(t)
	require.NoError(t, barrier.Initialize(ctx, seal.Credentials{}))
	require.NoError(t, barrier.Seal())
	e.barrier = barrier
	e.svc = e.newService(barrier)
	return e, sealedwindow.WithAnonymous(ctx)
}

// TestRegisterOfficer_RefusesAnonymousAfterEnrollmentClosed: the
// PreOperationalOnly rule is enforced by the ceremony itself, under its own
// lock, not only by the listener's gate, so no path that reaches
// RegisterOfficer anonymously -- a gate that missed it, or one that sampled
// enrollment just before it closed -- can enrol a holder once enrollment is
// over, in either domain.
func TestRegisterOfficer_RefusesAnonymousAfterEnrollmentClosed(t *testing.T) {
	e, anon := registerAnonymouslyAfterInit(t)
	h := newHolder(t, "mallory", seal.RoleSecurityOfficer)
	for _, domain := range []seal.QuorumDomain{seal.DomainUnseal, seal.DomainRecovery} {
		_, err := e.svc.RegisterOfficer(anon, domain, h.request("admin"))
		require.ErrorIs(t, err, ErrEnrollmentClosed, domain)
	}
}

// TestRegisterOfficer_AnonymousAdmittedWhileEnrollmentOpen: on a fresh
// barrier the first-boot exemption still holds.
func TestRegisterOfficer_AnonymousAdmittedWhileEnrollmentOpen(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	h := newHolder(t, "alice", seal.RoleSecurityOfficer)
	_, err := e.svc.RegisterOfficer(sealedwindow.WithAnonymous(context.Background()), seal.DomainUnseal, h.request("admin"))
	require.NoError(t, err)
}

// TestRegisterOfficer_AuthenticatedCallerUnaffected: a caller whose context
// is not marked anonymous -- an authenticated listener's -- still enrols
// holders after enrollment closed, as rekey and recovery need. It does so on
// the unsealed barrier: on a sealed, initialized one no caller can enrol
// (ErrRegistrationWhileSealed), because the persisted roster is out of sight.
func TestRegisterOfficer_AuthenticatedCallerUnaffected(t *testing.T) {
	ctx := context.Background()
	e, _ := registerAnonymouslyAfterInit(t)
	h := newHolder(t, "alice", seal.RoleSecurityOfficer)
	_, err := e.svc.RegisterOfficer(ctx, seal.DomainUnseal, h.request("admin"))
	require.ErrorIs(t, err, ErrRegistrationWhileSealed, "sealed and initialized")

	barrier := testBarrier(t)
	require.NoError(t, barrier.Initialize(ctx, seal.Credentials{}))
	e.barrier = barrier
	e.svc = e.newService(barrier)
	_, err = e.svc.RegisterOfficer(ctx, seal.DomainUnseal, h.request("admin"))
	require.NoError(t, err)
}
