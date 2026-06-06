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

package transport

import (
	"context"
	"strings"
)

// TenantDatabaseClient wraps a DatabaseClient with automatic tenant scoping.
// All create operations inject the tenant ID, and all read/list operations
// filter results to only databases owned by this tenant. Follows the same
// pattern as TenantKVClient.
type TenantDatabaseClient struct {
	inner    DatabaseClient
	tenantID string
}

// WithTenantDatabase creates a new TenantDatabaseClient that scopes all
// database operations to the given tenant. The tenantID must not be empty
// or contain path separators.
func WithTenantDatabase(client DatabaseClient, tenantID string) (*TenantDatabaseClient, error) {
	if tenantID == "" {
		return nil, &ConfigError{Field: "tenantID", Reason: "must not be empty"}
	}
	if strings.Contains(tenantID, "/") || strings.Contains(tenantID, "\\") || strings.Contains(tenantID, "..") {
		return nil, &ConfigError{Field: "tenantID", Reason: "must not contain path separators or '..'"}
	}
	return &TenantDatabaseClient{
		inner:    client,
		tenantID: tenantID,
	}, nil
}

// TenantID returns the tenant ID used for database scoping.
func (t *TenantDatabaseClient) TenantID() string {
	return t.tenantID
}

// CreateDatabase creates a new database with the tenant ID injected into
// the request.
func (t *TenantDatabaseClient) CreateDatabase(ctx context.Context, req CreateDatabaseRequest) (*DatabaseInfo, error) {
	req.TenantID = t.tenantID
	return t.inner.CreateDatabase(ctx, req)
}

// GetDatabase returns information about a database, verifying it belongs
// to this tenant.
func (t *TenantDatabaseClient) GetDatabase(ctx context.Context, name string) (*DatabaseInfo, error) {
	info, err := t.inner.GetDatabase(ctx, name)
	if err != nil {
		return nil, err
	}
	if info.TenantID != t.tenantID {
		return nil, &TenantDatabaseAccessDeniedError{TenantID: t.tenantID, Database: name}
	}
	return info, nil
}

// ListDatabases returns only databases owned by this tenant.
func (t *TenantDatabaseClient) ListDatabases(ctx context.Context) ([]DatabaseInfo, error) {
	all, err := t.inner.ListDatabases(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]DatabaseInfo, 0, len(all))
	for _, db := range all {
		if db.TenantID == t.tenantID {
			filtered = append(filtered, db)
		}
	}
	return filtered, nil
}

// DropDatabase removes a database after verifying tenant ownership.
func (t *TenantDatabaseClient) DropDatabase(ctx context.Context, name string) error {
	if err := t.verifyOwnership(ctx, name); err != nil {
		return err
	}
	return t.inner.DropDatabase(ctx, name)
}

// UpdateDatabaseProtocol updates wire protocol config for a tenant-owned database.
func (t *TenantDatabaseClient) UpdateDatabaseProtocol(ctx context.Context, req UpdateDatabaseProtocolRequest) (*DatabaseInfo, error) {
	if err := t.verifyOwnership(ctx, req.Name); err != nil {
		return nil, err
	}
	return t.inner.UpdateDatabaseProtocol(ctx, req)
}

// UpdateDatabaseEtcd updates etcd wire protocol config for a tenant-owned database.
func (t *TenantDatabaseClient) UpdateDatabaseEtcd(ctx context.Context, req UpdateDatabaseEtcdRequest) (*DatabaseInfo, error) {
	if err := t.verifyOwnership(ctx, req.Name); err != nil {
		return nil, err
	}
	return t.inner.UpdateDatabaseEtcd(ctx, req)
}

// UpdateDatabaseRaft updates Raft config for a tenant-owned database.
func (t *TenantDatabaseClient) UpdateDatabaseRaft(ctx context.Context, req UpdateDatabaseRaftRequest) (*DatabaseInfo, error) {
	if err := t.verifyOwnership(ctx, req.Name); err != nil {
		return nil, err
	}
	return t.inner.UpdateDatabaseRaft(ctx, req)
}

// UpdateDatabaseBarrier updates barrier config for a tenant-owned database.
func (t *TenantDatabaseClient) UpdateDatabaseBarrier(ctx context.Context, req UpdateDatabaseBarrierRequest) (*DatabaseInfo, error) {
	if err := t.verifyOwnership(ctx, req.Name); err != nil {
		return nil, err
	}
	return t.inner.UpdateDatabaseBarrier(ctx, req)
}

// UpdateDatabaseConnection updates connection config for a tenant-owned database.
func (t *TenantDatabaseClient) UpdateDatabaseConnection(ctx context.Context, req UpdateDatabaseConnectionRequest) (*DatabaseInfo, error) {
	if err := t.verifyOwnership(ctx, req.Name); err != nil {
		return nil, err
	}
	return t.inner.UpdateDatabaseConnection(ctx, req)
}

// SealDatabase seals the barrier for a tenant-owned database.
func (t *TenantDatabaseClient) SealDatabase(ctx context.Context, name string) error {
	if err := t.verifyOwnership(ctx, name); err != nil {
		return err
	}
	return t.inner.SealDatabase(ctx, name)
}

// UnsealDatabase unseals the barrier for a tenant-owned database.
func (t *TenantDatabaseClient) UnsealDatabase(ctx context.Context, name, secret string) error {
	if err := t.verifyOwnership(ctx, name); err != nil {
		return err
	}
	return t.inner.UnsealDatabase(ctx, name, secret)
}

// DatabaseBarrierStatus returns the barrier status for a tenant-owned database.
func (t *TenantDatabaseClient) DatabaseBarrierStatus(ctx context.Context, name string) (*DatabaseBarrierStatus, error) {
	if err := t.verifyOwnership(ctx, name); err != nil {
		return nil, err
	}
	return t.inner.DatabaseBarrierStatus(ctx, name)
}

// verifyOwnership checks that the named database belongs to this tenant.
func (t *TenantDatabaseClient) verifyOwnership(ctx context.Context, name string) error {
	info, err := t.inner.GetDatabase(ctx, name)
	if err != nil {
		return err
	}
	if info.TenantID != t.tenantID {
		return &TenantDatabaseAccessDeniedError{TenantID: t.tenantID, Database: name}
	}
	return nil
}

// Compile-time check: TenantDatabaseClient implements DatabaseClient.
var _ DatabaseClient = (*TenantDatabaseClient)(nil)
