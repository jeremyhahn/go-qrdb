// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package role

import (
	"context"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// fakeAuditLogger captures emitted events for assertions.
type fakeAuditLogger struct {
	mu     sync.Mutex
	events []audit.AuditEvent
}

func (f *fakeAuditLogger) Emit(_ context.Context, ev audit.AuditEvent) {
	f.mu.Lock()
	f.events = append(f.events, ev)
	f.mu.Unlock()
}

func (f *fakeAuditLogger) Query(_ context.Context, _ audit.Filter) (*audit.Page, error) {
	return &audit.Page{Total: -1}, nil
}

func (f *fakeAuditLogger) Close() error { return nil }

func (f *fakeAuditLogger) snapshot() []audit.AuditEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]audit.AuditEvent, len(f.events))
	copy(out, f.events)
	return out
}

// findByAction returns the first captured event with the given action, or a
// zero event and false when absent.
func (f *fakeAuditLogger) findByAction(action string) (audit.AuditEvent, bool) {
	for _, ev := range f.snapshot() {
		if ev.Action == action {
			return ev, true
		}
	}
	return audit.AuditEvent{}, false
}

func newAuditTestStore(t *testing.T) (RoleServicer, *fakeAuditLogger) {
	t.Helper()
	al := &fakeAuditLogger{}
	svc, err := NewStore(newMemKVStore(), WithAuditLogger(al))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return svc, al
}

// auditCtx returns a context carrying an acting identity.
func auditCtx() context.Context {
	return auth.WithIdentity(context.Background(), &auth.Identity{
		Subject:  "admin@system",
		TenantID: "acme",
	})
}

func TestRoleAudit_Create(t *testing.T) {
	svc, al := newAuditTestStore(t)
	ctx := auditCtx()

	r, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:        "editor",
		TenantID:    "acme",
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	ev, ok := al.findByAction("create")
	if !ok {
		t.Fatal("expected a create audit event")
	}
	if ev.Actor != "admin@system" {
		t.Errorf("Actor: got %q, want %q", ev.Actor, "admin@system")
	}
	if ev.TenantID != "acme" {
		t.Errorf("TenantID: got %q, want %q", ev.TenantID, "acme")
	}
	if ev.Resource != "role:"+r.ID {
		t.Errorf("Resource: got %q, want %q", ev.Resource, "role:"+r.ID)
	}
	if ev.Outcome != audit.OutcomeSuccess {
		t.Errorf("Outcome: got %q, want %q", ev.Outcome, audit.OutcomeSuccess)
	}
}

func TestRoleAudit_Update(t *testing.T) {
	svc, al := newAuditTestStore(t)
	ctx := auditCtx()

	r := createTestRole(t, svc, "editor", "acme")
	if _, err := svc.UpdateRole(ctx, &UpdateRoleRequest{
		ID:          r.ID,
		Permissions: []permission.Permission{permission.KVGet, permission.KVDelete},
	}); err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}

	ev, ok := al.findByAction("update")
	if !ok {
		t.Fatal("expected an update audit event")
	}
	if ev.Resource != "role:"+r.ID {
		t.Errorf("Resource: got %q, want %q", ev.Resource, "role:"+r.ID)
	}
	if changed, _ := ev.Attributes["permissions_changed"].(bool); !changed {
		t.Errorf("expected permissions_changed=true, got %v", ev.Attributes["permissions_changed"])
	}
}

func TestRoleAudit_Delete(t *testing.T) {
	svc, al := newAuditTestStore(t)
	ctx := auditCtx()

	r := createTestRole(t, svc, "editor", "acme")
	if err := svc.DeleteRole(ctx, r.ID); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}

	ev, ok := al.findByAction("delete")
	if !ok {
		t.Fatal("expected a delete audit event")
	}
	if ev.Resource != "role:"+r.ID {
		t.Errorf("Resource: got %q, want %q", ev.Resource, "role:"+r.ID)
	}
	if ev.Actor != "admin@system" {
		t.Errorf("Actor: got %q, want %q", ev.Actor, "admin@system")
	}
}

// TestRoleAudit_NilLoggerNoPanic verifies mutations succeed and do not panic
// when no audit logger is configured (the default construction path).
func TestRoleAudit_NilLoggerNoPanic(t *testing.T) {
	svc := newTestStore(t)
	ctx := auditCtx()
	r := createTestRole(t, svc, "editor", "acme")
	if err := svc.DeleteRole(ctx, r.ID); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}
}
