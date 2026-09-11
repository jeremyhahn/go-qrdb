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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// ErrBarrierNotEnabled is returned when a barrier operation is attempted
// but the seal subsystem is not configured.
var ErrBarrierNotEnabled = errors.New("tenant: barrier not enabled (seal subsystem disabled)")

// ErrBarrierIsolationUnavailable is returned when barrier isolation is
// requested for a tenant while the seal subsystem is disabled. The no-seal
// KV-backed tenant service has no barrier registry, so it cannot register or
// initialize a per-tenant barrier. Accepting the request would silently
// persist a barrier-isolated tenant whose barrier never exists, causing the
// store resolver to fail later when it tries to resolve the (absent) barrier.
// This fails closed instead: barrier isolation requires the seal subsystem.
var ErrBarrierIsolationUnavailable = errors.New("tenant: barrier isolation requires the seal subsystem (disabled)")

// kvTenantService implements TenantServicer using a kvstore.KVStore backend.
// It is used when the seal subsystem is not enabled and provides full tenant
// CRUD with no-op barrier operations (TenantBarrierInit, TenantBarrierUnseal
// return ErrBarrierNotEnabled).
type kvTenantService struct {
	store  kvstore.KVStore
	logger *slog.Logger
}

// Compile-time interface check.
var _ TenantServicer = (*kvTenantService)(nil)

// NewKVTenantService creates a TenantServicer backed by a kvstore.KVStore.
// Used when the seal subsystem is disabled. Barrier operations return
// ErrBarrierNotEnabled.
func NewKVTenantService(store kvstore.KVStore, logger *slog.Logger) (TenantServicer, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &kvTenantService{store: store, logger: logger}, nil
}

// CreateTenant creates a new tenant and persists its metadata to the KV store.
// When isolationType is empty, IsolationPrefix is used (barrier isolation is
// opt-in). This service runs without a seal barrier, so every tenant is
// effectively prefix-isolated; barrier operations return ErrBarrierNotEnabled.
//
// An explicit IsolationBarrier request is rejected with
// ErrBarrierIsolationUnavailable: the no-seal service has no barrier registry
// and cannot honor barrier isolation. Failing closed here prevents persisting
// a barrier-isolated tenant whose barrier never exists.
func (s *kvTenantService) CreateTenant(ctx context.Context, id, name string, isolationType IsolationType, _ ...CreateTenantOption) (*TenantInfo, error) {
	if id == "" {
		return nil, ErrEmptyTenantID
	}
	if name == "" {
		return nil, ErrEmptyTenantName
	}
	if err := validateReservedTenantID(ctx, id); err != nil {
		return nil, err
	}
	if isolationType == "" {
		isolationType = IsolationPrefix
	}
	if isolationType == IsolationBarrier || isolationType == IsolationRaft {
		// Both require a seal barrier registry (raft additionally needs a
		// dedicated DataRoot + per-database shard). The no-seal service cannot
		// honor either, so fail closed rather than persist an unusable tenant.
		return nil, ErrBarrierIsolationUnavailable
	}

	key := metadataPrefix + id
	existing, err := s.store.Get(ctx, key)
	if err != nil {
		return nil, &StoreError{Op: "check-exists", Err: err}
	}
	if existing != nil {
		return nil, ErrTenantAlreadyExists
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
		return nil, &StoreError{Op: "marshal", Err: err}
	}

	if err := s.store.Put(ctx, key, data); err != nil {
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
// patch.IsolationType differs, the caller is expected to have verified the
// tenant has no data before calling this method.
//
// Upgrading a tenant to IsolationBarrier is rejected with
// ErrBarrierIsolationUnavailable for the same reason CreateTenant rejects it:
// the no-seal service has no barrier registry to back the isolation.
func (s *kvTenantService) UpdateTenant(ctx context.Context, tenantID string, patch TenantPatch) (*TenantInfo, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if patch.IsolationType == IsolationBarrier {
		return nil, ErrBarrierIsolationUnavailable
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

	s.logger.Info("tenant updated", slog.String("tenant_id", tenantID))

	return info, nil
}

// GetTenant retrieves tenant metadata by ID from the KV store.
func (s *kvTenantService) GetTenant(ctx context.Context, tenantID string) (*TenantInfo, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}

	key := metadataPrefix + tenantID
	data, err := s.store.Get(ctx, key)
	if err != nil {
		return nil, &StoreError{Op: "get", Err: err}
	}
	if data == nil {
		return nil, ErrTenantNotFound
	}

	var info TenantInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, &StoreError{Op: "unmarshal", Err: err}
	}

	return &info, nil
}

// ListTenants returns metadata for all tenants stored under the metadata prefix.
func (s *kvTenantService) ListTenants(ctx context.Context) ([]TenantInfo, error) {
	keys, err := s.store.List(ctx, metadataPrefix)
	if err != nil {
		return nil, &StoreError{Op: "list", Err: err}
	}

	tenants := make([]TenantInfo, 0, len(keys))
	for _, key := range keys {
		data, err := s.store.Get(ctx, key)
		if err != nil {
			return nil, &StoreError{Op: "get", Err: err}
		}
		if data == nil {
			// Entry disappeared between list and get — skip.
			continue
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

// DeleteTenant removes tenant metadata from the KV store.
func (s *kvTenantService) DeleteTenant(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		return ErrEmptyTenantID
	}

	key := metadataPrefix + tenantID
	data, err := s.store.Get(ctx, key)
	if err != nil {
		return &StoreError{Op: "get", Err: err}
	}
	if data == nil {
		return ErrTenantNotFound
	}

	if err := s.store.Delete(ctx, key); err != nil {
		return &StoreError{Op: "delete", Err: err}
	}

	s.logger.Info("tenant deleted", slog.String("tenant_id", tenantID))

	return nil
}

// UpdateQuotas updates the resource quotas for an existing tenant.
func (s *kvTenantService) UpdateQuotas(ctx context.Context, tenantID string, quotas *quota.Quotas) (*TenantInfo, error) {
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

// UpdateEntitlements updates the feature entitlements for an existing tenant.
func (s *kvTenantService) UpdateEntitlements(ctx context.Context, tenantID string, entitlements *quota.Entitlements) (*TenantInfo, error) {
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

// CheckIsolation returns nil if the tenant is entitled to the given isolation
// mode, or a *quota.EntitlementError otherwise. The system tenant is implicitly
// entitled to every mode. The no-seal KV service configures no operator
// defaults, so nil entitlements fall back to the conservative built-in
// defaults (prefix-only). Implements quota.EntitlementChecker.
func (s *kvTenantService) CheckIsolation(ctx context.Context, tenantID, mode string) error {
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

// CheckFeature returns nil if the tenant is entitled to the named feature, or a
// *quota.EntitlementError otherwise. The system tenant is implicitly entitled
// to every feature. Implements quota.EntitlementChecker.
func (s *kvTenantService) CheckFeature(ctx context.Context, tenantID, feature string) error {
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

// CheckBarrierMode returns nil if the tenant may use the given barrier sealing
// strategy. The shared system-barrier option (seal.StrategySystemBarrier) is
// gated by the tenant's SystemBarrierQuota (0 ⇒ unavailable); every other
// (dedicated) mode is gated by AllowedBarrierModes. The system tenant is exempt.
func (s *kvTenantService) CheckBarrierMode(ctx context.Context, tenantID string, mode seal.StrategyID) error {
	if IsSystemTenant(tenantID) {
		return nil
	}
	if mode == seal.StrategySystemBarrier {
		info, err := s.GetTenant(ctx, tenantID)
		if err != nil {
			return err
		}
		q := info.Quotas
		if q == nil {
			q = &quota.Quotas{}
		}
		if q.AllowsSystemBarrier() {
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

// effectiveEntitlements returns the tenant's own entitlements when set,
// otherwise the conservative built-in defaults (prefix-only).
func (s *kvTenantService) effectiveEntitlements(ctx context.Context, tenantID string) (*quota.Entitlements, error) {
	info, err := s.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if info.Entitlements != nil {
		return info.Entitlements, nil
	}
	return quota.DefaultEntitlements(), nil
}

// IncrementQuotaUsage increments the current-usage counter for the given
// resource by delta and persists the change. Returns QuotaExceededError
// when the resulting usage would exceed the configured limit.
func (s *kvTenantService) IncrementQuotaUsage(ctx context.Context, tenantID string, resource quota.Resource, delta int64) (*TenantInfo, error) {
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
	current, qErr := info.Quotas.CurrentUsage(resource)
	if qErr != nil {
		return nil, &StoreError{Op: "increment-quota", Err: qErr}
	}
	limit, qErr := info.Quotas.Limit(resource)
	if qErr != nil {
		return nil, &StoreError{Op: "increment-quota", Err: qErr}
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
	if qErr := info.Quotas.SetCurrentUsage(resource, current+delta); qErr != nil {
		return nil, &StoreError{Op: "increment-quota", Err: qErr}
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
		slog.Int64("delta", delta))
	return info, nil
}

// DecrementQuotaUsage decrements the current-usage counter for the given
// resource by delta. The counter is clamped to zero.
func (s *kvTenantService) DecrementQuotaUsage(ctx context.Context, tenantID string, resource quota.Resource, delta int64) (*TenantInfo, error) {
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
	current, qErr := info.Quotas.CurrentUsage(resource)
	if qErr != nil {
		return nil, &StoreError{Op: "decrement-quota", Err: qErr}
	}
	next := current - delta
	if next < 0 {
		next = 0
	}
	if qErr := info.Quotas.SetCurrentUsage(resource, next); qErr != nil {
		return nil, &StoreError{Op: "decrement-quota", Err: qErr}
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
		slog.Int64("delta", delta))
	return info, nil
}

// SetQuotaUsage overwrites the current-usage counter for the given resource to
// the supplied absolute value and persists the change without enforcing the
// configured limit. It is used by the storage-quota reconciler to correct drift
// from the in-memory accumulator. Negative values are clamped to zero.
func (s *kvTenantService) SetQuotaUsage(ctx context.Context, tenantID string, resource quota.Resource, value int64) (*TenantInfo, error) {
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
	if qErr := info.Quotas.SetCurrentUsage(resource, value); qErr != nil {
		return nil, &StoreError{Op: "set-quota-usage", Err: qErr}
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

// GetQuotaUsage returns the current-usage value for the given resource and
// tenant without modifying any state.
func (s *kvTenantService) GetQuotaUsage(ctx context.Context, tenantID string, resource quota.Resource) (int64, error) {
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
	usage, qErr := info.Quotas.CurrentUsage(resource)
	if qErr != nil {
		return 0, &StoreError{Op: "get-quota-usage", Err: qErr}
	}
	return usage, nil
}

// TenantBarrierInit returns ErrBarrierNotEnabled. The seal subsystem is not
// configured when this service implementation is in use.
func (s *kvTenantService) TenantBarrierInit(_ context.Context, _ string, _ []byte, _, _ int) (*TenantBarrierInitResult, error) {
	return nil, ErrBarrierNotEnabled
}

// TenantBarrierInitWithStrategy returns ErrBarrierNotEnabled. The seal
// subsystem is not configured when this service implementation is in use.
func (s *kvTenantService) TenantBarrierInitWithStrategy(_ context.Context, _ string, _ []byte, _, _ int, _ seal.StrategyID) (*TenantBarrierInitResult, error) {
	return nil, ErrBarrierNotEnabled
}

// TenantBarrierUnseal returns ErrBarrierNotEnabled. The seal subsystem is not
// configured when this service implementation is in use.
func (s *kvTenantService) TenantBarrierUnseal(_ context.Context, _ string, _ []byte) error {
	return ErrBarrierNotEnabled
}

// TenantBarrierUnsealShare returns ErrBarrierNotEnabled. The seal subsystem is
// not configured when this service implementation is in use.
func (s *kvTenantService) TenantBarrierUnsealShare(_ context.Context, _, _ string) (*TenantBarrierUnsealProgress, error) {
	return nil, ErrBarrierNotEnabled
}

// TenantBarrierSeal returns ErrBarrierNotEnabled. The seal subsystem is not
// configured when this service implementation is in use.
func (s *kvTenantService) TenantBarrierSeal(_ context.Context, _ string) error {
	return ErrBarrierNotEnabled
}

// TenantBarrierStatus returns ErrBarrierNotEnabled. The seal subsystem is not
// configured when this service implementation is in use.
func (s *kvTenantService) TenantBarrierStatus(_ context.Context, _ string) (*TenantBarrierStatus, error) {
	return nil, ErrBarrierNotEnabled
}

// TenantBarrierOfficerRegister returns ErrBarrierNotEnabled. The seal
// subsystem is not configured when this service implementation is in use.
func (s *kvTenantService) TenantBarrierOfficerRegister(_ context.Context, _ string, _ *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	return nil, ErrBarrierNotEnabled
}

// TenantBarrierOfficerList returns ErrBarrierNotEnabled. The seal subsystem
// is not configured when this service implementation is in use.
func (s *kvTenantService) TenantBarrierOfficerList(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
	return nil, ErrBarrierNotEnabled
}

// TenantBarrierShareClaimBegin returns ErrBarrierNotEnabled. The seal
// subsystem is not configured when this service implementation is in use.
func (s *kvTenantService) TenantBarrierShareClaimBegin(_ context.Context, _, _, _ string) (*ceremony.ChallengeResponse, error) {
	return nil, ErrBarrierNotEnabled
}

// TenantBarrierShareClaimComplete returns ErrBarrierNotEnabled. The seal
// subsystem is not configured when this service implementation is in use.
func (s *kvTenantService) TenantBarrierShareClaimComplete(_ context.Context, _, _, _ string, _ []byte, _ string) (*ceremony.ShareClaimResult, error) {
	return nil, ErrBarrierNotEnabled
}

// TenantBarrierOfficerClaimStatus returns ErrBarrierNotEnabled. The seal
// subsystem is not configured when this service implementation is in use.
func (s *kvTenantService) TenantBarrierOfficerClaimStatus(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
	return nil, ErrBarrierNotEnabled
}

// TenantBarrierShamirSealVote returns ErrBarrierNotEnabled. The seal
// subsystem is not configured when this service implementation is in use.
func (s *kvTenantService) TenantBarrierShamirSealVote(_ context.Context, _, _ string) (*TenantBarrierUnsealProgress, error) {
	return nil, ErrBarrierNotEnabled
}
