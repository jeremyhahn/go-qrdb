//go:build !integration

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// newTestCredentialBinding wires a CredentialBinding to an embedded client
// backed by svc.
func newTestCredentialBinding(t *testing.T, svc *mockCredSvc) *CredentialBinding {
	t.Helper()
	conn := newTestConnectionBinding(t, &noopKVService{}, newMockAdminService(),
		embedded.WithCredentialService(svc))
	return NewCredentialBinding(testLogger(t), conn)
}

// newTestLockBinding wires a LockBinding to an embedded client backed by svc.
func newTestLockBinding(t *testing.T, svc *mockLockService) *LockBinding {
	t.Helper()
	conn := newTestConnectionBinding(t, &noopKVService{}, newMockAdminService(),
		embedded.WithLockService(svc))
	return NewLockBinding(testLogger(t), conn)
}

// assertNilService fails unless err is the embedded transport's
// NilServiceError, returned when the service behind a call is not wired.
func assertNilService(t *testing.T, err error) {
	t.Helper()
	var ns *embedded.NilServiceError
	if !errors.As(err, &ns) {
		t.Fatalf("want *embedded.NilServiceError, got %T: %v", err, err)
	}
}

func TestCredentialBinding_RoundTrip(t *testing.T) {
	svc := newMockCredSvc()
	svc.reaped = 3
	b := newTestCredentialBinding(t, svc)

	if err := b.Submit("submitted", "s1"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := b.Store("stored", "v1"); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, err := b.Retrieve("stored")
	if err != nil || got != "v1" {
		t.Fatalf("Retrieve = %q, %v; want v1", got, err)
	}
	names, err := b.List()
	if err != nil || len(names) != 2 {
		t.Fatalf("List = %v, %v; want two names", names, err)
	}
	strategy, err := b.Strategy()
	if err != nil || strategy != "software" {
		t.Fatalf("Strategy = %q, %v; want software", strategy, err)
	}
	rot, err := b.Rotate(&transport.CredentialRotateRequest{Name: "stored", NewValue: []byte("v2")})
	if err != nil || rot.NewVersion != 2 {
		t.Fatalf("Rotate = %+v, %v; want new version 2", rot, err)
	}
	svc.versions["stored"] = []credentials.CredentialVersion{{Version: 1}, {Version: 2, IsCurrent: true}}
	vers, err := b.ListVersions("stored")
	if err != nil || len(vers.Versions) != 2 {
		t.Fatalf("ListVersions = %+v, %v; want two versions", vers, err)
	}
	got, err = b.RetrieveVersion("stored", 2)
	if err != nil || got != "v2" {
		t.Fatalf("RetrieveVersion = %q, %v; want v2", got, err)
	}
	reaped, err := b.GCExpired("stored")
	if err != nil || reaped != 3 {
		t.Fatalf("GCExpired = %d, %v; want 3", reaped, err)
	}
	if err := b.Delete("stored"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := svc.store["stored"]; ok {
		t.Fatal("Delete left the credential in the service")
	}
}

// credentialBindingCalls exercises every CredentialBinding method once and
// returns each call's error, keyed by method name.
func credentialBindingCalls(b *CredentialBinding) map[string]error {
	errs := map[string]error{}
	errs["Submit"] = b.Submit("n", "v")
	errs["Store"] = b.Store("n", "v")
	_, errs["Retrieve"] = b.Retrieve("n")
	_, errs["List"] = b.List()
	_, errs["Strategy"] = b.Strategy()
	errs["Delete"] = b.Delete("n")
	_, errs["Rotate"] = b.Rotate(&transport.CredentialRotateRequest{Name: "n", NewValue: []byte("v")})
	_, errs["ListVersions"] = b.ListVersions("n")
	_, errs["RetrieveVersion"] = b.RetrieveVersion("n", 1)
	_, errs["GCExpired"] = b.GCExpired("n")
	return errs
}

func TestCredentialBinding_NotConnected(t *testing.T) {
	b := NewCredentialBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	for name, err := range credentialBindingCalls(b) {
		t.Run(name, func(t *testing.T) { assertNotConnected(t, err) })
	}
}

func TestCredentialBinding_ServiceNotWired(t *testing.T) {
	conn := newTestConnectionBinding(t, &noopKVService{}, newMockAdminService())
	b := NewCredentialBinding(testLogger(t), conn)
	for name, err := range credentialBindingCalls(b) {
		t.Run(name, func(t *testing.T) { assertNilService(t, err) })
	}
}

func TestCredentialBinding_ServiceErrors(t *testing.T) {
	sentinel := errors.New("credential backend down")
	svc := newMockCredSvc()
	svc.submitErr, svc.storeErr, svc.retrieveErr = sentinel, sentinel, sentinel
	svc.listErr, svc.deleteErr, svc.rotateErr, svc.gcErr = sentinel, sentinel, sentinel, sentinel
	b := newTestCredentialBinding(t, svc)
	errs := credentialBindingCalls(b)
	// Strategy has no error path in the service; it is covered above.
	delete(errs, "Strategy")
	for name, err := range errs {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(err, sentinel) {
				t.Fatalf("want the service error, got %T: %v", err, err)
			}
		})
	}
}

func TestLockBinding_RoundTrip(t *testing.T) {
	svc := newMockLockService()
	b := newTestLockBinding(t, svc)

	info, err := b.Acquire(&transport.LockAcquireRequest{Key: "jobs/a", HolderID: "h1", TTL: 30})
	if err != nil || info.FencingToken != 1 || info.HolderID != "h1" {
		t.Fatalf("Acquire = %+v, %v; want token 1 held by h1", info, err)
	}
	ext, err := b.Extend(&transport.LockExtendRequest{Key: "jobs/a", FencingToken: info.FencingToken})
	if err != nil || ext.Key != "jobs/a" {
		t.Fatalf("Extend = %+v, %v", ext, err)
	}
	st, err := b.Status("jobs/a")
	if err != nil || st == nil || st.HolderID != "h1" {
		t.Fatalf("Status = %+v, %v; want held by h1", st, err)
	}
	list, err := b.List("jobs/")
	if err != nil || len(list.Locks) != 1 {
		t.Fatalf("List = %+v, %v; want one lock", list, err)
	}
	if err := b.Release(&transport.LockReleaseRequest{Key: "jobs/a", FencingToken: info.FencingToken}); err != nil {
		t.Fatalf("Release: %v", err)
	}
	tl, err := b.TryLock(&transport.LockAcquireRequest{Key: "jobs/b", HolderID: "h2", TTL: 30})
	if err != nil || tl.HolderID != "h2" {
		t.Fatalf("TryLock = %+v, %v; want held by h2", tl, err)
	}
}

func TestLockBinding_AcquireServiceError(t *testing.T) {
	sentinel := errors.New("lock backend down")
	svc := newMockLockService()
	svc.lockFn = func(context.Context, string, string, time.Duration, bool) (*lock.Lock, error) {
		return nil, sentinel
	}
	b := newTestLockBinding(t, svc)
	if _, err := b.Acquire(&transport.LockAcquireRequest{Key: "k", TTL: 1}); !errors.Is(err, sentinel) {
		t.Fatalf("Acquire error = %v; want the service error", err)
	}
}

// lockBindingCalls exercises every LockBinding method once and returns each
// call's error, keyed by method name.
func lockBindingCalls(b *LockBinding) map[string]error {
	errs := map[string]error{}
	_, errs["Acquire"] = b.Acquire(&transport.LockAcquireRequest{Key: "k", TTL: 1})
	_, errs["TryLock"] = b.TryLock(&transport.LockAcquireRequest{Key: "k", TTL: 1})
	errs["Release"] = b.Release(&transport.LockReleaseRequest{Key: "k", FencingToken: 1})
	_, errs["Extend"] = b.Extend(&transport.LockExtendRequest{Key: "k", FencingToken: 1})
	_, errs["Status"] = b.Status("k")
	_, errs["List"] = b.List("")
	return errs
}

func TestLockBinding_NotConnected(t *testing.T) {
	b := NewLockBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	for name, err := range lockBindingCalls(b) {
		t.Run(name, func(t *testing.T) { assertNotConnected(t, err) })
	}
}

func TestLockBinding_ServiceNotWired(t *testing.T) {
	conn := newTestConnectionBinding(t, &noopKVService{}, newMockAdminService())
	b := NewLockBinding(testLogger(t), conn)
	for name, err := range lockBindingCalls(b) {
		t.Run(name, func(t *testing.T) { assertNilService(t, err) })
	}
}

func TestAuditBinding_Query(t *testing.T) {
	sentinel := errors.New("audit store down")
	tests := []struct {
		name      string
		queryFn   func(context.Context, audit.Filter) (*audit.Page, error)
		wantErr   error
		wantCount int
	}{
		{
			name: "events",
			queryFn: func(context.Context, audit.Filter) (*audit.Page, error) {
				return &audit.Page{Events: []audit.AuditEvent{{Actor: "alice"}, {Actor: "bob"}}, Total: 2}, nil
			},
			wantCount: 2,
		},
		{
			name: "service error",
			queryFn: func(context.Context, audit.Filter) (*audit.Page, error) {
				return nil, sentinel
			},
			wantErr: sentinel,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := newTestConnectionBinding(t, &noopKVService{}, newMockAdminService(),
				embedded.WithAuditLogger(&mockAuditLogger{queryFn: tt.queryFn}))
			page, err := NewAuditBinding(testLogger(t), conn).Query(&transport.AuditFilter{})
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Query error = %v; want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil || len(page.Events) != tt.wantCount {
				t.Fatalf("Query = %+v, %v; want %d events", page, err, tt.wantCount)
			}
		})
	}
}

func TestAuditBinding_QueryNotConnected(t *testing.T) {
	b := NewAuditBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.Query(&transport.AuditFilter{})
	assertNotConnected(t, err)
}

func TestTenantBinding_TrustAnchorsNotConnected(t *testing.T) {
	b := NewTenantBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.AddTrustAnchors("acme", "pem")
	assertNotConnected(t, err)
	_, err = b.ListTrustAnchors("acme")
	assertNotConnected(t, err)
	assertNotConnected(t, b.RemoveTrustAnchor("acme", "fp"))
}

// The embedded transport serves no trust-anchor API; each binding call
// surfaces its typed NotImplementedError unchanged.
func TestTenantBinding_TrustAnchorsNotImplemented(t *testing.T) {
	conn := newTestConnectionBinding(t, &noopKVService{}, newMockAdminService())
	b := NewTenantBinding(testLogger(t), conn)
	assertNotImplemented := func(t *testing.T, err error) {
		t.Helper()
		var ni *transport.NotImplementedError
		if !errors.As(err, &ni) {
			t.Fatalf("want *transport.NotImplementedError, got %T: %v", err, err)
		}
	}
	_, err := b.AddTrustAnchors("acme", "pem")
	assertNotImplemented(t, err)
	_, err = b.ListTrustAnchors("acme")
	assertNotImplemented(t, err)
	assertNotImplemented(t, b.RemoveTrustAnchor("acme", "fp"))
}
