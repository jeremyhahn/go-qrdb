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
	"errors"
	"log/slog"
	"testing"
)

// statusChange is one BeginStatusChange call and what settled it.
type statusChange struct {
	tenantID string
	from, to Status
	settled  int
	err      error
}

// fakeStatusAuditor records every status change it is told of; a non-nil
// refuse makes BeginStatusChange refuse the change.
type fakeStatusAuditor struct {
	refuse  error
	changes []*statusChange
}

var _ StatusChangeAuditor = (*fakeStatusAuditor)(nil)

func (f *fakeStatusAuditor) BeginStatusChange(_ context.Context, tenantID string, from, to Status) (func(error), error) {
	c := &statusChange{tenantID: tenantID, from: from, to: to}
	f.changes = append(f.changes, c)
	if f.refuse != nil {
		return nil, f.refuse
	}
	return func(err error) { c.settled++; c.err = err }, nil
}

// statusAuditServices returns both shipped tenant services, each holding
// tenant "acme" (active) and wired to its own fake auditor.
func statusAuditServices(t *testing.T) map[string]struct {
	svc     TenantServicer
	auditor *fakeStatusAuditor
} {
	t.Helper()
	barrierSvc, _ := newTestService(t)
	kvSvc, err := NewKVTenantService(newMemKVStore(), slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	out := map[string]struct {
		svc     TenantServicer
		auditor *fakeStatusAuditor
	}{}
	for name, svc := range map[string]TenantServicer{"barrier": barrierSvc, "kv": kvSvc} {
		if _, err := svc.CreateTenant(context.Background(), "acme", "Acme", IsolationPrefix); err != nil {
			t.Fatalf("%s CreateTenant: %v", name, err)
		}
		a := &fakeStatusAuditor{}
		svc.(StatusAuditWiring).SetStatusChangeAuditor(a)
		out[name] = struct {
			svc     TenantServicer
			auditor *fakeStatusAuditor
		}{svc, a}
	}
	return out
}

func statusPatch(s Status) TenantPatch { return TenantPatch{Status: &s} }

// Each status change is reported once, with the old and the new status, and
// settled once with the store's answer.
func TestUpdateTenant_StatusChangeIsAudited(t *testing.T) {
	for name, c := range statusAuditServices(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if _, err := c.svc.UpdateTenant(ctx, "acme", statusPatch(StatusSuspended)); err != nil {
				t.Fatalf("suspend: %v", err)
			}
			if _, err := c.svc.UpdateTenant(ctx, "acme", statusPatch(StatusActive)); err != nil {
				t.Fatalf("reactivate: %v", err)
			}
			want := []statusChange{
				{tenantID: "acme", from: StatusActive, to: StatusSuspended, settled: 1},
				{tenantID: "acme", from: StatusSuspended, to: StatusActive, settled: 1},
			}
			if len(c.auditor.changes) != len(want) {
				t.Fatalf("audited %d changes, want %d", len(c.auditor.changes), len(want))
			}
			for i, w := range want {
				if got := *c.auditor.changes[i]; got != w {
					t.Fatalf("change %d = %+v, want %+v", i, got, w)
				}
			}
		})
	}
}

// An update that leaves the status as it was, and one the service refuses,
// audit nothing.
func TestUpdateTenant_NoStatusChangeAuditsNothing(t *testing.T) {
	for name, c := range statusAuditServices(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if _, err := c.svc.UpdateTenant(ctx, "acme", statusPatch(StatusActive)); err != nil {
				t.Fatalf("same status: %v", err)
			}
			if _, err := c.svc.UpdateTenant(ctx, "acme", TenantPatch{Name: "Acme Corp"}); err != nil {
				t.Fatalf("rename: %v", err)
			}
			var ie *InvalidStatusError
			if _, err := c.svc.UpdateTenant(ctx, "acme", statusPatch("paused")); !errors.As(err, &ie) {
				t.Fatalf("unknown status = %v, want *InvalidStatusError", err)
			}
			if n := len(c.auditor.changes); n != 0 {
				t.Fatalf("audited %d changes, want none", n)
			}
			if _, err := c.svc.UpdateTenant(ctx, "acme", statusPatch(StatusDisabled)); err != nil {
				t.Fatalf("disable: %v", err)
			}
			var te *StatusTransitionError
			if _, err := c.svc.UpdateTenant(ctx, "acme", statusPatch(StatusSuspended)); !errors.As(err, &te) {
				t.Fatalf("disabled -> suspended = %v, want *StatusTransitionError", err)
			}
			if n := len(c.auditor.changes); n != 1 {
				t.Fatalf("audited %d changes, want only the disable", n)
			}
		})
	}
}

// An auditor that refuses the change -- its record could not be written under
// a fail-closed policy -- refuses the update, and nothing is stored.
func TestUpdateTenant_AuditRefusalStoresNothing(t *testing.T) {
	refused := errors.New("audit trail unavailable")
	for name, c := range statusAuditServices(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			c.auditor.refuse = refused
			if _, err := c.svc.UpdateTenant(ctx, "acme", statusPatch(StatusSuspended)); !errors.Is(err, refused) {
				t.Fatalf("UpdateTenant = %v, want the auditor's refusal", err)
			}
			info, err := c.svc.GetTenant(ctx, "acme")
			if err != nil || info.Status != StatusActive {
				t.Fatalf("status after a refused audit = %q, %v; want active", info.Status, err)
			}
		})
	}
}

// A change the store fails is settled with the failure.
func TestUpdateTenant_StatusChangeSettlesStoreFailure(t *testing.T) {
	store := &putFaultKV{memKVStore: newMemKVStore()}
	svc, err := NewKVTenantService(store, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	ctx := context.Background()
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	a := &fakeStatusAuditor{}
	svc.(StatusAuditWiring).SetStatusChangeAuditor(a)
	store.failPut = true
	var se *StoreError
	if _, err := svc.UpdateTenant(ctx, "acme", statusPatch(StatusSuspended)); !errors.As(err, &se) {
		t.Fatalf("UpdateTenant = %v, want *StoreError", err)
	}
	if len(a.changes) != 1 || a.changes[0].settled != 1 || !errors.Is(a.changes[0].err, errKVFault) {
		t.Fatalf("changes = %+v, want one settled with the store's failure", a.changes)
	}
}

// With no auditor a change is stored unaudited; a nil-settle auditor is
// tolerated; WithStatusChangeAuditor wires and nil unwires.
func TestStatusAuditHook_Wiring(t *testing.T) {
	s := &TenantService{}
	a := &fakeStatusAuditor{}
	WithStatusChangeAuditor(a)(s)
	if ref := s.auditor.Load(); ref == nil || ref.auditor != a {
		t.Fatal("WithStatusChangeAuditor did not set the auditor")
	}
	s.SetStatusChangeAuditor(nil)
	if s.auditor.Load() != nil {
		t.Fatal("a nil auditor must unwire the auditor")
	}
	settle, err := s.beginStatusChange(context.Background(), "acme", StatusActive, StatusSuspended)
	if err != nil || settle == nil {
		t.Fatalf("unwired beginStatusChange = %v (settle nil: %t); want a no-op settle", err, settle == nil)
	}
	settle(nil)
	s.SetStatusChangeAuditor(nilSettleAuditor{})
	settle, err = s.beginStatusChange(context.Background(), "acme", StatusActive, StatusSuspended)
	if err != nil || settle == nil {
		t.Fatalf("nil-settle beginStatusChange = %v (settle nil: %t); want a no-op settle", err, settle == nil)
	}
	settle(nil)
}

// nilSettleAuditor admits every change and returns no settle function.
type nilSettleAuditor struct{}

func (nilSettleAuditor) BeginStatusChange(context.Context, string, Status, Status) (func(error), error) {
	return nil, nil
}
