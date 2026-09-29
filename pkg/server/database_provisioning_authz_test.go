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

package server

import (
	"context"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// provisioningRecorder is the database service behind the transports: it
// records every create that reaches it.
type provisioningRecorder struct {
	service.DatabaseServicer
	mu      sync.Mutex
	created []string
}

var _ service.DatabaseCredentialCreator = (*provisioningRecorder)(nil)

func (r *provisioningRecorder) record(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = append(r.created, cfg.Name)
	return &database.DatabaseInfo{Name: cfg.Name, Engine: cfg.Engine, Backend: cfg.Backend}, nil
}

func (r *provisioningRecorder) CreateForTenant(_ context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return r.record(cfg)
}

func (r *provisioningRecorder) CreateWithCredentials(_ context.Context, cfg database.DatabaseConfig, _ seal.Credentials) (*database.DatabaseInfo, error) {
	return r.record(cfg)
}

func (r *provisioningRecorder) CreateForTenantWithCredentials(_ context.Context, cfg database.DatabaseConfig, _ seal.Credentials) (*database.DatabaseInfo, error) {
	return r.record(cfg)
}

// isolatedTenants answers GetTenant with each tenant's isolation tier.
type isolatedTenants struct {
	mockTenantServicer
	isolation map[string]tenant.IsolationType
}

func (s *isolatedTenants) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, IsolationType: s.isolation[id]}, nil
}

// withoutPermissions grants every permission except the listed ones.
type withoutPermissions map[permission.Permission]bool

func (w withoutPermissions) Authorize(_ context.Context, _ *auth.Identity, resource, action string) error {
	if w[permission.Permission(resource+":"+action)] {
		return auth.ErrUnauthorized
	}
	return nil
}

func provisioningCtx(tenantID string) context.Context {
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice", TenantID: tenantID})
	return tenant.WithTenantID(ctx, tenantID)
}

// The database service the server hands every network transport and both SQL
// wire listeners checks databases:create_raft and databases:create_volume in
// addition to databases:create: the transports' entry check can only check
// databases:create, because which of the two a create needs depends on the
// request's backend and on the tenant's isolation tier. Neither was checked
// anywhere, so any holder of databases:create could provision a dedicated
// Raft group or a volume-isolated database.
func TestServer_DatabaseCreateChecksProvisioningPermissions(t *testing.T) {
	cases := []struct {
		name     string
		tenantID string
		backend  database.BackendType
		missing  permission.Permission
	}{
		{"raft backend", "acme", database.BackendRaft, permission.DatabasesCreateRaft},
		{"raft backend, system tenant", tenantid.System, database.BackendRaft, permission.DatabasesCreateRaft},
		{"volume-isolated tenant", "vol", database.BackendPebble, permission.DatabasesCreateVolume},
		{"raft-isolated tenant", "rft", database.BackendPebble, permission.DatabasesCreateRaft},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := &provisioningRecorder{}
			tenants := &isolatedTenants{isolation: map[string]tenant.IsolationType{
				"acme": tenant.IsolationPrefix, "vol": tenant.IsolationVolume, "rft": tenant.IsolationRaft,
			}}
			srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{},
				WithDatabaseService(rec),
				WithTenantService(tenants),
				WithAuthMiddleware(authmw.New(withoutPermissions{c.missing: true})))
			cfg := database.DatabaseConfig{Name: "db", Engine: database.EngineKV, Backend: c.backend}

			_, err := service.CreateDatabaseForTenantWithCredentials(provisioningCtx(c.tenantID), srv.opts.databaseSvc, cfg, seal.Credentials{})
			if err == nil {
				t.Fatalf("create succeeded without %s", c.missing)
			}
			if len(rec.created) != 0 {
				t.Fatalf("a create refused %s reached the database service: %v", c.missing, rec.created)
			}
			if class := authmw.ClassifyAuthError(err); class != authmw.AuthFailureUnauthorized {
				t.Fatalf("refusal %v classifies as %s, want unauthorized (403 / PERMISSION_DENIED)", err, class)
			}

			// With the permission the same create goes through.
			granted := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{},
				WithDatabaseService(rec),
				WithTenantService(tenants),
				WithAuthMiddleware(authmw.New(withoutPermissions{})))
			if _, err := service.CreateDatabaseForTenantWithCredentials(provisioningCtx(c.tenantID), granted.opts.databaseSvc, cfg, seal.Credentials{}); err != nil {
				t.Fatalf("create with %s: %v", c.missing, err)
			}
		})
	}
}

// An ordinary database of a shared tenant needs databases:create alone.
func TestServer_DatabaseCreateOrdinaryNeedsNoProvisioningPermission(t *testing.T) {
	rec := &provisioningRecorder{}
	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{},
		WithDatabaseService(rec),
		WithTenantService(&isolatedTenants{isolation: map[string]tenant.IsolationType{"acme": tenant.IsolationPrefix}}),
		WithAuthMiddleware(authmw.New(withoutPermissions{
			permission.DatabasesCreateRaft: true, permission.DatabasesCreateVolume: true,
		})))
	cfg := database.DatabaseConfig{Name: "db", Engine: database.EngineKV, Backend: database.BackendPebble}
	if _, err := srv.opts.databaseSvc.CreateForTenant(provisioningCtx("acme"), cfg); err != nil {
		t.Fatalf("ordinary create: %v", err)
	}
	if len(rec.created) != 1 {
		t.Fatalf("created = %v, want [db]", rec.created)
	}
}
