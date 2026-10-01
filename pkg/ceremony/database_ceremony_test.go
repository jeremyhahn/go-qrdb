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
	"encoding/hex"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestDatabaseCeremony_HolderLifecycle drives a database barrier's holders
// through the one holder engine: enrolment in the tenant's holder namespace,
// distribution, claim, and a quorum seal and unseal.
func TestDatabaseCeremony_HolderLifecycle(t *testing.T) {
	ctx := context.Background()
	backend := seal.NewMemoryBackend()
	barrier := shamirBarrier(t, backend, 2, 2)
	svc, err := NewDatabaseCeremonyService("acme", "orders", backend, HolderDeps{CA: testCA(t), TrustDomain: testTrustDomain, Factors: testFactors(t)}, testLogger())
	require.NoError(t, err)
	barrier.SetCeremonyDistributor(svc)
	barrier.SetHolderVerifier(svc)

	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", seal.RoleCustodian)
	for _, h := range []*testHolder{a, b} {
		res, err := svc.RegisterOfficer(ctx, h.request("db-admin"))
		require.NoError(t, err)
		h.recordCert(t, res.CertPEM)
		cert, err := mtls.ParseCertificatePEM(res.CertPEM)
		require.NoError(t, err)
		id, ok, err := spiffe.ParseID(cert.URIs[0])
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, "acme", id.TenantID, "a database holder's identity is in the database's tenant")
		assert.Equal(t, spiffe.KindHolder, id.Kind)
	}
	_, err = barrier.InitializeShamir(ctx, seal.FactorPolicyDesignated)
	require.NoError(t, err)

	for _, h := range []*testHolder{a, b} {
		ch, err := svc.BeginClaimShare(ctx, h.username, h.thumb)
		require.NoError(t, err)
		sig, err := SignShareClaim(h.certKey, DatabaseClaimLabel("orders"), h.username, ch.Nonce)
		require.NoError(t, err)
		res, err := svc.CompleteClaimShare(ctx, h.username, hex.EncodeToString(ch.Nonce), sig, h.thumb)
		require.NoError(t, err)
		h.envelope = res.Envelope
	}

	submit := func(op seal.QuorumOp, h *testHolder) (*seal.QuorumOutcome, error) {
		ch, err := svc.BeginQuorum(ctx, barrier, op, h.username, "")
		if err != nil {
			return nil, err
		}
		return barrier.SubmitHolderShare(ctx, &seal.HolderSubmission{
			Domain: seal.DomainUnseal, Op: op, Holder: h.username,
			Proof: seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: h.answer(t, ch)},
			Share: h.openShare(t),
		})
	}
	_, err = submit(seal.OpSeal, a)
	require.NoError(t, err)
	out, err := submit(seal.OpSeal, b)
	require.NoError(t, err)
	require.True(t, out.Complete)
	require.True(t, barrier.IsSealed())

	_, err = submit(seal.OpUnseal, b)
	require.NoError(t, err)
	out, err = submit(seal.OpUnseal, a)
	require.NoError(t, err)
	assert.True(t, out.Complete)
	assert.False(t, barrier.IsSealed())

	svc.Cleanup()
}

// TestDatabaseCeremony_ScopesAreDistinct pins that a database's holder
// registry is its own: a holder enrolled for one database is unknown to the
// tenant's barrier and to another database, even over the same storage.
func TestDatabaseCeremony_ScopesAreDistinct(t *testing.T) {
	ctx := context.Background()
	backend := seal.NewMemoryBackend()
	deps := HolderDeps{CA: testCA(t), TrustDomain: testTrustDomain}
	orders, err := NewDatabaseCeremonyService("acme", "orders", backend, deps, testLogger())
	require.NoError(t, err)
	billing, err := NewDatabaseCeremonyService("acme", "billing", backend, deps, testLogger())
	require.NoError(t, err)
	tenant, err := NewTenantCeremonyService("acme", backend, deps, testLogger())
	require.NoError(t, err)

	a := newHolder(t, "alice", "")
	_, err = orders.RegisterOfficer(ctx, a.request("db-admin"))
	require.NoError(t, err)

	for name, svc := range map[string]*ScopedCeremonyService{"billing": billing, "tenant": tenant} {
		list, err := svc.ListOfficers(ctx)
		require.NoError(t, err, name)
		assert.Empty(t, list, "%s sees another scope's holder", name)
	}
	list, err := orders.ListOfficers(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

// TestDatabaseCeremony_ConstructorRefusals covers the constructor checks.
func TestDatabaseCeremony_ConstructorRefusals(t *testing.T) {
	deps := HolderDeps{CA: testCA(t), TrustDomain: testTrustDomain}
	backend := seal.NewMemoryBackend()
	_, err := NewDatabaseCeremonyService("", "orders", backend, deps, testLogger())
	assert.ErrorIs(t, err, ErrEmptyTenantID)
	_, err = NewDatabaseCeremonyService("acme", "", backend, deps, testLogger())
	assert.ErrorIs(t, err, ErrEmptyDatabase)
	_, err = NewDatabaseCeremonyService("acme", "orders", nil, deps, testLogger())
	assert.ErrorIs(t, err, ErrNilBackend)
	_, err = NewDatabaseCeremonyService("acme", "orders", backend, HolderDeps{}, testLogger())
	assert.ErrorIs(t, err, ErrNilCA)
	_, err = NewDatabaseCeremonyService("acme", "orders", backend, deps, nil)
	assert.ErrorIs(t, err, ErrNilLogger)
}

// TestScopeDatabase pins the database scope's registry prefix and its names.
func TestScopeDatabase(t *testing.T) {
	s := ScopeDatabase("acme", "orders")
	assert.Equal(t, "ceremony/tenants/acme/databases/orders/officers/", s.StoragePrefix())
	assert.Equal(t, "database:acme/orders", s.String())
	assert.Equal(t, slog.KindGroup, s.LogValue().Kind())
	assert.NotEqual(t, ScopeTenant("acme").StoragePrefix(), s.StoragePrefix())
}
