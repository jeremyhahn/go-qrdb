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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// TenantCeremonyService orchestrates per-tenant Shamir share distribution.
// It provides the same claim semantics as CeremonyService but scoped to a
// single tenant barrier. There is exactly one TenantCeremonyService per
// active tenant barrier operating under the Shamir strategy; it is created
// by TenantService when the barrier is initialized and destroyed when the
// barrier is decommissioned.
type TenantCeremonyService struct {
	mu         sync.Mutex
	scope      BarrierScope
	shareStore *sealedShareStore
	registry   *OfficerRegistry
	ca         CeremonyCA
	challenges map[string]*ChallengeResponse
	logger     *slog.Logger
}

// Compile-time check: TenantCeremonyService satisfies seal.CeremonyDistributor.
var _ seal.CeremonyDistributor = (*TenantCeremonyService)(nil)

// NewTenantCeremonyService constructs a TenantCeremonyService for the given
// tenant. The registry is backed by the tenant's own barrier storage backend.
//
// Returns ErrEmptyTenantID if tenantID is empty, ErrNilBackend if backend is
// nil, or ErrNilCA if ca is nil.
func NewTenantCeremonyService(
	tenantID string,
	backend seal.StorageBackend,
	ca CeremonyCA,
	logger *slog.Logger,
) (*TenantCeremonyService, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if backend == nil {
		return nil, ErrNilBackend
	}
	if ca == nil {
		return nil, ErrNilCA
	}
	if logger == nil {
		return nil, ErrNilLogger
	}

	scope := ScopeTenant(tenantID)
	reg, err := NewScopedOfficerRegistry(backend, scope)
	if err != nil {
		return nil, err
	}

	return &TenantCeremonyService{
		scope:      scope,
		shareStore: newSealedShareStore(),
		registry:   reg,
		ca:         ca,
		challenges: make(map[string]*ChallengeResponse),
		logger:     logger,
	}, nil
}

// RegisterOfficer registers a security officer for this tenant's barrier.
// Validates the CSR, signs it with the CA, persists public metadata in the
// officer registry, and returns the signed certificate PEM. Does NOT split
// or store any shares; that happens at SealAndDistribute time.
func (t *TenantCeremonyService) RegisterOfficer(
	ctx context.Context,
	req RegisterOfficerRequest,
) (*RegisterOfficerResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if req.Username == "" {
		return nil, ErrMissingUsername
	}
	if len(req.CSRPEM) == 0 {
		return nil, ErrMissingCSRPEM
	}
	if err := req.AuthenticatorType.Validate(); err != nil {
		return nil, err
	}

	// Uniqueness check.
	if _, err := t.registry.Load(ctx, req.Username); err == nil {
		return nil, ErrDuplicateUsername
	}

	cert, csr, certPEM, err := signOfficerCSR(ctx, t.ca, req.CSRPEM, t.scope.TenantID(), req.Username)
	if err != nil {
		return nil, err
	}

	shareIndex := len(t.registry.Cached()) + 1

	thumb := certThumbprint(cert.Raw)
	pubPEM, marshalErr := marshalPublicKeyPEM(csr.PublicKey)
	if marshalErr != nil {
		return nil, &CertEncodeError{Err: marshalErr}
	}

	persisted := &PersistentOfficer{
		Username:       req.Username,
		CertThumbprint: thumb,
		PubKeyPEM:      pubPEM,
		ShareIndex:     shareIndex,
		AuthFactor:     authFactorFromAuthenticator(req.AuthenticatorType.Normalized()),
		EnrolledAt:     time.Now().UTC(),
	}

	if err := t.registry.Save(ctx, persisted); err != nil {
		// Best-effort: cache even when persistence fails (sealed barrier).
		t.registry.mu.Lock()
		t.registry.cache[persisted.Username] = persisted
		t.registry.mu.Unlock()
	}

	t.logger.Info("tenant officer registered",
		slog.String("tenant_id", t.scope.tenantID),
		slog.String("username", req.Username),
		slog.Int("share_index", shareIndex))

	return &RegisterOfficerResult{
		CertPEM:    certPEM,
		ShareIndex: shareIndex,
	}, nil
}

// ListOfficers returns all enrolled officers for this tenant's barrier.
func (t *TenantCeremonyService) ListOfficers(ctx context.Context) ([]*OfficerStatus, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return officerStatusFromRegistry(t.registry, t.shareStore), nil
}

// SealAndDistribute splits shareSecret into shares for the N enrolled officers
// and places each share into the per-officer enclave in the sealedShareStore.
// shareSecret is wiped before this function returns regardless of outcome.
// Returns the number of shares sealed.
// Returns ErrInsufficientOfficers if fewer than threshold officers are enrolled.
func (t *TenantCeremonyService) SealAndDistribute(
	ctx context.Context,
	shareSecret []byte,
	threshold, total int,
) (int, error) {
	defer secure.Wipe(shareSecret)

	t.mu.Lock()
	defer t.mu.Unlock()

	return sealSharesToOfficers(ctx, shareSecret, threshold, total, t.registry, t.shareStore)
}

// BeginClaimShare issues a challenge nonce to the named officer. The officer
// must present a valid mTLS certificate whose thumbprint matches the registry.
func (t *TenantCeremonyService) BeginClaimShare(
	ctx context.Context,
	username string,
	callerCertThumbprint string,
) (*ChallengeResponse, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return beginClaimShareLocked(ctx, username, callerCertThumbprint,
		t.registry, t.shareStore, t.challenges, "share:")
}

// CompleteClaimShare verifies the officer's nonce signature and returns the
// share bytes exactly once, then destroys the enclave. Returns
// ErrShareAlreadyClaimed on a second call. The returned bytes are the
// caller's responsibility to wipe after use.
func (t *TenantCeremonyService) CompleteClaimShare(
	ctx context.Context,
	username string,
	nonceHex string,
	signature []byte,
	callerCertThumbprint string,
) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	result, err := completeClaimShareLocked(ctx, username, nonceHex, signature, callerCertThumbprint,
		t.registry, t.shareStore, nil, t.challenges, "share:")
	if err != nil {
		return nil, err
	}

	if markErr := t.registry.MarkClaimed(ctx, username, time.Now().UTC()); markErr != nil {
		t.logger.Warn("failed to mark tenant share claimed",
			slog.String("username", username),
			slog.String("error", markErr.Error()))
	}

	return result.ShareBytes, nil
}

// OfficerClaimStatus returns a per-officer claim status snapshot (for admin
// visibility). Never includes share bytes.
func (t *TenantCeremonyService) OfficerClaimStatus(
	ctx context.Context,
) ([]*OfficerStatus, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return officerStatusFromRegistry(t.registry, t.shareStore), nil
}

// RekeyAndDistribute generates new shares from newShareSecret and seals them
// to the registered officers. Old unclaimed shares, if any, are destroyed
// before new ones are stored. newShareSecret is wiped on return.
func (t *TenantCeremonyService) RekeyAndDistribute(
	ctx context.Context,
	newShareSecret []byte,
	newThreshold, newTotal int,
) (int, error) {
	defer secure.Wipe(newShareSecret)

	t.mu.Lock()
	defer t.mu.Unlock()

	// INV-10: destroy prior round's enclaves and clear claim history so
	// officers who claimed in the prior round can claim their new shares.
	t.shareStore.Reset()
	if err := t.registry.ResetClaimedAt(ctx); err != nil {
		t.logger.Warn("failed to reset tenant claimed-at timestamps during rekey",
			slog.String("error", err.Error()))
	}

	return sealSharesToOfficers(ctx, newShareSecret, newThreshold, newTotal, t.registry, t.shareStore)
}

// Cleanup destroys all remaining share enclaves and clears challenge state.
func (t *TenantCeremonyService) Cleanup() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.shareStore.DestroyAll()

	for key, challenge := range t.challenges {
		secure.Wipe(challenge.Nonce)
		delete(t.challenges, key)
	}
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
