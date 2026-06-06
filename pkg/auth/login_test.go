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
	mu        sync.Mutex
	factors   map[string]*AuthFactor // by lookup-key+type composite
	lookupErr error
	markedID  string
	markCalls atomic.Int32
	markDone  chan struct{}
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

func (s *stubFactorStore) MarkUsed(_ context.Context, id string, _ time.Time) error {
	s.mu.Lock()
	s.markedID = id
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

func TestNewLoginResolver_ok(t *testing.T) {
	r := NewLoginResolver(newStubFactorStore(), &stubUserLookup{})
	if r == nil {
		t.Fatal("NewLoginResolver returned nil")
	}
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
		tc := tc
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

func TestUserLookupFunc_invokes(t *testing.T) {
	called := false
	f := UserLookupFunc(func(_ context.Context, id uint64) (*Principal, error) {
		called = true
		return &Principal{ID: id}, nil
	})
	p, err := f.LookupUser(context.Background(), 7)
	if err != nil {
		t.Fatalf("LookupUser: %v", err)
	}
	if !called {
		t.Fatal("UserLookupFunc did not invoke wrapped fn")
	}
	if p.ID != 7 {
		t.Fatalf("p.ID = %d, want 7", p.ID)
	}
}
