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

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
)

// ErrBarrierNotEnabled is returned when a barrier operation is attempted
// but the seal subsystem is not configured.
var ErrBarrierNotEnabled = errors.New("tenant: barrier not enabled (seal subsystem disabled)")

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
// When isolationType is empty, IsolationBarrier is used.
func (s *kvTenantService) CreateTenant(ctx context.Context, id, name string, isolationType IsolationType) (*TenantInfo, error) {
	if id == "" {
		return nil, ErrEmptyTenantID
	}
	if name == "" {
		return nil, ErrEmptyTenantName
	}
	if isolationType == "" {
		isolationType = IsolationBarrier
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
func (s *kvTenantService) UpdateTenant(ctx context.Context, tenantID string, patch TenantPatch) (*TenantInfo, error) {
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

// TenantBarrierInit returns ErrBarrierNotEnabled. The seal subsystem is not
// configured when this service implementation is in use.
func (s *kvTenantService) TenantBarrierInit(_ context.Context, _ string, _, _ int) error {
	return ErrBarrierNotEnabled
}

// TenantBarrierUnseal returns ErrBarrierNotEnabled. The seal subsystem is not
// configured when this service implementation is in use.
func (s *kvTenantService) TenantBarrierUnseal(_ context.Context, _ string, _ []byte) error {
	return ErrBarrierNotEnabled
}
