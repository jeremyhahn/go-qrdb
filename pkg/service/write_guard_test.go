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
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// recordingHost records every command proposed to it and answers with
// result.
func recordingHost(result sm.Result) (*mockHost, *[][]byte) {
	var cmds [][]byte
	record := func(cmd []byte) { cmds = append(cmds, append([]byte(nil), cmd...)) }
	return &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			record(cmd)
			return result, nil
		},
		proposeWithModeFn: func(_ context.Context, _ uint64, cmd []byte, _ writemode.Mode) (sm.Result, error) {
			record(cmd)
			return result, nil
		},
	}, &cmds
}

func mustDropFenceGuard(t *testing.T, tenantID, db string) WriteGuard {
	t.Helper()
	g, err := DropFenceGuard(tenantID, db, "inc-"+db)
	if err != nil {
		t.Fatalf("DropFenceGuard: %v", err)
	}
	return g
}

// The backward-compatibility half of the wire format: a write proposed
// under a context with no guard is byte-for-byte the command it always was.
func TestPropose_WithoutAGuardProposesTheCommandUnchanged(t *testing.T) {
	host, cmds := recordingHost(sm.Result{})
	svc := NewKVService(host, 1)

	if err := svc.Put(context.Background(), "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	want, err := kv.EncodePut([]byte("k"), []byte("v"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	if len(*cmds) != 1 || !bytes.Equal((*cmds)[0], want) {
		t.Fatalf("proposed %x, want the unguarded command %x", *cmds, want)
	}
}

// Under a guarded context the same write is wrapped, and the wrapped inner
// command is exactly the unguarded one.
func TestPropose_WithAGuardWrapsTheCommand(t *testing.T) {
	host, cmds := recordingHost(sm.Result{})
	svc := NewKVService(host, 1)
	ctx := WithWriteGuard(context.Background(), mustDropFenceGuard(t, "acme", "orders"))

	if err := svc.PutWithMode(ctx, "k", []byte("v"), writemode.Sync); err != nil {
		t.Fatalf("PutWithMode: %v", err)
	}
	if len(*cmds) != 1 || (*cmds)[0][0] != protocol.OpGuarded {
		t.Fatalf("proposed %x, want one OpGuarded command", *cmds)
	}
	compares, inner, err := protocol.DecodeGuarded((*cmds)[0][1:])
	if err != nil {
		t.Fatalf("DecodeGuarded: %v", err)
	}
	if len(compares) != 1 || string(compares[0].Key) != DropFenceKey("acme", "orders") {
		t.Fatalf("compares = %+v, want the drop fence of acme/orders", compares)
	}
	want, _ := kv.EncodePut([]byte("k"), []byte("v"))
	if !bytes.Equal(inner, want) {
		t.Fatalf("inner = %x, want %x", inner, want)
	}
}

// A guard refusal from the state machine surfaces as the typed drop error,
// marked as provably not stored, for the database the fence names.
func TestCheckResultError_DropFenceRefusalIsDroppingError(t *testing.T) {
	data := protocol.EncodeGuardRefusal([]byte(DropFenceKey("acme", "orders")))
	host, _ := recordingHost(sm.Result{Data: data})
	svc := NewKVService(host, 1)

	err := svc.Put(context.Background(), "k", []byte("v"))
	var dropping *database.DroppingError
	if !errors.As(err, &dropping) {
		t.Fatalf("error = %v (%T), want *database.DroppingError", err, err)
	}
	if dropping.Name != "orders" || dropping.TenantID != "acme" {
		t.Fatalf("DroppingError = %+v, want acme/orders", dropping)
	}
	if !errors.Is(err, ErrNotStored) {
		t.Fatal("a refused write stored nothing and must match ErrNotStored")
	}
}

// Any other refused guard is reported as itself.
func TestCheckResultError_OtherGuardRefusalIsWriteGuardRefusedError(t *testing.T) {
	err := checkResultError(sm.Result{Data: protocol.EncodeGuardRefusal([]byte("some/key"))})
	var refused *WriteGuardRefusedError
	if !errors.As(err, &refused) || refused.Key != "some/key" {
		t.Fatalf("error = %v (%T), want *WriteGuardRefusedError for some/key", err, err)
	}
	if !errors.Is(err, ErrNotStored) || refused.Error() == "" {
		t.Fatal("WriteGuardRefusedError must match ErrNotStored and describe itself")
	}
}

func TestDropFenceKey_ComposesTheNamespace(t *testing.T) {
	if got := DropFenceKey("acme", "orders"); got != DropFencePrefix+"acme/orders/" {
		t.Fatalf("DropFenceKey = %q", got)
	}
	if got := DropFenceKey("", ""); got != DropFencePrefix+tenant.ComposeNamespace("", "") {
		t.Fatalf("DropFenceKey for the defaults = %q", got)
	}
	if !IsInternalKey(DropFenceKey("acme", "orders")) {
		t.Fatal("the drop fence must live in the internal key space no namespaced view can write")
	}
}

func TestDroppingErrorFor_ParsesOnlyFenceKeys(t *testing.T) {
	tests := []struct {
		key    string
		ok     bool
		tenant string
		db     string
	}{
		{DropFenceKey("acme", "orders"), true, "acme", "orders"},
		{DropFencePrefix + "acme/orders", false, "", ""},
		{DropFencePrefix + "acme/orders/extra", false, "", ""},
		{DropFencePrefix + "/orders/", false, "", ""},
		{DropFencePrefix + "acme//", false, "", ""},
		{DropFencePrefix + "acme", false, "", ""},
		{"acme/orders/", false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got, ok := droppingErrorFor(tt.key)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && (got.TenantID != tt.tenant || got.Name != tt.db) {
				t.Fatalf("DroppingError = %+v", got)
			}
		})
	}
}

func TestNewWriteGuard_ValidatesTargets(t *testing.T) {
	if g, err := NewWriteGuard(); err != nil || !g.IsZero() {
		t.Fatalf("NewWriteGuard() = (%v, %v), want the zero guard", g, err)
	}
	var guardErr *WriteGuardError
	_, err := NewWriteGuard(Compare{Key: []byte("k"), Target: CompareModRevision, ModRevision: 1})
	if !errors.As(err, &guardErr) || !errors.Is(err, ErrNotStored) || guardErr.Error() == "" {
		t.Fatalf("revision target: error = %v, want *WriteGuardError", err)
	}
	if guardErr.Unwrap() == nil {
		t.Fatal("WriteGuardError must wrap the cause")
	}
	if _, err := NewWriteGuard(Compare{Key: []byte("k"), Target: CompareValue, Value: []byte("v")}); err != nil {
		t.Fatalf("value target: %v", err)
	}
}

func TestWithWriteGuard_MergesAndDeduplicates(t *testing.T) {
	a := mustDropFenceGuard(t, "acme", "orders")
	b := mustDropFenceGuard(t, "acme", "billing")
	ctx := context.Background()

	if got := WithWriteGuard(ctx, WriteGuard{}); got != ctx {
		t.Fatal("attaching the zero guard must return ctx unchanged")
	}
	withA := WithWriteGuard(ctx, a)
	if again := WithWriteGuard(withA, a); again != withA {
		t.Fatal("attaching the same guard twice must be idempotent")
	}
	both := WithWriteGuard(withA, b)
	if n := len(WriteGuardFrom(both).compares); n != 2 {
		t.Fatalf("merged guard has %d compares, want 2", n)
	}
	if n := len(WriteGuardFrom(withA).compares); n != 1 {
		t.Fatalf("merging must not mutate the outer context's guard (it has %d compares)", n)
	}
	if !WriteGuardFrom(ctx).IsZero() {
		t.Fatal("a context with no guard must report the zero guard")
	}
}

func TestGuardCommand_RefusesWhatCannotBeWrapped(t *testing.T) {
	ctx := WithWriteGuard(context.Background(), mustDropFenceGuard(t, "acme", "orders"))
	var guardErr *WriteGuardError
	if _, err := guardCommand(ctx, nil, 1, nil); !errors.As(err, &guardErr) {
		t.Fatalf("empty command: error = %v, want *WriteGuardError", err)
	}
	guarded, err := guardCommand(ctx, nil, 1, []byte{kv.OpPut, 0, 0})
	if err != nil {
		t.Fatalf("guardCommand: %v", err)
	}
	if _, err := guardCommand(ctx, nil, 1, guarded); !errors.As(err, &guardErr) {
		t.Fatalf("already-guarded command: error = %v, want *WriteGuardError", err)
	}

	host, cmds := recordingHost(sm.Result{})
	if _, err := propose(ctx, host, 1, nil); !errors.As(err, &guardErr) {
		t.Fatalf("propose of an unwrappable command: error = %v", err)
	}
	if _, err := proposeWithMode(ctx, host, 1, nil, writemode.Sync); !errors.As(err, &guardErr) {
		t.Fatalf("proposeWithMode of an unwrappable command: error = %v", err)
	}
	if len(*cmds) != 0 {
		t.Fatal("nothing may be proposed when the guard cannot be applied: that would be a fail-open")
	}
}

// The service-level fence verbs set the marker to the tombstone, to an
// incarnation, or remove it, in the store the resolver names.
func TestKeyspaceReclaimService_FenceAndUnfence(t *testing.T) {
	ctx := context.Background()
	system := newMemoryKVSvc()
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: system})
	key := DropFenceKey("acme", "orders")

	if err := svc.UnfenceDatabaseKeyspace(ctx, "acme", "orders", "inc-1"); err != nil {
		t.Fatalf("UnfenceDatabaseKeyspace: %v", err)
	}
	if v, err := system.Get(ctx, key); err != nil || string(v) != "inc-1" {
		t.Fatalf("marker = (%q, %v), want the live incarnation", v, err)
	}
	if err := svc.FenceDatabaseKeyspace(ctx, "acme", "orders"); err != nil {
		t.Fatalf("FenceDatabaseKeyspace: %v", err)
	}
	if v, err := system.Get(ctx, key); err != nil || !bytes.Equal(v, dropFenceTombstone) {
		t.Fatalf("marker = (%q, %v), want the dropped tombstone", v, err)
	}
	if err := svc.FenceDatabaseKeyspace(ctx, "acme", "orders"); err != nil {
		t.Fatalf("fencing twice must be idempotent: %v", err)
	}
	if err := svc.UnfenceDatabaseKeyspace(ctx, "acme", "orders", ""); err != nil {
		t.Fatalf("UnfenceDatabaseKeyspace with no incarnation: %v", err)
	}
	if _, err := system.Get(ctx, key); err == nil {
		t.Fatal("an empty incarnation is live while the marker is ABSENT; the marker survived")
	}
	if err := svc.UnfenceDatabaseKeyspace(ctx, "acme", "orders", ""); err != nil {
		t.Fatalf("removing an absent marker must succeed: %v", err)
	}

	var scopeErr *ReclaimScopeError
	if err := svc.FenceDatabaseKeyspace(ctx, "acme", ""); !errors.As(err, &scopeErr) {
		t.Fatalf("unscoped fence: error = %v", err)
	}
	if err := svc.UnfenceDatabaseKeyspace(ctx, "acme", "", "inc-1"); !errors.As(err, &scopeErr) {
		t.Fatalf("unscoped unfence: error = %v", err)
	}
}

// A store that belongs to one database alone -- a dedicated-raft shard --
// IS the incarnation, so its marker is never set to one: it is removed.
func TestKeyspaceReclaimService_UnfencePerDatabaseStoreRemovesTheMarker(t *testing.T) {
	ctx := context.Background()
	shard := newMemoryKVSvc()
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: newMemoryKVSvc()})
	svc.SetStoreResolver(&fixedStoreResolver{target: ReclaimTarget{KV: shard}, dedicated: true, perDatabase: true})

	if err := svc.FenceDatabaseKeyspace(ctx, "rt", "orders"); err != nil {
		t.Fatalf("FenceDatabaseKeyspace: %v", err)
	}
	if err := svc.UnfenceDatabaseKeyspace(ctx, "rt", "orders", "inc-1"); err != nil {
		t.Fatalf("UnfenceDatabaseKeyspace: %v", err)
	}
	if _, err := shard.Get(ctx, DropFenceKey("rt", "orders")); err == nil {
		t.Fatal("a per-database store's marker must be absent while the database is live")
	}
}

func TestKeyspaceReclaimService_FenceRoutesThroughTheResolver(t *testing.T) {
	ctx := context.Background()
	system := newMemoryKVSvc()
	dedicated := newMemoryKVSvc()
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: system})
	svc.SetStoreResolver(&fixedStoreResolver{target: ReclaimTarget{KV: dedicated}, dedicated: true})

	if err := svc.FenceDatabaseKeyspace(ctx, "acme", "orders"); err != nil {
		t.Fatalf("FenceDatabaseKeyspace: %v", err)
	}
	if _, err := dedicated.Get(ctx, DropFenceKey("acme", "orders")); err != nil {
		t.Fatalf("the fence is not in the dedicated store: %v", err)
	}
	if _, err := system.Get(ctx, DropFenceKey("acme", "orders")); err == nil {
		t.Fatal("the fence was written to the system store as well")
	}

	svc.SetStoreResolver(&fixedStoreResolver{dedicated: false})
	if err := svc.FenceDatabaseKeyspace(ctx, "acme", "billing"); err != nil {
		t.Fatalf("FenceDatabaseKeyspace: %v", err)
	}
	if _, err := system.Get(ctx, DropFenceKey("acme", "billing")); err != nil {
		t.Fatalf("a non-dedicated database's fence belongs in the system store: %v", err)
	}
}

func TestKeyspaceReclaimService_FenceFailures(t *testing.T) {
	ctx := context.Background()
	system := newMemoryKVSvc()

	// A resolver error fails both verbs rather than falling back.
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: system})
	svc.SetStoreResolver(&fixedStoreResolver{err: errors.New("host down")})
	var unavailable *ReclaimStoreUnavailableError
	if err := svc.FenceDatabaseKeyspace(ctx, "acme", "orders"); !errors.As(err, &unavailable) {
		t.Fatalf("resolver error: FenceDatabaseKeyspace = %v", err)
	}
	if err := svc.UnfenceDatabaseKeyspace(ctx, "acme", "orders", "inc-1"); !errors.As(err, &unavailable) {
		t.Fatalf("resolver error: UnfenceDatabaseKeyspace = %v", err)
	}

	// A dedicated store that does not exist: nothing to unfence, and a drop
	// refuses to fence the system store in its place.
	svc.SetStoreResolver(&fixedStoreResolver{dedicated: true})
	var fenceErr *DropFenceError
	var missing *FenceStoreMissingError
	err := svc.FenceDatabaseKeyspace(ctx, "acme", "orders")
	if !errors.As(err, &fenceErr) || !errors.As(err, &missing) {
		t.Fatalf("missing store: FenceDatabaseKeyspace = %v, want DropFenceError wrapping FenceStoreMissingError", err)
	}
	if fenceErr.Error() == "" || missing.Error() == "" {
		t.Fatal("the fence errors must describe themselves")
	}
	if err := svc.UnfenceDatabaseKeyspace(ctx, "acme", "orders", "inc-1"); err != nil {
		t.Fatalf("missing store: UnfenceDatabaseKeyspace = %v, want success", err)
	}

	// A store write failure is reported with the verb that failed.
	boom := errors.New("disk")
	svc = NewKeyspaceReclaimService(ReclaimTarget{KV: &failingReclaimKV{putErr: boom, deleteErr: boom}})
	if err := svc.FenceDatabaseKeyspace(ctx, "acme", "orders"); !errors.As(err, &fenceErr) || fenceErr.Op != "write" || !errors.Is(err, boom) {
		t.Fatalf("Put failure: FenceDatabaseKeyspace = %v", err)
	}
	if err := svc.UnfenceDatabaseKeyspace(ctx, "acme", "orders", ""); !errors.As(err, &fenceErr) || fenceErr.Op != "remove" || !errors.Is(err, boom) {
		t.Fatalf("Delete failure: UnfenceDatabaseKeyspace = %v", err)
	}
	if err := svc.UnfenceDatabaseKeyspace(ctx, "acme", "orders", "inc-1"); !errors.As(err, &fenceErr) || fenceErr.Op != "mark live" || !errors.Is(err, boom) {
		t.Fatalf("Put failure: UnfenceDatabaseKeyspace = %v", err)
	}
}

// The drop-fence guard names an incarnation: it holds only while the marker
// holds exactly that value, and an empty incarnation holds only while the
// marker is absent.
func TestDropFenceGuard_IsIncarnationScoped(t *testing.T) {
	g, err := DropFenceGuard("acme", "orders", "inc-1")
	if err != nil {
		t.Fatalf("DropFenceGuard: %v", err)
	}
	if len(g.compares) != 1 {
		t.Fatalf("guard has %d compares, want 1", len(g.compares))
	}
	c, _, err := protocol.DecodeCompare(g.compares[0])
	if err != nil {
		t.Fatalf("DecodeCompare: %v", err)
	}
	tests := []struct {
		name    string
		current []byte
		want    bool
	}{
		{"live incarnation", []byte("inc-1"), true},
		{"a later incarnation of the name", []byte("inc-2"), false},
		{"dropped", dropFenceTombstone, false},
		{"absent", nil, false},
	}
	for _, tt := range tests {
		if got := protocol.CompareHolds(c, tt.current); got != tt.want {
			t.Errorf("%s: guard holds = %v, want %v", tt.name, got, tt.want)
		}
	}

	legacy, err := DropFenceGuard("acme", "orders", "")
	if err != nil {
		t.Fatalf("DropFenceGuard: %v", err)
	}
	lc, _, _ := protocol.DecodeCompare(legacy.compares[0])
	if !protocol.CompareHolds(lc, nil) {
		t.Error("an empty incarnation must hold while the marker is absent")
	}
	if protocol.CompareHolds(lc, dropFenceTombstone) || protocol.CompareHolds(lc, []byte("inc-2")) {
		t.Error("an empty incarnation must be refused once the name is dropped or created again")
	}
}

// A create must never take a name whose marker holds ANOTHER live
// incarnation -- a database of that name created on another node, whose
// catalog this node does not share. The marker may be claimed only from
// absent or from the dropped tombstone.
func TestKeyspaceReclaimService_UnfenceNeverOverwritesALiveIncarnation(t *testing.T) {
	ctx := context.Background()
	system := newMemoryKVSvc()
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: system})
	key := DropFenceKey("acme", "orders")

	if err := svc.UnfenceDatabaseKeyspace(ctx, "acme", "orders", "node-a-incarnation"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := svc.UnfenceDatabaseKeyspace(ctx, "acme", "orders", "node-b-incarnation"); err == nil {
		t.Fatal("a second create overwrote a live incarnation")
	}
	if v, _ := system.Get(ctx, key); string(v) != "node-a-incarnation" {
		t.Fatalf("marker = %q, want node A's incarnation untouched", v)
	}
	if err := svc.UnfenceDatabaseKeyspace(ctx, "acme", "orders", ""); err == nil {
		t.Fatal("removing the marker of a live incarnation must be refused")
	}
	if v, _ := system.Get(ctx, key); string(v) != "node-a-incarnation" {
		t.Fatalf("marker = %q, want node A's incarnation untouched", v)
	}
	if err := svc.UnfenceDatabaseKeyspace(ctx, "acme", "orders", "node-a-incarnation"); err != nil {
		t.Fatalf("re-marking the live incarnation must be idempotent: %v", err)
	}
	if err := svc.FenceDatabaseKeyspace(ctx, "acme", "orders"); err != nil {
		t.Fatalf("FenceDatabaseKeyspace: %v", err)
	}
	if err := svc.UnfenceDatabaseKeyspace(ctx, "acme", "orders", "node-b-incarnation"); err != nil {
		t.Fatalf("a create over the dropped tombstone must succeed: %v", err)
	}
}
