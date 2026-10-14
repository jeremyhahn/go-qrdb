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

// White-box tests for kvStoreAdapter's index routing and atomic delegation.
package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// ---------------------------------------------------------------------------
// mockIndexServicer
// ---------------------------------------------------------------------------

// createdIndex records one CreateIndex call so a test can assert the adapter
// forwarded the name and field the DAO will later look up under.
type createdIndex struct {
	name      string
	fieldName string
	config    index.IndexConfig
}

// mockIndexServicer stands in for the node's IndexServicer. It records what
// the adapter asked for and returns what a test tells it to.
type mockIndexServicer struct {
	created   []createdIndex
	createErr error

	queryKeys [][]byte
	queryErr  error
	queried   []string

	scanEntries map[string][][]byte
	scanErr     error
}

func (m *mockIndexServicer) CreateIndex(_ context.Context, name, fieldName string, config index.IndexConfig) error {
	m.created = append(m.created, createdIndex{name: name, fieldName: fieldName, config: config})
	return m.createErr
}

func (m *mockIndexServicer) DropIndex(context.Context, string) error { return nil }

// DropOwnedIndex answers as DropIndex does: this double records no
// ownership, and no test that uses it exercises an owner-checked drop.
func (m *mockIndexServicer) DropOwnedIndex(ctx context.Context, name, _ string) error {
	return m.DropIndex(ctx, name)
}

func (m *mockIndexServicer) Query(_ context.Context, indexName string, _ []byte) ([][]byte, error) {
	m.queried = append(m.queried, indexName)
	return m.queryKeys, m.queryErr
}

func (m *mockIndexServicer) Scan(context.Context, string, []byte, []byte) (map[string][][]byte, error) {
	return m.scanEntries, m.scanErr
}

func (m *mockIndexServicer) ListIndexes(context.Context) ([]string, error) { return nil, nil }

// memberIndexDefs is the registration a DAO over a member entity makes.
func memberIndexDefs() []kvstore.EntityIndex {
	return []kvstore.EntityIndex{{FieldName: "Team", JSONFieldName: "team", CaseSensitive: true}}
}

// ---------------------------------------------------------------------------
// Index routing (#31)
// ---------------------------------------------------------------------------

// TestKVStoreAdapter_RegisterEntityIndexesReachesTheEngine proves the
// registration is real work now, not a no-op that returned nil. The index
// name matters: pkg/dao looks up "{entityType}.{FieldName}", so a name that
// does not match produces queries against an index nobody created.
func TestKVStoreAdapter_RegisterEntityIndexesReachesTheEngine(t *testing.T) {
	idx := &mockIndexServicer{}
	store := newKVStoreAdapter(&mockKVServicer{}, idx)

	if err := store.RegisterEntityIndexes("member", memberIndexDefs()); err != nil {
		t.Fatalf("RegisterEntityIndexes: %v", err)
	}
	if len(idx.created) != 1 {
		t.Fatalf("created %d indexes; want 1", len(idx.created))
	}
	if idx.created[0].name != "member.Team" {
		t.Errorf("index name = %q; want %q — pkg/dao resolves this exact name",
			idx.created[0].name, "member.Team")
	}
	if idx.created[0].fieldName != "team" {
		t.Errorf("field name = %q; want the JSON tag %q", idx.created[0].fieldName, "team")
	}
}

func TestKVStoreAdapter_RegisterEntityIndexesCarriesUniqueness(t *testing.T) {
	idx := &mockIndexServicer{}
	store := newKVStoreAdapter(&mockKVServicer{}, idx)

	err := store.RegisterEntityIndexes("member", []kvstore.EntityIndex{
		{FieldName: "Email", JSONFieldName: "email", Unique: true, Sparse: true, CaseSensitive: true},
	})
	if err != nil {
		t.Fatalf("RegisterEntityIndexes: %v", err)
	}
	got := idx.created[0].config
	if !got.Unique || got.Type != index.IndexTypeUnique {
		t.Errorf("config = %+v; a unique definition must create a unique index", got)
	}
	if !got.Sparse {
		t.Error("the sparse flag must reach the engine")
	}
}

// TestKVStoreAdapter_RegisterEntityIndexesWrapsEngineFailure pins that a real
// registration failure is reported, not swallowed. Swallowing it would leave
// a DAO believing it has an index the engine never created — the same defect
// one layer up.
func TestKVStoreAdapter_RegisterEntityIndexesWrapsEngineFailure(t *testing.T) {
	boom := errors.New("engine unavailable")
	store := newKVStoreAdapter(&mockKVServicer{}, &mockIndexServicer{createErr: boom})

	err := store.RegisterEntityIndexes("member", memberIndexDefs())
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v; want it to wrap the engine failure", err)
	}
	var regErr *kvstore.IndexRegistrationError
	if !errors.As(err, &regErr) {
		t.Fatalf("error = %T; want *kvstore.IndexRegistrationError", err)
	}
	if regErr.EntityType != "member" {
		t.Errorf("EntityType = %q; want %q", regErr.EntityType, "member")
	}
	if errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Error("a transient engine failure must NOT masquerade as ErrIndexUnsupported; " +
			"that would make pkg/dao silently downgrade to full scans forever")
	}
}

// TestKVStoreAdapter_CompoundDefinitionsAreRefusedNotSkipped guards two ways
// this could go wrong.
//
// The first is registering a compound index as if it were a single-field one
// on its first field, producing an index whose contents do not match what a
// compound query asks for.
//
// The second is subtler and was caught in review: SKIPPING the compound
// definition and returning nil. That tells pkg/dao the compound index exists,
// so buildCatalog advertises it, the planner routes a query to
// QueryCompoundIndex, and the query ERRORS where a full scan would have
// answered. Registration is the only point at which the incapability can be
// reported, so it must be reported there.
func TestKVStoreAdapter_CompoundDefinitionsAreRefusedNotSkipped(t *testing.T) {
	idx := &mockIndexServicer{}
	store := newKVStoreAdapter(&mockKVServicer{}, idx)

	err := store.RegisterEntityIndexes("member", []kvstore.EntityIndex{{
		FieldNames:     []string{"Team", "Name"},
		JSONFieldNames: []string{"team", "name"},
	}})
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("RegisterEntityIndexes = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if len(idx.created) != 0 {
		t.Errorf("created %+v; a compound definition must not become a single-field index",
			idx.created)
	}
}

// A qualified name was once refused here, because the engine selected the
// indexes a write maintains by matching the index NAME against the key's
// first path segment. The engine now selects by key SCOPE on every path, and
// the adapter declares the scope explicitly; see
// TestKVStoreAdapter_EngineScopeIsTheDeclaredKeyPrefix below.

func TestKVStoreAdapter_QueryIndexReturnsEngineKeys(t *testing.T) {
	want := [][]byte{[]byte("member/1"), []byte("member/2")}
	idx := &mockIndexServicer{queryKeys: want}
	store := newKVStoreAdapter(&mockKVServicer{}, idx)

	got, err := store.QueryIndex(context.Background(), "member.Team", []byte("alpha"))
	if err != nil {
		t.Fatalf("QueryIndex: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d keys; want %d", len(got), len(want))
	}
	if len(idx.queried) != 1 || idx.queried[0] != "member.Team" {
		t.Errorf("queried %v; want the index name to reach the engine unchanged", idx.queried)
	}
}

func TestKVStoreAdapter_QueryIndexPropagatesEngineError(t *testing.T) {
	boom := errors.New("read failed")
	store := newKVStoreAdapter(&mockKVServicer{}, &mockIndexServicer{queryErr: boom})

	got, err := store.QueryIndex(context.Background(), "member.Team", []byte("alpha"))
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v; want the engine error", err)
	}
	if got != nil {
		t.Errorf("got %v alongside an error; a failed query carries no results", got)
	}
}

func TestKVStoreAdapter_ScanIndexReturnsEngineEntries(t *testing.T) {
	want := map[string][][]byte{"alpha": {[]byte("member/1")}}
	store := newKVStoreAdapter(&mockKVServicer{}, &mockIndexServicer{scanEntries: want})

	got, err := store.ScanIndex(context.Background(), "member.Team", []byte("a"), []byte("z"))
	if err != nil {
		t.Fatalf("ScanIndex: %v", err)
	}
	if len(got["alpha"]) != 1 {
		t.Errorf("got %v; want the engine's entries", got)
	}
}

// TestKVStoreAdapter_WithoutIndexServicerRefuses covers the hand-rolled
// construction (NewKVStoreAdapter, volume-pool cursor): no index subsystem,
// so every index method refuses with the typed error. Refusing is what lets
// pkg/dao plan a full scan; returning (nil, nil) here is what made a tagged
// field answer "no records".
func TestKVStoreAdapter_WithoutIndexServicerRefuses(t *testing.T) {
	store := newKVStoreAdapter(&mockKVServicer{}, nil)
	ctx := context.Background()

	if err := store.RegisterEntityIndexes("member", memberIndexDefs()); !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("RegisterEntityIndexes = %v; want kvstore.ErrIndexUnsupported", err)
	}

	keys, err := store.QueryIndex(ctx, "member.Team", []byte("alpha"))
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("QueryIndex error = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if keys != nil {
		t.Errorf("QueryIndex returned %v alongside a refusal", keys)
	}

	entries, err := store.ScanIndex(ctx, "member.Team", []byte("a"), []byte("z"))
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("ScanIndex error = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if entries != nil {
		t.Errorf("ScanIndex returned %v alongside a refusal", entries)
	}
}

// TestKVStoreAdapter_CompoundQueriesAlwaysRefuse holds even with an index
// servicer present: IndexServicer carries no compound verbs, so the adapter
// cannot serve one and must say so.
func TestKVStoreAdapter_CompoundQueriesAlwaysRefuse(t *testing.T) {
	store := newKVStoreAdapter(&mockKVServicer{}, &mockIndexServicer{})
	ctx := context.Background()

	if _, err := store.QueryCompoundIndex(ctx, "member.Team_Name", [][]byte{[]byte("a")}); !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("QueryCompoundIndex = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if _, err := store.ScanCompoundIndex(ctx, "member.Team_Name", [][]byte{[]byte("a")}, nil, nil); !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("ScanCompoundIndex = %v; want kvstore.ErrIndexUnsupported", err)
	}
}

// ---------------------------------------------------------------------------
// Atomic delegation (#30)
// ---------------------------------------------------------------------------

// TestKVStoreAdapter_SatisfiesAtomicKVStore is the capability assertion a
// consumer performs at construction. Before this change the adapter had no
// PutIfAbsent at all, so the SYSTEM tenant — the store Resolve returns for
// SystemTenantID — had no atomic path from the resolver even though the
// engine underneath has a native compare-and-set.
func TestKVStoreAdapter_SatisfiesAtomicKVStore(t *testing.T) {
	var store kvstore.KVStore = newKVStoreAdapter(&mockKVServicer{}, nil)
	if _, ok := store.(kvstore.AtomicKVStore); !ok {
		t.Fatal("kvStoreAdapter must satisfy kvstore.AtomicKVStore")
	}
}

func TestKVStoreAdapter_PutIfAbsentDelegatesToTheService(t *testing.T) {
	var gotKey string
	var gotValue []byte
	svc := &mockKVServicer{
		putIfAbsentFn: func(_ context.Context, key string, value []byte) (bool, error) {
			gotKey, gotValue = key, value
			return true, nil
		},
	}
	store, ok := newKVStoreAdapter(svc, nil).(kvstore.AtomicKVStore)
	if !ok {
		t.Fatal("adapter must be an AtomicKVStore")
	}

	stored, err := store.PutIfAbsent(context.Background(), "member/claim", []byte("v"))
	if err != nil {
		t.Fatalf("PutIfAbsent: %v", err)
	}
	if !stored {
		t.Error("stored = false; the service reported a successful create")
	}
	if gotKey != "member/claim" || string(gotValue) != "v" {
		t.Errorf("service saw (%q, %q); want (%q, %q)", gotKey, gotValue, "member/claim", "v")
	}
}

// TestKVStoreAdapter_PutIfAbsentReportsTheLoser pins that a losing claim is
// reported as stored=false with a NIL error — the contract's way of saying
// "the key was already there", which a caller must be able to tell apart from
// a failure.
func TestKVStoreAdapter_PutIfAbsentReportsTheLoser(t *testing.T) {
	svc := &mockKVServicer{
		putIfAbsentFn: func(context.Context, string, []byte) (bool, error) { return false, nil },
	}
	store, _ := newKVStoreAdapter(svc, nil).(kvstore.AtomicKVStore)

	stored, err := store.PutIfAbsent(context.Background(), "member/claim", []byte("v"))
	if err != nil {
		t.Fatalf("a losing claim is not an error: %v", err)
	}
	if stored {
		t.Error("stored = true; the key already existed")
	}
}

func TestKVStoreAdapter_PutIfAbsentPropagatesServiceError(t *testing.T) {
	boom := errors.New("propose failed")
	svc := &mockKVServicer{
		putIfAbsentFn: func(context.Context, string, []byte) (bool, error) { return false, boom },
	}
	store, _ := newKVStoreAdapter(svc, nil).(kvstore.AtomicKVStore)

	stored, err := store.PutIfAbsent(context.Background(), "member/claim", []byte("v"))
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v; want the service error", err)
	}
	if stored {
		t.Error("stored must be false when the outcome is unknown")
	}
}

// TestNewKVStoreAdapter_RefusesIndexes covers the exported constructor used by
// daemon wiring for the volume pool's replicated cursor. It is built without
// an IndexServicer on purpose — that record is a single cluster-wide scalar —
// so it must refuse rather than answer index queries empty.
func TestNewKVStoreAdapter_RefusesIndexes(t *testing.T) {
	store := NewKVStoreAdapter(&mockKVServicer{})
	if store == nil {
		t.Fatal("NewKVStoreAdapter returned nil")
	}
	if err := store.RegisterEntityIndexes("member", memberIndexDefs()); !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("RegisterEntityIndexes = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if _, err := store.QueryIndex(context.Background(), "member.Team", []byte("alpha")); !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("QueryIndex = %v; want kvstore.ErrIndexUnsupported", err)
	}
}

// TestNewReplicatedSealBackend_UsesTheTransactionForAtomicity pins that the
// seal backend's cross-node compare-and-set goes through the transaction
// service, not the KV service's own PutIfAbsent: the volume pool's
// slot-assignment record needs first-writer-wins across replicas.
func TestNewReplicatedSealBackend_UsesTheTransactionForAtomicity(t *testing.T) {
	backend := NewReplicatedSealBackend(&mockKVServicer{}, nil)
	if backend == nil {
		t.Fatal("NewReplicatedSealBackend returned nil")
	}
	putter, ok := backend.(interface {
		PutIfAbsent(context.Context, string, []byte) (bool, error)
	})
	if !ok {
		t.Fatal("the seal backend must offer PutIfAbsent")
	}
	// nil transaction service: the typed refusal, never a silent success
	// through some other path.
	stored, err := putter.PutIfAbsent(context.Background(), "pool/slot", []byte("v"))
	if !errors.Is(err, ErrNoTransactionService) {
		t.Errorf("error = %v; want ErrNoTransactionService", err)
	}
	if stored {
		t.Error("a refused PutIfAbsent must not report stored=true")
	}
}

// TestKVStoreAdapter_CaseInsensitiveDefinitionsReachTheEngine covers
// go-qrdb#34, and replaces the refusal this adapter used to return.
//
// service.IndexServicer.CreateIndex encoded its request with a wire format
// carrying name, field, unique, sparse and numeric — and no case-sensitivity
// flag — so KVEngine.handleIndexCreate built every index with
// CaseSensitive:true. Accepting a case-insensitive definition would have
// created a case-SENSITIVE index while reporting success, and a DAO lookup for
// "Alpha" over a stored "alpha" would have come back empty with a nil error.
// The definition was therefore refused with kvstore.ErrIndexUnsupported so
// pkg/dao planned a correct full scan.
//
// protocol.IndexFlagCaseInsensitive carries the flag now, so the definition
// must be CREATED, and created with the case behaviour the caller asked for.
// Asserting the forwarded config rather than only the absence of an error is
// the point: an adapter that created the index while dropping the flag would
// satisfy a "no error" assertion and reintroduce the silent-empty defect.
func TestKVStoreAdapter_CaseInsensitiveDefinitionsReachTheEngine(t *testing.T) {
	idx := &mockIndexServicer{}
	store := newKVStoreAdapter(&mockKVServicer{}, idx)

	err := store.RegisterEntityIndexes("member", []kvstore.EntityIndex{
		{FieldName: "Team", JSONFieldName: "team", CaseSensitive: false},
	})
	if err != nil {
		t.Fatalf("RegisterEntityIndexes = %v; want nil", err)
	}
	if len(idx.created) != 1 {
		t.Fatalf("created %d indexes; want 1", len(idx.created))
	}
	if idx.created[0].config.CaseSensitive {
		t.Error("a case-insensitive definition was forwarded as CaseSensitive:true; " +
			"the engine would build a case-SENSITIVE index and answer a folded " +
			"lookup empty with a nil error")
	}
}

// TestKVStoreAdapter_CaseSensitiveDefinitionStaysSensitive is the control and
// the polarity guard. index.IndexConfig names its field for the DEFAULT, so a
// config left at its zero value folds case: an adapter that merely stopped
// refusing, without forwarding the flag, would turn every ordinary index
// case-insensitive.
func TestKVStoreAdapter_CaseSensitiveDefinitionStaysSensitive(t *testing.T) {
	idx := &mockIndexServicer{}
	store := newKVStoreAdapter(&mockKVServicer{}, idx)

	if err := store.RegisterEntityIndexes("member", memberIndexDefs()); err != nil {
		t.Fatalf("RegisterEntityIndexes: %v", err)
	}
	if len(idx.created) != 1 {
		t.Fatalf("created %d indexes; want 1", len(idx.created))
	}
	if !idx.created[0].config.CaseSensitive {
		t.Error("an ordinary definition was forwarded as case-INSENSITIVE")
	}
}

// TestKVStoreAdapter_NumericDefinitionReachesTheEngine proves a numeric
// definition is built with numeric terms, which is what puts the engine's
// range scans over it in numeric order.
func TestKVStoreAdapter_NumericDefinitionReachesTheEngine(t *testing.T) {
	idx := &mockIndexServicer{}
	store := newKVStoreAdapter(&mockKVServicer{}, idx)

	defs := []kvstore.EntityIndex{{FieldName: "Age", JSONFieldName: "age", Numeric: true}}
	if err := store.RegisterEntityIndexes("member", defs); err != nil {
		t.Fatalf("RegisterEntityIndexes: %v", err)
	}
	if len(idx.created) != 1 {
		t.Fatalf("created %d indexes; want 1", len(idx.created))
	}
	if got := idx.created[0].config.ValueType; got != index.IndexValueTypeNumeric {
		t.Errorf("numeric definition forwarded with ValueType %v; want IndexValueTypeNumeric", got)
	}
}

// TestKVStoreAdapter_OrdinaryDefinitionStaysText is the control: a definition
// that does not ask for numeric terms keeps the text encoding.
func TestKVStoreAdapter_OrdinaryDefinitionStaysText(t *testing.T) {
	idx := &mockIndexServicer{}
	store := newKVStoreAdapter(&mockKVServicer{}, idx)

	if err := store.RegisterEntityIndexes("member", memberIndexDefs()); err != nil {
		t.Fatalf("RegisterEntityIndexes: %v", err)
	}
	if len(idx.created) != 1 {
		t.Fatalf("created %d indexes; want 1", len(idx.created))
	}
	if got := idx.created[0].config.ValueType; got != index.IndexValueTypeString {
		t.Errorf("ordinary definition forwarded with ValueType %v; want IndexValueTypeString", got)
	}
}

// TestKVStoreAdapter_EngineScopeIsTheDeclaredKeyPrefix is go-qrdb#40's
// adapter half. The engine maintains an index for exactly the keys under
// engine.ComposeIndexScope(name, declared prefix), so the adapter hands it the
// caller's ABSOLUTE key prefix made relative to the namespace the name
// carries. Whatever the caller's layout -- a bare entity, a DAO built
// WithDatabase, a shared-prefix tenant's "{tenant}/" store, a transport's
// "{tenant}/{database}/" namespace -- the scope the engine ends up maintaining
// must be precisely the prefix the caller's keys carry. Before this, every
// qualified name was refused and a shared-prefix tenant had no index at all.
func TestKVStoreAdapter_EngineScopeIsTheDeclaredKeyPrefix(t *testing.T) {
	tests := []struct {
		name       string
		entityType string
		def        kvstore.EntityIndex
		wantPrefix string // the scope the engine must end up maintaining
	}{
		{
			name:       "bare entity",
			entityType: "member",
			def:        kvstore.EntityIndex{FieldName: "Team", JSONFieldName: "team"},
			wantPrefix: "member/",
		},
		{
			name:       "DAO built WithDatabase",
			entityType: "member",
			def:        kvstore.EntityIndex{IndexName: "tenantdb.member.Team", FieldName: "Team", JSONFieldName: "team"},
			wantPrefix: "member/",
		},
		{
			name:       "shared-prefix tenant store",
			entityType: "workitem",
			def: kvstore.EntityIndex{
				IndexName: "acme/workitem.State", KeyPrefix: "acme/workitem/",
				FieldName: "State", JSONFieldName: "state",
			},
			wantPrefix: "acme/workitem/",
		},
		{
			name:       "per-(tenant, database) namespace",
			entityType: "workitem",
			def: kvstore.EntityIndex{
				IndexName: "acme/sales/workitem.State", KeyPrefix: "acme/sales/workitem/",
				FieldName: "State", JSONFieldName: "state",
			},
			wantPrefix: "acme/sales/workitem/",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			idx := &mockIndexServicer{}
			store := newKVStoreAdapter(&mockKVServicer{}, idx)
			if err := store.RegisterEntityIndexes(tc.entityType, []kvstore.EntityIndex{tc.def}); err != nil {
				t.Fatalf("RegisterEntityIndexes: %v", err)
			}
			if len(idx.created) != 1 {
				t.Fatalf("created %d indexes; want 1", len(idx.created))
			}
			got := idx.created[0]
			if want := tc.def.ResolvedName(tc.entityType); got.name != want {
				t.Errorf("created under %q; want the name the caller queries, %q", got.name, want)
			}
			if scope := string(engine.ComposeIndexScope(got.name, got.config.KeyPrefix)); scope != tc.wantPrefix {
				t.Errorf("engine will maintain scope %q; want %q", scope, tc.wantPrefix)
			}
		})
	}
}

// TestKVStoreAdapter_RefusesANameOutsideItsKeys covers the case the
// translation cannot make true: a name whose namespace the caller's keys do
// not live under. The engine would create it, maintain it over a keyspace no
// write of this entity reaches, and answer every query empty with a nil error.
// Registration is the one point that can be refused honestly.
func TestKVStoreAdapter_RefusesANameOutsideItsKeys(t *testing.T) {
	idx := &mockIndexServicer{}
	store := newKVStoreAdapter(&mockKVServicer{}, idx)

	err := store.RegisterEntityIndexes("workitem", []kvstore.EntityIndex{{
		IndexName:     "globex/sales/workitem.State",
		KeyPrefix:     "acme/sales/workitem/",
		FieldName:     "State",
		JSONFieldName: "state",
	}})
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("RegisterEntityIndexes = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if len(idx.created) != 0 {
		t.Errorf("created %+v; an index over another namespace must not be created", idx.created)
	}
}

// TestKVStoreAdapter_AcceptsTheUnqualifiedName is the control: the name the
// engine does maintain is still created, so the check above is a guard rather
// than a blanket refusal.
func TestKVStoreAdapter_AcceptsTheUnqualifiedName(t *testing.T) {
	idx := &mockIndexServicer{}
	store := newKVStoreAdapter(&mockKVServicer{}, idx)

	// Both the implicit form (IndexName empty) and an explicit one that
	// matches what the engine maintains.
	if err := store.RegisterEntityIndexes("member", memberIndexDefs()); err != nil {
		t.Fatalf("implicit name: %v", err)
	}
	if err := store.RegisterEntityIndexes("member", []kvstore.EntityIndex{{
		IndexName:     "member.Rank",
		FieldName:     "Rank",
		JSONFieldName: "rank",
		CaseSensitive: true,
	}}); err != nil {
		t.Fatalf("explicit unqualified name: %v", err)
	}
	if len(idx.created) != 2 {
		t.Errorf("created %d indexes; want 2", len(idx.created))
	}
}
