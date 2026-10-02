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

package ceremony

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// holderAuthority is the holder surface the sealed-registration regression
// drives, identical for the system, tenant and database scopes.
type holderAuthority interface {
	register(ctx context.Context, req RegisterOfficerRequest) (*RegisterOfficerResult, error)
	list(ctx context.Context) ([]*OfficerStatus, error)
	beginClaim(ctx context.Context, username, thumb string) (*ChallengeResponse, error)
	completeClaim(ctx context.Context, username, nonceHex string, sig []byte, thumb string) (*ShareClaimResult, error)
	beginQuorum(ctx context.Context, op seal.QuorumOp, username string) (*QuorumChallenge, error)
	claimLabel() string
}

// systemAuthority is the system ceremony's unseal domain.
type systemAuthority struct{ svc *CeremonyService }

func (a systemAuthority) register(ctx context.Context, req RegisterOfficerRequest) (*RegisterOfficerResult, error) {
	return a.svc.RegisterOfficer(ctx, seal.DomainUnseal, req)
}

func (a systemAuthority) list(ctx context.Context) ([]*OfficerStatus, error) {
	return a.svc.ListOfficers(ctx, seal.DomainUnseal)
}

func (a systemAuthority) beginClaim(ctx context.Context, username, thumb string) (*ChallengeResponse, error) {
	return a.svc.BeginClaimShare(ctx, seal.DomainUnseal, username, thumb)
}

func (a systemAuthority) completeClaim(ctx context.Context, username, nonceHex string, sig []byte, thumb string) (*ShareClaimResult, error) {
	return a.svc.CompleteClaimShare(ctx, seal.DomainUnseal, username, nonceHex, sig, thumb)
}

func (a systemAuthority) beginQuorum(ctx context.Context, op seal.QuorumOp, username string) (*QuorumChallenge, error) {
	return a.svc.BeginQuorum(ctx, seal.DomainUnseal, op, username, "")
}

func (a systemAuthority) claimLabel() string { return ScopeSystem.ClaimLabel() }

// scopedAuthority is a tenant or database holder authority over its barrier.
type scopedAuthority struct {
	svc     *ScopedCeremonyService
	barrier *seal.StorageBarrier
}

func (a scopedAuthority) register(ctx context.Context, req RegisterOfficerRequest) (*RegisterOfficerResult, error) {
	return a.svc.RegisterOfficer(ctx, req)
}

func (a scopedAuthority) list(ctx context.Context) ([]*OfficerStatus, error) {
	return a.svc.ListOfficers(ctx)
}

func (a scopedAuthority) beginClaim(ctx context.Context, username, thumb string) (*ChallengeResponse, error) {
	return a.svc.BeginClaimShare(ctx, username, thumb)
}

func (a scopedAuthority) completeClaim(ctx context.Context, username, nonceHex string, sig []byte, thumb string) (*ShareClaimResult, error) {
	return a.svc.CompleteClaimShare(ctx, username, nonceHex, sig, thumb)
}

func (a scopedAuthority) beginQuorum(ctx context.Context, op seal.QuorumOp, username string) (*QuorumChallenge, error) {
	return a.svc.BeginQuorum(ctx, a.barrier, op, username, "")
}

func (a scopedAuthority) claimLabel() string { return a.svc.holder.scope.ClaimLabel() }

// sealedRegisterScopes builds each scope's authority over barrier, installed
// as its distributor and verifier and storing its records behind it, as the
// node, the tenant service and the database barrier manager do.
func sealedRegisterScopes(t *testing.T) map[string]func(t *testing.T, barrier *seal.StorageBarrier) holderAuthority {
	t.Helper()
	ca := testCA(t)
	factors := testFactors(t)
	deps := HolderDeps{CA: ca, Factors: factors}
	scoped := func(build func(*seal.StorageBarrier) (*ScopedCeremonyService, error)) func(t *testing.T, barrier *seal.StorageBarrier) holderAuthority {
		return func(t *testing.T, barrier *seal.StorageBarrier) holderAuthority {
			svc, err := build(barrier)
			require.NoError(t, err)
			barrier.SetCeremonyDistributor(svc)
			barrier.SetHolderVerifier(svc)
			return scopedAuthority{svc: svc, barrier: barrier}
		}
	}
	return map[string]func(t *testing.T, barrier *seal.StorageBarrier) holderAuthority{
		"system": func(t *testing.T, barrier *seal.StorageBarrier) holderAuthority {
			svc, err := NewCeremonyService(&CeremonyConfig{DataDir: t.TempDir()}, barrier, testLogger())
			require.NoError(t, err)
			svc.SetCA(ca)
			svc.SetFactorRegistry(factors)
			return systemAuthority{svc: svc}
		},
		"tenant": scoped(func(b *seal.StorageBarrier) (*ScopedCeremonyService, error) {
			return NewTenantCeremonyService("acme", b, deps, testLogger())
		}),
		"database": scoped(func(b *seal.StorageBarrier) (*ScopedCeremonyService, error) {
			return NewDatabaseCeremonyService("acme", "orders", b, deps, testLogger())
		}),
	}
}

// TestRegister_SealedInitializedBarrierRefusesEnrolment is the regression for
// a registration made while the barrier is sealed AND initialized: the record
// could only be cached, and the unseal that followed wrote the cache over the
// persisted holder of the same name. An attacker who registered "alice" with
// their own key while the node was sealed became alice the moment alice and
// bob unsealed it. Cache-only enrolment is legitimate only before
// initialization; on an initialized barrier the persisted roster is
// authoritative and a sealed scope cannot see it.
func TestRegister_SealedInitializedBarrierRefusesEnrolment(t *testing.T) {
	for name, build := range sealedRegisterScopes(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			backend := seal.NewMemoryBackend()
			barrier := shamirBarrier(t, backend, 2, 2)
			auth := build(t, barrier)

			alice, bob := newHolder(t, "alice", ""), newHolder(t, "bob", seal.RoleCustodian)
			for _, h := range []*testHolder{alice, bob} {
				res, err := auth.register(ctx, h.request("admin"))
				require.NoError(t, err, h.username)
				h.recordCert(t, res.CertPEM)
			}
			_, err := barrier.InitializeShamir(ctx, seal.FactorPolicyDesignated)
			require.NoError(t, err)
			for _, h := range []*testHolder{alice, bob} {
				ch, err := auth.beginClaim(ctx, h.username, h.thumb)
				require.NoError(t, err)
				sig, err := SignShareClaim(h.certKey, auth.claimLabel(), h.username, ch.Nonce)
				require.NoError(t, err)
				res, err := auth.completeClaim(ctx, h.username, hex.EncodeToString(ch.Nonce), sig, h.thumb)
				require.NoError(t, err)
				h.envelope = res.Envelope
			}

			// A restart: the barrier is sealed and the authority's cache empty.
			barrier = shamirBarrier(t, backend, 2, 2)
			require.True(t, barrier.IsSealed())
			auth = build(t, barrier)

			attacker := newHolder(t, "alice", "")
			_, err = auth.register(ctx, attacker.request("admin"))
			require.ErrorIs(t, err, ErrRegistrationWhileSealed, "re-registering a persisted holder while sealed")
			_, err = auth.register(ctx, newHolder(t, "carol", seal.RoleCustodian).request("admin"))
			require.ErrorIs(t, err, ErrRegistrationWhileSealed, "a new holder while sealed")

			for _, h := range []*testHolder{alice, bob} {
				ch, err := auth.beginQuorum(ctx, seal.OpUnseal, h.username)
				require.NoError(t, err)
				_, err = barrier.SubmitHolderShare(ctx, &seal.HolderSubmission{
					Domain: seal.DomainUnseal, Op: seal.OpUnseal, Holder: h.username,
					Proof: seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: h.answer(t, ch)},
					Share: h.openShare(t),
				})
				require.NoError(t, err, h.username)
			}
			require.False(t, barrier.IsSealed())

			statuses, err := auth.list(ctx)
			require.NoError(t, err)
			require.Len(t, statuses, 2, "no holder was added while sealed")
			for _, s := range statuses {
				if s.Username == "alice" {
					require.Equal(t, alice.thumb, s.CertThumbprint, "alice's enrolled certificate survives the unseal")
				}
			}
		})
	}
}

// failingGetBackend is a storage backend whose reads fail with a fault that
// is not "not found", as a corrupt or unavailable store does.
type failingGetBackend struct {
	seal.StorageBackend
	err error
}

func (b failingGetBackend) Get(context.Context, string) ([]byte, error) { return nil, b.err }

// TestRegister_LoadFaultIsNotAFreeName: a registry read that fails for any
// reason other than "not found" says nothing about whether the name is free,
// so registration fails rather than enrolling over a record it could not
// read.
func TestRegister_LoadFaultIsNotAFreeName(t *testing.T) {
	fault := errors.New("storage unavailable")
	svc, err := NewTenantCeremonyService("acme", failingGetBackend{StorageBackend: seal.NewMemoryBackend(), err: fault},
		HolderDeps{CA: testCA(t), Factors: testFactors(t)}, testLogger())
	require.NoError(t, err)
	_, err = svc.RegisterOfficer(context.Background(), newHolder(t, "alice", "").request("admin"))
	require.ErrorIs(t, err, fault)
}

// TestSync_ConflictEvictsAndAuditsWithoutWedging: the reconciliation that
// runs on the first unsealed access writes only cached holders storage does
// not have. A cached record that names a persisted holder with a different
// identity is a conflict: the persisted record is left as it was, the cached
// one is evicted and the conflict audited, and the reconciliation carries on
// -- the other cached holders are written and the scope is usable. A
// conflict must not wedge the scope, or one sealed-window registration could
// lock every holder operation out until restart.
func TestSync_ConflictEvictsAndAuditsWithoutWedging(t *testing.T) {
	ctx := context.Background()
	backend := seal.NewMemoryBackend()
	h, err := newHolderScope(ScopeTenant("acme"), seal.DomainUnseal, "acme", backend, DefaultShareTTL, func() bool { return false }, testLogger())
	require.NoError(t, err)
	var audited []map[string]string
	h.audit = func(action, outcome string, details map[string]string) {
		require.Equal(t, "holder-record-conflict", action)
		require.Equal(t, "deny", outcome)
		audited = append(audited, details)
	}

	persisted := &PersistentOfficer{Username: "alice", CertThumbprint: "aa", PubKeyPEM: "alice-key"}
	require.NoError(t, h.registry.Save(ctx, persisted))
	h.registry.cacheOnly(&PersistentOfficer{Username: "alice", CertThumbprint: "ff", PubKeyPEM: "attacker-key"})
	h.registry.cacheOnly(&PersistentOfficer{Username: "carol", CertThumbprint: "cc", PubKeyPEM: "carol-key"})

	require.NoError(t, h.sync(ctx), "a conflict must not wedge the scope")
	require.Len(t, audited, 1)
	require.Equal(t, "alice", audited[0]["username"])
	require.Equal(t, h.scope.String(), audited[0]["scope"])

	cached, ok := h.registry.cached("alice")
	require.True(t, ok)
	require.Equal(t, "aa", cached.CertThumbprint, "the conflicting cached record was evicted for the persisted one")
	fresh, err := NewScopedOfficerRegistry(backend, ScopeTenant("acme"))
	require.NoError(t, err)
	got, err := fresh.Load(ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, "aa", got.CertThumbprint, "the persisted holder is untouched")
	carol, err := fresh.Load(ctx, "carol")
	require.NoError(t, err, "the other cached holders were still written")
	require.Equal(t, "cc", carol.CertThumbprint)

	_, err = h.status(ctx)
	require.NoError(t, err, "the scope serves holder operations after the conflict")
}

// TestSync_PersistsHoldersEnrolledBeforeInitialization: the legitimate
// cache-only enrolment, before the barrier exists, is written on the first
// unsealed access, and a cached copy identical to its persisted record is not
// a conflict.
func TestSync_PersistsHoldersEnrolledBeforeInitialization(t *testing.T) {
	ctx := context.Background()
	backend := seal.NewMemoryBackend()
	h, err := newHolderScope(ScopeTenant("acme"), seal.DomainUnseal, "acme", backend, DefaultShareTTL, func() bool { return false }, testLogger())
	require.NoError(t, err)
	bob := &PersistentOfficer{Username: "bob", CertThumbprint: "bb", PubKeyPEM: "bob-key"}
	require.NoError(t, h.registry.Save(ctx, bob))
	h.registry.cacheOnly(&PersistentOfficer{Username: "carol", CertThumbprint: "cc", PubKeyPEM: "carol-key"})

	require.NoError(t, h.sync(ctx))
	fresh, err := NewScopedOfficerRegistry(backend, ScopeTenant("acme"))
	require.NoError(t, err)
	got, err := fresh.Load(ctx, "carol")
	require.NoError(t, err)
	require.Equal(t, "cc", got.CertThumbprint)
}
