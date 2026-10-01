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
)

// TenantCeremonyService is the share-holder authority for one tenant
// barrier: the same holder engine as the system ceremony, scoped to the
// tenant's unseal-key domain. There is exactly one per active Shamir tenant
// barrier; TenantService creates it and installs it as the tenant barrier's
// distributor and holder verifier.
type TenantCeremonyService struct {
	mu     sync.Mutex
	deps   HolderDeps
	holder *holderScope
	logger *slog.Logger
}

// Compile-time checks.
var (
	_ seal.CeremonyDistributor = (*TenantCeremonyService)(nil)
	_ seal.HolderVerifier      = (*TenantCeremonyService)(nil)
)

// NewTenantCeremonyService constructs the holder authority for tenantID.
// Holder records live in backend: the tenant barrier itself when it exists
// (encrypted at rest), or the service store during pre-initialization
// enrolment.
func NewTenantCeremonyService(tenantID string, backend seal.StorageBackend, deps HolderDeps, logger *slog.Logger) (*TenantCeremonyService, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
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
	h, err := newHolderScope(ScopeTenant(tenantID), seal.DomainUnseal, tenantID, backend, DefaultShareTTL, sealed, logger)
	if err != nil {
		return nil, err
	}
	return &TenantCeremonyService{deps: deps, holder: h, logger: logger}, nil
}

// RegisterOfficer enrols a share holder of the tenant barrier.
func (t *TenantCeremonyService) RegisterOfficer(ctx context.Context, req RegisterOfficerRequest) (*RegisterOfficerResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.holder.register(ctx, &t.deps, req)
}

// ListOfficers returns every holder with its claim state.
func (t *TenantCeremonyService) ListOfficers(ctx context.Context) ([]*OfficerStatus, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.holder.status(ctx)
}

// BeginClaimShare issues the claim nonce to a holder.
func (t *TenantCeremonyService) BeginClaimShare(ctx context.Context, username, callerThumbprint string) (*ChallengeResponse, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.holder.beginClaim(ctx, username, callerThumbprint)
}

// CompleteClaimShare delivers the holder's share envelope exactly once.
func (t *TenantCeremonyService) CompleteClaimShare(ctx context.Context, username, nonceHex string, signature []byte, callerThumbprint string) (*ShareClaimResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.holder.completeClaim(ctx, username, nonceHex, signature, callerThumbprint)
}

// BeginQuorum issues a factor challenge for a holder's submission to op on
// barrier, the tenant barrier this service is installed on.
func (t *TenantCeremonyService) BeginQuorum(ctx context.Context, barrier *seal.StorageBarrier, op seal.QuorumOp, username, factor string) (*QuorumChallenge, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.holder.beginQuorum(ctx, &t.deps, barrier, op, username, factor)
}

// PrepareRoster implements seal.CeremonyDistributor.
func (t *TenantCeremonyService) PrepareRoster(ctx context.Context, domain seal.QuorumDomain, threshold int, policy seal.FactorPolicy, exclude []string) (*seal.HolderRoster, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.holder.prepareRoster(ctx, domain, threshold, policy, exclude)
}

// Distribute implements seal.CeremonyDistributor.
func (t *TenantCeremonyService) Distribute(ctx context.Context, secret []byte, roster *seal.HolderRoster, digest []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.holder.distribute(ctx, secret, roster, digest)
}

// Committed implements seal.CeremonyDistributor.
func (t *TenantCeremonyService) Committed(ctx context.Context, roster *seal.HolderRoster, removed []string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.holder.committed(ctx, roster, removed)
}

// VerifyHolder implements seal.HolderVerifier.
func (t *TenantCeremonyService) VerifyHolder(ctx context.Context, domain seal.QuorumDomain, op seal.QuorumOp, holder string, roster *seal.HolderRoster, digest []byte, proof seal.HolderProof) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if domain != seal.DomainUnseal {
		return &seal.QuorumOpInvalidError{Domain: domain, Op: op}
	}
	return t.holder.verifyHolder(ctx, &t.deps, domain, op, holder, roster, digest, proof)
}

// Cleanup destroys every held envelope and pending challenge.
func (t *TenantCeremonyService) Cleanup() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.holder.cleanup()
}

// TenantCeremonyRegistry manages the set of active TenantCeremonyService
// instances, one per tenant barrier operating under the Shamir strategy.
type TenantCeremonyRegistry struct {
	mu       sync.RWMutex
	services map[string]*TenantCeremonyService
}

// NewTenantCeremonyRegistry creates a new TenantCeremonyRegistry.
func NewTenantCeremonyRegistry() *TenantCeremonyRegistry {
	return &TenantCeremonyRegistry{
		services: make(map[string]*TenantCeremonyService),
	}
}

// Get retrieves the TenantCeremonyService for the given tenant ID, if present.
func (r *TenantCeremonyRegistry) Get(tenantID string) (*TenantCeremonyService, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	svc, ok := r.services[tenantID]
	return svc, ok
}

// Set registers a TenantCeremonyService for the given tenant ID, replacing
// any existing entry.
func (r *TenantCeremonyRegistry) Set(tenantID string, svc *TenantCeremonyService) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.services[tenantID] = svc
}

// Delete removes the TenantCeremonyService for the given tenant ID. If a
// service is found, its Cleanup method is called before removal to destroy
// any remaining share enclaves.
func (r *TenantCeremonyRegistry) Delete(tenantID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if svc, ok := r.services[tenantID]; ok {
		svc.Cleanup()
		delete(r.services, tenantID)
	}
}
