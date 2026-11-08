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
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// auditedTenants returns a tenant service holding tenant "acme" (active),
// its status changes audited to logger under policy.
func auditedTenants(t *testing.T, logger audit.AuditLogger, policy audit.FailurePolicy) tenant.TenantServicer {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	if _, err := svc.CreateTenant(context.Background(), "acme", "Acme", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	svc.(tenant.StatusAuditWiring).SetStatusChangeAuditor(NewTenantStatusAudit(logger, policy))
	return svc
}

func setStatus(s tenant.Status) tenant.TenantPatch { return tenant.TenantPatch{Status: &s} }

// A status change is recorded as a permitted record before it is stored and
// its outcome after, both keyed by the affected tenant and carrying the
// caller, the old and the new status.
func TestTenantStatusAudit_RecordsTheChange(t *testing.T) {
	ring := audit.NewRingBuffer(16)
	svc := auditedTenants(t, ring, audit.FailClosed)
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "root"})
	if _, err := svc.UpdateTenant(ctx, "acme", setStatus(tenant.StatusSuspended)); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	evs := ring.Snapshot()
	if len(evs) != 2 {
		t.Fatalf("recorded %d events, want the permitted record and its outcome", len(evs))
	}
	for i, want := range []string{audit.OutcomePermitted, audit.OutcomeSuccess} {
		ev := evs[i]
		if ev.Action != ActionTenantStatusChange || ev.Outcome != want {
			t.Fatalf("event %d = %s/%s, want %s/%s", i, ev.Action, ev.Outcome, ActionTenantStatusChange, want)
		}
		if ev.TenantID != tenantid.System || ev.Resource != "tenant:acme" || ev.Actor != "root" {
			t.Fatalf("event %d = %+v, want TenantID=acme Resource=tenant Actor=root", i, ev)
		}
		if ev.Attributes["from"] != string(tenant.StatusActive) || ev.Attributes["to"] != string(tenant.StatusSuspended) {
			t.Fatalf("event %d attributes = %v, want from=active to=suspended", i, ev.Attributes)
		}
	}
	if _, err := svc.UpdateTenant(ctx, "acme", tenant.TenantPatch{Name: "Acme Corp"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if n := len(ring.Snapshot()); n != 2 {
		t.Fatalf("a rename recorded %d more events, want none", n-2)
	}
}

// Under the default fail-closed policy a record that cannot be written
// refuses the change; under fail-open the change is stored regardless.
func TestTenantStatusAudit_GateFollowsThePolicy(t *testing.T) {
	ctx := context.Background()
	sink := &refusingAuditLogger{err: errors.New("audit sink unavailable")}
	closed := auditedTenants(t, sink, audit.FailClosed)
	var gate *audit.GateError
	if _, err := closed.UpdateTenant(ctx, "acme", setStatus(tenant.StatusSuspended)); !errors.As(err, &gate) {
		t.Fatalf("fail-closed UpdateTenant = %v, want *audit.GateError", err)
	}
	if info, _ := closed.GetTenant(ctx, "acme"); info.Status != tenant.StatusActive {
		t.Fatalf("a refused audit stored status %q", info.Status)
	}

	open := auditedTenants(t, sink, audit.FailOpen)
	if _, err := open.UpdateTenant(ctx, "acme", setStatus(tenant.StatusSuspended)); err != nil {
		t.Fatalf("fail-open UpdateTenant: %v", err)
	}
	if info, _ := open.GetTenant(ctx, "acme"); info.Status != tenant.StatusSuspended {
		t.Fatalf("fail-open status = %q, want suspended", info.Status)
	}
}

// A change the store fails settles as a failure; with no logger there is no
// auditor to wire.
func TestTenantStatusAudit_FailureOutcomeAndNilLogger(t *testing.T) {
	ring := audit.NewRingBuffer(4)
	settle, err := NewTenantStatusAudit(ring, audit.FailClosed).BeginStatusChange(context.Background(), "acme", tenant.StatusActive, tenant.StatusDisabled)
	if err != nil {
		t.Fatalf("BeginStatusChange: %v", err)
	}
	settle(errors.New("store down"))
	ev := findEvent(ring.Snapshot(), ActionTenantStatusChange)
	if ev == nil || ev.Outcome != audit.OutcomeFailure+":status-change-failed" || ev.Actor != "" {
		t.Fatalf("outcome event = %+v, want an anonymous failure", ev)
	}
	if a := NewTenantStatusAudit(nil, audit.FailClosed); a != nil {
		t.Fatalf("NewTenantStatusAudit(nil) = %v, want nil", a)
	}
}
