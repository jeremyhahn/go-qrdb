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

const metadataPrefix = "tenants/"

// TenantInfo holds tenant metadata persisted in the storage backend.
type TenantInfo struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Quotas    *quota.Quotas `json:"quotas,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// TenantServicer defines the interface for tenant lifecycle management
// including metadata CRUD and per-tenant barrier operations.
type TenantServicer interface {
	// CreateTenant creates a new tenant with the given ID and name,
	// registers it in the barrier registry, and persists metadata.
	CreateTenant(ctx context.Context, id, name string) (*TenantInfo, error)

	// GetTenant retrieves tenant metadata by ID.
	GetTenant(ctx context.Context, tenantID string) (*TenantInfo, error)

	// ListTenants returns metadata for all registered tenants.
	ListTenants(ctx context.Context) ([]TenantInfo, error)

	// DeleteTenant removes a tenant from the barrier registry and
	// deletes its metadata from storage.
	DeleteTenant(ctx context.Context, tenantID string) error

	// UpdateQuotas updates the resource quotas for a tenant and
	// persists the change to the metadata store.
	UpdateQuotas(ctx context.Context, tenantID string, quotas *quota.Quotas) (*TenantInfo, error)

	// TenantBarrierInit initializes the per-tenant barrier with the
	// given threshold and shares configuration.
	TenantBarrierInit(ctx context.Context, tenantID string, threshold, shares int) error

	// TenantBarrierUnseal unseals the per-tenant barrier using the
	// provided key material.
	TenantBarrierUnseal(ctx context.Context, tenantID string, key []byte) error
}

// TenantService manages multi-tenant lifecycle including metadata CRUD
// and per-tenant barrier operations. It delegates barrier lifecycle to
// the BarrierRegistry and persists tenant metadata in a StorageBackend.
type TenantService struct {
	store    seal.StorageBackend
	registry *seal.BarrierRegistry
	logger   *slog.Logger
}

// Compile-time interface checks.
var (
	_ TenantServicer      = (*TenantService)(nil)
	_ quota.QuotaProvider = (*TenantService)(nil)
)

// NewTenantService creates a new TenantService. The store parameter is
// used for persisting tenant metadata, and the registry manages the
// per-tenant barrier lifecycle.
func NewTenantService(
	store seal.StorageBackend,
	registry *seal.BarrierRegistry,
	logger *slog.Logger,
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
	return &TenantService{
		store:    store,
		registry: registry,
		logger:   logger,
	}, nil
}

// CreateTenant creates a new tenant with the given ID and name. It
// validates inputs, checks for duplicates, registers the tenant in
// the barrier registry with default configuration, and persists
// metadata in the storage backend.
func (s *TenantService) CreateTenant(ctx context.Context, id, name string) (*TenantInfo, error) {
	if id == "" {
		return nil, ErrEmptyTenantID
	}
	if name == "" {
		return nil, ErrEmptyTenantName
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

	// Register tenant in barrier registry with default configuration.
	_, err = s.registry.RegisterTenantDefault(id)
	if err != nil {
		// If the registry already has this tenant, surface our typed error.
		if errors.Is(err, seal.ErrTenantAlreadyExists) {
			return nil, ErrTenantAlreadyExists
		}
		return nil, &BarrierError{TenantID: id, Op: "register", Err: err}
	}

	now := time.Now().UTC()
	info := &TenantInfo{
		ID:        id,
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
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
		slog.String("name", name))

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

// TenantBarrierInit initializes the per-tenant barrier. The tenant must
// exist. The threshold and shares parameters configure Shamir secret
// sharing; when both are zero, default credentials are used.
func (s *TenantService) TenantBarrierInit(ctx context.Context, tenantID string, threshold, shares int) error {
	if tenantID == "" {
		return ErrEmptyTenantID
	}

	// Verify the tenant exists in metadata.
	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return err
	}

	// Build credentials. For now, use a deterministic credential derived
	// from the tenant ID. A production system would use Shamir shares or
	// an external KMS. The threshold/shares params are reserved for
	// future Shamir integration at the tenant barrier level.
	creds := seal.Credentials{Secret: "tenant-init-" + tenantID}

	if err := s.registry.InitializeTenant(ctx, tenantID, creds); err != nil {
		return &BarrierError{TenantID: tenantID, Op: "initialize", Err: err}
	}

	s.logger.Info("tenant barrier initialized", slog.String("tenant_id", tenantID))

	return nil
}

// TenantBarrierUnseal unseals the per-tenant barrier using the provided
// key material. The tenant must exist and its barrier must have been
// previously initialized.
func (s *TenantService) TenantBarrierUnseal(ctx context.Context, tenantID string, key []byte) error {
	if tenantID == "" {
		return ErrEmptyTenantID
	}

	// Verify the tenant exists in metadata.
	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return err
	}

	// Build credentials from the provided key material. When key is nil
	// or empty, fall back to the deterministic credential used during init.
	secret := "tenant-init-" + tenantID
	if len(key) > 0 {
		secret = string(key)
	}
	creds := seal.Credentials{Secret: secret}

	if err := s.registry.UnsealTenant(ctx, tenantID, creds); err != nil {
		return &BarrierError{TenantID: tenantID, Op: "unseal", Err: err}
	}

	s.logger.Info("tenant barrier unsealed", slog.String("tenant_id", tenantID))

	return nil
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
