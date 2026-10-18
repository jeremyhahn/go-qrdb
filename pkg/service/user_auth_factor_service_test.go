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
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
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

func newTestUserAuthFactorService(t *testing.T, users UserStateUpdater) (UserAuthFactorServicer, *afMockKV) {
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
	svc, _ := newTestUserAuthFactorService(t, nil)
	if svc == nil {
		t.Fatal("nil svc")
	}
}

// ---------------------------------------------------------------------------
// Add / List / Get round trips
// ---------------------------------------------------------------------------

func TestAdd_List_Get_eachType(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
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
	svc, _ := newTestUserAuthFactorService(t, nil)
	err := svc.Add(context.Background(), 0, mtlsFactor("x", 0, []byte("k")))
	if !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("want ErrInvalidFactor, got %v", err)
	}
}

func TestAdd_unsupportedType(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
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
	svc, _ := newTestUserAuthFactorService(t, nil)
	err := svc.Add(context.Background(), 1, &auth.AuthFactor{
		Type: auth.AuthFactorMTLS,
	})
	if !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("want ErrInvalidFactor, got %v", err)
	}
}

func TestAdd_duplicateCredential(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	ctx := context.Background()
	if err := svc.Add(ctx, 1, mtlsFactor("a", 1, []byte("k"))); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	err := svc.Add(ctx, 2, mtlsFactor("b", 2, []byte("k")))
	if !errors.Is(err, auth.ErrFactorAlreadyExists) {
		t.Fatalf("want ErrFactorAlreadyExists, got %v", err)
	}
}

// guardedFactorStores names the store shapes a conditional add commits
// through: one with single-key guards, and one with the multi-guarded batch
// as well.
func guardedFactorStores() map[string]func() AuthFactorKV {
	return map[string]func() AuthFactorKV{
		"single-key guards": func() AuthFactorKV { return newAFMockKV() },
		"batch":             func() AuthFactorKV { return kvstoretest.NewGuardedMemoryStore() },
	}
}

// factorStores adds to guardedFactorStores the two shapes an unconditional
// add also runs on: the batch without single-key guards, and a store with no
// guarded write at all.
func factorStores() map[string]func() AuthFactorKV {
	stores := guardedFactorStores()
	stores["batch only"] = func() AuthFactorKV {
		store := kvstoretest.NewGuardedMemoryStore()
		return batchOnlyFactorKV{AuthFactorKV: store, batch: store}
	}
	stores["unguarded"] = func() AuthFactorKV { return unguardedFactorKV{newAFMockKV()} }
	return stores
}

// unguardedFactorKV hides every guarded write of the store it wraps.
type unguardedFactorKV struct{ AuthFactorKV }

// batchOnlyFactorKV offers the multi-guarded batch and no single-key guard.
type batchOnlyFactorKV struct {
	AuthFactorKV
	batch factorBatchStore
}

var _ factorBatchStore = batchOnlyFactorKV{}

func (b batchOnlyFactorKV) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	return b.batch.PutAllIfAllUnchanged(ctx, guards, writes)
}

// assertFactorIntact fails unless factor id is still the active factor of
// userID holding credential, and still the one its credential logs in as.
func assertFactorIntact(t *testing.T, svc UserAuthFactorServicer, id string, userID uint64, credential string) {
	t.Helper()
	ctx := context.Background()
	got, err := svc.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get %s: %v, want the original factor", id, err)
	}
	if got.UserID != userID || string(got.Credential) != credential || got.RevokedAt != nil {
		t.Fatalf("factor %s = user %d credential %q revoked %v, want user %d credential %q active",
			id, got.UserID, got.Credential, got.RevokedAt, userID, credential)
	}
	login, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte(credential))
	if err != nil || login.ID != id {
		t.Fatalf("login with %q = %+v, %v, want factor %s", credential, login, err, id)
	}
}

// TestAdd_existingIDIsRefusedAndTheOriginalSurvives covers an in-process
// caller naming a factor ID already in use: the add is refused with
// FactorAlreadyExistsError before it writes, so it neither overwrites the
// original nor, once refused, deletes it.
func TestAdd_existingIDIsRefusedAndTheOriginalSurvives(t *testing.T) {
	for storeName, newStore := range factorStores() {
		for userName, user := range map[string]uint64{"same user": 1, "other user": 2} {
			t.Run(storeName+"/"+userName, func(t *testing.T) {
				ctx := context.Background()
				svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{KV: newStore()})
				if err != nil {
					t.Fatalf("NewUserAuthFactorService: %v", err)
				}
				if err := svc.Add(ctx, 1, mtlsFactor("a", 1, []byte("k1"))); err != nil {
					t.Fatalf("first Add: %v", err)
				}

				err = svc.Add(ctx, user, mtlsFactor("a", user, []byte("k2")))
				var exists *auth.FactorAlreadyExistsError
				if !errors.As(err, &exists) || exists.FactorID != "a" {
					t.Fatalf("Add for user %d reusing ID a = %v, want FactorAlreadyExistsError{a}", user, err)
				}
				assertFactorIntact(t, svc, "a", 1, "k1")
				if _, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("k2")); !errors.Is(err, auth.ErrFactorNotFound) {
					t.Fatalf("login with the refused credential = %v, want ErrFactorNotFound", err)
				}
			})
		}
	}
}

// TestAdd_conditionalReusingAnIDIsRefused is the same refusal through
// AddConditional: a stale condition used to fail after the add had
// overwritten the primary record, and its release then deleted the original.
func TestAdd_conditionalReusingAnIDIsRefused(t *testing.T) {
	for storeName, newStore := range guardedFactorStores() {
		t.Run(storeName, func(t *testing.T) {
			ctx := context.Background()
			svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{KV: newStore()})
			if err != nil {
				t.Fatalf("NewUserAuthFactorService: %v", err)
			}
			if err := svc.Add(ctx, 1, mtlsFactor("a", 1, []byte("k1"))); err != nil {
				t.Fatalf("first Add: %v", err)
			}
			version, err := svc.FactorSetVersion(ctx, 1)
			if err != nil {
				t.Fatalf("FactorSetVersion: %v", err)
			}

			stale := version + 1
			_, err = svc.AddConditional(ctx, 1, mtlsFactor("a", 1, []byte("k2")), auth.FactorAddCondition{ExpectedVersion: &stale})
			if !errors.Is(err, auth.ErrFactorAlreadyExists) {
				t.Fatalf("AddConditional reusing ID a = %v, want ErrFactorAlreadyExists", err)
			}
			assertFactorIntact(t, svc, "a", 1, "k1")
		})
	}
}

// TestRotate_toAnIDInUseIsRefusedWithoutTheBatch covers the rotation a store
// with only single-key guards writes one record at a time: a new factor that
// names an ID in use -- another factor's, or the old factor's own -- is
// refused before anything is written.
func TestRotate_toAnIDInUseIsRefusedWithoutTheBatch(t *testing.T) {
	for _, newID := range []string{"b", "a"} {
		t.Run(newID, func(t *testing.T) {
			svc, _ := newTestUserAuthFactorService(t, nil)
			ctx := context.Background()
			for _, f := range []*auth.AuthFactor{mtlsFactor("a", 1, []byte("k1")), mtlsFactor("b", 1, []byte("k2"))} {
				if err := svc.Add(ctx, 1, f); err != nil {
					t.Fatalf("Add %s: %v", f.ID, err)
				}
			}

			err := svc.Rotate(ctx, "a", mtlsFactor(newID, 1, []byte("k3")))
			var exists *auth.FactorAlreadyExistsError
			if !errors.As(err, &exists) || exists.FactorID != newID {
				t.Fatalf("Rotate to ID %s = %v, want FactorAlreadyExistsError{%s}", newID, err, newID)
			}
			assertFactorIntact(t, svc, "a", 1, "k1")
			assertFactorIntact(t, svc, "b", 1, "k2")
		})
	}
}

func TestGet_notFound(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	_, err := svc.Get(context.Background(), "missing")
	if !errors.Is(err, auth.ErrFactorNotFound) {
		t.Fatalf("want ErrFactorNotFound, got %v", err)
	}
}

func TestGet_emptyID(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	_, err := svc.Get(context.Background(), "")
	if !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("want ErrInvalidFactor, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// LookupByCredentialKey
// ---------------------------------------------------------------------------

func TestLookupByCredentialKey_active(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
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
	svc, _ := newTestUserAuthFactorService(t, newStubUserStateUpdater())
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
	svc, _ := newTestUserAuthFactorService(t, nil)
	_, err := svc.LookupByCredentialKey(context.Background(), auth.AuthFactorMTLS, []byte("missing"))
	if !errors.Is(err, auth.ErrFactorNotFound) {
		t.Fatalf("want ErrFactorNotFound, got %v", err)
	}
}

func TestLookupByCredentialKey_unsupportedType(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	_, err := svc.LookupByCredentialKey(context.Background(), "totp", []byte("k"))
	if !errors.Is(err, auth.ErrFactorTypeUnsupported) {
		t.Fatalf("want ErrFactorTypeUnsupported, got %v", err)
	}
}

func TestLookupByCredentialKey_emptyKey(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	_, err := svc.LookupByCredentialKey(context.Background(), auth.AuthFactorMTLS, nil)
	if !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("want ErrInvalidFactor, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Remove
// ---------------------------------------------------------------------------

func TestRemove_lastFactorBlocks(t *testing.T) {
	users := newStubUserStateUpdater()
	svc, _ := newTestUserAuthFactorService(t, users)
	ctx := context.Background()
	if err := svc.Add(ctx, 1, mtlsFactor("only", 1, []byte("k"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	err := svc.Remove(ctx, "only", false)
	if !errors.Is(err, auth.ErrLastFactor) {
		t.Fatalf("want ErrLastFactor, got %v", err)
	}
	if active, called := users.calls[1]; called {
		t.Fatalf("refused removal changed the user's state (active=%v); the user must stay active", active)
	}
	f, err := svc.Get(ctx, "only")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !f.IsActive() {
		t.Fatal("refused removal revoked the last factor")
	}
}

func TestRemove_lastFactorForce(t *testing.T) {
	users := newStubUserStateUpdater()
	svc, _ := newTestUserAuthFactorService(t, users)
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
	svc, _ := newTestUserAuthFactorService(t, nil)
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
	svc, _ := newTestUserAuthFactorService(t, nil)
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
	svc, _ := newTestUserAuthFactorService(t, nil)
	err := svc.Remove(context.Background(), "missing", false)
	if !errors.Is(err, auth.ErrFactorNotFound) {
		t.Fatalf("want ErrFactorNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Rotate
// ---------------------------------------------------------------------------

func TestRotate_atomic(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
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
	svc, _ := newTestUserAuthFactorService(t, nil)
	err := svc.Rotate(context.Background(), "missing", mtlsFactor("new", 1, []byte("k")))
	if !errors.Is(err, auth.ErrFactorNotFound) {
		t.Fatalf("want ErrFactorNotFound, got %v", err)
	}
}

func TestRotate_collisionWithThirdParty(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
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
	svc, _ := newTestUserAuthFactorService(t, nil)
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
	svc, _ := newTestUserAuthFactorService(t, nil)
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
	svc, _ := newTestUserAuthFactorService(t, nil)
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
	svc, _ := newTestUserAuthFactorService(t, newStubUserStateUpdater())
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
	svc, _ := newTestUserAuthFactorService(t, nil)
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
	svc, _ := newTestUserAuthFactorService(t, nil)
	err := svc.MarkUsed(context.Background(), "", time.Now())
	if !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("want ErrInvalidFactor, got %v", err)
	}
}

// gatingFactorKV wraps afMockKV with a mutex (the regression test below
// drives the service from multiple goroutines) and a write gate that parks
// MarkUsed's read-modify-write Put of the primary record — identified as a
// primary-key Put carrying LastUsedAt but no RevokedAt — until the test
// releases it. This deterministically reproduces the interleaving where the
// async MarkUsed goroutine spawned by auth.LoginResolver.Resolve reads the
// factor BEFORE a concurrent Remove revokes it and writes the stale
// (still-active) record back AFTER, silently resurrecting the revoked
// credential (lost update).
type gatingFactorKV struct {
	*afMockKV
	mu sync.Mutex

	// gateArmed is set (before any concurrent access) once the fixture
	// records are in place, so setup writes pass through ungated.
	gateArmed bool

	// staleWriteBlocked is closed when the stale MarkUsed Put parks.
	staleWriteBlocked chan struct{}

	// releaseStaleWrite is closed by the test to unpark the stale Put.
	releaseStaleWrite chan struct{}

	// sawRevokingPut is closed after a RevokedAt-bearing Put is applied.
	sawRevokingPut chan struct{}

	blockOnce sync.Once
	sawOnce   sync.Once
}

// newGatingFactorKV constructs the gate in the disarmed state.
func newGatingFactorKV() *gatingFactorKV {
	return &gatingFactorKV{
		afMockKV:          newAFMockKV(),
		staleWriteBlocked: make(chan struct{}),
		releaseStaleWrite: make(chan struct{}),
		sawRevokingPut:    make(chan struct{}),
	}
}

// Put applies the gate on armed primary-record writes, then delegates to
// the embedded store under the mutex.
func (g *gatingFactorKV) Put(ctx context.Context, key string, val []byte) error {
	revoking := false
	if g.gateArmed && strings.HasPrefix(key, userFactorPrimaryPrefix) {
		var f auth.AuthFactor
		if err := json.Unmarshal(val, &f); err == nil {
			switch {
			case f.RevokedAt != nil:
				revoking = true
			case f.LastUsedAt != nil:
				// MarkUsed's read-modify-write landing with a stale
				// still-active record: park until the test releases it.
				g.blockOnce.Do(func() { close(g.staleWriteBlocked) })
				<-g.releaseStaleWrite
			}
		}
	}
	g.mu.Lock()
	err := g.afMockKV.Put(ctx, key, val)
	g.mu.Unlock()
	if revoking && err == nil {
		g.sawOnce.Do(func() { close(g.sawRevokingPut) })
	}
	return err
}

// Get delegates under the mutex.
func (g *gatingFactorKV) Get(ctx context.Context, key string) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.afMockKV.Get(ctx, key)
}

// Delete delegates under the mutex.
func (g *gatingFactorKV) Delete(ctx context.Context, key string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.afMockKV.Delete(ctx, key)
}

// List delegates under the mutex.
func (g *gatingFactorKV) List(ctx context.Context, prefix string) ([]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.afMockKV.List(ctx, prefix)
}

// Scan delegates under the mutex.
func (g *gatingFactorKV) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.afMockKV.Scan(ctx, prefix)
}

// TestMarkUsed_ConcurrentRemove_CannotResurrectRevokedFactor pins the
// fail-closed revocation contract at the MarkUsed/Remove seam: the async
// best-effort LastUsedAt update spawned by every successful login
// resolution (auth.LoginResolver.Resolve) must NEVER write a stale
// still-active record over a concurrent revocation. Without MarkUsed
// holding the service mutex, the interleaving
//
//	MarkUsed.Get(active) -> Remove persists RevokedAt -> MarkUsed.Put(stale)
//
// resurrects the revoked factor and a revoked credential resolves again —
// the regression originally caught by the quicbaas M13 last-factor
// integration test.
func TestMarkUsed_ConcurrentRemove_CannotResurrectRevokedFactor(t *testing.T) {
	kv := newGatingFactorKV()
	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{
		KV:               kv,
		UserStateUpdater: newStubUserStateUpdater(),
	})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	ctx := context.Background()

	credential := []byte("piv-last-factor-credential")
	if err := svc.Add(ctx, 1, pivFactor("last", 1, credential)); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Arm the gate AFTER setup so only the contested writes are gated.
	kv.gateArmed = true

	// The login-side async update reads the ACTIVE record, then parks its
	// stale write inside the KV.
	markUsedDone := make(chan error, 1)
	go func() { markUsedDone <- svc.MarkUsed(ctx, "last", time.Now().UTC()) }()
	<-kv.staleWriteBlocked

	// Concurrent force-revocation of the same factor.
	removeDone := make(chan error, 1)
	go func() { removeDone <- svc.Remove(ctx, "last", true) }()

	// Unlocked (buggy) MarkUsed lets Remove's revoking Put land while the
	// stale write is parked — wait for it, then release the stale write so
	// it clobbers the revocation. Correctly serialized MarkUsed makes that
	// ordering impossible (Remove blocks on the service mutex), so the
	// bounded wait lapses and the release simply unparks MarkUsed first.
	select {
	case <-kv.sawRevokingPut:
	case <-time.After(500 * time.Millisecond):
	}
	close(kv.releaseStaleWrite)

	if err := <-markUsedDone; err != nil {
		t.Fatalf("MarkUsed: %v", err)
	}
	if err := <-removeDone; err != nil {
		t.Fatalf("Remove(force=true): %v", err)
	}

	got, err := svc.Get(ctx, "last")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RevokedAt == nil {
		t.Fatal("revoked factor resurrected: MarkUsed's stale write clobbered RevokedAt (lost update)")
	}
	if _, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorPIV, credential); !errors.Is(err, auth.ErrFactorRevoked) {
		t.Fatalf("LookupByCredentialKey after Remove: want ErrFactorRevoked, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Cross-type collision avoidance — same lookup key bytes under different
// factor types must not collide. Same bytes across users is rejected as
// a duplicate (correct: the (type, key) tuple identifies a unique
// credential).
// ---------------------------------------------------------------------------

func TestAdd_sameBytesDifferentType_noCollision(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
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
	svc, _ := newTestUserAuthFactorService(t, nil)
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

// ---------------------------------------------------------------------------
// HasActiveFactor (fail-closed ActiveFactorChecker)
// ---------------------------------------------------------------------------

// failingGetKV wraps afMockKV and injects a Get error for chosen keys, so
// the strict checker's fail-closed behavior under partial reads is testable.
type failingGetKV struct {
	*afMockKV
	failKeys map[string]error
}

func (f *failingGetKV) Get(ctx context.Context, key string) ([]byte, error) {
	if err, ok := f.failKeys[key]; ok {
		return nil, err
	}
	return f.afMockKV.Get(ctx, key)
}

func checkerFor(t *testing.T, svc UserAuthFactorServicer) ActiveFactorChecker {
	t.Helper()
	c, ok := svc.(ActiveFactorChecker)
	if !ok {
		t.Fatal("production service does not implement ActiveFactorChecker")
	}
	return c
}

func TestHasActiveFactor_ActiveAndRevoked(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	ctx := context.Background()
	c := checkerFor(t, svc)

	// No factors at all.
	has, err := c.HasActiveFactor(ctx, 7)
	if err != nil || has {
		t.Fatalf("empty: has=%v err=%v, want false,nil", has, err)
	}

	// One active factor.
	if err := svc.Add(ctx, 7, fido2Factor("f1", 7, []byte("k1"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	has, err = c.HasActiveFactor(ctx, 7)
	if err != nil || !has {
		t.Fatalf("active: has=%v err=%v, want true,nil", has, err)
	}

	// Revoke it (force: last active factor) — revoked factors do not count.
	if err := svc.Remove(ctx, "f1", true); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	has, err = c.HasActiveFactor(ctx, 7)
	if err != nil || has {
		t.Fatalf("revoked-only: has=%v err=%v, want false,nil", has, err)
	}
}

func TestHasActiveFactor_ZeroUserIDRejected(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	if _, err := checkerFor(t, svc).HasActiveFactor(context.Background(), 0); err == nil {
		t.Fatal("expected error for zero userID")
	}
}

// TestHasActiveFactor_SeesOrphanedPrimary asserts the checker answers from
// the canonical primary records, not the per-user index: Add writes the
// primary before the index, so an enrollment whose index write failed
// leaves an active primary no index entry names. List misses it by design;
// the fail-closed checker must not.
func TestHasActiveFactor_SeesOrphanedPrimary(t *testing.T) {
	svc, kv := newTestUserAuthFactorService(t, nil)
	ctx := context.Background()
	if err := svc.Add(ctx, 7, fido2Factor("f1", 7, []byte("k1"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Reproduce the partial-write state: primary present, user index gone.
	delete(kv.store, userIndexKey(7, "f1"))

	if got, err := svc.List(ctx, 7); err != nil || len(got) != 0 {
		t.Fatalf("List = %v,%v — precondition broken, index removal did not orphan the primary", got, err)
	}
	has, err := checkerFor(t, svc).HasActiveFactor(ctx, 7)
	if err != nil || !has {
		t.Fatalf("orphaned primary: has=%v err=%v, want true,nil", has, err)
	}
}

// TestHasActiveFactor_FailsClosedOnRecordReadError asserts a read failure on
// any canonical factor record surfaces as an error — never as "no factors".
// This is the contract List deliberately does not provide.
func TestHasActiveFactor_FailsClosedOnRecordReadError(t *testing.T) {
	base := newAFMockKV()
	kv := &failingGetKV{afMockKV: base, failKeys: map[string]error{}}
	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{KV: kv})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	ctx := context.Background()
	if err := svc.Add(ctx, 7, fido2Factor("f1", 7, []byte("k1"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	kv.failKeys[primaryKey("f1")] = errors.New("transient read failure")

	if _, err := checkerFor(t, svc).HasActiveFactor(ctx, 7); err == nil {
		t.Fatal("expected error on unreadable factor record, got nil")
	}
}

// parkingListKV parks the first List call until released, so the test can
// hold HasActiveFactor inside its critical section while a concurrent Add
// tries to enroll a factor.
type parkingListKV struct {
	*afMockKV
	parked  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *parkingListKV) List(ctx context.Context, prefix string) ([]string, error) {
	p.once.Do(func() { close(p.parked) })
	<-p.release
	return p.afMockKV.List(ctx, prefix)
}

// TestHasActiveFactor_SerializedWithAdd asserts the checker holds the
// service mutation lock for its full scan: an Add racing the check cannot
// land its writes mid-scan. The checker is parked inside List while an Add
// for the same user runs concurrently; because Add serializes on the same
// mutex, the store must still hold no factor when the parked List finally
// executes, and the enrollment must land entirely after the answer.
func TestHasActiveFactor_SerializedWithAdd(t *testing.T) {
	kv := &parkingListKV{
		afMockKV: newAFMockKV(),
		parked:   make(chan struct{}),
		release:  make(chan struct{}),
	}
	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{KV: kv})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	ctx := context.Background()
	c := checkerFor(t, svc)

	hasCh := make(chan bool, 1)
	go func() {
		has, herr := c.HasActiveFactor(ctx, 7)
		if herr != nil {
			t.Errorf("HasActiveFactor: %v", herr)
		}
		hasCh <- has
	}()
	<-kv.parked // checker holds s.mu, parked inside List

	addCh := make(chan error, 1)
	go func() { addCh <- svc.Add(ctx, 7, fido2Factor("f1", 7, []byte("k1"))) }()

	// Release the scan. If Add had bypassed the mutex, its primary record
	// could have landed during the park and the invariant below would trip.
	close(kv.release)
	has := <-hasCh
	if aerr := <-addCh; aerr != nil {
		t.Fatalf("Add: %v", aerr)
	}
	if has {
		// The checker held the lock first, so the Add must have been
		// excluded for the whole scan; seeing the factor means the write
		// interleaved mid-scan.
		t.Fatal("checker observed a factor written during its critical section")
	}

	// After the racing Add lands, the checker must see it.
	has, err = c.HasActiveFactor(ctx, 7)
	if err != nil || !has {
		t.Fatalf("post-add: has=%v err=%v, want true,nil", has, err)
	}
}

// TestHasActiveFactor_IgnoresOtherUsersFactors asserts another user's
// factors — active or revoked — never count toward this user.
func TestHasActiveFactor_IgnoresOtherUsersFactors(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	ctx := context.Background()
	if err := svc.Add(ctx, 8, fido2Factor("f8", 8, []byte("k8"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	has, err := checkerFor(t, svc).HasActiveFactor(ctx, 7)
	if err != nil || has {
		t.Fatalf("cross-user: has=%v err=%v, want false,nil", has, err)
	}
}
