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

package service

import (
	"context"
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/dek"
)

// DEKServicer defines the business logic interface for DEK operations.
type DEKServicer interface {
	// Initialize initializes the cluster DEK with the given algorithm and key size.
	Initialize(ctx context.Context, algorithm string, keyBytes int) error

	// GetStatus returns the current cluster DEK status.
	GetStatus(ctx context.Context) (*dek.ClusterDEKStatus, error)

	// Rotate rotates the cluster DEK to a new version.
	Rotate(ctx context.Context) error

	// CurrentDEKVersion returns the version of the live cluster DEK.
	CurrentDEKVersion(ctx context.Context) (uint64, error)

	// SyncToNode synchronizes the DEK to a specific node.
	SyncToNode(ctx context.Context, nodeID uint64) error

	// TenantGetOrCreate returns the DEK for a tenant, creating if needed.
	TenantGetOrCreate(ctx context.Context, tenantID string) ([]byte, error)

	// TenantRotate rotates the DEK for a specific tenant.
	TenantRotate(ctx context.Context, tenantID string) error

	// TenantDelete deletes a tenant's DEK.
	TenantDelete(ctx context.Context, tenantID string) error

	// TenantList returns all tenant IDs that have DEKs.
	TenantList(ctx context.Context) ([]string, error)

	// TenantGetStatus returns the status of a tenant's DEK.
	TenantGetStatus(ctx context.Context, tenantID string) (*dek.TenantDEKStatus, error)
}

// DEKService implements DEKServicer using the pkg/dek managers.
type DEKService struct {
	cluster *dek.ClusterDEKManager
	tenant  dek.TenantDEKManager
	logger  *slog.Logger
}

// Compile-time interface check.
var _ DEKServicer = (*DEKService)(nil)

// NewDEKService creates a new DEK service.
func NewDEKService(cluster *dek.ClusterDEKManager, tenant dek.TenantDEKManager, logger *slog.Logger) (*DEKService, error) {
	if cluster == nil {
		return nil, &dek.NilInputError{Op: "NewDEKService", Field: "cluster"}
	}
	if logger == nil {
		return nil, &dek.NilInputError{Op: "NewDEKService", Field: "logger"}
	}
	return &DEKService{
		cluster: cluster,
		tenant:  tenant,
		logger:  logger,
	}, nil
}

// Initialize initializes the cluster DEK with the given algorithm and key size.
func (s *DEKService) Initialize(ctx context.Context, algorithm string, keyBytes int) error {
	return s.cluster.Initialize(ctx, algorithm, keyBytes)
}

// GetStatus returns the current cluster DEK status.
func (s *DEKService) GetStatus(ctx context.Context) (*dek.ClusterDEKStatus, error) {
	result, err := s.cluster.GetStatus(ctx)
	if err != nil {
		return nil, err
	}
	status, ok := result.(*dek.ClusterDEKStatus)
	if !ok {
		return nil, &dek.CryptoError{Op: "DEKService.GetStatus", Detail: "unexpected status type"}
	}
	return status, nil
}

// Rotate rotates the cluster DEK to a new version.
func (s *DEKService) Rotate(ctx context.Context) error {
	return s.cluster.Rotate(ctx)
}

// CurrentDEKVersion returns the version of the live cluster DEK.
//
// The DEK bytes themselves do not cross this boundary. They stay in the
// cluster manager's protected memory, readable only under scoped access
// (dek.ClusterDEKManager.WithCurrentDEK), and no transport exposes them: the
// version is the whole of the caller-visible state.
func (s *DEKService) CurrentDEKVersion(_ context.Context) (uint64, error) {
	return s.cluster.CurrentVersion()
}

// SyncToNode synchronizes the DEK to a specific node.
func (s *DEKService) SyncToNode(ctx context.Context, nodeID uint64) error {
	return s.cluster.SyncToNode(ctx, nodeID)
}

// TenantGetOrCreate returns the DEK for a tenant, creating if needed.
func (s *DEKService) TenantGetOrCreate(ctx context.Context, tenantID string) ([]byte, error) {
	if s.tenant == nil {
		return nil, &dek.NotInitializedError{Op: "DEKService.TenantGetOrCreate", Resource: "tenant DEK manager"}
	}
	return s.tenant.GetOrCreateTenantDEK(ctx, tenantID)
}

// TenantRotate rotates the DEK for a specific tenant.
func (s *DEKService) TenantRotate(ctx context.Context, tenantID string) error {
	if s.tenant == nil {
		return &dek.NotInitializedError{Op: "DEKService.TenantRotate", Resource: "tenant DEK manager"}
	}
	return s.tenant.RotateTenantDEK(ctx, tenantID)
}

// TenantDelete deletes a tenant's DEK.
func (s *DEKService) TenantDelete(ctx context.Context, tenantID string) error {
	if s.tenant == nil {
		return &dek.NotInitializedError{Op: "DEKService.TenantDelete", Resource: "tenant DEK manager"}
	}
	return s.tenant.DeleteTenantDEK(ctx, tenantID)
}

// TenantList returns all tenant IDs that have DEKs.
func (s *DEKService) TenantList(ctx context.Context) ([]string, error) {
	if s.tenant == nil {
		return nil, &dek.NotInitializedError{Op: "DEKService.TenantList", Resource: "tenant DEK manager"}
	}
	return s.tenant.ListTenantDEKs(ctx)
}

// TenantGetStatus returns the status of a tenant's DEK.
func (s *DEKService) TenantGetStatus(ctx context.Context, tenantID string) (*dek.TenantDEKStatus, error) {
	if s.tenant == nil {
		return nil, &dek.NotInitializedError{Op: "DEKService.TenantGetStatus", Resource: "tenant DEK manager"}
	}
	return s.tenant.GetTenantDEKStatus(ctx, tenantID)
}
