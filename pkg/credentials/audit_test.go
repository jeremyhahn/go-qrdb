// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package credentials

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func auditCtx() context.Context {
	return auth.WithIdentity(context.Background(), &auth.Identity{
		Subject:  "admin@system",
		TenantID: "acme",
	})
}

func newAuditManualService(t *testing.T) (*CredentialService, *fakeAuditLogger) {
	t.Helper()
	al := &fakeAuditLogger{}
	svc, err := NewCredentialService(nil, nil, testLogger(), WithAuditLogger(al))
	require.NoError(t, err)
	return svc, al
}

func TestCredentialAudit_Submit(t *testing.T) {
	svc, al := newAuditManualService(t)
	ctx := auditCtx()

	require.NoError(t, svc.SubmitCredential(ctx, "db-password", []byte("s3cr3t")))

	ev, ok := al.findByAction("submit")
	require.True(t, ok, "expected a submit audit event")
	assert.Equal(t, "admin@system", ev.Actor)
	assert.Equal(t, "acme", ev.TenantID)
	assert.Equal(t, "credential:db-password", ev.Resource)
	assert.Equal(t, audit.OutcomeSuccess, ev.Outcome)
}

func TestCredentialAudit_Store(t *testing.T) {
	svc, al := newAuditManualService(t)
	ctx := auditCtx()

	require.NoError(t, svc.StoreCredential(ctx, "api-key", []byte("k")))

	ev, ok := al.findByAction("store")
	require.True(t, ok, "expected a store audit event")
	assert.Equal(t, "credential:api-key", ev.Resource)
}

func TestCredentialAudit_Rotate(t *testing.T) {
	svc, al := newAuditManualService(t)
	ctx := auditCtx()

	require.NoError(t, svc.StoreCredential(ctx, "db-password", []byte("v1")))
	_, err := svc.RotateCredential(ctx, "db-password", RotateOptions{NewValue: []byte("v2")})
	require.NoError(t, err)

	ev, ok := al.findByAction("rotate")
	require.True(t, ok, "expected a rotate audit event")
	assert.Equal(t, "credential:db-password", ev.Resource)
	assert.Equal(t, uint64(2), ev.Attributes["new_version"])
}

func TestCredentialAudit_Delete(t *testing.T) {
	svc, al := newAuditManualService(t)
	ctx := auditCtx()

	require.NoError(t, svc.StoreCredential(ctx, "db-password", []byte("v1")))
	require.NoError(t, svc.DeleteCredential(ctx, "db-password"))

	ev, ok := al.findByAction("delete")
	require.True(t, ok, "expected a delete audit event")
	assert.Equal(t, "credential:db-password", ev.Resource)
}

// TestCredentialAudit_SetAfterConstruction verifies the post-construction
// setter path used by the daemon (the seal subsystem builds the service
// before the compliance logger exists).
func TestCredentialAudit_SetAfterConstruction(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)
	al := &fakeAuditLogger{}
	svc.SetAuditLogger(al)

	require.NoError(t, svc.StoreCredential(auditCtx(), "late", []byte("v")))
	_, ok := al.findByAction("store")
	require.True(t, ok, "expected a store audit event after SetAuditLogger")
}

// TestCredentialAudit_NilLoggerNoPanic verifies mutations succeed without an
// audit logger configured (the default construction path).
func TestCredentialAudit_NilLoggerNoPanic(t *testing.T) {
	svc, err := NewCredentialService(nil, nil, testLogger())
	require.NoError(t, err)
	require.NoError(t, svc.StoreCredential(auditCtx(), "x", []byte("v")))
	require.NoError(t, svc.DeleteCredential(auditCtx(), "x"))
}
