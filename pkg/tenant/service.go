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
)

// TenantInfo holds tenant metadata persisted in the storage backend.
type TenantInfo struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	IsolationType IsolationType `json:"isolation_type,omitempty"`
	Quotas        *quota.Quotas `json:"quotas,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
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
	// isolation type. When isolationType is empty, IsolationBarrier is
	// used. Registers the tenant in the barrier registry and persists
	// metadata.
	CreateTenant(ctx context.Context, id, name string, isolationType IsolationType) (*TenantInfo, error)

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
	TenantBarrierInit(ctx context.Context, tenantID, passphrase string, threshold, shares int) error

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
	TenantBarrierInitWithStrategy(ctx context.Context, tenantID, passphrase string, threshold, shares int, strategyID seal.StrategyID) error

	// TenantBarrierUnseal unseals the per-tenant barrier using the
	// caller-supplied key material. There is no derived credential
	// fallback: empty key material is rejected.
	TenantBarrierUnseal(ctx context.Context, tenantID string, key []byte) error

	// TenantBarrierSeal seals the per-tenant barrier, returning it to the
	// sealed state. Subsequent reads and writes against the tenant's
	// namespace fail until the barrier is unsealed again.
	TenantBarrierSeal(ctx context.Context, tenantID string) error

	// TenantBarrierStatus returns the current state of the per-tenant
	// barrier (sealed/initialized flags, active strategy, hardware-backing).
	TenantBarrierStatus(ctx context.Context, tenantID string) (*TenantBarrierStatus, error)
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
}

// TenantService manages multi-tenant lifecycle including metadata CRUD
// and per-tenant barrier operations. It delegates barrier lifecycle to
// the BarrierRegistry and persists tenant metadata in a StorageBackend.
type TenantService struct {
	store            seal.StorageBackend
	registry         *seal.BarrierRegistry
	strategyRegistry seal.StrategyRegistry
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

// Compile-time interface checks.
var (
	_ TenantServicer      = (*TenantService)(nil)
	_ quota.QuotaProvider = (*TenantService)(nil)
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
		store:    store,
		registry: registry,
		logger:   logger,
	}
	for _, o := range opts {
		o(svc)
	}
	return svc, nil
}

// CreateTenant creates a new tenant with the given ID, name, and isolation
// type. When isolationType is empty, IsolationBarrier is used. It validates
// inputs, checks for duplicates, registers the tenant in the barrier registry
// with default configuration, and persists metadata in the storage backend.
func (s *TenantService) CreateTenant(ctx context.Context, id, name string, isolationType IsolationType) (*TenantInfo, error) {
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
	if id == SystemTenantID && isolationType == IsolationBarrier {
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
	if isolationType == IsolationBarrier {
		if _, regErr := s.registry.RegisterTenantPooled(ctx, id); regErr != nil {
			if errors.Is(regErr, seal.ErrTenantAlreadyExists) {
				return nil, ErrTenantAlreadyExists
			}
			return nil, &BarrierError{TenantID: id, Op: "register", Err: regErr}
		}
		if initErr := s.registry.InitializeTenant(ctx, id, seal.Credentials{}); initErr != nil {
			// Roll back the registration so a failed init does not leave a
			// dangling, uninitialized barrier behind.
			_ = s.registry.UnregisterTenant(id)
			return nil, &BarrierError{TenantID: id, Op: "initialize", Err: initErr}
		}
	}

	now := time.Now().UTC()
	info := &TenantInfo{
		ID:            id,
		Name:          name,
		IsolationType: isolationType,
		CreatedAt:     now,
		UpdatedAt:     now,
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

// Quotas returns the resource limits configured for the given tenant.
// When the tenant has no quotas configured, a zero-value Quotas is
// returned (all limits at system default). Implements quota.QuotaProvider.
func (s *TenantService) Quotas(ctx context.Context, tenantID string) (*quota.Quotas, error) {
	info, err := s.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if info.Quotas == nil {
		return &quota.Quotas{}, nil
	}
	return info.Quotas, nil
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

// TenantBarrierInit initializes the per-tenant barrier using the default
// passphrase (software) seal strategy. It delegates to
// TenantBarrierInitWithStrategy with seal.StrategyPassphrase for
// backward compatibility with all existing callers.
//
// Authorization — who may call this — is enforced at the API/transport
// layer, not here.
func (s *TenantService) TenantBarrierInit(ctx context.Context, tenantID, passphrase string, threshold, shares int) error {
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
) error {
	if strategyID == "" {
		strategyID = seal.StrategyPassphrase
	}
	if tenantID == "" {
		return ErrEmptyTenantID
	}

	// The passphrase strategy requires caller-supplied key material.
	// Hardware/cloud strategies wrap the root key via their own KEK;
	// no passphrase is needed.
	if seal.IsPassphraseStrategy(strategyID) && passphrase == "" {
		return ErrEmptyPassphrase
	}

	// Verify the tenant exists in metadata.
	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return err
	}

	// Resolve the sealing strategy from the injected registry when one is
	// present, or fall back to the built-in software factories for the
	// strategies qrdb owns directly (passphrase, none). Hardware/cloud
	// strategies must be registered via WithStrategyRegistry.
	strategy, err := s.resolveStrategy(strategyID)
	if err != nil {
		return err
	}

	// Build credentials. For the passphrase strategy the Secret carries
	// the tenant admin's passphrase; for all other strategies the
	// Credentials struct is empty (the strategy's KEK handles wrapping).
	creds := seal.Credentials{}
	if seal.IsPassphraseStrategy(strategyID) {
		creds.Secret = passphrase
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
			return &BarrierError{TenantID: tenantID, Op: "register", Err: regErr}
		}
		s.logger.Info("tenant barrier registered",
			slog.String("tenant_id", tenantID),
			slog.String("strategy", string(strategyID)))
	}

	if err := s.registry.InitializeTenant(ctx, tenantID, creds); err != nil {
		return &BarrierError{TenantID: tenantID, Op: "initialize", Err: err}
	}

	s.logger.Info("tenant barrier initialized",
		slog.String("tenant_id", tenantID),
		slog.String("strategy", string(strategyID)))

	return nil
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

	// Require caller-supplied key material. There is no derived fallback.
	if len(key) == 0 {
		return ErrEmptyPassphrase
	}

	// Verify the tenant exists in metadata.
	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return err
	}

	creds := seal.Credentials{Secret: string(key)}

	if err := s.registry.UnsealTenant(ctx, tenantID, creds); err != nil {
		return &BarrierError{TenantID: tenantID, Op: "unseal", Err: err}
	}

	s.logger.Info("tenant barrier unsealed", slog.String("tenant_id", tenantID))

	return nil
}

// TenantBarrierSeal seals the per-tenant barrier, refusing further reads
// and writes against the tenant namespace until unsealed again. The tenant
// must exist and its barrier must have been previously initialized.
func (s *TenantService) TenantBarrierSeal(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		return ErrEmptyTenantID
	}

	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return err
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

	return &TenantBarrierStatus{
		TenantID:       tenantID,
		Sealed:         st.Sealed,
		Initialized:    true,
		Strategy:       string(st.Strategy),
		HardwareBacked: st.HardwareBacked,
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
