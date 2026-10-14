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

package dao

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// capabilityMember carries an indexed field, which is what makes the planner
// prefer an index lookup over a full scan.
type capabilityMember struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
	Team string `json:"team" index:"true"`
}

// EntityID implements Entity.
func (m *capabilityMember) EntityID() uint64 { return m.ID }

// SetEntityID implements Entity.
func (m *capabilityMember) SetEntityID(id uint64) { m.ID = id }

// newCapabilityMember is the entity factory.
func newCapabilityMember() *capabilityMember { return &capabilityMember{} }

// refusingIndexStore is a real in-memory store whose INDEX half refuses, the
// shape seal.tenantSharedKVStore has: data operations work, index operations
// return the typed error.
type refusingIndexStore struct {
	kvstore.KVStore
}

func (refusingIndexStore) RegisterEntityIndexes(string, []kvstore.EntityIndex) error {
	return kvstore.ErrIndexUnsupported
}

func (refusingIndexStore) QueryIndex(context.Context, string, []byte) ([][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

func (refusingIndexStore) ScanIndex(context.Context, string, []byte, []byte) (map[string][][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

func (refusingIndexStore) QueryCompoundIndex(context.Context, string, [][]byte) ([][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

func (refusingIndexStore) ScanCompoundIndex(context.Context, string, [][]byte, []byte, []byte) (map[string][][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

// failingRegistrationStore returns a NON-capability error from registration,
// standing in for a store whose index subsystem is present but broken.
type failingRegistrationStore struct {
	kvstore.KVStore
	err error
}

func (s failingRegistrationStore) RegisterEntityIndexes(string, []kvstore.EntityIndex) error {
	return s.err
}

// seedCapabilityMembers writes the fixture set and returns the alpha count.
func seedCapabilityMembers(ctx context.Context, t *testing.T, d GenericDAO[*capabilityMember]) int {
	t.Helper()
	for _, m := range []*capabilityMember{
		{Name: "ana", Team: "alpha"},
		{Name: "ben", Team: "alpha"},
		{Name: "cal", Team: "beta"},
	} {
		if err := d.Save(ctx, m); err != nil {
			t.Fatalf("Save(%s): %v", m.Name, err)
		}
	}
	return 2
}

// TestDAO_PlannedQueryFallsBackToFullScanOnARefusingStore is the fix for the
// inversion at the heart of #32: the query planner built its catalog from the
// entity's STRUCT TAGS, never asking the store what it could serve. A field
// with `index:"true"` therefore routed to QueryIndex on a store that
// maintains no indexes and produced zero rows with a nil error, while the
// same field WITHOUT the tag fell back to a full scan and was correct.
// Adding the index is what broke the query.
//
// With the capability probe the tagged field is answered by a full scan and
// the result is right.
func TestDAO_PlannedQueryFallsBackToFullScanOnARefusingStore(t *testing.T) {
	ctx := context.Background()
	store := refusingIndexStore{KVStore: kvstore.NewMemoryKVStore()}

	d, err := New(store, "member", newCapabilityMember)
	if err != nil {
		t.Fatalf("New over a refusing store must succeed: %v", err)
	}
	wantAlpha := seedCapabilityMembers(ctx, t, d)

	got, err := d.Find(ctx).Where("Team = ?", "alpha").Execute()
	if err != nil {
		t.Fatalf("planned query: %v", err)
	}
	if len(got) != wantAlpha {
		t.Fatalf("got %d entities; want %d — a tagged field on a store with no "+
			"index must be answered by a full scan, not by an index nobody maintains",
			len(got), wantAlpha)
	}
	for _, m := range got {
		if m.Team != "alpha" {
			t.Errorf("entity %q has team %q; the fallback must still filter", m.Name, m.Team)
		}
	}
}

// TestDAO_TaggedAndUntaggedQueriesAgreeOnARefusingStore pins the inversion
// directly: the two queries differ only in whether the field carries an index
// tag, so they must return the same rows. Before the fix the tagged one
// returned nothing.
func TestDAO_TaggedAndUntaggedQueriesAgreeOnARefusingStore(t *testing.T) {
	ctx := context.Background()
	store := refusingIndexStore{KVStore: kvstore.NewMemoryKVStore()}

	d, err := New(store, "member", newCapabilityMember)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	seedCapabilityMembers(ctx, t, d)

	tagged, err := d.Find(ctx).Where("Team = ?", "alpha").Execute() // Team is indexed
	if err != nil {
		t.Fatalf("tagged query: %v", err)
	}
	untagged, err := d.Find(ctx).Where("Name = ?", "ana").Execute() // Name is not
	if err != nil {
		t.Fatalf("untagged query: %v", err)
	}
	if len(tagged) == 0 && len(untagged) > 0 {
		t.Fatal("the indexed field returned nothing while the unindexed one worked: " +
			"adding an index must never be what breaks a query")
	}
}

// TestDAO_DirectIndexQueryFailsClosedOnARefusingStore covers the other entry
// point. QueryByIndex bypasses the planner, so it cannot fall back — it must
// surface the typed refusal rather than an empty slice, or a caller reading
// its own work queue concludes there is no work with no error anywhere.
func TestDAO_DirectIndexQueryFailsClosedOnARefusingStore(t *testing.T) {
	ctx := context.Background()
	store := refusingIndexStore{KVStore: kvstore.NewMemoryKVStore()}

	d, err := New(store, "member", newCapabilityMember)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	seedCapabilityMembers(ctx, t, d)

	got, err := d.QueryByIndex(ctx, "Team", "alpha")
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("QueryByIndex error = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entities alongside a refusal", len(got))
	}

	if _, err := d.ScanByIndex(ctx, "Team", "a", "z"); !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("ScanByIndex error = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if _, err := d.CountByIndex(ctx, "Team", "alpha"); !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("CountByIndex error = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if _, err := d.PageByIndex(ctx, "Team", "alpha", PageQuery{Page: 1, PageSize: 10}); !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("PageByIndex error = %v; want kvstore.ErrIndexUnsupported", err)
	}
}

// TestDAO_UsesTheIndexWhenTheStoreMaintainsOne is the positive control. Without
// it the fallback could be hiding a fix that simply disabled indexes
// everywhere, which would pass every other test in this file.
func TestDAO_UsesTheIndexWhenTheStoreMaintainsOne(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()

	d, err := New(store, "member", newCapabilityMember)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	wantAlpha := seedCapabilityMembers(ctx, t, d)

	got, err := d.QueryByIndex(ctx, "Team", "alpha")
	if err != nil {
		t.Fatalf("QueryByIndex on an index-capable store must succeed: %v", err)
	}
	if len(got) != wantAlpha {
		t.Fatalf("got %d entities; want %d from the maintained index", len(got), wantAlpha)
	}

	// The store really was asked: the index it registered exists under the
	// name pkg/dao resolves.
	keys, err := store.QueryIndex(ctx, "member.Team", []byte("alpha"))
	if err != nil {
		t.Fatalf("the DAO must have registered index %q: %v", "member.Team", err)
	}
	if len(keys) != wantAlpha {
		t.Errorf("index holds %d keys; want %d", len(keys), wantAlpha)
	}
}

// TestDAO_ConstructionFailsOnARealRegistrationError separates the two error
// classes. ErrIndexUnsupported is a capability statement and degrades to full
// scans; anything else is a failure, and continuing past it would leave the
// DAO believing it has indexes the store never created — the same defect one
// layer up, which is exactly what the discarded `_ =` used to allow.
func TestDAO_ConstructionFailsOnARealRegistrationError(t *testing.T) {
	boom := errors.New("index subsystem unavailable")
	store := failingRegistrationStore{KVStore: kvstore.NewMemoryKVStore(), err: boom}

	d, err := New(store, "member", newCapabilityMember)
	if !errors.Is(err, boom) {
		t.Fatalf("New error = %v; want the registration failure to fail construction", err)
	}
	if d != nil {
		t.Error("a failed construction must not return a DAO")
	}
}

// TestDAO_EntityWithoutIndexTagsIgnoresStoreCapability confirms the probe only
// runs where indexes are actually declared: an entity with no tags never
// registers anything, so a refusing store is irrelevant to it.
func TestDAO_EntityWithoutIndexTagsIgnoresStoreCapability(t *testing.T) {
	ctx := context.Background()
	store := failingRegistrationStore{
		KVStore: kvstore.NewMemoryKVStore(),
		err:     errors.New("must not be called"),
	}

	d, err := New[*plainEntity](store, "plain", func() *plainEntity { return &plainEntity{} })
	if err != nil {
		t.Fatalf("New on an unindexed entity must not consult the index subsystem: %v", err)
	}
	if err := d.Save(ctx, &plainEntity{Name: "ana"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := d.Find(ctx).Where("Name = ?", "ana").Execute()
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d entities; want 1 from the full scan", len(got))
	}
}

// plainEntity has no index tags at all.
type plainEntity struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
}

// EntityID implements Entity.
func (e *plainEntity) EntityID() uint64 { return e.ID }

// SetEntityID implements Entity.
func (e *plainEntity) SetEntityID(id uint64) { e.ID = id }

// compoundOnlyRefusingStore serves single-field indexes for real but refuses
// compound ones — the exact shape lifecycle.kvStoreAdapter has, since the
// engine maintains single-field indexes and service.IndexServicer carries no
// compound verb at all.
type compoundOnlyRefusingStore struct {
	*kvstore.MemoryKVStore
}

func (s compoundOnlyRefusingStore) RegisterEntityIndexes(entityType string, indexes []kvstore.EntityIndex) error {
	for _, ei := range indexes {
		if ei.IsCompound() {
			return kvstore.ErrIndexUnsupported
		}
	}
	return s.MemoryKVStore.RegisterEntityIndexes(entityType, indexes)
}

// compoundMember declares BOTH a single-field index and a compound one.
type compoundMember struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
	Team string `json:"team" index:"true"`
	Rank int    `json:"rank"`
}

// EntityID implements Entity.
func (m *compoundMember) EntityID() uint64 { return m.ID }

// SetEntityID implements Entity.
func (m *compoundMember) SetEntityID(id uint64) { m.ID = id }

// CompoundIndexes implements index.CompoundIndexRegisterer.
func (m *compoundMember) CompoundIndexes() []index.CompoundIndexDef {
	return []index.CompoundIndexDef{{Fields: []string{"Team", "Rank"}}}
}

// TestDAO_CompoundRefusalDoesNotDisableSingleFieldIndexes covers a defect the
// first cut of this change introduced and a review caught: the store was asked
// to register single-field and compound definitions in ONE call, so a store
// that skipped compound definitions and returned nil told the DAO its compound
// indexes existed. buildCatalog then advertised them, the planner routed a
// compound query to QueryCompoundIndex, and the query ERRORED where a full
// scan would have answered.
//
// The two kinds are now registered and tracked separately, so a compound
// refusal must not cost the working single-field index, and a compound query
// must still be answered.
func TestDAO_CompoundRefusalDoesNotDisableSingleFieldIndexes(t *testing.T) {
	ctx := context.Background()
	store := compoundOnlyRefusingStore{MemoryKVStore: kvstore.NewMemoryKVStore()}

	d, err := New(store, "member", func() *compoundMember { return &compoundMember{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, m := range []*compoundMember{
		{Name: "ana", Team: "alpha", Rank: 1},
		{Name: "ben", Team: "alpha", Rank: 2},
		{Name: "cal", Team: "beta", Rank: 1},
	} {
		if err := d.Save(ctx, m); err != nil {
			t.Fatalf("Save(%s): %v", m.Name, err)
		}
	}

	// The single-field index survived the compound refusal and answers.
	single, err := d.QueryByIndex(ctx, "Team", "alpha")
	if err != nil {
		t.Fatalf("QueryByIndex must still work: %v", err)
	}
	if len(single) != 2 {
		t.Errorf("got %d entities; want 2 from the maintained single-field index", len(single))
	}

	// A query the planner would want a COMPOUND index for must fall back to a
	// path that answers, never error and never come back empty.
	compound, err := d.Find(ctx).Where("Team = ?", "alpha").Where("Rank = ?", 2).Execute()
	if err != nil {
		t.Fatalf("a compound-shaped query over a store with no compound index "+
			"must fall back, not error: %v", err)
	}
	if len(compound) != 1 {
		t.Errorf("got %d entities; want 1 — the fallback must still answer correctly",
			len(compound))
	}
}

// TestDAO_WithDatabaseRegistersTheNameItQueries covers the second review
// finding. pkg/dao resolves a database-scoped index as
// "{database}.{entityType}.{field}", but every store composed the unqualified
// "{entityType}.{field}" itself. A DAO built with WithDatabase therefore
// advertised an index, planned a lookup, and queried a name nobody created.
//
// The DAO now supplies the resolved name on the definition, so registration
// and lookup agree.
func TestDAO_WithDatabaseRegistersTheNameItQueries(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()

	d, err := New(store, "member", newCapabilityMember, WithDatabase("tenantdb"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	wantAlpha := seedCapabilityMembers(ctx, t, d)

	got, err := d.QueryByIndex(ctx, "Team", "alpha")
	if err != nil {
		t.Fatalf("QueryByIndex on a database-scoped DAO: %v", err)
	}
	if len(got) != wantAlpha {
		t.Fatalf("got %d entities; want %d — the index was registered under a "+
			"different name than the query resolves", len(got), wantAlpha)
	}

	// And the index really carries the qualified name.
	if _, err := store.QueryIndex(ctx, "tenantdb.member.Team", []byte("alpha")); err != nil {
		t.Errorf("index %q must exist: %v", "tenantdb.member.Team", err)
	}

	planned, err := d.Find(ctx).Where("Team = ?", "alpha").Execute()
	if err != nil {
		t.Fatalf("planned query: %v", err)
	}
	if len(planned) != wantAlpha {
		t.Errorf("planned query got %d; want %d", len(planned), wantAlpha)
	}
}

// TestDAO_RepeatedConstructionOverOneStoreIsIdempotent guards the fix for the
// duplicate-registration refusal against a false positive. dao.New now calls
// RegisterEntityIndexes twice (single-field, then compound), and the same DAO
// is legitimately rebuilt over the same store — pkg/invite caches one store
// per tenant precisely because construction registers indexes. A conflict
// check that fired on a regenerated but identical definition would break
// every one of those call sites at startup.
func TestDAO_RepeatedConstructionOverOneStoreIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()

	first, err := New(store, "member", newCapabilityMember)
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	wantAlpha := seedCapabilityMembers(ctx, t, first)

	second, err := New(store, "member", newCapabilityMember)
	if err != nil {
		t.Fatalf("second New over the same store must succeed: %v", err)
	}

	// The rebuilt DAO sees the index the first one registered.
	got, err := second.QueryByIndex(ctx, "Team", "alpha")
	if err != nil {
		t.Fatalf("QueryByIndex on the rebuilt DAO: %v", err)
	}
	if len(got) != wantAlpha {
		t.Errorf("got %d entities; want %d", len(got), wantAlpha)
	}
}

// TestDAO_CompoundEntityConstructionIsIdempotent covers the same property for
// an entity declaring BOTH kinds, which is the case that exercises the
// two-call registration path twice.
func TestDAO_CompoundEntityConstructionIsIdempotent(t *testing.T) {
	store := compoundOnlyRefusingStore{MemoryKVStore: kvstore.NewMemoryKVStore()}

	for i := 0; i < 3; i++ {
		if _, err := New(store, "member", func() *compoundMember { return &compoundMember{} }); err != nil {
			t.Fatalf("New attempt %d: %v", i+1, err)
		}
	}
}

// TestDAO_OpenEndedIndexScanReturnsMatches covers a review finding, and it is
// the same defect class as the rest of this change reached through a
// different door.
//
// The planner emits only ONE bound for a one-sided range: `Team > "alpha"`
// sets RangeStart and leaves RangeEnd nil. ScanByIndex converted both bounds
// with fmt.Sprint, so the nil open bound became the literal string "<nil>" —
// which sorts below every lowercase term, making ["alpha", "<nil>") an empty
// range. The query returned NOTHING, with a nil error, over an index that
// held matches.
//
// A nil bound means open and must stay nil. ScanByCompoundIndex already got
// this right, which is what makes it an oversight rather than a design.
func TestDAO_OpenEndedIndexScanReturnsMatches(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()

	d, err := New(store, "member", newCapabilityMember)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	seedCapabilityMembers(ctx, t, d) // teams: alpha, alpha, beta

	// Open upper bound: everything from "beta" onward.
	fromBeta, err := d.ScanByIndex(ctx, "Team", "beta", nil)
	if err != nil {
		t.Fatalf("ScanByIndex with an open upper bound: %v", err)
	}
	if len(fromBeta) != 1 {
		t.Errorf("got %d entities; want 1 — a nil upper bound means open, not "+
			"the string %q", len(fromBeta), "<nil>")
	}

	// Open lower bound: everything up to "beta".
	untilBeta, err := d.ScanByIndex(ctx, "Team", nil, "beta")
	if err != nil {
		t.Fatalf("ScanByIndex with an open lower bound: %v", err)
	}
	if len(untilBeta) != 2 {
		t.Errorf("got %d entities; want 2 — a nil lower bound means open", len(untilBeta))
	}
}

// TestDAO_PlannedOneSidedRangeQueryReturnsMatches drives the same bug through
// the planner, which is how a consumer actually hits it: a `>` predicate
// produces exactly one bound and leaves the other nil.
func TestDAO_PlannedOneSidedRangeQueryReturnsMatches(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()

	d, err := New(store, "member", newCapabilityMember)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	seedCapabilityMembers(ctx, t, d)

	got, err := d.Find(ctx).Where("Team >= ?", "beta").Execute()
	if err != nil {
		t.Fatalf("planned one-sided range query: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d entities; want 1 — an indexed one-sided range must "+
			"return its matches, not an empty set", len(got))
	}
}

// nilStringer is a nilable named type used to build a TYPED nil bound.
type nilStringer *string

// TestDAO_TypedNilScanBoundIsOpen covers the second half of the open-bound
// finding. A typed nil carried in an interface is NOT == nil, so the untyped
// check alone left the bug reachable: fmt.Sprint renders (*string)(nil) as
// "<nil>" just the same, producing a bound that sorts below every term
// instead of an open one.
func TestDAO_TypedNilScanBoundIsOpen(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()

	d, err := New(store, "member", newCapabilityMember)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	seedCapabilityMembers(ctx, t, d) // teams: alpha, alpha, beta

	var typedNil nilStringer
	got, err := d.ScanByIndex(ctx, "Team", "beta", typedNil)
	if err != nil {
		t.Fatalf("ScanByIndex with a typed-nil upper bound: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d entities; want 1 — a typed nil is still an open bound, "+
			"not the string %q", len(got), "<nil>")
	}
}

// TestDAO_TypedNilCompoundScanBoundIsOpen pins the same property on the
// compound path, which guarded its bounds with a bare `!= nil` check that a
// typed nil passes straight through.
func TestDAO_TypedNilCompoundScanBoundIsOpen(t *testing.T) {
	ctx := context.Background()
	store := compoundOnlyRefusingStore{MemoryKVStore: kvstore.NewMemoryKVStore()}

	d, err := New(store, "member", func() *compoundMember { return &compoundMember{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The store refuses compound indexes, so this surfaces the typed refusal
	// rather than a wrong answer — the point is that the bound conversion
	// does not panic or fabricate a "<nil>" bound on the way there.
	var typedNil nilStringer
	_, err = d.ScanByCompoundIndex(ctx, "member.Team_Rank",
		[]interface{}{"alpha"}, nil, typedNil)
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("ScanByCompoundIndex = %v; want kvstore.ErrIndexUnsupported", err)
	}
}

// queryTimeRefusingStore accepts index registration but refuses at QUERY
// time — the shape the SDK transport adapter has, which registers indexes
// happily and then cannot express an open range bound over a string-typed
// wire call. A capability probe at construction cannot predict that.
type queryTimeRefusingStore struct {
	*kvstore.MemoryKVStore
}

func (queryTimeRefusingStore) QueryIndex(context.Context, string, []byte) ([][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

func (queryTimeRefusingStore) ScanIndex(context.Context, string, []byte, []byte) (map[string][][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

// TestDAO_PlannedQueryFallsBackWhenTheStoreRefusesAtQueryTime covers a review
// finding on the first cut of this change.
//
// Registration succeeded, so the DAO advertised the index and planned an
// index access path; the store then refused when the query arrived, and the
// error propagated. That failed ORDINARY VALID QUERIES — the opposite of the
// contract, which promises a slower correct answer whenever one is available.
//
// A planned query names no access path in its signature, so it is free to
// choose another, and a full scan always exists. It now falls back.
func TestDAO_PlannedQueryFallsBackWhenTheStoreRefusesAtQueryTime(t *testing.T) {
	ctx := context.Background()
	store := queryTimeRefusingStore{MemoryKVStore: kvstore.NewMemoryKVStore()}

	d, err := New(store, "member", newCapabilityMember)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	wantAlpha := seedCapabilityMembers(ctx, t, d)

	got, err := d.Find(ctx).Where("Team = ?", "alpha").Execute()
	if err != nil {
		t.Fatalf("a planned query must fall back, not fail: %v", err)
	}
	if len(got) != wantAlpha {
		t.Fatalf("got %d entities; want %d", len(got), wantAlpha)
	}
	for _, m := range got {
		if m.Team != "alpha" {
			t.Errorf("entity %q has team %q; the fallback must still filter", m.Name, m.Team)
		}
	}

	// A one-sided range, the case the SDK adapter actually refuses.
	ranged, err := d.Find(ctx).Where("Team >= ?", "beta").Execute()
	if err != nil {
		t.Fatalf("a planned range query must fall back, not fail: %v", err)
	}
	if len(ranged) != 1 {
		t.Errorf("got %d entities; want 1", len(ranged))
	}
}

// TestDAO_DirectIndexCallStillFailsClosedOnAQueryTimeRefusal is the boundary
// on the fallback above. QueryByIndex names its access path in the signature,
// so it must NOT silently substitute another — the caller asked for the index
// specifically and has to learn it was not used.
func TestDAO_DirectIndexCallStillFailsClosedOnAQueryTimeRefusal(t *testing.T) {
	ctx := context.Background()
	store := queryTimeRefusingStore{MemoryKVStore: kvstore.NewMemoryKVStore()}

	d, err := New(store, "member", newCapabilityMember)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	seedCapabilityMembers(ctx, t, d)

	if _, err := d.QueryByIndex(ctx, "Team", "alpha"); !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("QueryByIndex = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if _, err := d.ScanByIndex(ctx, "Team", "a", "z"); !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("ScanByIndex = %v; want kvstore.ErrIndexUnsupported", err)
	}
}
