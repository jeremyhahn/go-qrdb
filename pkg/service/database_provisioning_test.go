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
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

func TestProvisioningPermissions(t *testing.T) {
	cases := []struct {
		name      string
		backend   database.BackendType
		isolation tenant.IsolationType
		want      []permission.Permission
	}{
		{"shared pebble", database.BackendPebble, tenant.IsolationPrefix, nil},
		{"raft backend", database.BackendRaft, tenant.IsolationPrefix, []permission.Permission{permission.DatabasesCreateRaft}},
		{"volume tenant", database.BackendPebble, tenant.IsolationVolume, []permission.Permission{permission.DatabasesCreateVolume}},
		{"raft tenant", database.BackendMemory, tenant.IsolationRaft, []permission.Permission{permission.DatabasesCreateRaft}},
		{"raft backend, raft tenant", database.BackendRaft, tenant.IsolationRaft, []permission.Permission{permission.DatabasesCreateRaft}},
		{"raft backend, volume tenant", database.BackendRaft, tenant.IsolationVolume,
			[]permission.Permission{permission.DatabasesCreateRaft, permission.DatabasesCreateVolume}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ProvisioningPermissions(database.DatabaseConfig{Backend: c.backend}, c.isolation)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("ProvisioningPermissions = %v, want %v", got, c.want)
			}
		})
	}
}

// provisionCreates is the inner database service: it records creates.
type provisionCreates struct {
	DatabaseServicer
	created []string
}

func (p *provisionCreates) CreateForTenant(_ context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	p.created = append(p.created, cfg.Name)
	return &database.DatabaseInfo{Name: cfg.Name}, nil
}

// provisionDeny refuses the listed permissions and records every decision.
type provisionDeny struct {
	deny  map[permission.Permission]bool
	asked []string
}

var errProvisionDenied = errors.New("denied")

func (d *provisionDeny) AuthorizeTenantTarget(_ context.Context, tenantID string, perm permission.Permission) error {
	d.asked = append(d.asked, tenantID+" "+string(perm))
	if d.deny[perm] {
		return errProvisionDenied
	}
	return nil
}

type provisionTenants struct {
	info *tenant.TenantInfo
	err  error
}

func (p provisionTenants) GetTenant(context.Context, string) (*tenant.TenantInfo, error) {
	return p.info, p.err
}

func TestProvisioningAuthorizedDatabaseService_RefusesBeforeCreating(t *testing.T) {
	inner := &provisionCreates{}
	authz := &provisionDeny{deny: map[permission.Permission]bool{permission.DatabasesCreateVolume: true}}
	svc := NewProvisioningAuthorizedDatabaseService(inner, authz, provisionTenants{info: &tenant.TenantInfo{ID: "vol", IsolationType: tenant.IsolationVolume}})
	ctx := tenant.WithTenantID(context.Background(), "vol")

	_, err := svc.CreateForTenant(ctx, database.DatabaseConfig{Name: "db", Backend: database.BackendPebble})
	var pde *ProvisioningDeniedError
	if !errors.As(err, &pde) || pde.Permission != permission.DatabasesCreateVolume || !errors.Is(err, errProvisionDenied) {
		t.Fatalf("CreateForTenant = %v, want *ProvisioningDeniedError for databases:create_volume wrapping the refusal", err)
	}
	if !strings.Contains(err.Error(), "databases:create_volume") {
		t.Errorf("error %q does not name the permission", err)
	}
	if len(inner.created) != 0 {
		t.Fatalf("a refused create reached the service: %v", inner.created)
	}
	if _, err := svc.(DatabaseCredentialCreator).CreateForTenantWithCredentials(ctx, database.DatabaseConfig{Name: "db"}, seal.Credentials{}); !errors.As(err, &pde) {
		t.Fatalf("CreateForTenantWithCredentials = %v, want the same refusal", err)
	}
	if _, err := svc.(DatabaseCredentialCreator).CreateWithCredentials(ctx, database.DatabaseConfig{Name: "db"}, seal.Credentials{}); !errors.As(err, &pde) {
		t.Fatalf("CreateWithCredentials = %v, want the same refusal", err)
	}
}

func TestProvisioningAuthorizedDatabaseService_Passes(t *testing.T) {
	inner := &provisionCreates{}
	authz := &provisionDeny{}
	svc := NewProvisioningAuthorizedDatabaseService(inner, authz, provisionTenants{info: &tenant.TenantInfo{ID: "acme", IsolationType: tenant.IsolationPrefix}})
	ctx := tenant.WithTenantID(context.Background(), "acme")

	if _, err := svc.CreateForTenant(ctx, database.DatabaseConfig{Name: "plain", Backend: database.BackendPebble}); err != nil {
		t.Fatalf("ordinary create: %v", err)
	}
	if len(authz.asked) != 0 {
		t.Fatalf("an ordinary create asked %v", authz.asked)
	}
	if _, err := svc.(DatabaseCredentialCreator).CreateForTenantWithCredentials(ctx, database.DatabaseConfig{Name: "group", Backend: database.BackendRaft}, seal.Credentials{}); err != nil {
		t.Fatalf("granted raft create: %v", err)
	}
	if !reflect.DeepEqual(authz.asked, []string{"acme databases:create_raft"}) {
		t.Fatalf("asked %v, want the raft check against acme", authz.asked)
	}
	if !reflect.DeepEqual(inner.created, []string{"plain", "group"}) {
		t.Fatalf("created %v", inner.created)
	}
	// Without credentialed creation inner cannot take a credentialed create.
	var unsupported *CapabilityUnsupportedError
	if _, err := svc.(DatabaseCredentialCreator).CreateWithCredentials(ctx, database.DatabaseConfig{Name: "c"}, seal.Credentials{}); !errors.As(err, &unsupported) {
		t.Fatalf("CreateWithCredentials over a plain service = %v, want *CapabilityUnsupportedError", err)
	}
}

// A tenant whose tier cannot be read is refused with the lookup's error: the
// tier decides what the create provisions.
func TestProvisioningAuthorizedDatabaseService_TenantLookupFailsClosed(t *testing.T) {
	inner := &provisionCreates{}
	lookupErr := &TenantNotFoundError{TenantID: "ghost"}
	svc := NewProvisioningAuthorizedDatabaseService(inner, &provisionDeny{}, provisionTenants{err: lookupErr})
	_, err := svc.CreateForTenant(tenant.WithTenantID(context.Background(), "ghost"), database.DatabaseConfig{Name: "db"})
	if !errors.Is(err, lookupErr) || len(inner.created) != 0 {
		t.Fatalf("CreateForTenant = %v (created %v), want the lookup's refusal", err, inner.created)
	}
	// The system tenant is never looked up; only the backend decides.
	if _, err := svc.CreateForTenant(context.Background(), database.DatabaseConfig{Name: "sys"}); err != nil {
		t.Fatalf("system create: %v", err)
	}
}
