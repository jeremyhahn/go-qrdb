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

package user

import (
	"context"
	"strconv"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// fakeAuditLogger captures emitted events for assertions.
type fakeAuditLogger struct {
	mu     sync.Mutex
	events []audit.AuditEvent
}

func (f *fakeAuditLogger) Emit(_ context.Context, ev audit.AuditEvent) error {
	f.mu.Lock()
	f.events = append(f.events, ev)
	f.mu.Unlock()
	return nil
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

func (f *fakeAuditLogger) findByAction(action string) (audit.AuditEvent, bool) {
	for _, ev := range f.snapshot() {
		if ev.Action == action {
			return ev, true
		}
	}
	return audit.AuditEvent{}, false
}

func newAuditTestStore(t *testing.T) (UserServicer, *fakeAuditLogger) {
	t.Helper()
	al := &fakeAuditLogger{}
	svc, err := NewStore(newMemKVStore(), newTestIDGen(t), WithAuditLogger(al))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return svc, al
}

func auditCtx() context.Context {
	return auth.WithIdentity(context.Background(), &auth.Identity{
		Subject:  "admin@system",
		TenantID: "acme",
	})
}

func TestUserAudit_Create(t *testing.T) {
	svc, al := newAuditTestStore(t)
	ctx := auditCtx()

	u, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "alice",
		Email:    "audit@example.com",
		TenantID: "acme",
		RoleIDs:  []string{"viewer"},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	ev, ok := al.findByAction("create")
	if !ok {
		t.Fatal("expected a create audit event")
	}
	if ev.Actor != "admin@system" {
		t.Errorf("Actor: got %q, want %q", ev.Actor, "admin@system")
	}
	if ev.Resource != "user:"+strconv.FormatUint(u.ID, 10) {
		t.Errorf("Resource: got %q, want user:%d", ev.Resource, u.ID)
	}
	if ev.Outcome != audit.OutcomeSuccess {
		t.Errorf("Outcome: got %q, want %q", ev.Outcome, audit.OutcomeSuccess)
	}
}

func TestUserAudit_Update(t *testing.T) {
	svc, al := newAuditTestStore(t)
	ctx := auditCtx()

	u := createTestUser(t, svc, "alice", "acme")
	if _, err := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:      u.ID,
		RoleIDs: []string{"viewer", "editor"},
	}); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}

	ev, ok := al.findByAction("update")
	if !ok {
		t.Fatal("expected an update audit event")
	}
	if ev.Resource != "user:"+strconv.FormatUint(u.ID, 10) {
		t.Errorf("Resource: got %q, want user:%d", ev.Resource, u.ID)
	}
	if changed, _ := ev.Attributes["roles_changed"].(bool); !changed {
		t.Errorf("expected roles_changed=true, got %v", ev.Attributes["roles_changed"])
	}
}

func TestUserAudit_Delete(t *testing.T) {
	svc, al := newAuditTestStore(t)
	ctx := auditCtx()

	u := createTestUser(t, svc, "alice", "acme")
	if err := svc.DeleteUser(ctx, u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	if ev, ok := al.findByAction("delete"); !ok {
		t.Fatal("expected a delete audit event")
	} else if ev.Resource != "user:"+strconv.FormatUint(u.ID, 10) {
		t.Errorf("Resource: got %q, want user:%d", ev.Resource, u.ID)
	}
}

func TestUserAudit_EnableDisable(t *testing.T) {
	svc, al := newAuditTestStore(t)
	ctx := auditCtx()

	u := createTestUser(t, svc, "alice", "acme")
	if err := svc.DisableUser(ctx, u.ID); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
	if err := svc.EnableUser(ctx, u.ID); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}

	if _, ok := al.findByAction("disable"); !ok {
		t.Error("expected a disable audit event")
	}
	if _, ok := al.findByAction("enable"); !ok {
		t.Error("expected an enable audit event")
	}
}

// TestUserAudit_NilLoggerNoPanic verifies mutations succeed and do not panic
// when no audit logger is configured (the default construction path).
func TestUserAudit_NilLoggerNoPanic(t *testing.T) {
	svc := newTestStore(t)
	ctx := auditCtx()
	u := createTestUser(t, svc, "alice", "acme")
	if err := svc.DeleteUser(ctx, u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
}
