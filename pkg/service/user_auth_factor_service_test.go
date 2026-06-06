// Copyright 2026 Jeremy Hahn
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

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// ---------------------------------------------------------------------------
// Helpers — reuse afMockKV from authfactor_service_test.go
// ---------------------------------------------------------------------------

type stubUserStateUpdater struct {
	calls map[uint64]bool
}

func newStubUserStateUpdater() *stubUserStateUpdater {
	return &stubUserStateUpdater{calls: make(map[uint64]bool)}
}

func (s *stubUserStateUpdater) SetUserActive(_ context.Context, id uint64, active bool) error {
	s.calls[id] = active
	return nil
}

func newUserAuthFactorService(t *testing.T, users UserStateUpdater) (UserAuthFactorServicer, *afMockKV) {
	t.Helper()
	kv := newAFMockKV()
	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{
		KV:               kv,
		UserStateUpdater: users,
	})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	return svc, kv
}

func mtlsFactor(id string, userID uint64, key []byte) *auth.AuthFactor {
	return &auth.AuthFactor{
		ID:         id,
		UserID:     userID,
		Type:       auth.AuthFactorMTLS,
		Credential: key,
		Label:      "mtls-" + id,
	}
}

func fido2Factor(id string, userID uint64, key []byte) *auth.AuthFactor {
	return &auth.AuthFactor{
		ID:         id,
		UserID:     userID,
		Type:       auth.AuthFactorFIDO2,
		Credential: key,
		Label:      "fido2-" + id,
	}
}

func pivFactor(id string, userID uint64, key []byte) *auth.AuthFactor {
	return &auth.AuthFactor{
		ID:         id,
		UserID:     userID,
		Type:       auth.AuthFactorPIV,
		Credential: key,
		Label:      "piv-" + id,
	}
}

// ---------------------------------------------------------------------------
// NewUserAuthFactorService
// ---------------------------------------------------------------------------

func TestNewUserAuthFactorService_nilKV(t *testing.T) {
	_, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{KV: nil})
	if !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("expected ErrInvalidFactor, got %v", err)
	}
}

func TestNewUserAuthFactorService_ok(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	if svc == nil {
		t.Fatal("nil svc")
	}
}

// ---------------------------------------------------------------------------
// Add / List / Get round trips
// ---------------------------------------------------------------------------

func TestAdd_List_Get_eachType(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	ctx := context.Background()

	cases := []*auth.AuthFactor{
		mtlsFactor("m1", 1, []byte("mtls-fp-1")),
		fido2Factor("f1", 1, []byte("fido2-credID-1")),
		pivFactor("p1", 1, []byte("piv-fp-1")),
	}

	for _, f := range cases {
		if err := svc.Add(ctx, f.UserID, f); err != nil {
			t.Fatalf("Add(%s): %v", f.Type, err)
		}
	}

	list, err := svc.List(ctx, 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len(List) = %d, want 3", len(list))
	}

	for _, want := range cases {
		got, err := svc.Get(ctx, want.ID)
		if err != nil {
			t.Fatalf("Get(%s): %v", want.ID, err)
		}
		if got.UserID != want.UserID || got.Type != want.Type {
			t.Fatalf("Get mismatch: got %+v want %+v", got, want)
		}
	}
}

func TestAdd_invalidUserID(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	err := svc.Add(context.Background(), 0, mtlsFactor("x", 0, []byte("k")))
	if !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("want ErrInvalidFactor, got %v", err)
	}
}

func TestAdd_unsupportedType(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	bad := &auth.AuthFactor{
		Type:       auth.AuthFactorType("bogus"),
		Credential: []byte("k"),
	}
	err := svc.Add(context.Background(), 1, bad)
	if !errors.Is(err, auth.ErrFactorTypeUnsupported) {
		t.Fatalf("want ErrFactorTypeUnsupported, got %v", err)
	}
}

func TestAdd_emptyCredential(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	err := svc.Add(context.Background(), 1, &auth.AuthFactor{
		Type: auth.AuthFactorMTLS,
	})
	if !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("want ErrInvalidFactor, got %v", err)
	}
}

func TestAdd_duplicateCredential(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	ctx := context.Background()
	if err := svc.Add(ctx, 1, mtlsFactor("a", 1, []byte("k"))); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	err := svc.Add(ctx, 2, mtlsFactor("b", 2, []byte("k")))
	if !errors.Is(err, auth.ErrFactorAlreadyExists) {
		t.Fatalf("want ErrFactorAlreadyExists, got %v", err)
	}
}

func TestGet_notFound(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	_, err := svc.Get(context.Background(), "missing")
	if !errors.Is(err, auth.ErrFactorNotFound) {
		t.Fatalf("want ErrFactorNotFound, got %v", err)
	}
}

func TestGet_emptyID(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	_, err := svc.Get(context.Background(), "")
	if !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("want ErrInvalidFactor, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// LookupByCredentialKey
// ---------------------------------------------------------------------------

func TestLookupByCredentialKey_active(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	ctx := context.Background()
	if err := svc.Add(ctx, 1, mtlsFactor("m1", 1, []byte("fp"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("fp"))
	if err != nil {
		t.Fatalf("LookupByCredentialKey: %v", err)
	}
	if got.ID != "m1" {
		t.Fatalf("got ID %q, want m1", got.ID)
	}
}

func TestLookupByCredentialKey_revoked(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, newStubUserStateUpdater())
	ctx := context.Background()
	if err := svc.Add(ctx, 1, mtlsFactor("primary", 1, []byte("k1"))); err != nil {
		t.Fatalf("Add primary: %v", err)
	}
	if err := svc.Add(ctx, 1, mtlsFactor("backup", 1, []byte("k2"))); err != nil {
		t.Fatalf("Add backup: %v", err)
	}
	if err := svc.Remove(ctx, "backup", false); err != nil {
		t.Fatalf("Remove backup: %v", err)
	}
	_, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("k2"))
	if !errors.Is(err, auth.ErrFactorRevoked) {
		t.Fatalf("want ErrFactorRevoked, got %v", err)
	}
}

func TestLookupByCredentialKey_notFound(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	_, err := svc.LookupByCredentialKey(context.Background(), auth.AuthFactorMTLS, []byte("missing"))
	if !errors.Is(err, auth.ErrFactorNotFound) {
		t.Fatalf("want ErrFactorNotFound, got %v", err)
	}
}

func TestLookupByCredentialKey_unsupportedType(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	_, err := svc.LookupByCredentialKey(context.Background(), "totp", []byte("k"))
	if !errors.Is(err, auth.ErrFactorTypeUnsupported) {
		t.Fatalf("want ErrFactorTypeUnsupported, got %v", err)
	}
}

func TestLookupByCredentialKey_emptyKey(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	_, err := svc.LookupByCredentialKey(context.Background(), auth.AuthFactorMTLS, nil)
	if !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("want ErrInvalidFactor, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Remove
// ---------------------------------------------------------------------------

func TestRemove_lastFactorBlocks(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, newStubUserStateUpdater())
	ctx := context.Background()
	if err := svc.Add(ctx, 1, mtlsFactor("only", 1, []byte("k"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	err := svc.Remove(ctx, "only", false)
	if !errors.Is(err, auth.ErrLastFactor) {
		t.Fatalf("want ErrLastFactor, got %v", err)
	}
}

func TestRemove_lastFactorForce(t *testing.T) {
	users := newStubUserStateUpdater()
	svc, _ := newUserAuthFactorService(t, users)
	ctx := context.Background()
	if err := svc.Add(ctx, 7, mtlsFactor("only", 7, []byte("k"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := svc.Remove(ctx, "only", true); err != nil {
		t.Fatalf("Remove force: %v", err)
	}
	if active, ok := users.calls[7]; !ok || active {
		t.Fatalf("expected SetUserActive(7, false), got calls=%+v", users.calls)
	}
	got, err := svc.Get(ctx, "only")
	if err != nil {
		t.Fatalf("Get after remove: %v", err)
	}
	if got.IsActive() {
		t.Fatal("factor should be revoked after force remove")
	}
}

func TestRemove_nonLastSucceeds(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	ctx := context.Background()
	if err := svc.Add(ctx, 1, mtlsFactor("a", 1, []byte("k1"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := svc.Add(ctx, 1, fido2Factor("b", 1, []byte("k2"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := svc.Remove(ctx, "a", false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

func TestRemove_alreadyRevoked(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	ctx := context.Background()
	if err := svc.Add(ctx, 1, mtlsFactor("a", 1, []byte("k1"))); err != nil {
		t.Fatalf("Add a: %v", err)
	}
	if err := svc.Add(ctx, 1, fido2Factor("b", 1, []byte("k2"))); err != nil {
		t.Fatalf("Add b: %v", err)
	}
	if err := svc.Remove(ctx, "a", false); err != nil {
		t.Fatalf("Remove a: %v", err)
	}
	err := svc.Remove(ctx, "a", false)
	if !errors.Is(err, auth.ErrFactorRevoked) {
		t.Fatalf("want ErrFactorRevoked, got %v", err)
	}
}

func TestRemove_notFound(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	err := svc.Remove(context.Background(), "missing", false)
	if !errors.Is(err, auth.ErrFactorNotFound) {
		t.Fatalf("want ErrFactorNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Rotate
// ---------------------------------------------------------------------------

func TestRotate_atomic(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	ctx := context.Background()
	if err := svc.Add(ctx, 9, mtlsFactor("old", 9, []byte("oldkey"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	newF := mtlsFactor("new", 9, []byte("newkey"))
	if err := svc.Rotate(ctx, "old", newF); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	old, err := svc.Get(ctx, "old")
	if err != nil {
		t.Fatalf("Get old: %v", err)
	}
	if old.IsActive() {
		t.Fatal("old factor should be revoked")
	}

	got, err := svc.Get(ctx, "new")
	if err != nil {
		t.Fatalf("Get new: %v", err)
	}
	if !got.IsActive() {
		t.Fatal("new factor should be active")
	}

	// New factor must be discoverable via lookup.
	resolved, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("newkey"))
	if err != nil {
		t.Fatalf("LookupByCredentialKey: %v", err)
	}
	if resolved.ID != "new" {
		t.Fatalf("lookup ID = %q, want new", resolved.ID)
	}
}

func TestRotate_oldNotFound(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	err := svc.Rotate(context.Background(), "missing", mtlsFactor("new", 1, []byte("k")))
	if !errors.Is(err, auth.ErrFactorNotFound) {
		t.Fatalf("want ErrFactorNotFound, got %v", err)
	}
}

func TestRotate_collisionWithThirdParty(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	ctx := context.Background()
	if err := svc.Add(ctx, 1, mtlsFactor("user1", 1, []byte("k1"))); err != nil {
		t.Fatalf("Add user1: %v", err)
	}
	if err := svc.Add(ctx, 2, mtlsFactor("user2", 2, []byte("k2"))); err != nil {
		t.Fatalf("Add user2: %v", err)
	}
	// Rotate user1's factor onto user2's existing key — collision.
	err := svc.Rotate(ctx, "user1", mtlsFactor("user1-new", 1, []byte("k2")))
	if !errors.Is(err, auth.ErrFactorAlreadyExists) {
		t.Fatalf("want ErrFactorAlreadyExists, got %v", err)
	}
}

func TestRotate_crossUserRejected(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	ctx := context.Background()
	if err := svc.Add(ctx, 1, mtlsFactor("a", 1, []byte("k1"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	bad := mtlsFactor("b", 2, []byte("k2"))
	err := svc.Rotate(ctx, "a", bad)
	if !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("want ErrInvalidFactor, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Relabel / MarkUsed
// ---------------------------------------------------------------------------

func TestRelabel(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	ctx := context.Background()
	if err := svc.Add(ctx, 1, mtlsFactor("a", 1, []byte("k"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := svc.Relabel(ctx, "a", "renamed"); err != nil {
		t.Fatalf("Relabel: %v", err)
	}
	got, err := svc.Get(ctx, "a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Label != "renamed" {
		t.Fatalf("Label = %q, want renamed", got.Label)
	}
}

func TestRelabel_notFound(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	err := svc.Relabel(context.Background(), "missing", "x")
	if !errors.Is(err, auth.ErrFactorNotFound) {
		t.Fatalf("want ErrFactorNotFound, got %v", err)
	}
}

// TestRelabel_revoked verifies Relabel rejects revoked factors with
// FactorRevokedError, matching the guard already enforced by Remove and
// Rotate. Renaming a revoked factor is meaningless and would obscure
// audit trails.
func TestRelabel_revoked(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, newStubUserStateUpdater())
	ctx := context.Background()
	if err := svc.Add(ctx, 1, mtlsFactor("primary", 1, []byte("k1"))); err != nil {
		t.Fatalf("Add primary: %v", err)
	}
	if err := svc.Add(ctx, 1, mtlsFactor("backup", 1, []byte("k2"))); err != nil {
		t.Fatalf("Add backup: %v", err)
	}
	// Force=false revokes a non-last factor cleanly.
	if err := svc.Remove(ctx, "backup", false); err != nil {
		t.Fatalf("Remove backup: %v", err)
	}
	err := svc.Relabel(ctx, "backup", "renamed")
	if !errors.Is(err, auth.ErrFactorRevoked) {
		t.Fatalf("want ErrFactorRevoked, got %v", err)
	}
	// Active factor must still be relabelable.
	if err := svc.Relabel(ctx, "primary", "primary-renamed"); err != nil {
		t.Fatalf("Relabel active: %v", err)
	}
}

func TestMarkUsed_updates(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	ctx := context.Background()
	if err := svc.Add(ctx, 1, mtlsFactor("a", 1, []byte("k"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	ts := time.Now().UTC().Truncate(time.Second)
	if err := svc.MarkUsed(ctx, "a", ts); err != nil {
		t.Fatalf("MarkUsed: %v", err)
	}
	got, err := svc.Get(ctx, "a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(ts) {
		t.Fatalf("LastUsedAt = %v, want %v", got.LastUsedAt, ts)
	}
}

func TestMarkUsed_emptyID(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	err := svc.MarkUsed(context.Background(), "", time.Now())
	if !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("want ErrInvalidFactor, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Cross-type collision avoidance — same lookup key bytes under different
// factor types must not collide. Same bytes across users is rejected as
// a duplicate (correct: the (type, key) tuple identifies a unique
// credential).
// ---------------------------------------------------------------------------

func TestAdd_sameBytesDifferentType_noCollision(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	ctx := context.Background()
	key := []byte("ambiguous-32-byte-blob-XXXXXXXXX")

	if err := svc.Add(ctx, 1, mtlsFactor("m1", 1, key)); err != nil {
		t.Fatalf("Add mtls: %v", err)
	}
	if err := svc.Add(ctx, 2, fido2Factor("f2", 2, key)); err != nil {
		t.Fatalf("Add fido2: %v", err)
	}
	if err := svc.Add(ctx, 3, pivFactor("p3", 3, key)); err != nil {
		t.Fatalf("Add piv: %v", err)
	}

	// Each must resolve back to its own user.
	mtls, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, key)
	if err != nil {
		t.Fatalf("Lookup mtls: %v", err)
	}
	if mtls.UserID != 1 {
		t.Fatalf("mtls UserID = %d, want 1", mtls.UserID)
	}
	fido2, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorFIDO2, key)
	if err != nil {
		t.Fatalf("Lookup fido2: %v", err)
	}
	if fido2.UserID != 2 {
		t.Fatalf("fido2 UserID = %d, want 2", fido2.UserID)
	}
	piv, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorPIV, key)
	if err != nil {
		t.Fatalf("Lookup piv: %v", err)
	}
	if piv.UserID != 3 {
		t.Fatalf("piv UserID = %d, want 3", piv.UserID)
	}
}

// ---------------------------------------------------------------------------
// FactorStore interface conformance — verify that the service can be passed
// directly to a LoginResolver and resolve a factor end to end.
// ---------------------------------------------------------------------------

func TestService_implementsFactorStore_andResolves(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	store, ok := svc.(auth.FactorStore)
	if !ok {
		t.Fatal("service does not implement auth.FactorStore")
	}
	users := auth.UserLookupFunc(func(_ context.Context, id uint64) (*auth.Principal, error) {
		return &auth.Principal{ID: id, Username: "u", Active: true}, nil
	})
	r := auth.NewLoginResolver(store, users)

	ctx := context.Background()
	if err := svc.Add(ctx, 99, mtlsFactor("e2e", 99, []byte("e2ekey"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	p, f, err := r.Resolve(ctx, auth.AuthFactorMTLS, []byte("e2ekey"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p.ID != 99 || f.ID != "e2e" {
		t.Fatalf("got p=%+v f=%+v", p, f)
	}
}
