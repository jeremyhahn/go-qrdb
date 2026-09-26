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

package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// metadataPrefix is the key prefix for all tenant metadata in the KV store.
// The "__sys:" prefix ensures these keys are excluded from user-facing KV list
// and scan operations by the store's internal-key filter (pkg/store/constants.go).
// Legacy deployments with keys stored under the old "tenants/" prefix would
// need a one-time migration; no in-place migration is performed here.
const metadataPrefix = "__sys:tenants/"

// IsolationType controls the data isolation strategy for a tenant.
// Both modes provide separate keyspaces; the difference is whether
// an additional cryptographic barrier is layered on top.
type IsolationType string

const (
	// IsolationBarrier routes a tenant's IAM data through a per-tenant
	// cryptographic barrier: its own DEK and a "{tenantID}/" storage
	// namespace in an independent backend. Barrier isolation is strictly
	// opt-in; the system tenant may not use it (it must stay reachable
	// during bootstrap while tenant barriers are sealed).
	IsolationBarrier IsolationType = "barrier"

	// IsolationPrefix isolates tenant data using only logical key
	// prefixing against the shared system store. No per-tenant barrier is
	// applied. This is the default when no isolation type is specified.
	IsolationPrefix IsolationType = "prefix"

	// IsolationVolume gives a tenant a dedicated filesystem DataRoot
	// (allocated from the configured volume pool) hosting a per-tenant local
	// Raft cluster with FULL engines (KV/MVCC/lease/watch/timeseries). The
	// tenant's data lives entirely under its DataRoot and is encrypted at
	// rest under the tenant's own DEK via a per-tenant barrier (registered
	// like IsolationBarrier). Requires the volume pool and the seal
	// subsystem to be configured. This is the Phase-2 dedicated-volume mode.
	IsolationVolume IsolationType = "volume"

	// IsolationRaft gives a tenant a dedicated Raft GROUP per database. Like
	// IsolationVolume the tenant gets a dedicated filesystem DataRoot (from the
	// volume pool) and a per-tenant barrier, but each database runs as its own
	// Raft shard on the SHARED system Host: the shard's state-machine data is
	// rooted under {DataRoot}/databases/{db} (encrypted under the tenant DEK)
	// while the Raft log stays in the shared host LogDB. Requires the volume
	// pool and the seal subsystem. This is the Phase-4 dedicated-raft mode.
	// MVP: each group is a single local replica; multi-node auto-placement is a
	// Phase-4c follow-on.
	IsolationRaft IsolationType = "raft"
)

// TenantInfo holds tenant metadata persisted in the storage backend.
type TenantInfo struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	IsolationType IsolationType       `json:"isolation_type,omitempty"`
	Quotas        *quota.Quotas       `json:"quotas,omitempty"`
	Entitlements  *quota.Entitlements `json:"entitlements,omitempty"`
	// DataRoot is the dedicated filesystem root allocated from the volume
	// pool for IsolationVolume tenants. It is the parent of the tenant's
	// "barrier" (IAM Pebble) and "databases" (per-tenant Raft host state)
	// subdirectories. Empty for prefix/barrier tenants.
	DataRoot string `json:"data_root,omitempty"`
	// BarrierAutoUnseal selects the dedicated-isolation tenant's barrier
	// key-management mode. False (the secure default) means self-managed: the
	// tenant admin initializes and unseals their own barrier, which comes up
	// sealed after a restart. True means system-managed auto-unseal: the tenant
	// root key is wrapped under the system barrier so the barrier is
	// auto-initialized at create and re-attached credential-free at boot. Only
	// meaningful for IsolationBarrier / IsolationVolume tenants.
	BarrierAutoUnseal bool      `json:"barrier_auto_unseal,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// CreateTenantOption configures optional CreateTenant behavior. Options are
// applied left to right; later options override earlier ones.
type CreateTenantOption func(*createTenantConfig)

// createTenantConfig accumulates the resolved CreateTenant options.
type createTenantConfig struct {
	// barrierAutoUnseal requests the system-managed auto-unseal key-management
	// mode for a dedicated-isolation tenant. Default false (self-managed).
	barrierAutoUnseal bool

	// dataRoot is the caller-requested dedicated-disk DataRoot (WithDataRoot).
	// nil means "no preference" (the default: allocate from the configured
	// volume pool). A non-nil pointer to an empty string is a caller error
	// (ErrEmptyDataRoot) rather than being silently treated as unset.
	dataRoot *string
}

// WithBarrierAutoUnseal selects the tenant's barrier key-management mode. Pass
// true to opt into system-managed auto-unseal (entitlement-gated); the default
// (false) is the secure self-managed mode where the tenant admin holds the
// barrier key. Only meaningful for IsolationBarrier / IsolationVolume tenants.
func WithBarrierAutoUnseal(autoUnseal bool) CreateTenantOption {
	return func(c *createTenantConfig) { c.barrierAutoUnseal = autoUnseal }
}

// WithDataRoot pins an IsolationVolume or IsolationRaft tenant's dedicated
// filesystem DataRoot to a caller-supplied path instead of letting
// RegisterVolumeTenant allocate one from the configured volume pool. This
// lets an operator place a tenant's data on a dedicated disk or mount for
// performance or capacity isolation.
//
// path must be a non-empty, absolute, already-canonical path naming an empty
// or not-yet-existing directory (see validateExplicitDataRoot); CreateTenant
// rejects it otherwise. It is only meaningful for IsolationVolume and
// IsolationRaft tenants -- CreateTenant rejects it with
// DataRootIsolationTypeError for prefix/barrier isolation, where a tenant has
// no dedicated filesystem location.
func WithDataRoot(path string) CreateTenantOption {
	return func(c *createTenantConfig) { c.dataRoot = &path }
}

// resolveCreateTenantOptions folds the variadic options into a config.
func resolveCreateTenantOptions(opts []CreateTenantOption) createTenantConfig {
	var cfg createTenantConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

// EffectiveIsolationType returns the tenant's isolation type, defaulting
// to IsolationPrefix for tenants created before this field existed (and
// for tenants created with an unspecified isolation type). Barrier
// isolation is strictly opt-in: a legacy/empty isolation type routes IAM
// data to the shared system store, matching the StoreResolver's
// fail-open-to-shared migration behaviour for prefix tenants.
func (t *TenantInfo) EffectiveIsolationType() IsolationType {
	if t.IsolationType == "" {
		return IsolationPrefix
	}
	return t.IsolationType
}

// TenantPatch carries the mutable fields that may be changed via UpdateTenant.
// Zero-value fields are ignored; only non-zero fields are applied.
type TenantPatch struct {
	// Name, when non-empty, replaces the tenant's display name.
	Name string

	// IsolationType, when non-empty, changes the isolation strategy.
	// The caller is responsible for ensuring the tenant has no data
	// before changing the isolation type (the service enforces this).
	IsolationType IsolationType
}

// TenantServicer defines the interface for tenant lifecycle management
// including metadata CRUD and per-tenant barrier operations.
type TenantServicer interface {
	// CreateTenant creates a new tenant with the given ID, name, and
	// isolation type. When isolationType is empty, IsolationPrefix is
	// used (barrier isolation is opt-in). Registers barrier-isolated
	// tenants in the barrier registry and persists metadata. Optional
	// CreateTenantOptions select the barrier key-management mode (see
	// WithBarrierAutoUnseal); the default is self-managed.
	CreateTenant(ctx context.Context, id, name string, isolationType IsolationType, opts ...CreateTenantOption) (*TenantInfo, error)

	// GetTenant retrieves tenant metadata by ID.
	GetTenant(ctx context.Context, tenantID string) (*TenantInfo, error)

	// ListTenants returns metadata for all registered tenants.
	ListTenants(ctx context.Context) ([]TenantInfo, error)

	// DeleteTenant removes a tenant from the barrier registry and
	// deletes its metadata from storage.
	DeleteTenant(ctx context.Context, tenantID string) error

	// ListDeletedTenants returns one page of retired tenant ids with their
	// deletion records, in ascending id order: a paged range read over the
	// tombstone prefix. pageSize 0 means DefaultDeletedTenantsPageSize and
	// is capped at MaxDeletedTenantsPageSize; pageToken is the previous
	// page's NextPageToken, or empty for the first page.
	ListDeletedTenants(ctx context.Context, pageToken string, pageSize int) (*DeletedTenantsPage, error)

	// ReconcileDeletedTenants removes, from THIS node, the on-disk residue of
	// every retired tenant id, never touching a directory a live tenant
	// claims. It is node-local by nature: each replica reclaims its own disk.
	ReconcileDeletedTenants(ctx context.Context) (*ReconcileReport, error)

	// UpdateTenant applies a partial update to an existing tenant.
	// Changing the IsolationType on a tenant that has data returns
	// ErrTenantHasData (409 Conflict). The caller must migrate or
	// delete tenant data first.
	UpdateTenant(ctx context.Context, tenantID string, patch TenantPatch) (*TenantInfo, error)

	// UpdateQuotas updates the resource quotas for a tenant and
	// persists the change to the metadata store.
	UpdateQuotas(ctx context.Context, tenantID string, quotas *quota.Quotas) (*TenantInfo, error)

	// UpdateEntitlements updates the feature entitlements for a tenant and
	// persists the change to the metadata store. A nil entitlements pointer
	// returns ErrNilEntitlements.
	UpdateEntitlements(ctx context.Context, tenantID string, entitlements *quota.Entitlements) (*TenantInfo, error)

	// CheckBarrierMode returns nil if the tenant may use the given barrier
	// sealing strategy: dedicated modes are gated by AllowedBarrierModes, the
	// shared system-barrier option by SystemBarrierQuota. The system tenant is
	// exempt. Returns a *quota.EntitlementError otherwise.
	CheckBarrierMode(ctx context.Context, tenantID string, mode seal.StrategyID) error

	// IncrementQuotaUsage increments the current-usage counter for the given
	// resource by delta (must be > 0) and persists the change. It returns
	// QuotaExceededError when the resulting usage would exceed the configured
	// limit for that resource. Callers should call this before creating a
	// resource and roll back on error.
	IncrementQuotaUsage(ctx context.Context, tenantID string, resource quota.Resource, delta int64) (*TenantInfo, error)

	// DecrementQuotaUsage decrements the current-usage counter for the given
	// resource by delta (must be > 0) and persists the change. The counter
	// is clamped to zero (never negative). Callers should call this after
	// successfully deleting a resource.
	DecrementQuotaUsage(ctx context.Context, tenantID string, resource quota.Resource, delta int64) (*TenantInfo, error)

	// GetQuotaUsage returns the current-usage value for the given resource
	// and tenant without modifying any state.
	GetQuotaUsage(ctx context.Context, tenantID string, resource quota.Resource) (int64, error)

	// TenantBarrierInit initializes the per-tenant barrier with a
	// caller-supplied passphrase using the default passphrase strategy.
	// It is equivalent to calling TenantBarrierInitWithStrategy with
	// seal.StrategyPassphrase. The threshold and shares parameters are
	// reserved for Shamir secret-sharing configuration. An empty
	// passphrase is rejected; there is no derived credential fallback.
	// passphrase is []byte, matching TenantBarrierUnseal's key, because a
	// Go string cannot be zeroized. It is borrowed for the call and never
	// retained, so the caller is free to wipe it on return.
	TenantBarrierInit(ctx context.Context, tenantID string, passphrase []byte, threshold, shares int) (*TenantBarrierInitResult, error)

	// TenantBarrierInitWithStrategy initializes the per-tenant barrier
	// using an explicitly selected seal strategy resolved from the
	// injected StrategyRegistry. When strategyID is empty or equals
	// seal.StrategyPassphrase the behaviour is identical to
	// TenantBarrierInit. Hardware and cloud-KMS strategies (tpm2,
	// pkcs11, aws-kms, gcp-kms, azure-kv, vault) are resolved at call
	// time from the registry; if the strategy is not registered,
	// InvalidStrategyError is returned. When no StrategyRegistry was
	// injected, only the built-in software strategies (passphrase, none,
	// shamir) are accepted and hardware/cloud strategy requests return
	// InvalidStrategyError.
	// passphrase is a borrow the caller may wipe on return; see
	// TenantBarrierInit.
	TenantBarrierInitWithStrategy(ctx context.Context, tenantID string, passphrase []byte, threshold, shares int, strategyID seal.StrategyID) (*TenantBarrierInitResult, error)

	// TenantBarrierUnseal unseals the per-tenant barrier using the
	// caller-supplied key material. There is no derived credential
	// fallback: empty key material is rejected.
	TenantBarrierUnseal(ctx context.Context, tenantID string, key []byte) error

	// TenantBarrierUnsealShare submits a single Shamir share toward the
	// per-tenant barrier's quorum threshold, automatically unsealing once
	// enough distinct shares have been submitted. It is the per-tenant
	// analogue of the system barrier's unseal-share endpoint and is only
	// valid for barriers initialized with the Shamir strategy.
	TenantBarrierUnsealShare(ctx context.Context, tenantID, share string) (*TenantBarrierUnsealProgress, error)

	// TenantBarrierSeal seals the per-tenant barrier, returning it to the
	// sealed state. Subsequent reads and writes against the tenant's
	// namespace fail until the barrier is unsealed again.
	TenantBarrierSeal(ctx context.Context, tenantID string) error

	// TenantBarrierStatus returns the current state of the per-tenant
	// barrier (sealed/initialized flags, active strategy, hardware-backing).
	TenantBarrierStatus(ctx context.Context, tenantID string) (*TenantBarrierStatus, error)

	// TenantBarrierOfficerRegister registers a Security Officer for the
	// named tenant's barrier. Validates the CSR, signs it with the CA, and
	// persists public metadata. No share bytes are produced at this step.
	// Officers must be registered before TenantBarrierInitWithStrategy(Shamir)
	// is called so that SealAndDistribute can seal one share per enrolled officer.
	TenantBarrierOfficerRegister(ctx context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error)

	// TenantBarrierOfficerList returns all enrolled Security Officers for
	// the named tenant's barrier.
	TenantBarrierOfficerList(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error)

	// TenantBarrierShareClaimBegin issues a challenge nonce to the named
	// Security Officer for the tenant barrier share claim handshake.
	TenantBarrierShareClaimBegin(ctx context.Context, tenantID, username, callerCertThumbprint string) (*ceremony.ChallengeResponse, error)

	// TenantBarrierShareClaimComplete verifies the officer's nonce signature
	// and returns the share bytes exactly once via the officer's own mTLS session.
	TenantBarrierShareClaimComplete(ctx context.Context, tenantID, username, nonceHex string, signature []byte, callerCertThumbprint string) (*ceremony.ShareClaimResult, error)

	// TenantBarrierOfficerClaimStatus returns a per-officer claim status
	// snapshot for the named tenant's barrier. Never includes share bytes.
	TenantBarrierOfficerClaimStatus(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error)

	// TenantBarrierShamirSealVote submits a Shamir share as a seal vote for
	// the named tenant's barrier. When the quorum threshold is met the barrier
	// is sealed automatically.
	TenantBarrierShamirSealVote(ctx context.Context, tenantID, share string) (*TenantBarrierUnsealProgress, error)
}

// TenantBarrierStatus reports the current state of a per-tenant barrier.
// It mirrors seal.BarrierStatus while adding the tenant identifier and an
// Initialized flag for callers that distinguish "never initialized" from
// "initialized and sealed".
type TenantBarrierStatus struct {
	// TenantID is the tenant whose barrier is being reported on.
	TenantID string `json:"tenant_id"`

	// Sealed is true when the tenant barrier is in sealed state.
	Sealed bool `json:"sealed"`

	// Initialized is true when the tenant barrier has been registered and
	// initialized at least once.
	Initialized bool `json:"initialized"`

	// Strategy identifies the active sealing strategy (e.g. "software",
	// "shamir", "pkcs11").
	Strategy string `json:"strategy"`

	// HardwareBacked is true when the active strategy uses dedicated
	// cryptographic hardware.
	HardwareBacked bool `json:"hardware_backed"`

	// ShamirThreshold is the minimum number of Shamir shares required to
	// unseal this tenant barrier. Zero when the active strategy is not Shamir.
	ShamirThreshold int `json:"shamir_threshold,omitempty"`

	// ShamirTotal is the total number of Shamir shares generated at
	// initialization. Zero when the active strategy is not Shamir.
	ShamirTotal int `json:"shamir_total,omitempty"`
}

// TenantBarrierInitResult reports the outcome of a per-tenant barrier
// initialization. For the Shamir strategy it carries the threshold, total, and
// the number of officer shares sealed to ceremony enclaves. Share material is
// never present here; Security Officers retrieve their shares via the
// TenantBarrierShareClaimBegin / TenantBarrierShareClaimComplete flow.
type TenantBarrierInitResult struct {
	// TenantID is the tenant whose barrier was initialized.
	TenantID string `json:"tenant_id"`

	// Strategy is the seal strategy the barrier was initialized with.
	Strategy string `json:"strategy"`

	// Threshold is the minimum number of shares required to unseal. Zero for
	// non-Shamir strategies.
	Threshold int `json:"threshold,omitempty"`

	// TotalShares is the total number of shares generated. Zero for non-Shamir
	// strategies.
	TotalShares int `json:"total_shares,omitempty"`

	// ShareCount is the number of shares successfully sealed to officer enclaves.
	// Equals TotalShares when all officers are enrolled; may be less when
	// enrollment is still in progress. Zero for non-Shamir strategies.
	ShareCount int `json:"share_count,omitempty"`
}

// TenantBarrierUnsealProgress reports quorum progress while submitting Shamir
// shares to unseal a per-tenant barrier. Once Complete is true the barrier has
// been unsealed and the quorum accumulator has been reset.
type TenantBarrierUnsealProgress struct {
	// TenantID is the tenant whose barrier is being unsealed.
	TenantID string `json:"tenant_id"`

	// Required is the number of shares needed to reach the threshold.
	Required int `json:"required"`

	// Submitted is the number of distinct shares submitted so far.
	Submitted int `json:"submitted"`

	// Complete indicates the threshold has been met and the barrier unsealed.
	Complete bool `json:"complete"`

	// Sealed reports whether the barrier is still sealed (the inverse of
	// Complete for the current quorum round).
	Sealed bool `json:"sealed"`
}

// TenantService manages multi-tenant lifecycle including metadata CRUD
// and per-tenant barrier operations. It delegates barrier lifecycle to
// the BarrierRegistry and persists tenant metadata in a StorageBackend.
type TenantService struct {
	store            seal.StorageBackend
	registry         *seal.BarrierRegistry
	strategyRegistry seal.StrategyRegistry
	distributor      seal.CeremonyDistributor
	tenantCeremonies *ceremony.TenantCeremonyRegistry
	ceremonyCA       ceremony.CeremonyCA
	defaultQuotas    *quota.Quotas
	defaultEnts      *quota.Entitlements
	// reservedDataRoots are additional filesystem roots an operator-supplied
	// dedicated DataRoot (CreateTenant WithDataRoot) must not overlap, beyond
	// the volume-pool base paths (resolved from the registry) and existing
	// tenants' DataRoots (resolved from the metadata store). It is the seam for
	// injecting the daemon's system data dir; see WithReservedDataRoots.
	reservedDataRoots []string

	// teardownSeams carries DeleteTenant's three optional dependencies; see
	// teardown.go for why each is an interface rather than a dependency. All
	// three are optional: a composition that wires none still retires the id,
	// which is the security half of a delete.
	teardownSeams

	// isolationChangeGuard carries UpdateTenant's data probe; see
	// isolation_change.go.
	isolationChangeGuard
	// runtimeCloser stops what this node runs for a tenant before
	// ReconcileDeletedTenants removes its directories; see ResidueWiring.
	runtimeCloser atomic.Pointer[runtimeCloserBox]

	// removeAll removes one reclaimed directory; the registry's
	// RemoveDataRoot in every composition, which empties a mount point rather
	// than failing on it. It is a field so a test can make a removal fail and
	// prove a second pass still finds the directory.
	removeAll func(path string) error

	logger *slog.Logger
}

// TenantServiceOption configures optional TenantService dependencies.
type TenantServiceOption func(*TenantService)

// WithStrategyRegistry injects a StrategyRegistry so that
// TenantBarrierInitWithStrategy can resolve hardware/cloud seal strategies
// by name. When no registry is injected, only the built-in software-only
// strategies (passphrase, none) are accepted for per-tenant barriers.
func WithStrategyRegistry(reg seal.StrategyRegistry) TenantServiceOption {
	return func(s *TenantService) { s.strategyRegistry = reg }
}

// WithTenantDistributor injects a CeremonyDistributor that is wired onto
// each newly registered Shamir tenant barrier before initialization. In
// production, pass a TenantCeremonyRegistry implementation; in tests, pass
// a capture distributor to retrieve generated shares for unseal operations.
// When set, this distributor takes precedence over the per-tenant ceremony
// service resolved from the TenantCeremonyRegistry.
func WithTenantDistributor(d seal.CeremonyDistributor) TenantServiceOption {
	return func(s *TenantService) { s.distributor = d }
}

// WithTenantCeremonyCA injects the CeremonyCA used when lazily constructing
// per-tenant TenantCeremonyService instances. If neither an explicit
// distributor (WithTenantDistributor) nor a CA is configured, Shamir
// initialization fails with ErrCeremonyDistributorNotConfigured because the
// tenant's StorageBarrier has no distributor to seal shares to officers.
func WithTenantCeremonyCA(ca ceremony.CeremonyCA) TenantServiceOption {
	return func(s *TenantService) { s.ceremonyCA = ca }
}

// WithReservedDataRoots registers additional filesystem roots that an
// operator-supplied dedicated DataRoot (CreateTenant's WithDataRoot option)
// must not overlap. Volume-pool base paths and existing tenants' DataRoots are
// always checked automatically; this option threads in reserved locations the
// tenant service cannot otherwise discover — principally the daemon's system
// data dir, so a dedicated disk cannot be nested inside the system's own
// storage tree. Empty paths are ignored; the rest are cleaned. Overlap is
// equal-or-ancestor in either direction (see DataRootOverlapError).
func WithReservedDataRoots(paths ...string) TenantServiceOption {
	return func(s *TenantService) {
		for _, p := range paths {
			if p != "" {
				s.reservedDataRoots = append(s.reservedDataRoots, filepath.Clean(p))
			}
		}
	}
}

// WithDefaultQuotas injects the operator-configured default quotas applied to
// tenants whose own Quotas are nil. When unset, a nil-quota tenant uses the
// zero-value Quotas (all limits at system default).
func WithDefaultQuotas(q *quota.Quotas) TenantServiceOption {
	return func(s *TenantService) { s.defaultQuotas = q }
}

// WithDefaultEntitlements injects the operator-configured default entitlements
// applied to tenants whose own Entitlements are nil. When unset, the
// conservative built-in defaults (quota.DefaultEntitlements) are used.
func WithDefaultEntitlements(e *quota.Entitlements) TenantServiceOption {
	return func(s *TenantService) { s.defaultEnts = e }
}

// Compile-time interface checks.
var (
	_ TenantServicer           = (*TenantService)(nil)
	_ quota.QuotaProvider      = (*TenantService)(nil)
	_ quota.EntitlementChecker = (*TenantService)(nil)
)

// NewTenantService creates a new TenantService. The store parameter is
// used for persisting tenant metadata, and the registry manages the
// per-tenant barrier lifecycle. Optional TenantServiceOptions may be
// supplied; see WithStrategyRegistry.
func NewTenantService(
	store seal.StorageBackend,
	registry *seal.BarrierRegistry,
	logger *slog.Logger,
	opts ...TenantServiceOption,
) (*TenantService, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if registry == nil {
		return nil, ErrNilRegistry
	}
	if logger == nil {
		logger = slog.Default()
	}
	svc := &TenantService{
		store:            store,
		registry:         registry,
		tenantCeremonies: ceremony.NewTenantCeremonyRegistry(),
		removeAll:        registry.RemoveDataRoot,
		logger:           logger,
	}
	for _, o := range opts {
		o(svc)
	}
	return svc, nil
}

// CreateTenant creates a new tenant with the given ID, name, and isolation
// type. When isolationType is empty, IsolationPrefix is used (barrier
// isolation is strictly opt-in). It validates inputs, checks for duplicates,
// registers barrier-isolated tenants in the barrier registry with default
// configuration, and persists metadata in the storage backend.
func (s *TenantService) CreateTenant(ctx context.Context, id, name string, isolationType IsolationType, opts ...CreateTenantOption) (*TenantInfo, error) {
	cfg := resolveCreateTenantOptions(opts)
	if id == "" {
		return nil, ErrEmptyTenantID
	}
	if name == "" {
		return nil, ErrEmptyTenantName
	}
	if err := validateReservedTenantID(ctx, id); err != nil {
		return nil, err
	}
	// A deleted id is retired, not released. This runs BEFORE every other
	// check and before any registration side effect, so a resurrection
	// attempt costs one read and changes nothing. See tombstone.go.
	if err := checkTombstone(ctx, s.store.Get, id); err != nil {
		return nil, err
	}
	// Barrier isolation is strictly opt-in: an unspecified isolation type
	// defaults to logical-prefix isolation against the shared system store.
	if isolationType == "" {
		isolationType = IsolationPrefix
	}
	// The system tenant is structurally a prefix tenant: its IAM data must
	// remain in the shared store so bootstrap works while every tenant
	// barrier is still sealed. Reject barrier isolation for it. (External
	// creation of the system tenant is already blocked by the reserved-id
	// check above; this guards the internal-call bootstrap path too.)
	if id == SystemTenantID && (isolationType == IsolationBarrier || isolationType == IsolationVolume || isolationType == IsolationRaft) {
		return nil, ErrSystemTenantMustBePrefix
	}

	// WithDataRoot pins a dedicated-isolation tenant's DataRoot to an
	// operator-supplied path instead of pool allocation. It is only
	// meaningful for IsolationVolume / IsolationRaft, whose tenants get a
	// dedicated filesystem DataRoot in the first place; validate the path
	// itself (absolute, canonical, empty-on-disk) before any registration
	// side effect runs.
	var explicitDataRoot string
	if cfg.dataRoot != nil {
		if isolationType != IsolationVolume && isolationType != IsolationRaft {
			return nil, &DataRootIsolationTypeError{TenantID: id, IsolationType: isolationType}
		}
		if err := validateExplicitDataRoot(*cfg.dataRoot); err != nil {
			return nil, err
		}
		explicitDataRoot = *cfg.dataRoot
		// A pool-allocated DataRoot is contained by construction (its slot is a
		// strict child of a pool base, unique per tenant). An explicit
		// dedicated-disk DataRoot is outside any pool, so reject it up front if
		// it overlaps the pool base, the system data dir, or another tenant's
		// DataRoot — nesting one dedicated tree inside another causes
		// data-confusion. Boot re-attach reuses an already-validated, persisted
		// root and is not re-checked here.
		if err := s.checkDataRootOverlap(ctx, id, explicitDataRoot); err != nil {
			return nil, err
		}
	}

	// Check for existing tenant metadata.
	key := metadataPrefix + id
	_, err := s.store.Get(ctx, key)
	if err == nil {
		return nil, ErrTenantAlreadyExists
	}
	if !errors.Is(err, seal.ErrSecretNotFound) {
		return nil, &StoreError{Op: "check-exists", Err: err}
	}

	// Register a per-tenant barrier only for barrier-isolated tenants.
	// Prefix tenants share the system KVStore and need no barrier
	// registration — their IAM records are separated logically by the
	// tenant-scoped key segments the IAM stores already embed. A
	// barrier-isolated tenant gets a durable, independent backend from the
	// volume pool (RegisterTenantPooled) and is initialized immediately so
	// its DEK exists; the barrier is unsealed and ready to serve IAM data.
	// A system-managed (auto-unseal) barrier is entitlement-gated: the plan
	// must permit it explicitly. The secure default (self-managed) is always
	// allowed for any dedicated-isolation tenant.
	if cfg.barrierAutoUnseal && (isolationType == IsolationBarrier || isolationType == IsolationVolume || isolationType == IsolationRaft) {
		if err := s.CheckFeature(ctx, id, quota.FeatureSystemManagedBarrier); err != nil {
			return nil, err
		}
	}

	var dataRoot string
	switch isolationType {
	case IsolationBarrier:
		if err := s.registerDedicatedBarrier(ctx, id, cfg.barrierAutoUnseal); err != nil {
			return nil, err
		}
	case IsolationVolume:
		// Entitlement plane: the tenant's plan (or the operator defaults, since
		// the metadata record does not exist yet) must permit volume isolation
		// and dedicated volume storage.
		if err := s.CheckIsolation(ctx, id, quota.IsolationModeVolume); err != nil {
			return nil, err
		}
		if err := s.CheckFeature(ctx, id, quota.FeatureVolumeStorage); err != nil {
			return nil, err
		}
		// Allocate the dedicated DataRoot from the volume pool and register the
		// per-tenant barrier (rooted at {DataRoot}/barrier) so the volume is
		// encrypted under the tenant's own DEK. For a system-managed tenant the
		// DEK is wrapped under the (already-unsealed) system barrier and the
		// barrier is initialized immediately so boot re-attach can unseal it
		// credential-free. For a self-managed tenant the barrier is registered
		// SEALED and left uninitialized: the tenant admin drives Initialize and
		// Unseal, and the per-tenant Host builds lazily only after unseal.
		// explicitDataRoot is empty unless WithDataRoot was used, in which case
		// RegisterVolumeTenant reuses it instead of allocating from the pool.
		root, _, regErr := s.registry.RegisterVolumeTenant(ctx, id, explicitDataRoot, cfg.barrierAutoUnseal)
		if regErr != nil {
			return nil, s.volumeRegisterError(ctx, id, "register-volume", regErr)
		}
		dataRoot = root
		if cfg.barrierAutoUnseal {
			if initErr := s.registry.InitializeTenant(ctx, id, seal.Credentials{}); initErr != nil {
				_ = s.registry.UnregisterTenant(id)
				return nil, &BarrierError{TenantID: id, Op: "initialize", Err: initErr}
			}
		}
	case IsolationRaft:
		// Entitlement plane: the tenant's plan (or operator defaults, since the
		// metadata record does not exist yet) must permit raft isolation and the
		// dedicated-raft feature.
		if err := s.CheckIsolation(ctx, id, quota.IsolationModeRaft); err != nil {
			return nil, err
		}
		if err := s.CheckFeature(ctx, id, quota.FeatureDedicatedRaft); err != nil {
			return nil, err
		}
		// Allocate the dedicated DataRoot from the volume pool and register the
		// per-tenant barrier exactly as for IsolationVolume (rooted at
		// {DataRoot}/barrier) so each per-database raft shard's state-machine
		// data is encrypted under the tenant's own DEK. The shards themselves run
		// on the SHARED system Host (one shard per database) and are started by
		// the server-layer RaftBackendFactory; only the barrier + DataRoot are
		// provisioned here. System-managed vs self-managed key handling matches
		// IsolationVolume. explicitDataRoot is empty unless WithDataRoot was
		// used, in which case RegisterVolumeTenant reuses it instead of
		// allocating from the pool.
		root, _, regErr := s.registry.RegisterVolumeTenant(ctx, id, explicitDataRoot, cfg.barrierAutoUnseal)
		if regErr != nil {
			return nil, s.volumeRegisterError(ctx, id, "register-raft", regErr)
		}
		dataRoot = root
		if cfg.barrierAutoUnseal {
			if initErr := s.registry.InitializeTenant(ctx, id, seal.Credentials{}); initErr != nil {
				_ = s.registry.UnregisterTenant(id)
				return nil, &BarrierError{TenantID: id, Op: "initialize", Err: initErr}
			}
		}
	}

	now := time.Now().UTC()
	info := &TenantInfo{
		ID:                id,
		Name:              name,
		IsolationType:     isolationType,
		DataRoot:          dataRoot,
		BarrierAutoUnseal: cfg.barrierAutoUnseal,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	// A volume tenant runs exactly one local Raft group (its per-tenant Host),
	// shared by all of its databases. Record that one group in the usage
	// counter so MaxRaftGroups accounting is accurate; the entitlement plane
	// above is the gate for whether the tenant may use volume isolation at all.
	if isolationType == IsolationVolume {
		info.Quotas = &quota.Quotas{CurrentRaftGroups: 1}
	}

	data, err := json.Marshal(info)
	if err != nil {
		// Roll back barrier registration on marshal failure.
		_ = s.registry.UnregisterTenant(id)
		return nil, &StoreError{Op: "marshal", Err: err}
	}

	if err := s.store.Put(ctx, key, data); err != nil {
		// Roll back barrier registration on storage failure.
		_ = s.registry.UnregisterTenant(id)
		return nil, &StoreError{Op: "put", Err: err}
	}

	s.logger.Info("tenant created",
		slog.String("tenant_id", id),
		slog.String("name", name),
		slog.String("isolation_type", string(isolationType)))

	return info, nil
}

// volumeRegisterError maps a volume/raft barrier registration failure onto
// the tenant package's errors. A DataRoot another tenant's record on this node
// still names is the caller's conflict, reported like any other DataRoot
// overlap. When that claimant is a RETIRED tenant -- deleted, its residue not
// yet reclaimed on this node -- the error says so and names the verb that
// reclaims it, because the operator's remedy is then to reconcile, not to
// pick another path.
func (s *TenantService) volumeRegisterError(ctx context.Context, id, op string, regErr error) error {
	if errors.Is(regErr, seal.ErrTenantAlreadyExists) {
		return ErrTenantAlreadyExists
	}
	var claimed *seal.DataRootClaimedError
	if !errors.As(regErr, &claimed) {
		return &BarrierError{TenantID: id, Op: op, Err: regErr}
	}
	overlap := &DataRootOverlapError{DataRoot: claimed.DataRoot, Conflict: claimed.DataRoot, Kind: "tenant " + claimed.ClaimedBy}
	_, retired, err := s.deletedAt(ctx, claimed.ClaimedBy)
	if err != nil {
		return err
	}
	if retired {
		overlap.Kind = "retired tenant " + claimed.ClaimedBy
		overlap.Remedy = retiredResidueRemedy
	}
	return overlap
}

// retiredResidueRemedy is the remedy a DataRootOverlapError names when the
// conflicting path is a retired tenant's unreclaimed residue.
const retiredResidueRemedy = "it is retired-tenant residue on this node; run `qrdb tenant reconcile-deleted` on this node to reclaim it"

// checkDataRootOverlap rejects an operator-supplied dedicated DataRoot that
// overlaps a reserved storage location. It composes the three overlap sources
// CreateTenant can reach:
//
//   - (a) the configured volume-pool base paths, resolved from the barrier
//     registry (empty when no pool is configured);
//   - (b) the reserved roots injected via WithReservedDataRoots (the daemon's
//     system data dir);
//   - (c) every existing tenant's DataRoot, from the metadata store.
//
// dataRoot is assumed already absolute and canonical (validateExplicitDataRoot
// ran first). Overlap is equal-or-ancestor in either direction, so a request
// that nests inside, contains, or equals a reserved path is rejected with a
// *DataRootOverlapError. The /data/foo vs /data/foo2 non-overlap is preserved
// by dataRootsOverlap's component-aware comparison.
func (s *TenantService) checkDataRootOverlap(ctx context.Context, tenantID, dataRoot string) error {
	poolBases, err := s.registry.PoolBasePaths(ctx)
	if err != nil {
		return &BarrierError{TenantID: tenantID, Op: "pool-base-paths", Err: err}
	}
	for _, base := range poolBases {
		if dataRootsOverlap(dataRoot, base) {
			return &DataRootOverlapError{DataRoot: dataRoot, Conflict: base, Kind: "volume pool"}
		}
	}
	for _, res := range s.reservedDataRoots {
		if dataRootsOverlap(dataRoot, res) {
			return &DataRootOverlapError{DataRoot: dataRoot, Conflict: res, Kind: "system data dir"}
		}
	}
	tenants, err := s.ListTenants(ctx)
	if err != nil {
		return err
	}
	for _, t := range tenants {
		if t.ID == tenantID || t.DataRoot == "" {
			continue
		}
		if dataRootsOverlap(dataRoot, t.DataRoot) {
			return &DataRootOverlapError{DataRoot: dataRoot, Conflict: t.DataRoot, Kind: "tenant " + t.ID}
		}
	}
	return nil
}

// registerDedicatedBarrier registers an IsolationBarrier tenant's per-tenant
// barrier in the chosen key-management mode. For a system-managed (auto-unseal)
// tenant the DEK is wrapped under the system barrier and the barrier is
// initialized immediately so it is unsealed and ready and re-attaches
// credential-free at boot. For a self-managed tenant the barrier is registered
// SEALED and left uninitialized; the tenant admin drives Initialize/Unseal via
// the TenantBarrier* endpoints. Registration is rolled back on a failed init so
// no dangling, uninitialized barrier is left behind.
func (s *TenantService) registerDedicatedBarrier(ctx context.Context, id string, autoUnseal bool) error {
	register := s.registry.RegisterTenantPooled
	if autoUnseal {
		register = s.registry.RegisterTenantPooledSystemManaged
	}
	if _, regErr := register(ctx, id); regErr != nil {
		if errors.Is(regErr, seal.ErrTenantAlreadyExists) {
			return ErrTenantAlreadyExists
		}
		return &BarrierError{TenantID: id, Op: "register", Err: regErr}
	}
	if !autoUnseal {
		// Self-managed: leave the barrier sealed for the tenant admin.
		return nil
	}
	if initErr := s.registry.InitializeTenant(ctx, id, seal.Credentials{}); initErr != nil {
		_ = s.registry.UnregisterTenant(id)
		return &BarrierError{TenantID: id, Op: "initialize", Err: initErr}
	}
	return nil
}

// UpdateTenant applies a partial update to an existing tenant. Non-empty
// patch fields replace the corresponding stored value. When
// patch.IsolationType differs from the current value, the caller is expected
// to have already verified that the tenant has no data (the REST handler
// enforces this gate and returns 409 Conflict when data exists).
func (s *TenantService) UpdateTenant(ctx context.Context, tenantID string, patch TenantPatch) (*TenantInfo, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}

	info, err := s.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if err := s.checkIsolationChange(ctx, info, patch); err != nil {
		return nil, err
	}

	if patch.IsolationType != "" {
		info.IsolationType = patch.IsolationType
	}
	if patch.Name != "" {
		info.Name = patch.Name
	}
	info.UpdatedAt = time.Now().UTC()

	data, err := json.Marshal(info)
	if err != nil {
		return nil, &StoreError{Op: "marshal", Err: err}
	}

	key := metadataPrefix + tenantID
	if err := s.store.Put(ctx, key, data); err != nil {
		return nil, &StoreError{Op: "put", Err: err}
	}

	s.logger.Info("tenant updated",
		slog.String("tenant_id", tenantID))

	return info, nil
}

// GetTenant retrieves tenant metadata by ID from the storage backend.
func (s *TenantService) GetTenant(ctx context.Context, tenantID string) (*TenantInfo, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}

	key := metadataPrefix + tenantID
	data, err := s.store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, seal.ErrSecretNotFound) {
			return nil, ErrTenantNotFound
		}
		return nil, &StoreError{Op: "get", Err: err}
	}

	var info TenantInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, &StoreError{Op: "unmarshal", Err: err}
	}

	return &info, nil
}

// MigrateMetadata copies tenant metadata records from src into dst for any
// tenant ID not already present in dst. It is a one-time, idempotent
// compatibility shim for deployments whose TenantInfo records were persisted
// to a node-local seal store before tenant metadata moved onto the replicated
// system shard (see the tenant-store wiring in the lifecycle package). Records
// already present in dst are left untouched. Per-record destination errors are
// logged and skipped so a transient backend issue (e.g. no elected leader yet)
// never aborts boot — the next boot retries because the record is still absent
// from dst. It returns the number of records copied into dst.
//
// On a freshly initialized cluster src holds no tenant metadata and this is a
// no-op.
func MigrateMetadata(ctx context.Context, src, dst seal.StorageBackend, logger *slog.Logger) (int, error) {
	if src == nil || dst == nil {
		return 0, ErrNilStore
	}
	if logger == nil {
		logger = slog.Default()
	}

	keys, err := src.List(ctx, metadataPrefix)
	if err != nil {
		return 0, &StoreError{Op: "migrate-list", Err: err}
	}

	migrated := 0
	for _, key := range keys {
		data, err := src.Get(ctx, key)
		if err != nil {
			if errors.Is(err, seal.ErrSecretNotFound) {
				continue
			}
			logger.Warn("tenant metadata migration: source record unreadable; skipping",
				slog.String("key", key), slog.String("error", err.Error()))
			continue
		}

		// Skip records already present in the destination.
		if _, derr := dst.Get(ctx, key); derr == nil {
			continue
		} else if !errors.Is(derr, seal.ErrSecretNotFound) {
			logger.Warn("tenant metadata migration: destination read failed; skipping",
				slog.String("key", key), slog.String("error", derr.Error()))
			continue
		}

		if perr := dst.Put(ctx, key, data); perr != nil {
			logger.Warn("tenant metadata migration: destination write failed; skipping",
				slog.String("key", key), slog.String("error", perr.Error()))
			continue
		}

		migrated++
		logger.Info("tenant metadata migrated to replicated store",
			slog.String("key", key))
	}

	return migrated, nil
}

// ListTenants returns metadata for all registered tenants by scanning
// the metadata prefix in the storage backend.
func (s *TenantService) ListTenants(ctx context.Context) ([]TenantInfo, error) {
	keys, err := s.store.List(ctx, metadataPrefix)
	if err != nil {
		return nil, &StoreError{Op: "list", Err: err}
	}

	tenants := make([]TenantInfo, 0, len(keys))
	for _, key := range keys {
		data, err := s.store.Get(ctx, key)
		if err != nil {
			// Skip entries that disappeared between list and get.
			if errors.Is(err, seal.ErrSecretNotFound) {
				continue
			}
			return nil, &StoreError{Op: "get", Err: err}
		}

		var info TenantInfo
		if err := json.Unmarshal(data, &info); err != nil {
			s.logger.Warn("skipping corrupt tenant metadata",
				slog.String("key", key),
				slog.String("error", err.Error()))
			continue
		}
		tenants = append(tenants, info)
	}

	return tenants, nil
}

// DeleteTenant retires a tenant id and reclaims what the tenant owned.
//
// The order is the whole design, and every step is placed by one rule:
// PERSIST THE RETIREMENT BEFORE ANYTHING IRREVERSIBLE (go-qrdb#483).
//
//  0. REFUSE a RESERVED id outright, before any read. The reserved set --
//     "system", "default", "_*", "*:*" -- is declared once, in
//     reservedTenantIDReason, and consulted by creation and deletion alike.
//     The reclamation in step 6 already refused the system tenant, but that
//     guards the last step and step 4 is the irreversible one, so the same
//     judgement is hoisted to the front.
//  1. REFUSE while the tenant still owns databases. Teardown is bottom-up: a
//     database's own drop reclaims the keyspace that database owns
//     (go-qrdb#412), so by the time this runs, what is left under the tenant
//     is its identity records. That is what keeps step 6 bounded.
//  2. WRITE THE TOMBSTONE. From here the id is retired permanently: no
//     tenant may ever be created under it again, so no later principal can
//     inherit whatever the later steps have not yet removed.
//  3. DELETE THE METADATA. Steps 2 and 3 together are the FENCE, and they are
//     in this order so a crash between them leaves an id that is retired and
//     a record that still describes it -- never the reverse. Once both have
//     committed the tenant no longer resolves: GetTenant reports not-found,
//     ListTenants omits it, and StoreResolver refuses the id outright rather
//     than serving it as an unregistered prefix tenant. Neither step destroys
//     anything, so a failure in either aborts with the tenant INTACT.
//  4. SHRED THE DEK. This is the first irreversible step and it is
//     deliberately the first step after the fence.
//  5. UNREGISTER AND DESTROY THE BARRIER, including the durable records
//     behind it. Leaving those behind is not a leak but a defect: the boot
//     path restores a per-tenant barrier from them, so a deleted tenant's
//     barrier would come back on the next restart.
//  6. RECLAIM THE KEYSPACE, synchronously.
//
// # Why the shred is not first
//
// It was, and that was the defect go-qrdb#483 reports. The ordering was
// argued from a choice of two: shred-then-delete can strand metadata for a
// tenant whose key is gone, and delete-then-shred can strand a key nothing
// names AND leave the data readable in the window. Shred-first was taken as
// the lesser, but its real cost is worse than "stranded metadata": a
// transient failure of the tombstone write returned an error that reads as
// "nothing changed" while leaving a LIVE, resolvable, listed tenant whose
// every byte was already permanently undecryptable.
//
// A third ordering dominates both, and it was not on that list. Fencing
// first strands no key that is readable -- steps 2 and 3 make the tenant
// unresolvable before step 4 can destroy anything -- and it strands no
// metadata for a keyless tenant, because the metadata is gone before the key
// is. What it can leave is a retired id whose DEK still exists: residue of
// exactly the same kind as steps 5 and 6 already leave, reachable by nobody,
// and removed by re-issuing the call.
//
// # Resuming
//
// Every step after the fence is idempotent -- DeleteTenantDEK treats an
// absent key as success, DestroyTenant treats an unregistered tenant as
// success, and the reclaimer treats an empty keyspace as success -- so
// re-issuing DeleteTenant over a partly finished teardown completes it
// rather than wedging. A failure on the far side of the fence is reported as
// *TeardownError, which says so.
//
// Step 6 is synchronous rather than a background sweep because step 1 has
// already bounded it. What remains under a tenant whose databases are gone
// is its identity records -- users, roles, permission scopes, invitations --
// which the quota plane limits by MaxUsers and MaxRoles. That is strictly
// less work than DROP DATABASE already performs synchronously on the same
// key-at-a-time path. A background reclaimer would add a restartable,
// observable, idempotent subsystem in order to do less work than a call this
// repository already makes inline.
func (s *TenantService) DeleteTenant(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		return ErrEmptyTenantID
	}
	// 0. A reserved id is not deletable, and this runs before step 1 rather
	//    than beside step 6 so it prevents the COMMITS, not just the
	//    destructive read. See validateDeletableTenantID.
	if err := validateDeletableTenantID(tenantID); err != nil {
		return err
	}

	// Read the tenant BEFORE anything is destroyed: the tombstone records
	// what the tenant was, and the reclamation needs to know where it lived.
	info, err := s.GetTenant(ctx, tenantID)
	if err != nil {
		if errors.Is(err, ErrTenantNotFound) {
			// A tenant whose metadata is already gone but whose id is
			// tombstoned is a teardown interrupted on the far side of the
			// fence. Resume it from step 4 rather than reporting not-found,
			// so a teardown that failed once can be completed by re-issuing
			// the same call. Steps 1 to 3 are already done, and steps 4 to 6
			// are exactly the steps whose failure leaves this state.
			if _, tombstoned, tErr := s.deletedAt(ctx, tenantID); tErr == nil && tombstoned {
				return s.finishTeardown(ctx, tenantID)
			}
		}
		return err
	}

	// 1. Bottom-up refusal.
	if err := s.refuseIfDatabasesRemain(ctx, tenantID); err != nil {
		return err
	}

	// 2 and 3. The fence. Nothing here destroys anything, so a failure
	//          leaves the tenant intact and the error says only that.
	if err := s.fence(ctx, tenantID, info); err != nil {
		return err
	}

	s.logger.Info("tenant retired",
		slog.String("tenant_id", tenantID),
		slog.String("isolation_type", string(info.EffectiveIsolationType())))

	// 4, 5 and 6.
	return s.finishTeardown(ctx, tenantID)
}

// fence runs DeleteTenant's steps 2 and 3: retire the id, then remove the
// metadata record that makes it resolvable.
//
// It is the whole of the reversible half of a deletion. Neither write
// destroys anything a later call could not reproduce, so a failure returns
// with the tenant exactly as it was -- which is what lets the errors it
// returns say "nothing changed" truthfully.
//
// The tombstone is written unconditionally rather than only when absent. A
// re-issued delete that reaches here refreshes DeletedAt, which is the
// honest reading -- the retirement that stuck is the one being completed --
// and it keeps this a plain write rather than a read-modify-write over a
// store several nodes share.
// The id is taken as its own argument rather than read off info, because the
// key the fence writes must be the one the caller asked about even when the
// record it decoded disagrees.
func (s *TenantService) fence(ctx context.Context, tenantID string, info *TenantInfo) error {
	tombstone, err := json.Marshal(newTombstone(info, time.Now().UTC()))
	if err != nil {
		return &StoreError{Op: "marshal-tombstone", Err: err}
	}
	if err := s.store.Put(ctx, tombstoneKey(tenantID), tombstone); err != nil {
		return &StoreError{Op: "put-tombstone", Err: err}
	}
	if err := s.store.Delete(ctx, metadataPrefix+tenantID); err != nil {
		return &StoreError{Op: "delete", Err: err}
	}
	return nil
}

// finishTeardown runs DeleteTenant's last three steps: shred the DEK,
// destroy the barrier and every durable record that would restore it, then
// reclaim the keyspace.
//
// They are one function because they are what a re-issued delete resumes.
// All three run after the fence has committed, so by the time any of them
// can fail the id is already retired and the tenant no longer resolves; all
// three are idempotent, so running them again over a partly-finished
// teardown completes it. Every failure is wrapped in *TeardownError, which
// is what tells the operator the tenant is gone from the system's view even
// though this call failed.
func (s *TenantService) finishTeardown(ctx context.Context, tenantID string) error {
	// 4. Crypto-shred. First irreversible step, first step past the fence.
	if s.dekShredder != nil {
		if err := s.dekShredder.DeleteTenantDEK(ctx, tenantID); err != nil {
			return &TeardownError{TenantID: tenantID, Err: &DEKShredError{TenantID: tenantID, Err: err}}
		}
	}

	// 5. Not-found is success: the registry may never have held this tenant
	//    (registration failed, or this is a node that never attached it), and
	//    the delete must stay re-issuable.
	if err := s.registry.DestroyTenant(ctx, tenantID); err != nil {
		if !errors.Is(err, seal.ErrTenantNotFound) {
			return &TeardownError{TenantID: tenantID, Err: &BarrierError{TenantID: tenantID, Op: "destroy", Err: err}}
		}
	}

	// Clean up any active tenant ceremony service (destroys pending share
	// enclaves and releases challenge state).
	s.tenantCeremonies.Delete(tenantID)

	// 6. Reclaim the keyspace.
	if err := s.reclaim(ctx, tenantID); err != nil {
		return &TeardownError{TenantID: tenantID, Err: err}
	}

	s.logger.Info("tenant deleted", slog.String("tenant_id", tenantID))
	return nil
}

// refuseIfDatabasesRemain returns *TenantHasDatabasesError when the tenant
// still owns databases.
//
// With no DatabaseLister wired the check cannot be evaluated and the deletion
// proceeds; see WithDatabaseLister for why that is not a fail-open. An
// enumeration that FAILS is a different matter and aborts the deletion:
// "I could not find out" is not the same answer as "none".
func (s *TenantService) refuseIfDatabasesRemain(ctx context.Context, tenantID string) error {
	if s.databases == nil {
		return nil
	}
	names, err := s.databases.TenantDatabases(ctx, tenantID)
	if err != nil {
		return &DatabaseListError{TenantID: tenantID, Err: err}
	}
	if len(names) == 0 {
		return nil
	}
	return &TenantHasDatabasesError{TenantID: tenantID, Databases: names}
}

// reclaim runs the configured KeyspaceReclaimer over the tenant, wrapping a
// failure so the caller can tell a reclamation failure (id already retired,
// re-issue to resume) from a refusal (nothing changed).
func (s *TenantService) reclaim(ctx context.Context, tenantID string) error {
	if s.reclaimer == nil {
		return nil
	}
	if err := s.reclaimer.ReclaimTenantKeyspace(ctx, tenantID); err != nil {
		return &KeyspaceReclaimError{TenantID: tenantID, Err: err}
	}
	return nil
}

// UpdateQuotas updates the resource quotas for a tenant and persists
// the change to the metadata store. The tenant must exist.
func (s *TenantService) UpdateQuotas(ctx context.Context, tenantID string, quotas *quota.Quotas) (*TenantInfo, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if quotas == nil {
		return nil, ErrNilQuotas
	}

	info, err := s.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	info.Quotas = quotas
	info.UpdatedAt = time.Now().UTC()

	data, err := json.Marshal(info)
	if err != nil {
		return nil, &StoreError{Op: "marshal", Err: err}
	}

	key := metadataPrefix + tenantID
	if err := s.store.Put(ctx, key, data); err != nil {
		return nil, &StoreError{Op: "put", Err: err}
	}

	s.logger.Info("tenant quotas updated", slog.String("tenant_id", tenantID))

	return info, nil
}

// UpdateEntitlements updates the feature entitlements for a tenant and persists
// the change to the metadata store. The tenant must exist; a nil entitlements
// pointer returns ErrNilEntitlements.
func (s *TenantService) UpdateEntitlements(ctx context.Context, tenantID string, entitlements *quota.Entitlements) (*TenantInfo, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if entitlements == nil {
		return nil, ErrNilEntitlements
	}

	info, err := s.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	info.Entitlements = entitlements
	info.UpdatedAt = time.Now().UTC()

	data, err := json.Marshal(info)
	if err != nil {
		return nil, &StoreError{Op: "marshal", Err: err}
	}

	key := metadataPrefix + tenantID
	if err := s.store.Put(ctx, key, data); err != nil {
		return nil, &StoreError{Op: "put", Err: err}
	}

	s.logger.Info("tenant entitlements updated", slog.String("tenant_id", tenantID))

	return info, nil
}

// Quotas returns the resource limits configured for the given tenant.
// When the tenant has no quotas configured, the operator-configured default
// quotas are returned, or a zero-value Quotas (all limits at system default)
// when no defaults are configured. Implements quota.QuotaProvider.
func (s *TenantService) Quotas(ctx context.Context, tenantID string) (*quota.Quotas, error) {
	info, err := s.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if info.Quotas != nil {
		return info.Quotas, nil
	}
	if s.defaultQuotas != nil {
		return s.defaultQuotas, nil
	}
	return &quota.Quotas{}, nil
}

// effectiveEntitlements returns the entitlements in force for the tenant: its
// own Entitlements when set, otherwise the operator-configured defaults, and
// finally the conservative built-in defaults (prefix-only) when neither is set.
func (s *TenantService) effectiveEntitlements(ctx context.Context, tenantID string) (*quota.Entitlements, error) {
	info, err := s.GetTenant(ctx, tenantID)
	if err != nil {
		// A not-yet-provisioned tenant has the configured defaults. This lets
		// CreateTenant gate the requested isolation mode against the operator's
		// default entitlements before the metadata record exists, instead of
		// surfacing the lookup miss as an error.
		if errors.Is(err, ErrTenantNotFound) {
			if s.defaultEnts != nil {
				return s.defaultEnts, nil
			}
			return quota.DefaultEntitlements(), nil
		}
		return nil, err
	}
	if info.Entitlements != nil {
		return info.Entitlements, nil
	}
	if s.defaultEnts != nil {
		return s.defaultEnts, nil
	}
	return quota.DefaultEntitlements(), nil
}

// CheckIsolation returns nil if the tenant is entitled to the given isolation
// mode, or a *quota.EntitlementError otherwise. The system tenant is implicitly
// entitled to every mode. Implements quota.EntitlementChecker.
func (s *TenantService) CheckIsolation(ctx context.Context, tenantID, mode string) error {
	if IsSystemTenant(tenantID) {
		return nil
	}
	ents, err := s.effectiveEntitlements(ctx, tenantID)
	if err != nil {
		return err
	}
	if ents.AllowsIsolation(mode) {
		return nil
	}
	return &quota.EntitlementError{TenantID: tenantID, Mode: mode}
}

// CheckBarrierMode returns nil if the tenant may use the given barrier sealing
// strategy for its isolated barrier, or a *quota.EntitlementError otherwise. The
// system tenant is implicitly entitled to every mode. The shared system-barrier
// option (seal.StrategySystemBarrier) is gated by the tenant's SystemBarrierQuota
// (0 ⇒ unavailable); every other (dedicated) mode is gated by AllowedBarrierModes.
// Implements the barrier-mode half of the tenant governance plane.
func (s *TenantService) CheckBarrierMode(ctx context.Context, tenantID string, mode seal.StrategyID) error {
	if IsSystemTenant(tenantID) {
		return nil
	}
	if mode == seal.StrategySystemBarrier {
		quotas, err := s.Quotas(ctx, tenantID)
		if err != nil {
			return err
		}
		if quotas.AllowsSystemBarrier() {
			return nil
		}
		return &quota.EntitlementError{TenantID: tenantID, Feature: "barrier_mode:" + string(mode)}
	}
	ents, err := s.effectiveEntitlements(ctx, tenantID)
	if err != nil {
		return err
	}
	if ents.AllowsBarrierMode(string(mode)) {
		return nil
	}
	return &quota.EntitlementError{TenantID: tenantID, Feature: "barrier_mode:" + string(mode)}
}

// CheckFeature returns nil if the tenant is entitled to the named feature, or a
// *quota.EntitlementError otherwise. The system tenant is implicitly entitled
// to every feature. Implements quota.EntitlementChecker.
func (s *TenantService) CheckFeature(ctx context.Context, tenantID, feature string) error {
	if IsSystemTenant(tenantID) {
		return nil
	}
	ents, err := s.effectiveEntitlements(ctx, tenantID)
	if err != nil {
		return err
	}
	if ents.AllowsFeature(feature) {
		return nil
	}
	return &quota.EntitlementError{TenantID: tenantID, Feature: feature}
}

// IncrementQuotaUsage increments the current-usage counter for the given
// resource by delta and persists the change. Returns QuotaExceededError
// when the resulting usage would exceed the configured limit. delta must
// be positive; a zero or negative delta returns an error.
func (s *TenantService) IncrementQuotaUsage(ctx context.Context, tenantID string, resource quota.Resource, delta int64) (*TenantInfo, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if delta <= 0 {
		return nil, &StoreError{Op: "increment-quota", Err: errors.New("delta must be positive")}
	}
	info, err := s.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if info.Quotas == nil {
		info.Quotas = &quota.Quotas{}
	}
	current, err := info.Quotas.CurrentUsage(resource)
	if err != nil {
		return nil, &StoreError{Op: "increment-quota", Err: err}
	}
	limit, err := info.Quotas.Limit(resource)
	if err != nil {
		return nil, &StoreError{Op: "increment-quota", Err: err}
	}
	if limit > 0 && current+delta > limit {
		return nil, &QuotaExceededError{
			TenantID: tenantID,
			Resource: resource.String(),
			Current:  current,
			Delta:    delta,
			Limit:    limit,
		}
	}
	if err := info.Quotas.SetCurrentUsage(resource, current+delta); err != nil {
		return nil, &StoreError{Op: "increment-quota", Err: err}
	}
	info.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(info)
	if err != nil {
		return nil, &StoreError{Op: "marshal", Err: err}
	}
	key := metadataPrefix + tenantID
	if err := s.store.Put(ctx, key, data); err != nil {
		return nil, &StoreError{Op: "put", Err: err}
	}
	s.logger.Debug("quota usage incremented",
		slog.String("tenant_id", tenantID),
		slog.String("resource", resource.String()),
		slog.Int64("delta", delta),
		slog.Int64("current", current+delta))
	return info, nil
}

// DecrementQuotaUsage decrements the current-usage counter for the given
// resource by delta. The counter is clamped to zero. delta must be
// positive; a zero or negative delta returns an error.
func (s *TenantService) DecrementQuotaUsage(ctx context.Context, tenantID string, resource quota.Resource, delta int64) (*TenantInfo, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if delta <= 0 {
		return nil, &StoreError{Op: "decrement-quota", Err: errors.New("delta must be positive")}
	}
	info, err := s.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if info.Quotas == nil {
		info.Quotas = &quota.Quotas{}
	}
	current, err := info.Quotas.CurrentUsage(resource)
	if err != nil {
		return nil, &StoreError{Op: "decrement-quota", Err: err}
	}
	next := current - delta
	if next < 0 {
		next = 0
	}
	if err := info.Quotas.SetCurrentUsage(resource, next); err != nil {
		return nil, &StoreError{Op: "decrement-quota", Err: err}
	}
	info.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(info)
	if err != nil {
		return nil, &StoreError{Op: "marshal", Err: err}
	}
	key := metadataPrefix + tenantID
	if err := s.store.Put(ctx, key, data); err != nil {
		return nil, &StoreError{Op: "put", Err: err}
	}
	s.logger.Debug("quota usage decremented",
		slog.String("tenant_id", tenantID),
		slog.String("resource", resource.String()),
		slog.Int64("delta", delta),
		slog.Int64("current", next))
	return info, nil
}

// SetQuotaUsage overwrites the current-usage counter for the given resource to
// the supplied absolute value and persists the change. Unlike
// IncrementQuotaUsage it does NOT enforce the configured limit, so the true
// usage can be recorded even when it exceeds the limit (subsequent writes are
// then blocked by enforcement). It is used by the storage-quota reconciler to
// correct drift from the in-memory accumulator. Negative values are clamped to
// zero.
func (s *TenantService) SetQuotaUsage(ctx context.Context, tenantID string, resource quota.Resource, value int64) (*TenantInfo, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if value < 0 {
		value = 0
	}
	info, err := s.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if info.Quotas == nil {
		info.Quotas = &quota.Quotas{}
	}
	if err := info.Quotas.SetCurrentUsage(resource, value); err != nil {
		return nil, &StoreError{Op: "set-quota-usage", Err: err}
	}
	info.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(info)
	if err != nil {
		return nil, &StoreError{Op: "marshal", Err: err}
	}
	key := metadataPrefix + tenantID
	if err := s.store.Put(ctx, key, data); err != nil {
		return nil, &StoreError{Op: "put", Err: err}
	}
	s.logger.Debug("quota usage set",
		slog.String("tenant_id", tenantID),
		slog.String("resource", resource.String()),
		slog.Int64("value", value))
	return info, nil
}

// GetQuotaUsage returns the current-usage value for the given resource
// and tenant without modifying any state.
func (s *TenantService) GetQuotaUsage(ctx context.Context, tenantID string, resource quota.Resource) (int64, error) {
	if tenantID == "" {
		return 0, ErrEmptyTenantID
	}
	info, err := s.GetTenant(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	if info.Quotas == nil {
		return 0, nil
	}
	usage, err := info.Quotas.CurrentUsage(resource)
	if err != nil {
		return 0, &StoreError{Op: "get-quota-usage", Err: err}
	}
	return usage, nil
}

// enforceTenantScope verifies the caller's authenticated tenant binding
// permits a barrier operation against targetTenantID. A caller bound to the
// system tenant passes: cross-tenant administrative flows, daemon-internal
// maintenance, and embedded in-process hosts carry no tenant binding and
// resolve to the system tenant via FromContext. A caller bound to any other
// tenant may only target its own tenant.
//
// This is the service-layer tenant-isolation defense. Transport-level RBAC
// (AuthorizeTenant + the ScopeTenantOnly rule on tenant_barrier:*
// permissions) is the first gate, but in-process surfaces and any transport
// that fails to bind the true target tenant must still fail closed here.
func enforceTenantScope(ctx context.Context, targetTenantID, op string) error {
	caller := FromContext(ctx)
	if IsSystemTenant(caller) || caller == targetTenantID {
		return nil
	}
	return &TenantScopeError{
		CallerTenantID: caller,
		TargetTenantID: targetTenantID,
		Op:             op,
	}
}

// TenantBarrierInit initializes the per-tenant barrier using the default
// passphrase (software) seal strategy. It delegates to
// TenantBarrierInitWithStrategy with seal.StrategyPassphrase for
// backward compatibility with all existing callers.
//
// Authorization — who may call this — is enforced at the API/transport
// layer; tenant-scope containment is enforced here.
func (s *TenantService) TenantBarrierInit(ctx context.Context, tenantID string, passphrase []byte, threshold, shares int) (*TenantBarrierInitResult, error) {
	return s.TenantBarrierInitWithStrategy(ctx, tenantID, passphrase, threshold, shares, seal.StrategyPassphrase)
}

// TenantBarrierInitWithStrategy initializes the per-tenant barrier using the
// explicitly requested seal strategy. When strategyID is empty it defaults to
// seal.StrategyPassphrase. The strategy is resolved from the injected
// StrategyRegistry when one is present; otherwise a built-in default factory
// is used for the software-only strategies (passphrase, none).
//
// The passphrase field is required for the passphrase strategy; for all other
// strategies (none, hardware/cloud KMS) it is ignored. It is BORROWED for the
// duration of the call — copied into protected memory and left as it was for
// the caller to wipe. The tenant must exist.
//
// Authorization — who may call this — is enforced at the API/transport
// layer, not here.
func (s *TenantService) TenantBarrierInitWithStrategy(
	ctx context.Context,
	tenantID string,
	passphrase []byte,
	threshold, shares int,
	strategyID seal.StrategyID,
) (*TenantBarrierInitResult, error) {
	if strategyID == "" {
		strategyID = seal.StrategyPassphrase
	}
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if err := enforceTenantScope(ctx, tenantID, "initialize"); err != nil {
		return nil, err
	}

	// Governance: the tenant must be entitled to the requested barrier mode.
	// Dedicated modes (none/passphrase/shamir/hardware) are gated by
	// AllowedBarrierModes; the shared system-barrier option by SystemBarrierQuota.
	if err := s.CheckBarrierMode(ctx, tenantID, strategyID); err != nil {
		return nil, err
	}

	// Shamir takes a dedicated path: it cannot be resolved through the
	// zero-arg strategy registry because it must carry the requested
	// threshold/total, and its initialization RETURNS the generated shares.
	if strategyID == seal.StrategyShamir {
		return s.tenantBarrierInitShamir(ctx, tenantID, threshold, shares)
	}

	// The passphrase strategy requires caller-supplied key material.
	// Hardware/cloud strategies wrap the root key via their own KEK;
	// no passphrase is needed.
	if seal.IsPassphraseStrategy(strategyID) && len(passphrase) == 0 {
		return nil, ErrEmptyPassphrase
	}

	// Verify the tenant exists in metadata.
	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return nil, err
	}

	// Resolve the sealing strategy from the injected registry when one is
	// present, or fall back to the built-in software factories for the
	// strategies qrdb owns directly (passphrase, none). Hardware/cloud
	// strategies must be registered via WithStrategyRegistry.
	strategy, err := s.resolveStrategy(strategyID)
	if err != nil {
		return nil, err
	}

	// Build credentials. For the passphrase strategy the Secret carries
	// the tenant admin's passphrase, copied into protected memory (the
	// caller's slice is borrowed, not consumed) and destroyed once the
	// initialization has used it. For all other strategies the Credentials
	// struct is empty (the strategy's KEK handles wrapping).
	creds := seal.Credentials{}
	if seal.IsPassphraseStrategy(strategyID) {
		var err error
		if creds, err = seal.CredentialsFor(passphrase); err != nil {
			return nil, &BarrierError{TenantID: tenantID, Op: "credentials", Err: err}
		}
		defer creds.Secret.Destroy()
	}

	// Register a sealed shell when the tenant barrier is not yet
	// registered. An already-registered tenant is reused. (We inline
	// register-shell+init against the registry rather than importing
	// pkg/bootstrap, which depends on pkg/tenant and would form an import
	// cycle.)
	if _, err := s.registry.GetTenant(tenantID); err != nil {
		// Register a durable, barrier-isolated tenant that honors the
		// caller-supplied strategy. RegisterTenantPooledWithStrategy uses
		// the same Pebble volume-pool backend as the inherit-the-system
		// default (RegisterTenantPooled) so the per-tenant barrier survives
		// a restart; the only difference is that the tenant's DEK is sealed
		// with the resolved per-tenant strategy instead of the system's.
		if _, regErr := s.registry.RegisterTenantPooledWithStrategy(ctx, tenantID, strategy); regErr != nil {
			return nil, &BarrierError{TenantID: tenantID, Op: "register", Err: regErr}
		}
		s.logger.Info("tenant barrier registered",
			slog.String("tenant_id", tenantID),
			slog.String("strategy", string(strategyID)))
	}

	// Honor an EXPLICIT strategy choice even on a node where the tenant's
	// barrier shell was pre-registered at CreateTenant with the node's
	// default strategy set: initialization then runs against exactly the
	// requested strategy (StorageBarrier.InitializeWithStrategy) instead of
	// silently falling back to whatever the shell carries — the silent
	// fallback made `barrier-init --strategy none` fail with "empty
	// credentials for deferred-passphrase strategy" on the tenant's create
	// node while succeeding on every other node. An empty strategyID keeps
	// the legacy best-strategy selection unchanged.
	var initErr error
	if strategyID != "" {
		initErr = s.registry.InitializeTenantWithStrategy(ctx, tenantID, strategy, creds)
	} else {
		initErr = s.registry.InitializeTenant(ctx, tenantID, creds)
	}
	if initErr != nil {
		return nil, &BarrierError{TenantID: tenantID, Op: "initialize", Err: initErr}
	}

	s.logger.Info("tenant barrier initialized",
		slog.String("tenant_id", tenantID),
		slog.String("strategy", string(strategyID)))

	return &TenantBarrierInitResult{
		TenantID: tenantID,
		Strategy: string(strategyID),
	}, nil
}

// tenantBarrierInitShamir registers (when needed) and initializes a per-tenant
// barrier with an M-of-N Shamir scheme, returning the generated shares. The
// shares are returned to the caller exactly once and are never persisted;
// subsequent unsealing requires resubmitting a quorum via TenantBarrierUnsealShare.
//
// threshold/total are validated by seal.NewShamirStrategy (threshold >= 2 and
// <= total) inside RegisterTenantPooledShamir, so invalid parameters surface as
// a register-phase BarrierError. A QuorumTTL of zero lets the barrier fall back
// to the default unseal-share TTL.
func (s *TenantService) tenantBarrierInitShamir(
	ctx context.Context,
	tenantID string,
	threshold, total int,
) (*TenantBarrierInitResult, error) {
	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return nil, err
	}

	if _, err := s.registry.GetTenant(tenantID); err != nil {
		if _, regErr := s.registry.RegisterTenantPooledShamir(ctx, tenantID, threshold, total, 0); regErr != nil {
			return nil, &BarrierError{TenantID: tenantID, Op: "register", Err: regErr}
		}
		s.logger.Info("tenant barrier registered",
			slog.String("tenant_id", tenantID),
			slog.String("strategy", string(seal.StrategyShamir)),
			slog.Int("threshold", threshold),
			slog.Int("total", total))
	}

	// Wire the ceremony distributor onto the tenant barrier before
	// initialization so shares are sealed to enrolled Security Officers.
	// Precedence: explicit injected distributor first; otherwise lazily
	// resolve/create the tenant's own TenantCeremonyService. If neither is
	// available the StorageBarrier returns ErrCeremonyDistributorNotConfigured.
	tb, err := s.registry.GetTenant(tenantID)
	if err != nil {
		return nil, &BarrierError{TenantID: tenantID, Op: "get-tenant", Err: err}
	}
	if s.distributor != nil {
		tb.Barrier().SetCeremonyDistributor(s.distributor)
	} else if s.ceremonyCA != nil {
		tsvc, tcErr := s.tenantCeremony(tenantID)
		if tcErr != nil {
			return nil, &BarrierError{TenantID: tenantID, Op: "ceremony-service", Err: tcErr}
		}
		tb.Barrier().SetCeremonyDistributor(tsvc)
	}
	// If neither distributor nor ceremonyCA is configured the barrier's
	// distributor is nil; InitializeShamir returns
	// ErrCeremonyDistributorNotConfigured automatically.

	res, err := s.registry.InitializeTenantShamir(ctx, tenantID, seal.Credentials{})
	if err != nil {
		return nil, &BarrierError{TenantID: tenantID, Op: "initialize", Err: err}
	}

	s.logger.Info("tenant shamir barrier initialized",
		slog.String("tenant_id", tenantID),
		slog.Int("threshold", res.Threshold),
		slog.Int("total", res.TotalShares))

	return &TenantBarrierInitResult{
		TenantID:    tenantID,
		Strategy:    string(seal.StrategyShamir),
		Threshold:   res.Threshold,
		TotalShares: res.TotalShares,
		ShareCount:  res.ShareCount,
	}, nil
}

// resolveStrategy returns the SealingStrategy for the given ID.
//
// Resolution order:
//  1. When a StrategyRegistry is injected, always consult it first. This
//     allows callers to override even the built-in software strategies.
//  2. When no registry is present, fall back to the built-in factories for
//     the software-only strategies qrdb owns (passphrase, none).
//  3. All other strategy IDs require an injected registry; their absence
//     returns InvalidStrategyError.
func (s *TenantService) resolveStrategy(id seal.StrategyID) (seal.SealingStrategy, error) {
	if s.strategyRegistry != nil {
		factory, err := s.strategyRegistry.LookupFactory(id)
		if err != nil {
			// Surface a user-friendly error with the list of registered names.
			available := s.strategyRegistry.ListAvailable()
			names := make([]string, len(available))
			for i, n := range available {
				names[i] = string(n)
			}
			return nil, &InvalidStrategyError{Strategy: string(id), Available: names}
		}
		return factory()
	}

	// No registry injected: accept only the built-in software strategies.
	switch id {
	case seal.StrategyPassphrase:
		return seal.NewDeferredSoftwareStrategy(), nil
	case seal.StrategyNone:
		return seal.NewNoneStrategy(), nil
	default:
		return nil, &InvalidStrategyError{Strategy: string(id)}
	}
}

// TenantBarrierUnseal unseals the per-tenant barrier using the
// caller-supplied key material. The tenant must exist and its barrier must
// have been previously initialized. There is no derived credential
// fallback: empty key material is rejected.
func (s *TenantService) TenantBarrierUnseal(ctx context.Context, tenantID string, key []byte) error {
	if tenantID == "" {
		return ErrEmptyTenantID
	}
	if err := enforceTenantScope(ctx, tenantID, "unseal"); err != nil {
		return err
	}

	// Require caller-supplied key material. There is no derived fallback.
	if len(key) == 0 {
		return ErrEmptyPassphrase
	}

	// Verify the tenant exists in metadata.
	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return err
	}

	// key is BORROWED: it is copied into protected memory and left for the
	// caller to wipe, and the credential is destroyed once the unseal has
	// used it.
	creds, err := seal.CredentialsFor(key)
	if err != nil {
		return &BarrierError{TenantID: tenantID, Op: "credentials", Err: err}
	}
	defer creds.Secret.Destroy()

	if err := s.registry.UnsealTenant(ctx, tenantID, creds); err != nil {
		return &BarrierError{TenantID: tenantID, Op: "unseal", Err: err}
	}

	s.logger.Info("tenant barrier unsealed", slog.String("tenant_id", tenantID))

	return nil
}

// TenantBarrierUnsealShare submits a single Shamir share toward the per-tenant
// barrier's quorum threshold. Once enough distinct shares have been submitted
// the barrier is automatically unsealed and the quorum accumulator is reset.
// The tenant must exist and its barrier must have been initialized with the
// Shamir strategy; an empty share is rejected.
func (s *TenantService) TenantBarrierUnsealShare(ctx context.Context, tenantID, share string) (*TenantBarrierUnsealProgress, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if share == "" {
		return nil, ErrEmptyShare
	}
	if err := enforceTenantScope(ctx, tenantID, "unseal-share"); err != nil {
		return nil, err
	}

	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return nil, err
	}

	prog, err := s.registry.UnsealTenantWithShare(ctx, tenantID, share)
	if err != nil {
		return nil, &BarrierError{TenantID: tenantID, Op: "unseal-share", Err: err}
	}

	if prog.Complete {
		s.logger.Info("tenant barrier unsealed via shamir quorum",
			slog.String("tenant_id", tenantID))
	}

	return &TenantBarrierUnsealProgress{
		TenantID:  tenantID,
		Required:  prog.Required,
		Submitted: prog.Submitted,
		Complete:  prog.Complete,
		Sealed:    !prog.Complete,
	}, nil
}

// TenantBarrierSeal seals the per-tenant barrier, refusing further reads
// and writes against the tenant namespace until unsealed again. The tenant
// must exist and its barrier must have been previously initialized.
//
// For Shamir-protected barriers this method returns
// seal.ErrShamirBarrierRequiresQuorum wrapped in a BarrierError; callers must
// use TenantBarrierShamirSealVote to collect the required quorum instead.
func (s *TenantService) TenantBarrierSeal(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		return ErrEmptyTenantID
	}
	if err := enforceTenantScope(ctx, tenantID, "seal"); err != nil {
		return err
	}

	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return err
	}

	// Gate Shamir barriers: sealing requires M-of-N quorum via seal votes.
	if tb, tbErr := s.registry.GetTenant(tenantID); tbErr == nil {
		if tb.Barrier().ActiveStrategy() == seal.StrategyShamir {
			return &BarrierError{
				TenantID: tenantID,
				Op:       "seal",
				Err:      seal.ErrShamirBarrierRequiresQuorum,
			}
		}
	}

	if err := s.registry.SealTenant(tenantID); err != nil {
		return &BarrierError{TenantID: tenantID, Op: "seal", Err: err}
	}

	s.logger.Info("tenant barrier sealed", slog.String("tenant_id", tenantID))

	return nil
}

// TenantBarrierStatus reports the current sealed/initialized state and
// active strategy metadata of a per-tenant barrier. The tenant must exist;
// when the barrier has never been registered the response carries Sealed=
// true, Initialized=false, and an empty Strategy.
func (s *TenantService) TenantBarrierStatus(ctx context.Context, tenantID string) (*TenantBarrierStatus, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if err := enforceTenantScope(ctx, tenantID, "status"); err != nil {
		return nil, err
	}

	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return nil, err
	}

	st, err := s.registry.TenantStatus(tenantID)
	if err != nil {
		// Not registered yet — report a sealed, uninitialized barrier
		// rather than treating absence as an error. Callers can decide
		// whether to call TenantBarrierInit based on the Initialized flag.
		return &TenantBarrierStatus{
			TenantID:    tenantID,
			Sealed:      true,
			Initialized: false,
		}, nil
	}

	// A registered barrier may be initialized (auto-unseal/system-managed, or a
	// self-managed barrier the tenant admin has initialized) or registered but
	// not yet initialized (a fresh self-managed barrier). InitializedAt is the
	// authoritative signal: it is persisted at Initialize time and recovered on
	// a warm boot, so it stays non-zero even while the barrier is sealed.
	return &TenantBarrierStatus{
		TenantID:        tenantID,
		Sealed:          st.Sealed,
		Initialized:     !st.InitializedAt.IsZero(),
		Strategy:        string(st.Strategy),
		HardwareBacked:  st.HardwareBacked,
		ShamirThreshold: st.ShamirThreshold,
		ShamirTotal:     st.ShamirTotal,
	}, nil
}

// TenantIDFromKey extracts the tenant ID from a metadata storage key.
// Returns an empty string if the key does not start with the metadata
// prefix.
func TenantIDFromKey(key string) string {
	if !strings.HasPrefix(key, metadataPrefix) {
		return ""
	}
	return key[len(metadataPrefix):]
}

// tenantCeremony returns the TenantCeremonyService for the given tenant,
// constructing and registering it on first access. The backend priority is:
//
//  1. The tenant barrier's own StorageBarrier (when available and unsealed),
//     so that officer records are stored encrypted under the barrier.
//  2. The service-level store (s.store), scoped by the ceremony prefix, for
//     the pre-init officer enrollment phase when the barrier doesn't exist yet.
//     Officer registration must work BEFORE TenantBarrierInitWithStrategy is
//     called (design §6.1, §10.1).
//
// ceremonyCA must be non-nil; callers should guard this before calling.
func (s *TenantService) tenantCeremony(tenantID string) (*ceremony.TenantCeremonyService, error) {
	if svc, ok := s.tenantCeremonies.Get(tenantID); ok {
		return svc, nil
	}
	// Choose backend: prefer the tenant barrier's own storage when available
	// so officer records are barrier-encrypted at rest. Fall back to the
	// service-level store during the pre-init enrollment phase.
	var backend seal.StorageBackend
	if tb, err := s.registry.GetTenant(tenantID); err == nil {
		backend = tb.Barrier()
	} else {
		backend = s.store
	}
	svc, err := ceremony.NewTenantCeremonyService(tenantID, backend, s.ceremonyCA, s.logger)
	if err != nil {
		return nil, err
	}
	s.tenantCeremonies.Set(tenantID, svc)
	return svc, nil
}

// TenantBarrierOfficerRegister registers a Security Officer for the named
// tenant's Shamir barrier. The officer's CSR is validated, signed by the
// CeremonyCA, and the public enrollment record is persisted. No share bytes
// are produced at this step; shares are sealed during
// TenantBarrierInitWithStrategy(Shamir). Officers must be registered before
// init so that SealAndDistribute can assign one share per enrolled officer.
//
// Returns ErrEmptyTenantID for an empty tenantID, ErrTenantNotFound when the
// tenant does not exist, and BarrierError-wrapped errors from the ceremony layer.
func (s *TenantService) TenantBarrierOfficerRegister(
	ctx context.Context,
	tenantID string,
	req *ceremony.RegisterOfficerRequest,
) (*ceremony.RegisterOfficerResult, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if err := enforceTenantScope(ctx, tenantID, "officer-register"); err != nil {
		return nil, err
	}
	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return nil, err
	}
	if s.ceremonyCA == nil && s.distributor == nil {
		return nil, &BarrierError{
			TenantID: tenantID,
			Op:       "officer-register",
			Err:      seal.ErrCeremonyDistributorNotConfigured,
		}
	}
	// When using a raw distributor override (test path), no per-tenant ceremony
	// service is available for officer management. Require ceremonyCA.
	if s.ceremonyCA == nil {
		return nil, &BarrierError{
			TenantID: tenantID,
			Op:       "officer-register",
			Err:      seal.ErrCeremonyDistributorNotConfigured,
		}
	}
	tsvc, err := s.tenantCeremony(tenantID)
	if err != nil {
		return nil, &BarrierError{TenantID: tenantID, Op: "officer-register", Err: err}
	}
	result, err := tsvc.RegisterOfficer(ctx, *req)
	if err != nil {
		return nil, &BarrierError{TenantID: tenantID, Op: "officer-register", Err: err}
	}
	s.logger.Info("tenant barrier officer registered",
		slog.String("tenant_id", tenantID),
		slog.String("username", req.Username),
		slog.Int("share_index", result.ShareIndex))
	return result, nil
}

// TenantBarrierOfficerList returns all enrolled Security Officers for the
// named tenant's barrier. Returns ErrEmptyTenantID, ErrTenantNotFound, or
// a BarrierError when the ceremony service cannot be resolved.
func (s *TenantService) TenantBarrierOfficerList(
	ctx context.Context,
	tenantID string,
) ([]*ceremony.OfficerStatus, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if err := enforceTenantScope(ctx, tenantID, "officer-list"); err != nil {
		return nil, err
	}
	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return nil, err
	}
	if s.ceremonyCA == nil {
		return nil, &BarrierError{
			TenantID: tenantID,
			Op:       "officer-list",
			Err:      seal.ErrCeremonyDistributorNotConfigured,
		}
	}
	tsvc, err := s.tenantCeremony(tenantID)
	if err != nil {
		return nil, &BarrierError{TenantID: tenantID, Op: "officer-list", Err: err}
	}
	officers, err := tsvc.ListOfficers(ctx)
	if err != nil {
		return nil, &BarrierError{TenantID: tenantID, Op: "officer-list", Err: err}
	}
	return officers, nil
}

// TenantBarrierShareClaimBegin issues a challenge nonce to the named Security
// Officer for the tenant barrier share claim handshake. The officer must
// present a valid mTLS certificate whose thumbprint matches the registry.
func (s *TenantService) TenantBarrierShareClaimBegin(
	ctx context.Context,
	tenantID, username, callerCertThumbprint string,
) (*ceremony.ChallengeResponse, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if err := enforceTenantScope(ctx, tenantID, "share-claim-begin"); err != nil {
		return nil, err
	}
	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return nil, err
	}
	if s.ceremonyCA == nil {
		return nil, &BarrierError{
			TenantID: tenantID,
			Op:       "share-claim-begin",
			Err:      seal.ErrCeremonyDistributorNotConfigured,
		}
	}
	tsvc, err := s.tenantCeremony(tenantID)
	if err != nil {
		return nil, &BarrierError{TenantID: tenantID, Op: "share-claim-begin", Err: err}
	}
	challenge, err := tsvc.BeginClaimShare(ctx, username, callerCertThumbprint)
	if err != nil {
		return nil, &BarrierError{TenantID: tenantID, Op: "share-claim-begin", Err: err}
	}
	return challenge, nil
}

// TenantBarrierShareClaimComplete verifies the officer's nonce signature and
// returns the share bytes exactly once via the officer's own mTLS session.
// The share bytes in ShareClaimResult.ShareBytes must be wiped by the caller
// after use. Returns ErrShareAlreadyClaimed on a second attempt.
func (s *TenantService) TenantBarrierShareClaimComplete(
	ctx context.Context,
	tenantID, username, nonceHex string,
	signature []byte,
	callerCertThumbprint string,
) (*ceremony.ShareClaimResult, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if err := enforceTenantScope(ctx, tenantID, "share-claim-complete"); err != nil {
		return nil, err
	}
	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return nil, err
	}
	if s.ceremonyCA == nil {
		return nil, &BarrierError{
			TenantID: tenantID,
			Op:       "share-claim-complete",
			Err:      seal.ErrCeremonyDistributorNotConfigured,
		}
	}
	tsvc, err := s.tenantCeremony(tenantID)
	if err != nil {
		return nil, &BarrierError{TenantID: tenantID, Op: "share-claim-complete", Err: err}
	}
	shareBytes, err := tsvc.CompleteClaimShare(ctx, username, nonceHex, signature, callerCertThumbprint)
	if err != nil {
		return nil, &BarrierError{TenantID: tenantID, Op: "share-claim-complete", Err: err}
	}
	// INV-5: do not log share bytes or nonce.
	s.logger.Info("tenant officer share claimed",
		slog.String("tenant_id", tenantID),
		slog.String("username", username))
	return &ceremony.ShareClaimResult{ShareBytes: shareBytes}, nil
}

// TenantBarrierOfficerClaimStatus returns a per-officer claim status snapshot
// for the named tenant's barrier. Never includes share bytes.
func (s *TenantService) TenantBarrierOfficerClaimStatus(
	ctx context.Context,
	tenantID string,
) ([]*ceremony.OfficerStatus, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if err := enforceTenantScope(ctx, tenantID, "officer-claim-status"); err != nil {
		return nil, err
	}
	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return nil, err
	}
	if s.ceremonyCA == nil {
		return nil, &BarrierError{
			TenantID: tenantID,
			Op:       "officer-claim-status",
			Err:      seal.ErrCeremonyDistributorNotConfigured,
		}
	}
	tsvc, err := s.tenantCeremony(tenantID)
	if err != nil {
		return nil, &BarrierError{TenantID: tenantID, Op: "officer-claim-status", Err: err}
	}
	statuses, err := tsvc.OfficerClaimStatus(ctx)
	if err != nil {
		return nil, &BarrierError{TenantID: tenantID, Op: "officer-claim-status", Err: err}
	}
	return statuses, nil
}

// TenantBarrierShamirSealVote submits a Shamir share as a seal vote for the
// named tenant's barrier. When the quorum threshold is met the barrier is
// sealed automatically and all vote enclaves are destroyed.
// Returns a BarrierError wrapping seal.ErrShamirNotConfigured when the tenant
// barrier is not in Shamir mode, or seal.ErrSealed when already sealed.
func (s *TenantService) TenantBarrierShamirSealVote(
	ctx context.Context,
	tenantID, share string,
) (*TenantBarrierUnsealProgress, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if err := enforceTenantScope(ctx, tenantID, "shamir-seal-vote"); err != nil {
		return nil, err
	}
	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return nil, err
	}
	tb, err := s.registry.GetTenant(tenantID)
	if err != nil {
		return nil, &BarrierError{TenantID: tenantID, Op: "shamir-seal-vote", Err: err}
	}
	prog, err := tb.Barrier().SubmitSealVote(ctx, share)
	if err != nil {
		return nil, &BarrierError{TenantID: tenantID, Op: "shamir-seal-vote", Err: err}
	}
	if prog.Complete {
		s.logger.Info("tenant barrier sealed via shamir seal-vote quorum",
			slog.String("tenant_id", tenantID))
	}
	return &TenantBarrierUnsealProgress{
		TenantID:  tenantID,
		Required:  prog.Required,
		Submitted: prog.Submitted,
		Complete:  prog.Complete,
		Sealed:    prog.Complete,
	}, nil
}
