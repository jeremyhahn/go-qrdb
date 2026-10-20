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
	// deps carries no CA on a BYO-TLS node, and none in the sealed window
	// before Key A is readable: the holder authority is built anyway,
	// because only enrolment signs. Quorum submission, unseal, claim,
	// unenrolment, rekey and roster changes verify, and serve without one.
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

// InitializeShamir initializes barrier, the tenant or database barrier this
// service is installed on, under Shamir custody
// (seal.StorageBarrier.InitializeShamir), with the initialization marked in
// flight: a registration, an unenrolment or a second initialization is
// refused with ErrInitializationInProgress until it has finished, so the
// holder set the barrier validates is the one it splits the key to. Every
// Shamir initialization of a tenant or database barrier goes through here;
// one made on the barrier directly is not serialized against them.
func (t *ScopedCeremonyService) InitializeShamir(ctx context.Context, barrier *seal.StorageBarrier, policy seal.FactorPolicy) (*seal.ShamirInitParams, error) {
	return t.holder.initializeShamir(ctx, &t.mu, barrier.InitializeShamir, policy)
}

// PrepareShamir is InitializeShamir's first half for the two-request
// initialization (seal.StorageBarrier.PrepareShamir), under the same in-flight
// marking: it splits a fresh unseal key to the holders and returns its check
// value and roster for the owner to sign, leaving the barrier sealed and
// keyless until seal.StorageBarrier.CompleteShamir claims that statement.
func (t *ScopedCeremonyService) PrepareShamir(ctx context.Context, barrier *seal.StorageBarrier, policy seal.FactorPolicy) (*seal.ShamirInitParams, error) {
	return t.holder.initializeShamir(ctx, &t.mu, barrier.PrepareShamir, policy)
}

// InitializeShamirWithRoster validates, registers, and initializes an
// entire M-of-N officer roster atomically -- the tenant/database-barrier
// counterpart of CeremonyService.Initialize's own M-of-N path for the
// system barrier, reusing the SAME pure, side-effect-free batch validator
// (validateOfficerCredentials) so the two inline-roster paths can never
// drift apart (go-qrdb#560 review B3's reasoning, applied here for the
// first time to a tenant or database barrier's own init).
//
// Every officer is validated BEFORE any of them is registered: role
// validity, principal name, self-enrolment, CSR/key/KEM/factors, and a key
// shared by two officers in the roster. What validation cannot catch --
// the real side effects that still run after it (the CA signing fault, a
// persist I/O error, a name or key race against a holder enrolled
// concurrently) -- is covered by registering in order and rolling back
// every officer already registered, in reverse order, the moment a LATER
// one fails; a failure in InitializeShamir itself (the barrier's own split,
// or persisting its result) rolls back the same way, because by then every
// officer in the roster is already committed exactly as a mid-loop failure
// would leave them.
func (t *ScopedCeremonyService) InitializeShamirWithRoster(
	ctx context.Context,
	barrier *seal.StorageBarrier,
	roster []OfficerConfig,
	enroller string,
	policy seal.FactorPolicy,
) (*seal.ShamirInitParams, []*RegisterOfficerResult, error) {
	return t.splitWithRoster(ctx, barrier.InitializeShamir, roster, enroller, policy)
}

// PrepareShamirWithRoster is InitializeShamirWithRoster with PrepareShamir's
// split: the roster is validated, registered and rolled back on a failure
// exactly as there, and the barrier stays sealed and keyless until the
// owner's signed statement completes it.
func (t *ScopedCeremonyService) PrepareShamirWithRoster(
	ctx context.Context,
	barrier *seal.StorageBarrier,
	roster []OfficerConfig,
	enroller string,
	policy seal.FactorPolicy,
) (*seal.ShamirInitParams, []*RegisterOfficerResult, error) {
	return t.splitWithRoster(ctx, barrier.PrepareShamir, roster, enroller, policy)
}

// splitWithRoster registers roster and runs split, rolling every registered
// officer back when either fails.
func (t *ScopedCeremonyService) splitWithRoster(
	ctx context.Context,
	split shamirSplit,
	roster []OfficerConfig,
	enroller string,
	policy seal.FactorPolicy,
) (*seal.ShamirInitParams, []*RegisterOfficerResult, error) {
	if err := validateOfficerCredentials(&t.deps, roster, enroller); err != nil {
		return nil, nil, err
	}

	t.mu.Lock()
	var registered []string
	var results []*RegisterOfficerResult
	for _, o := range roster {
		req := RegisterOfficerRequest{
			Username: o.Username, Role: o.Role, CSRPEM: o.CSRPEM,
			Factors: o.Factors, Designated: o.Designated, Enroller: enroller,
		}
		if o.EncryptionKEM != "" || len(o.EncryptionKey) > 0 {
			req.EncryptionKey = &EncryptionKeyEnrolment{KEM: o.EncryptionKEM, PublicKey: o.EncryptionKey}
		}
		reg, err := t.holder.register(ctx, &t.deps, req)
		if err != nil {
			t.rollbackRosterInit(ctx, registered)
			t.mu.Unlock()
			return nil, nil, err
		}
		registered = append(registered, o.Username)
		results = append(results, reg)
	}
	t.mu.Unlock()

	// initializeShamir manages its own locking (whileInitializing, passed
	// the same t.mu), exactly like the unrostered InitializeShamir above;
	// the lock is released first because the barrier calls back into this
	// service as its distributor.
	params, err := t.holder.initializeShamir(ctx, &t.mu, split, policy)
	if err != nil {
		t.mu.Lock()
		t.rollbackRosterInit(ctx, registered)
		t.mu.Unlock()
		return nil, nil, err
	}
	return params, results, nil
}

// rollbackRosterInit undoes a partially-committed inline roster
// (InitializeShamirWithRoster's own counterpart of
// CeremonyService.rollbackOfficerInit): it removes every officer in
// registered, in reverse enrollment order. Unlike the system barrier's
// rollback, there is no SO/User PIN to un-provision here -- a tenant or
// database barrier is authorized by RBAC alone, never a PIN.
//
// It never masks the caller's own failure: every error here is LOGGED, not
// returned, for the same reason CeremonyService.rollbackOfficerInit's own
// doc comment gives. Must be called with t.mu held; does not unlock.
func (t *ScopedCeremonyService) rollbackRosterInit(ctx context.Context, registered []string) {
	for i := len(registered) - 1; i >= 0; i-- {
		username := registered[i]
		if err := t.holder.removeEnrolled(ctx, username, func() error { return nil }); err != nil {
			t.logger.Error("ceremony: failed to roll back a partially-registered officer after a later officer's enrolment failed",
				slog.String("username", username), slog.Any("error", err))
		}
	}
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
