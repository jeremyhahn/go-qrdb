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
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// deleteHarness is a factor service over a store that commits multi-guarded
// batches, the shape every production store resolves to.
type deleteHarness struct {
	svc   UserAuthFactorServicer
	kv    *kvstoretest.GuardedMemoryStore
	users *stubUserStateUpdater
	trail *audit.RingBuffer
}

func newDeleteHarness(t *testing.T) *deleteHarness {
	t.Helper()
	h := &deleteHarness{
		kv:    kvstoretest.NewGuardedMemoryStore(),
		users: newStubUserStateUpdater(),
		trail: audit.NewRingBuffer(16),
	}
	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{
		KV:               h.kv,
		UserStateUpdater: h.users,
		AuditLogger:      h.trail,
	})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	h.svc = svc
	return h
}

// spkiFactor is an mtls factor carrying an SPKI credential, so it owns all
// three index entries a hard delete must drop.
func spkiFactor(id string, userID uint64, cred, spki string) *auth.AuthFactor {
	f := mtlsFactor(id, userID, []byte(cred))
	f.SPKICredential = []byte(spki)
	return f
}

// value reads a raw key, failing the test on a store error.
func (h *deleteHarness) value(t *testing.T, key string) []byte {
	t.Helper()
	v, err := h.kv.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get(%s): %v", key, err)
	}
	return v
}

// set decodes the user's factor-set record.
func (h *deleteHarness) set(t *testing.T, userID uint64) factorSet {
	t.Helper()
	var set factorSet
	if err := json.Unmarshal(h.value(t, factorSetKey(userID)), &set); err != nil {
		t.Fatalf("decode factor set: %v", err)
	}
	return set
}

func TestDelete_dropsEveryIndexInOneBatch(t *testing.T) {
	h := newDeleteHarness(t)
	ctx := context.Background()
	if err := h.svc.Add(ctx, 7, spkiFactor("keep", 7, "cred-keep", "spki-keep")); err != nil {
		t.Fatalf("Add keep: %v", err)
	}
	f := spkiFactor("gone", 7, "cred-gone", "spki-gone")
	if err := h.svc.Add(ctx, 7, f); err != nil {
		t.Fatalf("Add gone: %v", err)
	}
	before := h.set(t, 7)

	if err := h.svc.Delete(ctx, "gone", false); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	for name, key := range map[string]string{
		"primary":    primaryKey("gone"),
		"user index": userIndexKey(7, "gone"),
		"credential": lookupKeyFor(f.Type, f.Credential),
		"spki":       lookupKeyFor(f.Type, f.SPKICredential),
	} {
		if v := h.value(t, key); v != nil {
			t.Errorf("%s entry survived the delete: %q", name, v)
		}
	}
	after := h.set(t, 7)
	if _, ok := after.Active["gone"]; ok {
		t.Error("deleted factor is still in the factor set")
	}
	if _, ok := after.Active["keep"]; !ok {
		t.Error("the sibling factor left the factor set")
	}
	if after.Version != before.Version+1 {
		t.Errorf("factor-set version = %d, want %d", after.Version, before.Version+1)
	}
	if got, err := h.svc.Get(ctx, "keep"); err != nil || got.ID != "keep" {
		t.Fatalf("sibling factor: %v, %v", got, err)
	}
	if len(h.users.calls) != 0 {
		t.Fatalf("a non-last delete changed user state: %+v", h.users.calls)
	}
}

func TestDelete_secondDeleteIsNotFound(t *testing.T) {
	h := newDeleteHarness(t)
	ctx := context.Background()
	if err := h.svc.Add(ctx, 7, mtlsFactor("once", 7, []byte("c"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := h.svc.Delete(ctx, "once", true); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	var nf *auth.FactorNotFoundError
	if err := h.svc.Delete(ctx, "once", true); !errors.As(err, &nf) || nf.FactorID != "once" {
		t.Fatalf("second Delete: want FactorNotFoundError{once}, got %v", err)
	}
	if _, err := h.svc.Get(ctx, "once"); !errors.As(err, &nf) {
		t.Fatalf("Get after delete: want FactorNotFoundError, got %v", err)
	}
}

func TestDelete_freesCredentialForRebind(t *testing.T) {
	h := newDeleteHarness(t)
	ctx := context.Background()
	if err := h.svc.Add(ctx, 7, spkiFactor("first", 7, "cert", "spki")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	var exists *auth.FactorAlreadyExistsError
	if err := h.svc.Add(ctx, 7, spkiFactor("again", 7, "cert", "spki")); !errors.As(err, &exists) {
		t.Fatalf("rebind before delete: want FactorAlreadyExistsError, got %v", err)
	}
	if err := h.svc.Delete(ctx, "first", true); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := h.svc.Add(ctx, 7, spkiFactor("again", 7, "cert", "spki")); err != nil {
		t.Fatalf("rebind after delete: %v", err)
	}
	got, err := h.svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("cert"))
	if err != nil || got.ID != "again" {
		t.Fatalf("lookup after rebind: %v, %v", got, err)
	}
}

func TestDelete_refusesRevokedFactor(t *testing.T) {
	h := newDeleteHarness(t)
	ctx := context.Background()
	if err := h.svc.Add(ctx, 7, mtlsFactor("other", 7, []byte("o"))); err != nil {
		t.Fatalf("Add other: %v", err)
	}
	f := spkiFactor("revoked", 7, "cred", "spki")
	if err := h.svc.Add(ctx, 7, f); err != nil {
		t.Fatalf("Add revoked: %v", err)
	}
	if err := h.svc.Remove(ctx, "revoked", false); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	var revoked *auth.FactorRevokedError
	if err := h.svc.Delete(ctx, "revoked", true); !errors.As(err, &revoked) {
		t.Fatalf("Delete revoked: want FactorRevokedError, got %v", err)
	}
	for _, key := range []string{
		primaryKey("revoked"),
		lookupKeyFor(f.Type, f.Credential),
		lookupKeyFor(f.Type, f.SPKICredential),
	} {
		if string(h.value(t, key)) == "" {
			t.Errorf("revoked factor lost %s", key)
		}
	}
	// The revocation keeps holding the credential: nothing reinstates it.
	var exists *auth.FactorAlreadyExistsError
	if err := h.svc.Add(ctx, 7, spkiFactor("reinstated", 7, "cred", "spki")); !errors.As(err, &exists) {
		t.Fatalf("re-add over a revoked factor: want FactorAlreadyExistsError, got %v", err)
	}
	if len(h.trail.Snapshot()) != 0 {
		t.Fatal("a refused delete was audited")
	}
}

func TestDelete_lastFactor(t *testing.T) {
	h := newDeleteHarness(t)
	ctx := context.Background()
	if err := h.svc.Add(ctx, 7, mtlsFactor("only", 7, []byte("k"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	var last *auth.LastFactorError
	if err := h.svc.Delete(ctx, "only", false); !errors.As(err, &last) {
		t.Fatalf("Delete without force: want LastFactorError, got %v", err)
	}
	if h.value(t, primaryKey("only")) == nil {
		t.Fatal("a refused delete removed the factor")
	}
	if err := h.svc.Delete(ctx, "only", true); err != nil {
		t.Fatalf("Delete force: %v", err)
	}
	if active, ok := h.users.calls[7]; !ok || active {
		t.Fatalf("expected SetUserActive(7, false), got calls=%+v", h.users.calls)
	}
	if len(h.set(t, 7).Active) != 0 {
		t.Fatal("factor set still lists a factor")
	}
}

// TestDelete_keepsIndexRepointedElsewhere covers an index entry that no
// longer names the deleted factor: a rotation moves the SPKI lookup to the
// new factor, and the delete must leave that entry to its new owner.
func TestDelete_keepsIndexRepointedElsewhere(t *testing.T) {
	h := newDeleteHarness(t)
	ctx := context.Background()
	f := spkiFactor("old", 7, "cred-old", "shared-spki")
	if err := h.svc.Add(ctx, 7, f); err != nil {
		t.Fatalf("Add: %v", err)
	}
	spki := lookupKeyFor(f.Type, f.SPKICredential)
	if err := h.kv.Put(ctx, spki, []byte("successor")); err != nil {
		t.Fatalf("repoint: %v", err)
	}
	if err := h.svc.Delete(ctx, "old", true); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := string(h.value(t, spki)); got != "successor" {
		t.Fatalf("SPKI entry = %q, want it left to its new owner", got)
	}
	if v := h.value(t, lookupKeyFor(f.Type, f.Credential)); v != nil {
		t.Fatalf("credential entry survived: %q", v)
	}
}

func TestDelete_auditsTheDeletion(t *testing.T) {
	h := newDeleteHarness(t)
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice", TenantID: "acme"})
	if err := h.svc.Add(ctx, 7, mtlsFactor("f", 7, []byte("k"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := h.svc.Delete(ctx, "f", true); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	events := h.trail.Snapshot()
	if len(events) != 1 {
		t.Fatalf("want one audit event, got %d", len(events))
	}
	ev := events[0]
	if ev.Action != "delete" || ev.Resource != "authfactor:f" || ev.Outcome != audit.OutcomeSuccess {
		t.Fatalf("event = %+v", ev)
	}
	if ev.Actor != "alice" || ev.TenantID != "acme" {
		t.Fatalf("event principal = %q/%q, want alice/acme", ev.Actor, ev.TenantID)
	}
	if ev.Attributes["factor_id"] != "f" || ev.Attributes["force"] != true || ev.Attributes["type"] != string(auth.AuthFactorMTLS) {
		t.Fatalf("event attributes = %+v", ev.Attributes)
	}
}

func TestDelete_rejectsEmptyID(t *testing.T) {
	h := newDeleteHarness(t)
	var invalid *auth.InvalidFactorError
	if err := h.svc.Delete(context.Background(), "", true); !errors.As(err, &invalid) {
		t.Fatalf("want InvalidFactorError, got %v", err)
	}
}

func TestDelete_requiresBatchStore(t *testing.T) {
	svc, kv := newTestUserAuthFactorService(t, newStubUserStateUpdater())
	ctx := context.Background()
	if err := svc.Add(ctx, 7, mtlsFactor("f", 7, []byte("k"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := svc.Delete(ctx, "f", true); !errors.Is(err, ErrUserAuthFactorBatchStoreRequired) {
		t.Fatalf("want ErrUserAuthFactorBatchStoreRequired, got %v", err)
	}
	if v, _ := kv.Get(ctx, primaryKey("f")); v == nil {
		t.Fatal("a refused delete removed the factor")
	}
}

// scriptedBatchStore answers every multi-guarded batch with a fixed outcome
// once armed; before that it applies them, so the factor a test deletes can
// first be enrolled through it.
type scriptedBatchStore struct {
	*kvstoretest.GuardedMemoryStore
	armed   bool
	applied bool
	err     error
	calls   int
}

func (s *scriptedBatchStore) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	if !s.armed {
		return s.GuardedMemoryStore.PutAllIfAllUnchanged(ctx, guards, writes)
	}
	s.calls++
	return s.applied, s.err
}

func newScriptedDeleteService(t *testing.T, store *scriptedBatchStore) UserAuthFactorServicer {
	t.Helper()
	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{KV: store})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	if err := svc.Add(context.Background(), 7, mtlsFactor("f", 7, []byte("k"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	store.armed = true
	return svc
}

func TestDelete_contendedFactorSetGivesUp(t *testing.T) {
	store := &scriptedBatchStore{GuardedMemoryStore: kvstoretest.NewGuardedMemoryStore()}
	svc := newScriptedDeleteService(t, store)
	err := svc.Delete(context.Background(), "f", true)
	var contended *FactorSetContendedError
	if !errors.As(err, &contended) || contended.UserID != 7 {
		t.Fatalf("want FactorSetContendedError{UserID: 7}, got %v", err)
	}
	if store.calls != factorSetCommitAttempts {
		t.Fatalf("attempts = %d, want %d", store.calls, factorSetCommitAttempts)
	}
}

func TestDelete_propagatesStoreError(t *testing.T) {
	boom := errors.New("proposal outcome unknown")
	store := &scriptedBatchStore{GuardedMemoryStore: kvstoretest.NewGuardedMemoryStore(), err: boom}
	svc := newScriptedDeleteService(t, store)
	if err := svc.Delete(context.Background(), "f", true); !errors.Is(err, boom) {
		t.Fatalf("want the store error, got %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("an unknown outcome was retried: %d calls", store.calls)
	}
}

// TestRoutedDelete_reachesTheCallersTenant drives Delete through the routing
// service over a prefix tenant, whose namespaced store must keep the
// multi-guarded batch the hard delete commits through.
func TestRoutedDelete_reachesTheCallersTenant(t *testing.T) {
	shared := kvstoretest.NewGuardedMemoryStore()
	tenants, err := tenant.NewKVTenantService(shared, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	if _, err := tenants.CreateTenant(context.Background(), "acme", "acme", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	res, err := tenant.NewStoreResolver(shared, nil, tenants)
	if err != nil {
		t.Fatalf("NewStoreResolver: %v", err)
	}
	trail := audit.NewRingBuffer(4)
	svc, err := NewRoutingUserAuthFactorService(RoutingUserAuthFactorServiceConfig{Resolver: res, AuditLogger: trail})
	if err != nil {
		t.Fatalf("NewRoutingUserAuthFactorService: %v", err)
	}
	ctx := ctxFor("acme")
	if err := svc.Add(ctx, 1, mtlsFactor("f", 1, []byte("cert"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := svc.Delete(ctx, "f", true); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.Add(ctx, 1, mtlsFactor("g", 1, []byte("cert"))); err != nil {
		t.Fatalf("rebind after delete: %v", err)
	}
	if n := len(trail.Snapshot()); n != 1 {
		t.Fatalf("routed delete audited %d events, want 1", n)
	}
}
