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

//go:build qrdb_vlog

package service

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

const barrierTenantPass = "vault-pass"

// standaloneBarrierFixture is a standalone node's view of barrier tenant
// "vault": a registry holding its barrier over a raw memory backend, and a
// log service whose resolver has no home group for it.
type standaloneBarrierFixture struct {
	reg     *seal.BarrierRegistry
	backend *seal.MemoryBackend
	svc     LogService
	route   *logRoute
	ctx     context.Context
	// wrap, when set, replaces the KV store the source hands out.
	wrap func(kvstore.KVStore) kvstore.KVStore
}

// standaloneBarrierResolver resolves no group for any tenant, as a
// standalone node's resolver does for a barrier tenant, and is the
// fixture's LogBarrierStoreSource.
type standaloneBarrierResolver struct{ f *standaloneBarrierFixture }

func (r *standaloneBarrierResolver) ResolveAuditShard(_ context.Context, tenantID string) (Host, uint64, error) {
	return nil, 0, &BarrierStoreUnavailableError{TenantID: tenantID}
}

func (r *standaloneBarrierResolver) TenantLogStore(_ context.Context, tenantID string) (LogBarrier, kvstore.KVStore, error) {
	tb, err := r.f.reg.GetTenant(tenantID)
	if err != nil {
		return nil, nil, err
	}
	kv, err := seal.NewTenantBarrierKVStore(tb)
	if err != nil {
		return nil, nil, err
	}
	if r.f.wrap != nil {
		kv = r.f.wrap(kv)
	}
	return tb, kv, nil
}

func newStandaloneBarrierFixture(t *testing.T) *standaloneBarrierFixture {
	t.Helper()
	ctx := context.Background()
	system, _ := newTestSoftwareBarrier(t)
	if err := system.Initialize(ctx, seal.MustCredentials(ctx, []byte("system-pass"))); err != nil {
		t.Fatalf("Initialize system: %v", err)
	}
	reg, err := seal.NewBarrierRegistry(testLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	f := &standaloneBarrierFixture{reg: reg, ctx: tenant.WithTenantID(ctx, "vault")}
	f.backend = f.registerVault(t)
	svc, err := NewLogService(LogServiceConfig{Resolver: &standaloneBarrierResolver{f: f}, Databases: noDatabases(t)})
	if err != nil {
		t.Fatalf("NewLogService: %v", err)
	}
	f.svc, f.route = svc, svc.(*logService).route
	return f
}

// registerVault registers and initializes a fresh barrier for "vault" over
// a fresh memory backend, returning the backend.
func (f *standaloneBarrierFixture) registerVault(t *testing.T) *seal.MemoryBackend {
	t.Helper()
	ctx := context.Background()
	strategy, err := seal.NewSoftwareStrategy(ctx, []byte(barrierTenantPass))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	backend := seal.NewMemoryBackend()
	tb, err := f.reg.RegisterTenant(ctx, "vault", backend, seal.BarrierConfig{}, strategy)
	if err != nil {
		t.Fatalf("RegisterTenant: %v", err)
	}
	if err := tb.Barrier().Initialize(ctx, seal.MustCredentials(ctx, []byte(barrierTenantPass))); err != nil {
		t.Fatalf("Initialize vault: %v", err)
	}
	return backend
}

var barrierTenantRef = LogRef{Database: "ledger", Log: "events"}

// createAndAppend creates the fixture's log and appends entries to it.
func (f *standaloneBarrierFixture) createAndAppend(t *testing.T, entries [][]byte) *CreateLogResponse {
	t.Helper()
	created, err := f.svc.CreateLog(f.ctx, &CreateLogRequest{LogRef: barrierTenantRef, Origin: "example.test/vault/events", SigningKeyType: vlog.SignatureEd25519})
	if err != nil {
		t.Fatalf("CreateLog: %v", err)
	}
	appended, err := f.svc.Append(f.ctx, &AppendRequest{LogRef: barrierTenantRef, Entries: entries})
	if err != nil || appended.FirstIndex != 0 || appended.Count != len(entries) {
		t.Fatalf("Append = %+v, %v; want first index 0, count %d", appended, err, len(entries))
	}
	return created
}

// headSize returns the size of the fixture log's head.
func (f *standaloneBarrierFixture) headSize(t *testing.T) uint64 {
	t.Helper()
	head, err := f.svc.Head(f.ctx, &HeadRequest{LogRef: barrierTenantRef})
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	return head.Head.Size
}

func TestLogService_ServesABarrierTenantOnItsStandaloneBarrier(t *testing.T) {
	f := newStandaloneBarrierFixture(t)
	entries := [][]byte{[]byte("vault-entry-alpha-0001"), []byte("vault-entry-bravo-0002"), []byte("vault-entry-charlie-0003")}
	created := f.createAndAppend(t, entries)
	got, err := f.svc.GetEntries(f.ctx, &GetEntriesRequest{LogRef: barrierTenantRef})
	if err != nil || len(got.Entries) != len(entries) {
		t.Fatalf("GetEntries = %+v, %v; want %d entries", got, err, len(entries))
	}
	for i, e := range got.Entries {
		if e.Index != uint64(i) || !bytes.Equal(e.Body, entries[i]) {
			t.Errorf("entry %d = %d %q, want %d %q", i, e.Index, e.Body, i, entries[i])
		}
	}
	if size := f.headSize(t); size != 3 {
		t.Fatalf("head size = %d, want 3", size)
	}
	if proof, err := f.svc.ProveInclusion(f.ctx, &InclusionRequest{LogRef: barrierTenantRef, Index: 1, Size: 3}); err != nil || len(proof.Hashes) == 0 {
		t.Fatalf("ProveInclusion = %+v, %v; want a proof", proof, err)
	}
	if proof, err := f.svc.ProveConsistency(f.ctx, &ConsistencyRequest{LogRef: barrierTenantRef, Size1: 1, Size2: 3}); err != nil || len(proof.Hashes) == 0 {
		t.Fatalf("ProveConsistency = %+v, %v; want a proof", proof, err)
	}
	signed, err := f.svc.SignHead(f.ctx, &SignHeadRequest{LogRef: barrierTenantRef})
	if err != nil {
		t.Fatalf("SignHead: %v", err)
	}
	pub, err := created.Key.Public()
	if err != nil {
		t.Fatalf("created key: %v", err)
	}
	if err := vlog.VerifyHead(pub, "example.test/vault/events", signed.Head); err != nil || signed.Head.Size != 3 {
		t.Fatalf("VerifyHead(size %d) = %v; want a verified head of size 3", signed.Head.Size, err)
	}

	// Every record is in the reserved keyspace, none in the home store's.
	tb, err := f.reg.GetTenant("vault")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	ctx := context.Background()
	if keys, err := tb.List(ctx, barrierLogPrefix); err != nil || len(keys) == 0 {
		t.Fatalf("List(%s) = %v, %v; want the log's records", barrierLogPrefix, keys, err)
	}
	if keys, err := tb.List(ctx, tenant.HomeName+"/"); err != nil || len(keys) != 0 {
		t.Fatalf("List(%s/) = %v, %v; want nothing in the home keyspace", tenant.HomeName, keys, err)
	}

	// The raw backend holds no entry in plaintext.
	raw, err := f.backend.List(ctx, "")
	if err != nil || len(raw) == 0 {
		t.Fatalf("raw List = %v, %v; want the barrier's records", raw, err)
	}
	for _, k := range raw {
		v, err := f.backend.Get(ctx, k)
		if err != nil {
			t.Fatalf("raw Get(%s): %v", k, err)
		}
		for _, e := range entries {
			if bytes.Contains(v, e) || bytes.Contains([]byte(k), e) {
				t.Errorf("raw record %s holds %q in plaintext", k, e)
			}
		}
	}
}

func TestLogService_SealedStandaloneBarrierTenantRefuses(t *testing.T) {
	f := newStandaloneBarrierFixture(t)
	f.createAndAppend(t, [][]byte{[]byte("one"), []byte("two"), []byte("three")})
	if err := f.reg.SealTenant("vault"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	if _, err := f.svc.Append(f.ctx, &AppendRequest{LogRef: barrierTenantRef, Entries: [][]byte{[]byte("sealed")}}); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("Append on a sealed barrier = %v, want tenant.ErrTenantSealed", err)
	}
	if _, err := f.svc.Head(f.ctx, &HeadRequest{LogRef: barrierTenantRef}); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("Head on a sealed barrier = %v, want tenant.ErrTenantSealed", err)
	}
	if s := f.route.barriers["vault"]; s == nil || s.logs.chain != nil {
		t.Fatalf("store %v keeps its chain across the seal", s)
	}
	ctx := context.Background()
	if err := f.reg.UnsealTenant(ctx, "vault", seal.MustCredentials(ctx, []byte(barrierTenantPass))); err != nil {
		t.Fatalf("UnsealTenant: %v", err)
	}
	if size := f.headSize(t); size != 3 {
		t.Fatalf("head size after unseal = %d, want 3", size)
	}
	appended, err := f.svc.Append(f.ctx, &AppendRequest{LogRef: barrierTenantRef, Entries: [][]byte{[]byte("four")}})
	if err != nil || appended.FirstIndex != 3 {
		t.Fatalf("Append after unseal = %+v, %v; want first index 3", appended, err)
	}
	if size := f.headSize(t); size != 4 {
		t.Fatalf("head size = %d, want 4", size)
	}
}

func TestLogService_RecycledStandaloneBarrierDropsTheChain(t *testing.T) {
	f := newStandaloneBarrierFixture(t)
	f.createAndAppend(t, [][]byte{[]byte("one")})
	before := f.route.barriers["vault"]
	if before == nil || before.logs.chain == nil {
		t.Fatalf("store %v holds no chain after an append", before)
	}
	if err := f.reg.RemoveTenant("vault"); err != nil {
		t.Fatalf("RemoveTenant: %v", err)
	}
	f.registerVault(t)
	// The new barrier holds no log: an append must not continue the old
	// barrier's chain.
	if _, err := f.svc.Append(f.ctx, &AppendRequest{LogRef: barrierTenantRef, Entries: [][]byte{[]byte("two")}}); err == nil {
		t.Fatal("Append on the recycled barrier succeeded; want the log absent")
	}
	after := f.route.barriers["vault"]
	if after == nil || after == before {
		t.Fatalf("store after recycle = %p, before = %p; want a fresh store", after, before)
	}
	f.createAndAppend(t, [][]byte{[]byte("fresh")})
	if size := f.headSize(t); size != 1 {
		t.Fatalf("head size on the recycled barrier = %d, want 1", size)
	}
}

// unbatchedKVStore hides every capability of its store but KVStore.
type unbatchedKVStore struct{ kvstore.KVStore }

func TestLogService_StandaloneBarrierStoreWithoutAtomicBatchRefuses(t *testing.T) {
	f := newStandaloneBarrierFixture(t)
	f.wrap = func(kv kvstore.KVStore) kvstore.KVStore { return unbatchedKVStore{kv} }
	_, err := f.svc.CreateLog(f.ctx, &CreateLogRequest{LogRef: barrierTenantRef, Origin: "example.test/vault/events", SigningKeyType: vlog.SignatureEd25519})
	if !errors.Is(err, errBarrierLogStoreNotAtomic) {
		t.Fatalf("CreateLog over an unbatched store = %v, want errBarrierLogStoreNotAtomic", err)
	}
	tb, err := f.reg.GetTenant("vault")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if keys, err := tb.List(context.Background(), barrierLogPrefix); err != nil || len(keys) != 0 {
		t.Fatalf("List(%s) = %v, %v; want nothing written", barrierLogPrefix, keys, err)
	}
}

// homeGroupSource is a LogGroupSource with no standalone store.
type homeGroupSource struct{}

func (homeGroupSource) TenantHomeGroup(context.Context, string) (Host, uint64, error) {
	return nil, 7, nil
}

// barrierStoreGroupSource is a LogGroupSource that is a LogBarrierStoreSource.
type barrierStoreGroupSource struct {
	homeGroupSource
	tenantID string
}

func (s *barrierStoreGroupSource) TenantLogStore(_ context.Context, tenantID string) (LogBarrier, kvstore.KVStore, error) {
	s.tenantID = tenantID
	return nil, nil, errors.New("served")
}

func TestSystemShardAuditResolver_TenantLogStoreForwardsToItsGroups(t *testing.T) {
	ctx := context.Background()
	var unavailable *BarrierStoreUnavailableError
	if _, _, err := NewSystemShardAuditResolver(nil, 1, nil, homeGroupSource{}).TenantLogStore(ctx, "vault"); !errors.As(err, &unavailable) || unavailable.TenantID != "vault" {
		t.Fatalf("TenantLogStore without a store source = %v, want BarrierStoreUnavailableError for vault", err)
	}
	if _, _, err := NewSystemShardAuditResolver(nil, 1, nil, nil).TenantLogStore(ctx, "vault"); !errors.As(err, &unavailable) {
		t.Fatalf("TenantLogStore without groups = %v, want BarrierStoreUnavailableError", err)
	}
	src := &barrierStoreGroupSource{}
	if _, _, err := NewSystemShardAuditResolver(nil, 1, nil, src).TenantLogStore(ctx, "vault"); err == nil || err.Error() != "served" || src.tenantID != "vault" {
		t.Fatalf("TenantLogStore = %v (asked for %q), want the source's answer for vault", err, src.tenantID)
	}
}
