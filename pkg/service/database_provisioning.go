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

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// backendProvisioningPermission is the permission a database backend needs in
// addition to databases:create: a backend: raft database is a dedicated Raft
// group, privileged provisioning.
var backendProvisioningPermission = map[database.BackendType]permission.Permission{
	database.BackendRaft: permission.DatabasesCreateRaft,
}

// isolationProvisioningPermission is the permission every database of a
// tenant with the given isolation tier needs in addition to databases:create:
// a volume-isolated tenant's database lives on the tenant's dedicated volume,
// and a raft-isolated tenant's database is a dedicated Raft group.
var isolationProvisioningPermission = map[tenant.IsolationType]permission.Permission{
	tenant.IsolationVolume: permission.DatabasesCreateVolume,
	tenant.IsolationRaft:   permission.DatabasesCreateRaft,
}

// ProvisioningPermissions returns the permissions creating cfg for a tenant of
// the given isolation tier needs in addition to databases:create, without
// duplicates, backend first.
func ProvisioningPermissions(cfg database.DatabaseConfig, isolation tenant.IsolationType) []permission.Permission {
	var perms []permission.Permission
	if p, ok := backendProvisioningPermission[cfg.Backend]; ok {
		perms = append(perms, p)
	}
	if p, ok := isolationProvisioningPermission[isolation]; ok && (len(perms) == 0 || perms[0] != p) {
		perms = append(perms, p)
	}
	return perms
}

// ProvisioningAuthorizer decides one permission for the identity in ctx
// against tenantID. It is satisfied by *middleware.AuthMiddleware's
// AuthorizeTenantTarget: a re-check behind the transport's entry check, so it
// charges no second rate-limit token and records only a refusal.
type ProvisioningAuthorizer interface {
	AuthorizeTenantTarget(ctx context.Context, tenantID string, perm permission.Permission) error
}

// ProvisioningDeniedError is returned when a database create is refused a
// provisioning permission it needs in addition to databases:create. Err is
// the authorizer's refusal, so its classification -- 403 for a missing grant,
// 503 for an audit sink that could not record the refusal -- is preserved
// through Unwrap.
type ProvisioningDeniedError struct {
	TenantID   string
	Database   string
	Permission permission.Permission
	Err        error
}

// Error returns a human-readable description.
func (e *ProvisioningDeniedError) Error() string {
	return "service: creating database " + e.Database + " needs " + string(e.Permission) + ": " + e.Err.Error()
}

// Unwrap returns the authorizer's refusal.
func (e *ProvisioningDeniedError) Unwrap() error { return e.Err }

// provisioningAuthorizedDatabaseService checks the provisioning permissions of
// every create before it reaches the database service. The transports' entry
// check can only check databases:create -- the route's permission -- because
// which provisioning permission a create needs depends on the request's
// backend and on the tenant's isolation tier; this is where both are known.
type provisioningAuthorizedDatabaseService struct {
	DatabaseServicer
	authz   ProvisioningAuthorizer
	tenants TenantInfoLookup
}

var (
	_ DatabaseServicer          = (*provisioningAuthorizedDatabaseService)(nil)
	_ DatabaseCredentialCreator = (*provisioningAuthorizedDatabaseService)(nil)
)

// NewProvisioningAuthorizedDatabaseService returns inner with every create
// checked for the provisioning permissions ProvisioningPermissions names,
// against the context's tenant, through authz. tenants resolves a tenant's
// isolation tier; without it only the backend decides. Every other method is
// inner's.
func NewProvisioningAuthorizedDatabaseService(inner DatabaseServicer, authz ProvisioningAuthorizer, tenants TenantInfoLookup) DatabaseServicer {
	return &provisioningAuthorizedDatabaseService{DatabaseServicer: inner, authz: authz, tenants: tenants}
}

// authorize checks every provisioning permission cfg needs for the context's
// tenant. A tenant whose isolation tier cannot be read is refused with the
// lookup's error: the tier decides what the create provisions.
func (s *provisioningAuthorizedDatabaseService) authorize(ctx context.Context, cfg database.DatabaseConfig) error {
	tenantID := tenant.FromContext(ctx)
	var isolation tenant.IsolationType
	if s.tenants != nil && !tenant.IsSystemTenant(tenantID) {
		info, err := s.tenants.GetTenant(ctx, tenantID)
		if err != nil {
			return err
		}
		if info != nil {
			isolation = info.EffectiveIsolationType()
		}
	}
	for _, perm := range ProvisioningPermissions(cfg, isolation) {
		if err := s.authz.AuthorizeTenantTarget(ctx, tenantID, perm); err != nil {
			return &ProvisioningDeniedError{TenantID: tenantID, Database: cfg.Name, Permission: perm, Err: err}
		}
	}
	return nil
}

// CreateForTenant checks the provisioning permissions, then creates.
func (s *provisioningAuthorizedDatabaseService) CreateForTenant(ctx context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	if err := s.authorize(ctx, cfg); err != nil {
		return nil, err
	}
	return s.DatabaseServicer.CreateForTenant(ctx, cfg)
}

// CreateForTenantWithCredentials checks the provisioning permissions, then
// creates through inner's credentialed path when it has one.
func (s *provisioningAuthorizedDatabaseService) CreateForTenantWithCredentials(ctx context.Context, cfg database.DatabaseConfig, creds seal.Credentials) (*database.DatabaseInfo, error) {
	if err := s.authorize(ctx, cfg); err != nil {
		return nil, err
	}
	return CreateDatabaseForTenantWithCredentials(ctx, s.DatabaseServicer, cfg, creds)
}

// CreateWithCredentials checks the provisioning permissions, then creates
// through inner's credentialed path. inner must support it: a credentialed
// create cannot drop its credentials.
func (s *provisioningAuthorizedDatabaseService) CreateWithCredentials(ctx context.Context, cfg database.DatabaseConfig, creds seal.Credentials) (*database.DatabaseInfo, error) {
	if err := s.authorize(ctx, cfg); err != nil {
		return nil, err
	}
	cc, ok := s.DatabaseServicer.(DatabaseCredentialCreator)
	if !ok {
		return nil, &CapabilityUnsupportedError{Message: "database: the database service does not support credentialed creation"}
	}
	return cc.CreateWithCredentials(ctx, cfg, creds)
}
