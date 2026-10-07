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
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// policyView is a database's MVCC view as the registry hands it out, holding
// its compaction policy as the namespaced view does.
type policyView struct {
	MVCCKVServicer
	policy    *compaction.Policy
	noHistory bool
	readErr   error
}

func (v *policyView) KeepsRevisionHistory() bool { return !v.noHistory }

func (v *policyView) CompactionPolicy(context.Context) (*compaction.Policy, error) {
	return v.policy, v.readErr
}

func (v *policyView) SetCompactionPolicy(_ context.Context, p *compaction.Policy) error {
	v.policy = p.Normalized()
	return nil
}

// policyRegistry resolves (tenant, db) to its policyView; any other pair is
// refused as the real registry refuses another tenant's database.
type policyRegistry struct {
	Registry
	views map[string]*policyView
}

func (r *policyRegistry) MVCCService(tenantID, db string) (MVCCKVServicer, error) {
	if v, ok := r.views[tenantID+"/"+db]; ok {
		return v, nil
	}
	return nil, &database.NotFoundError{Name: db}
}

func newCompactionDatabaseService(t *testing.T) (*DatabaseService, *policyRegistry, *database.Manager) {
	t.Helper()
	mgr := database.NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	for _, cfg := range []database.DatabaseConfig{
		{Name: "acme-db", TenantID: "acme", Backend: database.BackendMemory},
		{Name: "sys-db", Backend: database.BackendMemory},
	} {
		if _, err := mgr.Create(cfg); err != nil {
			t.Fatal(err)
		}
	}
	reg := &policyRegistry{views: map[string]*policyView{
		"acme/acme-db":              {},
		tenantid.System + "/sys-db": {},
	}}
	svc := NewDatabaseService(mgr).(*DatabaseService)
	svc.SetCompactionPolicies(NewCompactionPolicies(reg))
	return svc, reg, mgr
}

// UpdateCompactionForTenant confines every caller -- the system tenant
// included -- to its own databases, writes the policy through the database's
// own view, and reports it back; a Get reports it from the same place.
func TestDatabaseService_UpdateCompactionForTenant(t *testing.T) {
	svc, reg, _ := newCompactionDatabaseService(t)
	policy := &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 2}
	acme := tenant.WithTenantID(context.Background(), "acme")
	system := tenant.WithTenantID(context.Background(), tenantid.System)

	info, err := svc.UpdateCompactionForTenant(acme, "acme-db", policy)
	if err != nil || info.Compaction == nil || info.Compaction.RetainRevisions != 2 {
		t.Fatalf("acme on its own database = %+v, %v", info, err)
	}
	if reg.views["acme/acme-db"].policy == nil {
		t.Fatal("the policy was not written through the database's view")
	}
	if info, err = svc.GetForTenant(acme, "acme-db"); err != nil || info.Compaction == nil {
		t.Fatalf("GetForTenant reports %+v, %v", info, err)
	}
	if info, err = svc.UpdateCompactionForTenant(system, "sys-db", policy); err != nil || info.Compaction == nil {
		t.Fatalf("system on its own database = %+v, %v", info, err)
	}

	var nf *database.NotFoundError
	if _, err := svc.UpdateCompactionForTenant(system, "acme-db", nil); !errors.As(err, &nf) {
		t.Fatalf("system on acme's database = %v, want NotFoundError", err)
	}
	if _, err := svc.UpdateCompactionForTenant(acme, "sys-db", nil); !errors.As(err, &nf) {
		t.Fatalf("acme on the system tenant's database = %v, want NotFoundError", err)
	}
	if reg.views["acme/acme-db"].policy == nil {
		t.Fatal("a refused caller cleared acme's policy")
	}

	if info, err = svc.UpdateCompactionForTenant(acme, "acme-db", nil); err != nil || info.Compaction != nil {
		t.Fatalf("turning the policy off = %+v, %v", info, err)
	}
}

// An invalid policy writes nothing; a service with no port, or a database
// whose store keeps no MVCC history, refuses rather than storing a policy
// anywhere node-local.
func TestDatabaseService_UpdateCompactionForTenant_Refusals(t *testing.T) {
	svc, reg, mgr := newCompactionDatabaseService(t)
	acme := tenant.WithTenantID(context.Background(), "acme")

	var invalid *compaction.InvalidPolicyError
	if _, err := svc.UpdateCompactionForTenant(acme, "acme-db", &compaction.Policy{Mode: compaction.Revision}); !errors.As(err, &invalid) {
		t.Fatalf("an invalid policy = %v", err)
	}
	if reg.views["acme/acme-db"].policy != nil {
		t.Fatal("an invalid policy was written")
	}

	reg.views["acme/acme-db"].noHistory = true
	var unavailable *CompactionPolicyUnavailableError
	if _, err := svc.UpdateCompactionForTenant(acme, "acme-db", &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 1}); !errors.As(err, &unavailable) {
		t.Fatalf("a store without history = %v", err)
	}
	if info, err := svc.GetForTenant(acme, "acme-db"); err != nil || info.Compaction != nil {
		t.Fatalf("GetForTenant on a store without history = %+v, %v", info, err)
	}

	bare := NewDatabaseService(mgr)
	if _, err := bare.UpdateCompactionForTenant(acme, "acme-db", nil); !errors.As(err, &unavailable) {
		t.Fatalf("a service with no port = %v", err)
	}
}

// GetForTenant reports the policy from the database's store. A store that
// cannot be read -- a degraded raft database -- does not fail the call: the
// rest of the database's information is still reported, with the policy
// marked unavailable rather than reported as off. A sealed database's views
// are refused by design, so its policy is not reported while it is sealed.
func TestDatabaseService_GetForTenant_ReportsThePolicy(t *testing.T) {
	svc, reg, mgr := newCompactionDatabaseService(t)
	acme := tenant.WithTenantID(context.Background(), "acme")
	reg.views["acme/acme-db"].readErr = errors.New("unavailable")
	info, err := svc.GetForTenant(acme, "acme-db")
	if err != nil {
		t.Fatalf("an unreadable store failed the whole call: %v", err)
	}
	if info.Name != "acme-db" || info.Compaction != nil || !info.CompactionUnavailable {
		t.Fatalf("info = %+v; want the database reported with its policy unavailable", info)
	}

	// An update that committed is reported as done, with the same marker.
	reg.views["acme/acme-db"].readErr = nil
	if _, err := svc.UpdateCompactionForTenant(acme, "acme-db", &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 2}); err != nil {
		t.Fatalf("UpdateCompactionForTenant: %v", err)
	}
	reg.views["acme/acme-db"].readErr = errors.New("unavailable")
	if _, err := svc.UpdateCompactionForTenant(acme, "acme-db", &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 3}); err != nil {
		t.Fatalf("a committed update reported a failure because the read-back failed: %v", err)
	}

	reg.views["acme/acme-db"].readErr = nil
	if info, err := svc.GetForTenant(acme, "acme-db"); err != nil || info.CompactionUnavailable || info.Compaction == nil {
		t.Fatalf("a readable store = %+v, %v; want the policy and no marker", info, err)
	}

	db, _ := mgr.Get("acme-db")
	db.SetStatus(database.StatusSealed)
	if info, err := svc.GetForTenant(acme, "acme-db"); err != nil || info.Compaction != nil {
		t.Fatalf("a sealed database = %+v, %v", info, err)
	}
}
