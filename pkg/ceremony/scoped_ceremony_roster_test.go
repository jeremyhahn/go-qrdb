// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// scoped_ceremony_roster_test.go covers ScopedCeremonyService.InitializeShamirWithRoster
// -- the tenant/database-barrier inline-roster path -- proving the
// go-qrdb#560-style guarantee review B2 asked for: every officer is
// validated as one pure batch before any of them is registered, and a
// failure that validation cannot catch (a real registration conflict, or
// InitializeShamir itself) rolls every already-registered officer back,
// leaving nothing persisted for a corrected retry to collide with.
package ceremony

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// officerConfig builds hs registration as the []OfficerConfig shape
// InitializeShamirWithRoster validates and registers.
func (h *testHolder) officerConfig() OfficerConfig {
	cfg := OfficerConfig{Username: h.username, Role: h.role, CSRPEM: h.csrPEM}
	if h.encKEM != "" {
		cfg.EncryptionKEM = h.encKEM
		cfg.EncryptionKey = h.encPub
	}
	if h.auth != nil {
		cfg.Factors = []FactorEnrolment{{Name: testFactorName, Params: h.auth.EnrollParams()}}
	}
	return cfg
}

// TestScopedCeremonyService_InitializeShamirWithRoster_Success registers
// and initializes a 2-of-2 roster atomically, over a tenant scope.
func TestScopedCeremonyService_InitializeShamirWithRoster_Success(t *testing.T) {
	ctx := context.Background()
	backend := seal.NewMemoryBackend()
	barrier := shamirBarrier(t, backend, 2, 2)
	svc, err := NewTenantCeremonyService("acme", backend, HolderDeps{TrustDomain: testTrustDomain, CA: testCA(t), Factors: testFactors(t)}, testLogger())
	require.NoError(t, err)
	barrier.SetCeremonyDistributor(svc)
	barrier.SetHolderVerifier(svc)

	a := newHolder(t, "alice", "")
	b := newHolder(t, "bob", seal.RoleCustodian)
	roster := []OfficerConfig{a.officerConfig(), b.officerConfig()}

	params, results, err := svc.InitializeShamirWithRoster(ctx, barrier, roster, "tenant-admin", seal.FactorPolicyDesignated)
	require.NoError(t, err)
	assert.Equal(t, 2, params.Threshold)
	assert.Equal(t, 2, params.TotalShares)
	require.Len(t, results, 2)
	assert.NotEmpty(t, results[0].CertPEM)
	assert.NotEmpty(t, results[1].CertPEM)

	list, err := svc.ListOfficers(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 2, "both officers from the roster must be registered")
}

// TestScopedCeremonyService_InitializeShamirWithRoster_PreValidationRejectsNothing
// proves the pure, side-effect-free pre-check: a roster whose SECOND entry
// has no CSR is refused before the FIRST (valid) entry is ever registered
// -- the batch failure mode go-qrdb#560 review B3 closed for the system
// barrier, applied here for a tenant barrier's own inline roster.
func TestScopedCeremonyService_InitializeShamirWithRoster_PreValidationRejectsNothing(t *testing.T) {
	ctx := context.Background()
	backend := seal.NewMemoryBackend()
	barrier := shamirBarrier(t, backend, 2, 2)
	svc, err := NewTenantCeremonyService("acme", backend, HolderDeps{TrustDomain: testTrustDomain, CA: testCA(t), Factors: testFactors(t)}, testLogger())
	require.NoError(t, err)
	barrier.SetCeremonyDistributor(svc)
	barrier.SetHolderVerifier(svc)

	a := newHolder(t, "alice", "")
	badCfg := OfficerConfig{Username: "bob"} // no CSRPEM
	roster := []OfficerConfig{a.officerConfig(), badCfg}

	_, _, err = svc.InitializeShamirWithRoster(ctx, barrier, roster, "tenant-admin", seal.FactorPolicyDesignated)
	require.ErrorIs(t, err, ErrMissingCSRPEM)

	list, err := svc.ListOfficers(ctx)
	require.NoError(t, err)
	assert.Empty(t, list, "the pure pre-check must refuse the whole batch before alice, the valid entry, is ever registered")
}

// TestScopedCeremonyService_InitializeShamirWithRoster_DuplicateUsernameRollsBack
// proves the rollback path validation cannot reach: two roster entries
// name the SAME username with different keys (so the pure duplicate-key
// pre-check does not catch it), the first registers, the second's
// register call fails on the live nameFree check, and the first must be
// rolled back -- nothing persisted for a corrected retry to collide with.
func TestScopedCeremonyService_InitializeShamirWithRoster_DuplicateUsernameRollsBack(t *testing.T) {
	ctx := context.Background()
	backend := seal.NewMemoryBackend()
	barrier := shamirBarrier(t, backend, 2, 2)
	svc, err := NewTenantCeremonyService("acme", backend, HolderDeps{TrustDomain: testTrustDomain, CA: testCA(t), Factors: testFactors(t)}, testLogger())
	require.NoError(t, err)
	barrier.SetCeremonyDistributor(svc)
	barrier.SetHolderVerifier(svc)

	first := newHolder(t, "alice", "")
	second := newHolder(t, "alice", "") // same username, different CSR/key
	roster := []OfficerConfig{first.officerConfig(), second.officerConfig()}

	_, _, err = svc.InitializeShamirWithRoster(ctx, barrier, roster, "tenant-admin", seal.FactorPolicyDesignated)
	require.ErrorIs(t, err, ErrDuplicateUsername)

	list, err := svc.ListOfficers(ctx)
	require.NoError(t, err)
	assert.Empty(t, list, "the first officer must be rolled back when the second, duplicate-named one fails to register")

	// A corrected retry (alice once, named uniquely, plus a second distinct
	// officer to satisfy the 2-of-2 threshold) must succeed cleanly -- the
	// rollback truly left nothing behind for alice's name to collide with.
	bob := newHolder(t, "bob", seal.RoleCustodian)
	retryRoster := []OfficerConfig{first.officerConfig(), bob.officerConfig()}
	_, results, err := svc.InitializeShamirWithRoster(ctx, barrier, retryRoster, "tenant-admin", seal.FactorPolicyDesignated)
	require.NoError(t, err)
	require.Len(t, results, 2)
}
