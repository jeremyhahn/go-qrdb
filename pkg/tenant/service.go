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
	"strings"
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
}

// WithBarrierAutoUnseal selects the tenant's barrier key-management mode. Pass
// true to opt into system-managed auto-unseal (entitlement-gated); the default
// (false) is the secure self-managed mode where the tenant admin holds the
// barrier key. Only meaningful for IsolationBarrier / IsolationVolume tenants.
func WithBarrierAutoUnseal(autoUnseal bool) CreateTenantOption {
	return func(c *createTenantConfig) { c.barrierAutoUnseal = autoUnseal }
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
	TenantBarrierInit(ctx context.Context, tenantID, passphrase string, threshold, shares int) (*TenantBarrierInitResult, error)

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
	TenantBarrierInitWithStrategy(ctx context.Context, tenantID, passphrase string, threshold, shares int, strategyID seal.StrategyID) (*TenantBarrierInitResult, error)

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
	logger           *slog.Logger
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
		root, _, regErr := s.registry.RegisterVolumeTenant(ctx, id, "", cfg.barrierAutoUnseal)
		if regErr != nil {
			if errors.Is(regErr, seal.ErrTenantAlreadyExists) {
				return nil, ErrTenantAlreadyExists
			}
			return nil, &BarrierError{TenantID: id, Op: "register-volume", Err: regErr}
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
		// IsolationVolume.
		root, _, regErr := s.registry.RegisterVolumeTenant(ctx, id, "", cfg.barrierAutoUnseal)
		if regErr != nil {
			if errors.Is(regErr, seal.ErrTenantAlreadyExists) {
				return nil, ErrTenantAlreadyExists
			}
			return nil, &BarrierError{TenantID: id, Op: "register-raft", Err: regErr}
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

// DeleteTenant removes a tenant from both the barrier registry and the
// metadata store. The tenant must exist in the metadata store.
func (s *TenantService) DeleteTenant(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		return ErrEmptyTenantID
	}

	// Verify the tenant exists in metadata.
	key := metadataPrefix + tenantID
	_, err := s.store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, seal.ErrSecretNotFound) {
			return ErrTenantNotFound
		}
		return &StoreError{Op: "get", Err: err}
	}

	// Unregister from barrier registry. Ignore not-found errors since
	// the registry may not have this tenant if initialization failed
	// previously.
	if err := s.registry.UnregisterTenant(tenantID); err != nil {
		if !errors.Is(err, seal.ErrTenantNotFound) {
			return &BarrierError{TenantID: tenantID, Op: "unregister", Err: err}
		}
	}

	// Delete metadata from storage.
	if err := s.store.Delete(ctx, key); err != nil {
		return &StoreError{Op: "delete", Err: err}
	}

	// Clean up any active tenant ceremony service (destroys pending share
	// enclaves and releases challenge state).
	s.tenantCeremonies.Delete(tenantID)

	s.logger.Info("tenant deleted", slog.String("tenant_id", tenantID))

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
	if tenantID == SystemTenantID {
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
	if tenantID == SystemTenantID {
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
	if tenantID == SystemTenantID {
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
	if caller == SystemTenantID || caller == targetTenantID {
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
func (s *TenantService) TenantBarrierInit(ctx context.Context, tenantID, passphrase string, threshold, shares int) (*TenantBarrierInitResult, error) {
	return s.TenantBarrierInitWithStrategy(ctx, tenantID, passphrase, threshold, shares, seal.StrategyPassphrase)
}

// TenantBarrierInitWithStrategy initializes the per-tenant barrier using the
// explicitly requested seal strategy. When strategyID is empty it defaults to
// seal.StrategyPassphrase. The strategy is resolved from the injected
// StrategyRegistry when one is present; otherwise a built-in default factory
// is used for the software-only strategies (passphrase, none).
//
// The passphrase field is required for the passphrase strategy; for all other
// strategies (none, hardware/cloud KMS) it is ignored. The tenant must exist.
//
// Authorization — who may call this — is enforced at the API/transport
// layer, not here.
func (s *TenantService) TenantBarrierInitWithStrategy(
	ctx context.Context,
	tenantID, passphrase string,
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
	if seal.IsPassphraseStrategy(strategyID) && passphrase == "" {
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
	// the tenant admin's passphrase; for all other strategies the
	// Credentials struct is empty (the strategy's KEK handles wrapping).
	creds := seal.Credentials{}
	if seal.IsPassphraseStrategy(strategyID) {
		creds.Secret = []byte(passphrase)
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

	if err := s.registry.InitializeTenant(ctx, tenantID, creds); err != nil {
		return nil, &BarrierError{TenantID: tenantID, Op: "initialize", Err: err}
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

	creds := seal.Credentials{Secret: key}

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
