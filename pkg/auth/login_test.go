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

package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test fakes
// ---------------------------------------------------------------------------

type stubFactorStore struct {
	mu          sync.Mutex
	factors     map[string]*AuthFactor // by lookup-key+type composite
	lookupErr   error
	markedID    string
	markedScope string
	markCalls   atomic.Int32
	markDone    chan struct{}
}

func newStubFactorStore() *stubFactorStore {
	return &stubFactorStore{
		factors:  make(map[string]*AuthFactor),
		markDone: make(chan struct{}, 1),
	}
}

func (s *stubFactorStore) put(t AuthFactorType, key []byte, f *AuthFactor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.factors[string(t)+"|"+string(key)] = f
}

func (s *stubFactorStore) LookupByCredentialKey(_ context.Context, t AuthFactorType, key []byte) (*AuthFactor, error) {
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.factors[string(t)+"|"+string(key)]
	if !ok {
		return nil, &FactorNotFoundError{}
	}
	return f, nil
}

// loginScopeKey is a request-scoped context key standing in for whatever the
// caller binds to a login context. pkg/auth cannot import pkg/tenant — the
// dependency runs the other way — so the real binding under test,
// tenant.WithTenantID, is modelled by its mechanism: a context VALUE.
type loginScopeKey struct{}

// MarkUsed records the scope value it was called with, not merely that it was
// called.
//
// The store is tenant-routed in production and reads that value off this
// context to pick which tenant's records to update, so a double that dropped
// the context — as this one did — cannot tell a detach that preserves request
// values from one that discards them. Both call MarkUsed exactly once.
func (s *stubFactorStore) MarkUsed(ctx context.Context, id string, _ time.Time) error {
	s.mu.Lock()
	s.markedID = id
	s.markedScope, _ = ctx.Value(loginScopeKey{}).(string)
	s.mu.Unlock()
	s.markCalls.Add(1)
	select {
	case s.markDone <- struct{}{}:
	default:
	}
	return nil
}

type stubUserLookup struct {
	users map[uint64]*Principal
	err   error
}

func (s *stubUserLookup) LookupUser(_ context.Context, id uint64) (*Principal, error) {
	if s.err != nil {
		return nil, s.err
	}
	p, ok := s.users[id]
	if !ok {
		return nil, &UserHasNoFactorsError{UserID: id}
	}
	return p, nil
}

// ---------------------------------------------------------------------------
// Constructor / panics
// ---------------------------------------------------------------------------

func TestNewLoginResolver_panicsOnNilStore(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil store")
		}
	}()
	NewLoginResolver(nil, &stubUserLookup{})
}

func TestNewLoginResolver_panicsOnNilUsers(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil users")
		}
	}()
	NewLoginResolver(newStubFactorStore(), nil)
}

// ---------------------------------------------------------------------------
// Resolve happy path
// ---------------------------------------------------------------------------

func TestResolve_happyPath_eachType(t *testing.T) {
	store := newStubFactorStore()
	users := &stubUserLookup{users: map[uint64]*Principal{
		1: {ID: 1, Username: "alice", Active: true},
		2: {ID: 2, Username: "bob", Active: true},
		3: {ID: 3, Username: "carol", Active: true},
	}}
	r := NewLoginResolver(store, users)

	cases := []struct {
		name   string
		typ    AuthFactorType
		userID uint64
		key    []byte
	}{
		{"mtls", AuthFactorMTLS, 1, []byte("mtls-fingerprint")},
		{"fido2", AuthFactorFIDO2, 2, []byte("credentialID-bytes")},
		{"piv", AuthFactorPIV, 3, []byte("piv-fingerprint")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &AuthFactor{
				ID:         "factor-" + tc.name,
				UserID:     tc.userID,
				Type:       tc.typ,
				Credential: tc.key,
			}
			store.put(tc.typ, tc.key, f)

			p, got, err := r.Resolve(context.Background(), tc.typ, tc.key)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if p == nil || p.ID != tc.userID {
				t.Fatalf("Resolve principal = %+v, want ID=%d", p, tc.userID)
			}
			if got == nil || got.ID != f.ID {
				t.Fatalf("Resolve factor = %+v, want %+v", got, f)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Resolve error paths
// ---------------------------------------------------------------------------

func TestResolve_unsupportedType(t *testing.T) {
	r := NewLoginResolver(newStubFactorStore(), &stubUserLookup{})
	_, _, err := r.Resolve(context.Background(), "garbage", []byte("k"))
	if !errors.Is(err, ErrFactorTypeUnsupported) {
		t.Fatalf("expected ErrFactorTypeUnsupported, got %v", err)
	}
}

func TestResolve_emptyKey(t *testing.T) {
	r := NewLoginResolver(newStubFactorStore(), &stubUserLookup{})
	_, _, err := r.Resolve(context.Background(), AuthFactorMTLS, nil)
	if !errors.Is(err, ErrInvalidFactor) {
		t.Fatalf("expected ErrInvalidFactor, got %v", err)
	}
}

func TestResolve_factorNotFound(t *testing.T) {
	r := NewLoginResolver(newStubFactorStore(), &stubUserLookup{})
	_, _, err := r.Resolve(context.Background(), AuthFactorMTLS, []byte("nope"))
	if !errors.Is(err, ErrFactorNotFound) {
		t.Fatalf("expected ErrFactorNotFound, got %v", err)
	}
}

func TestResolve_factorRevoked(t *testing.T) {
	store := newStubFactorStore()
	users := &stubUserLookup{users: map[uint64]*Principal{1: {ID: 1, Active: true}}}
	r := NewLoginResolver(store, users)
	now := time.Now().UTC()
	store.put(AuthFactorMTLS, []byte("revoked"), &AuthFactor{
		ID:         "f1",
		UserID:     1,
		Type:       AuthFactorMTLS,
		Credential: []byte("revoked"),
		RevokedAt:  &now,
	})
	_, _, err := r.Resolve(context.Background(), AuthFactorMTLS, []byte("revoked"))
	if !errors.Is(err, ErrFactorRevoked) {
		t.Fatalf("expected ErrFactorRevoked, got %v", err)
	}
}

func TestResolve_userMissing(t *testing.T) {
	store := newStubFactorStore()
	users := &stubUserLookup{users: map[uint64]*Principal{}}
	r := NewLoginResolver(store, users)
	store.put(AuthFactorPIV, []byte("piv"), &AuthFactor{
		ID:         "f1",
		UserID:     999,
		Type:       AuthFactorPIV,
		Credential: []byte("piv"),
	})
	_, _, err := r.Resolve(context.Background(), AuthFactorPIV, []byte("piv"))
	if err == nil {
		t.Fatal("expected user-missing error")
	}
}

// ---------------------------------------------------------------------------
// Resolve invokes MarkUsed asynchronously.
// ---------------------------------------------------------------------------

func TestResolve_invokesMarkUsed(t *testing.T) {
	store := newStubFactorStore()
	users := &stubUserLookup{users: map[uint64]*Principal{1: {ID: 1, Active: true}}}
	r := NewLoginResolver(store, users)
	store.put(AuthFactorMTLS, []byte("k"), &AuthFactor{
		ID:         "f1",
		UserID:     1,
		Type:       AuthFactorMTLS,
		Credential: []byte("k"),
	})
	if _, _, err := r.Resolve(context.Background(), AuthFactorMTLS, []byte("k")); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	select {
	case <-store.markDone:
		// MarkUsed was invoked; verify the right factor.
		if store.markedID != "f1" {
			t.Fatalf("markedID = %q, want %q", store.markedID, "f1")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async MarkUsed")
	}
}

// ---------------------------------------------------------------------------
// UserLookupFunc
// ---------------------------------------------------------------------------

// TestResolve_markUsedKeepsTheRequestScope asserts the detached LastUsedAt
// update still carries the values bound to the resolve context.
//
// The update is deliberately detached so a returning login response cannot
// cancel it. Detaching with context.Background() would also drop the tenant
// binding the routed factor store reads, which would not fail the login and
// would not fail this test either unless the scope is asserted: MarkUsed is
// still called, just against the wrong tenant's store, and LastUsedAt would
// silently stop advancing for every tenant but the default one.
func TestResolve_markUsedKeepsTheRequestScope(t *testing.T) {
	store := newStubFactorStore()
	users := &stubUserLookup{users: map[uint64]*Principal{77: {ID: 77, Username: "scoped", Active: true}}}
	store.put(AuthFactorMTLS, []byte("scoped-key"), &AuthFactor{ID: "factor-scope", UserID: 77, Type: AuthFactorMTLS})

	r := NewLoginResolver(store, users)
	ctx := context.WithValue(context.Background(), loginScopeKey{}, "acme")
	if _, _, err := r.Resolve(ctx, AuthFactorMTLS, []byte("scoped-key")); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	select {
	case <-store.markDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async MarkUsed")
	}
	store.mu.Lock()
	scope := store.markedScope
	store.mu.Unlock()
	if scope != "acme" {
		t.Fatalf("MarkUsed ran with scope %q, want \"acme\": the detached context dropped the request values", scope)
	}
}

// TestResolve_markUsedSurvivesTheRequestContext asserts the other half of the
// same detach: cancelling the request context must not cancel the update.
func TestResolve_markUsedSurvivesTheRequestContext(t *testing.T) {
	store := newStubFactorStore()
	users := &stubUserLookup{users: map[uint64]*Principal{78: {ID: 78, Username: "detached", Active: true}}}
	store.put(AuthFactorMTLS, []byte("detach-key"), &AuthFactor{ID: "factor-detach", UserID: 78, Type: AuthFactorMTLS})

	r := NewLoginResolver(store, users)
	ctx, cancel := context.WithCancel(context.Background())
	if _, _, err := r.Resolve(ctx, AuthFactorMTLS, []byte("detach-key")); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cancel()

	select {
	case <-store.markDone:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the request cancelled the detached MarkUsed")
	}
}
