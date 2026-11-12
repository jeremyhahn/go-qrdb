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
	svc := NewProvisioningAuthorizedDatabaseService(inner, authz, provisionTenants{info: &tenant.TenantInfo{ID: "vol", IsolationType: tenant.IsolationVolume}}, nil)
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
	svc := NewProvisioningAuthorizedDatabaseService(inner, authz, provisionTenants{info: &tenant.TenantInfo{ID: "acme", IsolationType: tenant.IsolationPrefix}}, nil)
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

// TestProvisioningAuthorizedDatabaseService_ChecksResolvedDefaults pins that
// a create omitting its backend under a node raft default is authorized as
// the raft database the manager records: databases:create_raft is required.
func TestProvisioningAuthorizedDatabaseService_ChecksResolvedDefaults(t *testing.T) {
	resolve := func(cfg database.DatabaseConfig) database.DatabaseConfig {
		if cfg.Backend == "" {
			cfg.Backend = database.BackendRaft
		}
		return cfg
	}
	prefix := provisionTenants{info: &tenant.TenantInfo{ID: "acme", IsolationType: tenant.IsolationPrefix}}
	ctx := tenant.WithTenantID(context.Background(), "acme")

	inner := &provisionCreates{}
	denied := &provisionDeny{deny: map[permission.Permission]bool{permission.DatabasesCreateRaft: true}}
	svc := NewProvisioningAuthorizedDatabaseService(inner, denied, prefix, resolve)
	_, err := svc.CreateForTenant(ctx, database.DatabaseConfig{Name: "dflt"})
	var refused *ProvisioningDeniedError
	if !errors.As(err, &refused) || refused.Permission != permission.DatabasesCreateRaft {
		t.Fatalf("defaulted raft create without databases:create_raft = %v, want *ProvisioningDeniedError for it", err)
	}
	if len(inner.created) != 0 {
		t.Fatalf("a refused create reached the service: %v", inner.created)
	}

	granted := &provisionDeny{}
	svc = NewProvisioningAuthorizedDatabaseService(inner, granted, prefix, resolve)
	if _, err := svc.CreateForTenant(ctx, database.DatabaseConfig{Name: "dflt"}); err != nil {
		t.Fatalf("defaulted raft create with databases:create_raft: %v", err)
	}
	if !reflect.DeepEqual(granted.asked, []string{"acme databases:create_raft"}) {
		t.Fatalf("asked %v, want the raft check against acme", granted.asked)
	}
	if !reflect.DeepEqual(inner.created, []string{"dflt"}) {
		t.Fatalf("created %v", inner.created)
	}
}

// A tenant whose tier cannot be read is refused with the lookup's error: the
// tier decides what the create provisions.
func TestProvisioningAuthorizedDatabaseService_TenantLookupFailsClosed(t *testing.T) {
	inner := &provisionCreates{}
	lookupErr := &TenantNotFoundError{TenantID: "ghost"}
	svc := NewProvisioningAuthorizedDatabaseService(inner, &provisionDeny{}, provisionTenants{err: lookupErr}, nil)
	_, err := svc.CreateForTenant(tenant.WithTenantID(context.Background(), "ghost"), database.DatabaseConfig{Name: "db"})
	if !errors.Is(err, lookupErr) || len(inner.created) != 0 {
		t.Fatalf("CreateForTenant = %v (created %v), want the lookup's refusal", err, inner.created)
	}
	// The system tenant is never looked up; only the backend decides.
	if _, err := svc.CreateForTenant(context.Background(), database.DatabaseConfig{Name: "sys"}); err != nil {
		t.Fatalf("system create: %v", err)
	}
}

// provisionHolders is an inner service that also implements
// DatabaseHolderServicer, recording the database name every call received.
type provisionHolders struct {
	DatabaseServicer
	calls []string
}

func (p *provisionHolders) DatabaseBarrierOfficerRegister(_ context.Context, name string, _ *BarrierOfficerRegisterRequest) (*BarrierOfficerRegisterResult, error) {
	p.calls = append(p.calls, "register:"+name)
	return &BarrierOfficerRegisterResult{Role: "so"}, nil
}

func (p *provisionHolders) DatabaseBarrierOfficerList(_ context.Context, name string) ([]*BarrierOfficerStatus, error) {
	p.calls = append(p.calls, "list:"+name)
	return nil, nil
}

func (p *provisionHolders) DatabaseBarrierShamirInitialize(_ context.Context, name string, _ seal.FactorPolicy) (*ShamirInitInfo, error) {
	p.calls = append(p.calls, "init-shamir:"+name)
	return &ShamirInitInfo{}, nil
}

func (p *provisionHolders) DatabaseBarrierShareClaimBegin(_ context.Context, name, username, _ string) (*BarrierShareClaimChallenge, error) {
	p.calls = append(p.calls, "claim-begin:"+name+":"+username)
	return &BarrierShareClaimChallenge{}, nil
}

func (p *provisionHolders) DatabaseBarrierShareClaimComplete(_ context.Context, name, username, _ string, _ []byte, _ string) (*BarrierShareClaimResult, error) {
	p.calls = append(p.calls, "claim-complete:"+name+":"+username)
	return &BarrierShareClaimResult{}, nil
}

func (p *provisionHolders) DatabaseBarrierQuorumBegin(_ context.Context, name string, _ *QuorumBeginRequest) (*QuorumChallengeInfo, error) {
	p.calls = append(p.calls, "quorum-begin:"+name)
	return &QuorumChallengeInfo{}, nil
}

func (p *provisionHolders) DatabaseBarrierQuorumSubmit(_ context.Context, name string, _ *QuorumSubmitRequest) (*QuorumOutcomeInfo, error) {
	p.calls = append(p.calls, "quorum-submit:"+name)
	return &QuorumOutcomeInfo{}, nil
}

func (p *provisionHolders) DatabaseBarrierRekeyStart(_ context.Context, name string, _ int, _ seal.FactorPolicy) (*QuorumRoundInfo, error) {
	p.calls = append(p.calls, "rekey:"+name)
	return &QuorumRoundInfo{}, nil
}

func (p *provisionHolders) DatabaseBarrierOfficerRevoke(_ context.Context, name, username string, _ int) (*QuorumRoundInfo, error) {
	p.calls = append(p.calls, "revoke:"+name+":"+username)
	return &QuorumRoundInfo{}, nil
}

func (p *provisionHolders) DatabaseBarrierOfficerUnenrol(_ context.Context, name, username string) error {
	p.calls = append(p.calls, "unenrol:"+name+":"+username)
	return nil
}

// TestProvisioningAuthorizedDatabaseService_HolderMethods_Unsupported proves
// every DatabaseHolderServicer method the wrapper declares refuses with
// *CapabilityUnsupportedError, not a type-assertion panic or a silent
// 501, when inner does not implement database share-holder custody -- the
// gap go-qrdb found in the production composition: NewProvisioningAuthorizedDatabaseService
// embeds the DatabaseServicer INTERFACE, which only promotes DatabaseServicer's
// own methods, not any additive interface (DatabaseHolderServicer included)
// the underlying *DatabaseService also happens to implement.
func TestProvisioningAuthorizedDatabaseService_HolderMethods_Unsupported(t *testing.T) {
	inner := &provisionCreates{}
	svc := NewProvisioningAuthorizedDatabaseService(inner, &provisionDeny{}, provisionTenants{}, nil)
	hs, ok := svc.(DatabaseHolderServicer)
	if !ok {
		t.Fatalf("NewProvisioningAuthorizedDatabaseService does not implement DatabaseHolderServicer")
	}
	ctx := context.Background()

	cases := map[string]func() error{
		"register": func() error {
			_, err := hs.DatabaseBarrierOfficerRegister(ctx, "db", &BarrierOfficerRegisterRequest{})
			return err
		},
		"list":        func() error { _, err := hs.DatabaseBarrierOfficerList(ctx, "db"); return err },
		"init-shamir": func() error { _, err := hs.DatabaseBarrierShamirInitialize(ctx, "db", ""); return err },
		"claim-begin": func() error { _, err := hs.DatabaseBarrierShareClaimBegin(ctx, "db", "alice", ""); return err },
		"claim-complete": func() error {
			_, err := hs.DatabaseBarrierShareClaimComplete(ctx, "db", "alice", "nonce", nil, "")
			return err
		},
		"quorum-begin":  func() error { _, err := hs.DatabaseBarrierQuorumBegin(ctx, "db", &QuorumBeginRequest{}); return err },
		"quorum-submit": func() error { _, err := hs.DatabaseBarrierQuorumSubmit(ctx, "db", &QuorumSubmitRequest{}); return err },
		"rekey":         func() error { _, err := hs.DatabaseBarrierRekeyStart(ctx, "db", 2, ""); return err },
		"revoke":        func() error { _, err := hs.DatabaseBarrierOfficerRevoke(ctx, "db", "alice", 2); return err },
		"unenrol":       func() error { return hs.DatabaseBarrierOfficerUnenrol(ctx, "db", "alice") },
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			var unsupported *CapabilityUnsupportedError
			if err := call(); !errors.As(err, &unsupported) {
				t.Fatalf("%s over a plain service = %v, want *CapabilityUnsupportedError", name, err)
			}
		})
	}
}

// TestProvisioningAuthorizedDatabaseService_HolderMethods_Delegate proves
// every DatabaseHolderServicer method is a pure passthrough to inner when
// inner supports database share-holder custody: no provisioning-permission
// check runs (authz.asked stays empty), and inner actually receives the call.
func TestProvisioningAuthorizedDatabaseService_HolderMethods_Delegate(t *testing.T) {
	inner := &provisionHolders{}
	authz := &provisionDeny{}
	svc := NewProvisioningAuthorizedDatabaseService(inner, authz, provisionTenants{}, nil)
	hs, ok := svc.(DatabaseHolderServicer)
	if !ok {
		t.Fatalf("NewProvisioningAuthorizedDatabaseService does not implement DatabaseHolderServicer")
	}
	ctx := context.Background()

	if _, err := hs.DatabaseBarrierOfficerRegister(ctx, "vault", &BarrierOfficerRegisterRequest{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := hs.DatabaseBarrierOfficerList(ctx, "vault"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, err := hs.DatabaseBarrierShamirInitialize(ctx, "vault", ""); err != nil {
		t.Fatalf("init-shamir: %v", err)
	}
	if _, err := hs.DatabaseBarrierShareClaimBegin(ctx, "vault", "alice", ""); err != nil {
		t.Fatalf("claim-begin: %v", err)
	}
	if _, err := hs.DatabaseBarrierShareClaimComplete(ctx, "vault", "alice", "nonce", nil, ""); err != nil {
		t.Fatalf("claim-complete: %v", err)
	}
	if _, err := hs.DatabaseBarrierQuorumBegin(ctx, "vault", &QuorumBeginRequest{}); err != nil {
		t.Fatalf("quorum-begin: %v", err)
	}
	if _, err := hs.DatabaseBarrierQuorumSubmit(ctx, "vault", &QuorumSubmitRequest{}); err != nil {
		t.Fatalf("quorum-submit: %v", err)
	}
	if _, err := hs.DatabaseBarrierRekeyStart(ctx, "vault", 2, ""); err != nil {
		t.Fatalf("rekey: %v", err)
	}
	if _, err := hs.DatabaseBarrierOfficerRevoke(ctx, "vault", "alice", 2); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := hs.DatabaseBarrierOfficerUnenrol(ctx, "vault", "alice"); err != nil {
		t.Fatalf("unenrol: %v", err)
	}

	want := []string{
		"register:vault", "list:vault", "init-shamir:vault", "claim-begin:vault:alice",
		"claim-complete:vault:alice", "quorum-begin:vault", "quorum-submit:vault",
		"rekey:vault", "revoke:vault:alice", "unenrol:vault:alice",
	}
	if !reflect.DeepEqual(inner.calls, want) {
		t.Fatalf("inner.calls = %v, want %v", inner.calls, want)
	}
	if len(authz.asked) != 0 {
		t.Fatalf("holder methods asked a provisioning permission: %v", authz.asked)
	}
}
