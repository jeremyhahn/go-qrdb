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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// fakeIsolationLookup answers GetTenant from a fixed map, keyed by tenant ID.
type fakeIsolationLookup struct {
	tenants map[string]*tenant.TenantInfo
}

func (f *fakeIsolationLookup) GetTenant(_ context.Context, tenantID string) (*tenant.TenantInfo, error) {
	info, ok := f.tenants[tenantID]
	if !ok {
		return nil, tenant.ErrTenantNotFound
	}
	return info, nil
}

type failingIsolationLookup struct{ err error }

func (f *failingIsolationLookup) GetTenant(context.Context, string) (*tenant.TenantInfo, error) {
	return nil, f.err
}

func TestSystemShardAuditResolver_RoutesSystemTenantToTheSystemShard(t *testing.T) {
	host := &mockHost{}
	r := NewSystemShardAuditResolver(host, 1, nil)
	gotHost, shardID, err := r.ResolveAuditShard(context.Background(), tenantid.System)
	if err != nil {
		t.Fatalf("ResolveAuditShard: %v", err)
	}
	if gotHost != host || shardID != 1 {
		t.Fatalf("resolved (%v, %d), want (%v, 1)", gotHost, shardID, host)
	}
}

func TestSystemShardAuditResolver_NilIsolationRoutesEveryTenantToTheSystemShard(t *testing.T) {
	host := &mockHost{}
	r := NewSystemShardAuditResolver(host, 1, nil)
	_, shardID, err := r.ResolveAuditShard(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ResolveAuditShard: %v", err)
	}
	if shardID != 1 {
		t.Fatalf("shardID = %d, want 1", shardID)
	}
}

func TestSystemShardAuditResolver_RoutesUnregisteredTenantToTheSystemShard(t *testing.T) {
	host := &mockHost{}
	r := NewSystemShardAuditResolver(host, 1, &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{}})
	_, shardID, err := r.ResolveAuditShard(context.Background(), "ghost")
	if err != nil {
		t.Fatalf("ResolveAuditShard: %v", err)
	}
	if shardID != 1 {
		t.Fatalf("shardID = %d, want 1", shardID)
	}
}

func TestSystemShardAuditResolver_RoutesPrefixAndBarrierTenantsToTheSystemShard(t *testing.T) {
	host := &mockHost{}
	lookup := &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{
		"acme":    {ID: "acme", IsolationType: tenant.IsolationPrefix},
		"widgets": {ID: "widgets", IsolationType: tenant.IsolationBarrier},
	}}
	r := NewSystemShardAuditResolver(host, 1, lookup)
	for _, id := range []string{"acme", "widgets"} {
		_, shardID, err := r.ResolveAuditShard(context.Background(), id)
		if err != nil {
			t.Fatalf("ResolveAuditShard(%q): %v", id, err)
		}
		if shardID != 1 {
			t.Fatalf("tenant %q resolved shard %d, want 1", id, shardID)
		}
	}
}

// TestSystemShardLogResolver_RefusesBarrierTenants: the log registry sits
// under the system barrier, so the log resolver refuses a barrier tenant.
func TestSystemShardLogResolver_RefusesBarrierTenants(t *testing.T) {
	lookup := &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{
		"widgets": {ID: "widgets", IsolationType: tenant.IsolationBarrier},
	}}
	r := NewSystemShardLogResolver(&mockHost{}, 1, lookup)
	_, _, err := r.ResolveAuditShard(context.Background(), "widgets")
	var refused *BarrierTenantLogNotSupportedError
	if !errors.As(err, &refused) || refused.TenantID != "widgets" {
		t.Fatalf("ResolveAuditShard = %v, want *BarrierTenantLogNotSupportedError for widgets", err)
	}
}

// TestSystemShardLogResolver_RoutesOtherTenantsAsTheAuditResolverDoes: only
// a barrier tenant is refused beyond what the audit resolver refuses.
func TestSystemShardLogResolver_RoutesOtherTenantsAsTheAuditResolverDoes(t *testing.T) {
	lookup := &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{
		"acme":  {ID: "acme", IsolationType: tenant.IsolationPrefix},
		"vault": {ID: "vault", IsolationType: tenant.IsolationVolume},
	}}
	r := NewSystemShardLogResolver(&mockHost{}, 1, lookup)
	for _, id := range []string{tenantid.System, "acme", "ghost"} {
		if _, shardID, err := r.ResolveAuditShard(context.Background(), id); err != nil || shardID != 1 {
			t.Fatalf("ResolveAuditShard(%q) = %d, %v; want shard 1", id, shardID, err)
		}
	}
	var dedicated *DedicatedTenantAuditNotSupportedError
	if _, _, err := r.ResolveAuditShard(context.Background(), "vault"); !errors.As(err, &dedicated) {
		t.Fatalf("ResolveAuditShard(vault) = %v, want *DedicatedTenantAuditNotSupportedError", err)
	}
}

// TestSystemShardAuditResolver_RefusesDedicatedIsolationTenants is the
// explicit-refusal property: a volume or raft tenant must never silently
// get the system shard's chain under its own segment.
func TestSystemShardAuditResolver_RefusesDedicatedIsolationTenants(t *testing.T) {
	host := &mockHost{}
	lookup := &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{
		"vault":  {ID: "vault", IsolationType: tenant.IsolationVolume},
		"shard1": {ID: "shard1", IsolationType: tenant.IsolationRaft},
	}}
	r := NewSystemShardAuditResolver(host, 1, lookup)
	for _, id := range []string{"vault", "shard1"} {
		_, _, err := r.ResolveAuditShard(context.Background(), id)
		var refused *DedicatedTenantAuditNotSupportedError
		if !errors.As(err, &refused) {
			t.Fatalf("tenant %q: err = %v, want *DedicatedTenantAuditNotSupportedError", id, err)
		}
		if refused.TenantID != id {
			t.Fatalf("refused.TenantID = %q, want %q", refused.TenantID, id)
		}
	}
}

func TestSystemShardAuditResolver_PropagatesLookupError(t *testing.T) {
	host := &mockHost{}
	lookupErr := errors.New("store unavailable")
	r := NewSystemShardAuditResolver(host, 1, &failingIsolationLookup{err: lookupErr})
	_, _, err := r.ResolveAuditShard(context.Background(), "acme")
	if !errors.Is(err, lookupErr) {
		t.Fatalf("err = %v, want %v", err, lookupErr)
	}
}

func TestDedicatedTenantAuditNotSupportedError_Error(t *testing.T) {
	err := &DedicatedTenantAuditNotSupportedError{TenantID: "vault", IsolationType: "volume"}
	if err.Error() == "" {
		t.Fatal("Error() returned empty string")
	}
}

func TestAuditFeatureNotActiveError_Error(t *testing.T) {
	err := &AuditFeatureNotActiveError{TenantID: "acme", ShardID: 1}
	if err.Error() == "" {
		t.Fatal("Error() returned empty string")
	}
}
