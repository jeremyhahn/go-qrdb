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
	"log/slog"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// ScopedCeremonyService is the share-holder authority for one tenant or
// database barrier: the same holder engine as the system ceremony, scoped to
// that barrier's unseal-key domain. There is exactly one per active Shamir
// tenant or database barrier; the tenant or database service creates it and
// installs it as the barrier's distributor and holder verifier.
type ScopedCeremonyService struct {
	mu     sync.Mutex
	deps   HolderDeps
	holder *holderScope
	logger *slog.Logger
}

// Compile-time checks.
var (
	_ seal.CeremonyDistributor = (*ScopedCeremonyService)(nil)
	_ seal.HolderVerifier      = (*ScopedCeremonyService)(nil)
)

// NewTenantCeremonyService constructs the holder authority for tenantID.
// Holder records live in backend: the tenant barrier itself when it exists
// (encrypted at rest), or the service store during pre-initialization
// enrolment.
func NewTenantCeremonyService(tenantID string, backend seal.StorageBackend, deps HolderDeps, logger *slog.Logger) (*ScopedCeremonyService, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	return newScopedCeremonyService(ScopeTenant(tenantID), tenantID, backend, deps, logger)
}

// NewDatabaseCeremonyService constructs the holder authority for the
// database barrier of database in tenantID. Its holders are issued
// certificates in the database's tenant (OU=[tenant, profile]); their records live in
// backend, the database barrier's own storage.
func NewDatabaseCeremonyService(tenantID, database string, backend seal.StorageBackend, deps HolderDeps, logger *slog.Logger) (*ScopedCeremonyService, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if database == "" {
		return nil, ErrEmptyDatabase
	}
	return newScopedCeremonyService(ScopeDatabase(tenantID, database), tenantID, backend, deps, logger)
}

// newScopedCeremonyService builds the holder authority for scope, whose
// holders' identities belong to tenantID.
func newScopedCeremonyService(scope BarrierScope, tenantID string, backend seal.StorageBackend, deps HolderDeps, logger *slog.Logger) (*ScopedCeremonyService, error) {
	if backend == nil {
		return nil, ErrNilBackend
	}
	if deps.CA == nil {
		return nil, ErrNilCA
	}
	if logger == nil {
		return nil, ErrNilLogger
	}
	sealed := func() bool { return false }
	if b, ok := backend.(interface{ IsSealed() bool }); ok {
		sealed = b.IsSealed
	}
	h, err := newHolderScope(scope, seal.DomainUnseal, tenantID, backend, DefaultShareTTL, sealed, logger)
	if err != nil {
		return nil, err
	}
	return &ScopedCeremonyService{deps: deps, holder: h, logger: logger}, nil
}

// RegisterOfficer enrols a share holder of the tenant barrier.
func (t *ScopedCeremonyService) RegisterOfficer(ctx context.Context, req RegisterOfficerRequest) (*RegisterOfficerResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.holder.register(ctx, &t.deps, req)
}

// UnenrolOfficer removes an enrolled holder before the barrier is
// initialized. After initialization a holder is removed by revocation.
func (t *ScopedCeremonyService) UnenrolOfficer(ctx context.Context, username string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.holder.unenrol(ctx, &t.deps, username)
}

// ListOfficers returns every holder with its claim state.
func (t *ScopedCeremonyService) ListOfficers(ctx context.Context) ([]*OfficerStatus, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.holder.status(ctx)
}

// BeginClaimShare issues the claim nonce to a holder.
func (t *ScopedCeremonyService) BeginClaimShare(ctx context.Context, username, callerThumbprint string) (*ChallengeResponse, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.holder.beginClaim(ctx, username, callerThumbprint)
}

// CompleteClaimShare delivers the holder's share envelope exactly once.
func (t *ScopedCeremonyService) CompleteClaimShare(ctx context.Context, username, nonceHex string, signature []byte, callerThumbprint string) (*ShareClaimResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.holder.completeClaim(ctx, &t.deps, username, nonceHex, signature, callerThumbprint)
}

// BeginQuorum issues a factor challenge for a holder's submission to op on
// barrier, the tenant barrier this service is installed on.
func (t *ScopedCeremonyService) BeginQuorum(ctx context.Context, barrier *seal.StorageBarrier, op seal.QuorumOp, username, factor string) (*QuorumChallenge, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.holder.beginQuorum(ctx, &t.deps, barrier, op, username, factor)
}

// StartRekey opens a rekey round on barrier, the tenant or database barrier
// this service is installed on (seal.StorageBarrier.StartRekey), and records
// its opening, or its refusal, on the compliance trail. Every rekey of the
// scope's holder set, a revocation included, is opened here.
func (t *ScopedCeremonyService) StartRekey(ctx context.Context, barrier *seal.StorageBarrier, params seal.RekeyParams) (*seal.QuorumRoundInfo, error) {
	// The barrier calls back into PrepareRoster, which takes t.mu, so the
	// lock is held only to read the deps.
	t.mu.Lock()
	deps := t.deps
	t.mu.Unlock()
	return t.holder.startRekey(ctx, &deps, params, func(p seal.RekeyParams) (*seal.QuorumRoundInfo, error) {
		return barrier.StartRekey(ctx, seal.DomainUnseal, p)
	})
}

// PrepareRoster implements seal.CeremonyDistributor.
func (t *ScopedCeremonyService) PrepareRoster(ctx context.Context, domain seal.QuorumDomain, threshold int, policy seal.FactorPolicy, exclude []string) (*seal.HolderRoster, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.holder.prepareRoster(ctx, domain, threshold, policy, exclude)
}

// Distribute implements seal.CeremonyDistributor.
func (t *ScopedCeremonyService) Distribute(ctx context.Context, secret *secure.Buffer, roster *seal.HolderRoster, digest []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.holder.distribute(ctx, secret, roster, digest)
}

// Committed implements seal.CeremonyDistributor.
func (t *ScopedCeremonyService) Committed(ctx context.Context, roster *seal.HolderRoster, removed []string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.holder.committed(ctx, &t.deps, roster, removed)
}

// AdmitSubmission implements seal.HolderVerifier: a share submission passes
// the caller's quorum throttle, in the submission bucket.
func (t *ScopedCeremonyService) AdmitSubmission(ctx context.Context, domain seal.QuorumDomain) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if domain != seal.DomainUnseal {
		return &seal.QuorumOpInvalidError{Domain: domain}
	}
	_, err := t.holder.throttle(ctx, &t.deps, quorumKindSubmit)
	return err
}

// VerifyHolder implements seal.HolderVerifier.
func (t *ScopedCeremonyService) VerifyHolder(ctx context.Context, domain seal.QuorumDomain, op seal.QuorumOp, holder string, roster *seal.HolderRoster, digest []byte, proof seal.HolderProof) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if domain != seal.DomainUnseal {
		return &seal.QuorumOpInvalidError{Domain: domain, Op: op}
	}
	return t.holder.verifyHolder(ctx, &t.deps, domain, op, holder, roster, digest, proof)
}

// Cleanup destroys every held envelope and pending challenge.
func (t *ScopedCeremonyService) Cleanup() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.holder.cleanup()
}

// ScopedCeremonyRegistry manages the set of active ScopedCeremonyService
// instances, one per tenant or database barrier operating under the Shamir
// strategy, keyed by a caller-chosen scope key.
type ScopedCeremonyRegistry struct {
	mu       sync.RWMutex
	services map[string]*ScopedCeremonyService
}

// NewScopedCeremonyRegistry creates a new ScopedCeremonyRegistry.
func NewScopedCeremonyRegistry() *ScopedCeremonyRegistry {
	return &ScopedCeremonyRegistry{
		services: make(map[string]*ScopedCeremonyService),
	}
}

// Get retrieves the ScopedCeremonyService for the given tenant ID, if present.
func (r *ScopedCeremonyRegistry) Get(tenantID string) (*ScopedCeremonyService, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	svc, ok := r.services[tenantID]
	return svc, ok
}

// Set registers a ScopedCeremonyService for the given tenant ID, replacing
// any existing entry.
func (r *ScopedCeremonyRegistry) Set(tenantID string, svc *ScopedCeremonyService) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.services[tenantID] = svc
}

// Delete removes the ScopedCeremonyService for the given tenant ID. If a
// service is found, its Cleanup method is called before removal to destroy
// any remaining share enclaves.
func (r *ScopedCeremonyRegistry) Delete(tenantID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if svc, ok := r.services[tenantID]; ok {
		svc.Cleanup()
		delete(r.services, tenantID)
	}
}
