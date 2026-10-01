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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// ---------------------------------------------------------------------------
// Doubles
// ---------------------------------------------------------------------------

// hookKVSvc is the faithful in-memory KV with hooks at the two moments the
// lifecycle races turn on: just after a document write commits, and just
// before a guarded metadata swap is attempted. A hook runs with no lock held,
// exactly as a concurrent caller's operation would interleave.
type hookKVSvc struct {
	*memoryKVSvc

	mu            sync.Mutex
	afterDocWrite func(key string)
	beforeSwap    func(key string)
}

var _ kvService = (*hookKVSvc)(nil)

func newHookKVSvc() *hookKVSvc {
	return &hookKVSvc{memoryKVSvc: newMemoryKVSvc()}
}

func (h *hookKVSvc) fireAfterDocWrite(key string) {
	h.mu.Lock()
	hook := h.afterDocWrite
	h.afterDocWrite = nil
	h.mu.Unlock()
	if hook != nil && strings.HasPrefix(key, collectionDataPrefix) {
		hook(key)
	}
}

func (h *hookKVSvc) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	stored, err := h.memoryKVSvc.PutIfAbsent(ctx, key, value)
	if stored && strings.HasPrefix(key, collectionDataPrefix) {
		h.fireAfterDocWrite(key)
	}
	return stored, err
}

func (h *hookKVSvc) Put(ctx context.Context, key string, value []byte) error {
	err := h.memoryKVSvc.Put(ctx, key, value)
	if err == nil && strings.HasPrefix(key, collectionDataPrefix) {
		h.fireAfterDocWrite(key)
	}
	return err
}

func (h *hookKVSvc) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	h.mu.Lock()
	hook := h.beforeSwap
	if strings.HasPrefix(key, collectionMetadataPrefix) {
		h.beforeSwap = nil
	} else {
		hook = nil
	}
	h.mu.Unlock()
	if hook != nil {
		hook(key)
	}
	return h.memoryKVSvc.CompareAndSwap(ctx, key, expected, next)
}

// nthCreateFailIndexSvc fails the Nth CreateIndex call and delegates the rest
// to the faithful in-memory index service.
type nthCreateFailIndexSvc struct {
	*memoryIndexSvc
	failOn int
	n      int
	err    error
	// beforeCreate, when set, runs before each create is applied.
	beforeCreate func()
}

func (f *nthCreateFailIndexSvc) CreateIndex(ctx context.Context, name, field string, cfg index.IndexConfig) error {
	f.n++
	if f.beforeCreate != nil {
		hook := f.beforeCreate
		f.beforeCreate = nil
		hook()
	}
	if f.n == f.failOn {
		return f.err
	}
	return f.memoryIndexSvc.CreateIndex(ctx, name, field, cfg)
}

// ownerOf returns the owner recorded on name, and whether it exists.
func (m *memoryIndexSvc) ownerOf(name string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, ok := m.indexes[name]
	return cfg.Owner, ok
}

func putMetadata(t *testing.T, kv kvService, info *CollectionInfo) {
	t.Helper()
	data, err := json.Marshal(info)
	require.NoError(t, err)
	require.NoError(t, kv.Put(context.Background(), collectionMetadataKey(info.Database, info.Name), data))
}

// ---------------------------------------------------------------------------
// go-qrdb#415: a create never exposes a collection whose indexes are not built
// ---------------------------------------------------------------------------

// TestCollectionService_CreateCollection_FailedIndexUnwinds is the #415
// measurement turned into a test. With three index definitions and the second
// failing, the create used to leave the collection resolvable, claiming three
// indexes with one built, and a retry failed with AlreadyExists. Now the
// failed create has created nothing and the retry succeeds.
func TestCollectionService_CreateCollection_FailedIndexUnwinds(t *testing.T) {
	ctx := context.Background()
	idxSvc := &nthCreateFailIndexSvc{memoryIndexSvc: newMemoryIndexSvc(), failOn: 2, err: errors.New("boom")}
	svc := NewCollectionServiceWithIndex(newMemoryKVSvc(), idxSvc)
	req := &CreateCollectionRequest{
		Name: "users", Database: "mydb",
		Indexes: []IndexDefinition{{FieldName: "a"}, {FieldName: "b"}, {FieldName: "c"}},
	}

	_, err := svc.CreateCollection(ctx, req)
	var meta *CollectionMetadataError
	require.ErrorAs(t, err, &meta)
	assert.Equal(t, "create-index", meta.Op)

	assert.Empty(t, idxSvc.names(), "the index built before the failure must be unwound")
	_, err = svc.GetCollectionInfo(ctx, "mydb", "users")
	var notFound *CollectionNotFoundError
	require.ErrorAs(t, err, &notFound, "a failed create must not leave a collection behind")

	idxSvc.failOn = 0
	info, err := svc.CreateCollection(ctx, req)
	require.NoError(t, err, "the retry must converge")
	assert.Equal(t, CollectionStateActive, info.State)
	assert.Equal(t, []string{"mydb.users.a", "mydb.users.b", "mydb.users.c"}, idxSvc.names())
}

// TestCollectionService_CreatingCollectionIsVisibleButServesNoDocuments pins
// what a collection in the CREATING state is: reported, with its state, by
// both metadata reads, and refused by every document verb that reads or
// stores documents.
func TestCollectionService_CreatingCollectionIsVisibleButServesNoDocuments(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	svc := NewCollectionService(kv)
	now := time.Now()
	putMetadata(t, kv, &CollectionInfo{
		Name: "users", Database: "mydb", IDStrategy: IDStrategyIncrement,
		CreatedAt: now, State: CollectionStateCreating, StateSince: now,
	})

	info, err := svc.GetCollectionInfo(ctx, "mydb", "users")
	require.NoError(t, err)
	assert.Equal(t, CollectionStateCreating, info.State)

	list, err := svc.ListCollections(ctx, &ListCollectionsRequest{Database: "mydb"})
	require.NoError(t, err)
	require.Len(t, list.Collections, 1)
	assert.Equal(t, CollectionStateCreating, list.Collections[0].State)

	doc := map[string]interface{}{"a": "b"}
	_, insErr := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "mydb", Collection: "users", Document: doc})
	_, getErr := svc.GetDocument(ctx, &GetDocumentRequest{Database: "mydb", Collection: "users", ID: 1})
	_, updErr := svc.UpdateDocument(ctx, &UpdateDocumentRequest{Database: "mydb", Collection: "users", ID: 1, Document: doc})
	_, upsErr := svc.UpsertDocument(ctx, &UpsertDocumentRequest{Database: "mydb", Collection: "users", ID: 1, Document: doc})
	_, lstErr := svc.ListDocuments(ctx, &ListDocumentsRequest{Database: "mydb", Collection: "users"})
	for name, err := range map[string]error{"insert": insErr, "get": getErr, "update": updErr, "upsert": upsErr, "list": lstErr} {
		var notActive *CollectionNotActiveError
		require.ErrorAs(t, err, &notActive, name)
		assert.Equal(t, CollectionStateCreating, notActive.State, name)
	}
	assert.Empty(t, kv.listAll(collectionDataPrefix), "nothing may be written into a creating collection")
}

// TestCollectionService_LegacyMetadataReadsActive pins the reading of a
// record written before the lifecycle state existed.
func TestCollectionService_LegacyMetadataReadsActive(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	svc := NewCollectionService(kv)
	require.NoError(t, kv.Put(ctx, collectionMetadataKey("mydb", "old"),
		[]byte(`{"name":"old","database":"mydb","id_strategy":"unix_nano","created_at":"2026-01-01T00:00:00Z"}`)))

	info, err := svc.GetCollectionInfo(ctx, "mydb", "old")
	require.NoError(t, err)
	assert.Equal(t, CollectionStateActive, info.State)
	_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "mydb", Collection: "old", Document: map[string]interface{}{"k": "v"}})
	require.NoError(t, err)
}

// TestCollectionService_CreateCollection_ActivationLosesToConcurrentDrop
// drops the collection while its index is being built. The drop's teardown
// runs before the index exists, so the creator -- whose activation then finds
// the record gone -- must remove the index itself, or it is orphaned.
func TestCollectionService_CreateCollection_ActivationLosesToConcurrentDrop(t *testing.T) {
	ctx := context.Background()
	idxSvc := &nthCreateFailIndexSvc{memoryIndexSvc: newMemoryIndexSvc()}
	svc := NewCollectionServiceWithIndex(newMemoryKVSvc(), idxSvc)
	var dropErr error
	idxSvc.beforeCreate = func() {
		dropErr = svc.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "users"})
	}

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb", Indexes: []IndexDefinition{{FieldName: "email", Unique: true}},
	})
	require.NoError(t, dropErr, "a CREATING collection can be dropped")
	var conflict *CollectionStateConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, "activate", conflict.Op)
	assert.Empty(t, idxSvc.names(), "the creator must remove the index the drop could not see")
	_, err = svc.GetCollectionInfo(ctx, "mydb", "users")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// go-qrdb#426: persisted index ownership
// ---------------------------------------------------------------------------

// TestCollectionService_CreateCollection_RefusesAnIndexItDoesNotOwn is the
// adoption half of #426: an index the explicit API created under the name a
// collection derives used to be silently adopted, with its own definition.
func TestCollectionService_CreateCollection_RefusesAnIndexItDoesNotOwn(t *testing.T) {
	ctx := context.Background()
	svc, idxSvc := newTestCollectionServiceWithIndex()
	require.NoError(t, idxSvc.CreateIndex(ctx, "mydb.users.email", "email", index.IndexConfig{}))

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb", Indexes: []IndexDefinition{{FieldName: "email", Unique: true}},
	})
	var own *index.OwnershipError
	require.ErrorAs(t, err, &own)
	assert.Equal(t, "mydb.users.email", own.Name)
	assert.Empty(t, own.Owner, "the recorded owner is the explicit API")

	owner, ok := idxSvc.ownerOf("mydb.users.email")
	require.True(t, ok, "the unwind must not remove the index the refusal was about")
	assert.Empty(t, owner)
	_, err = svc.GetCollectionInfo(ctx, "mydb", "users")
	require.Error(t, err)
}

// TestCollectionService_CreateCollection_DottedNameCollisionRefused is the
// rider on #426: "users"+"v2.email" and "users.v2"+"email" derive the same
// index name. The second collection is refused instead of sharing the first's
// index.
func TestCollectionService_CreateCollection_DottedNameCollisionRefused(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestCollectionServiceWithIndex()
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb", Indexes: []IndexDefinition{{FieldName: "v2.email"}},
	})
	require.NoError(t, err)

	_, err = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users.v2", Database: "mydb", Indexes: []IndexDefinition{{FieldName: "email"}},
	})
	var own *index.OwnershipError
	require.ErrorAs(t, err, &own)
}

// TestCollectionService_DropCollection_LeavesAnIndexItDoesNotOwn is the
// destruction half of #426: an index under the collection's derived name that
// somebody else now holds survives the collection's drop.
func TestCollectionService_DropCollection_LeavesAnIndexItDoesNotOwn(t *testing.T) {
	ctx := context.Background()
	svc, idxSvc := newTestCollectionServiceWithIndex()
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb", Indexes: []IndexDefinition{{FieldName: "email"}},
	})
	require.NoError(t, err)

	// The explicit API removes the collection's index and creates its own
	// under the same name.
	require.NoError(t, idxSvc.DropIndex(ctx, "mydb.users.email"))
	require.NoError(t, idxSvc.CreateIndex(ctx, "mydb.users.email", "email", index.IndexConfig{}))

	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "users"}))
	owner, ok := idxSvc.ownerOf("mydb.users.email")
	require.True(t, ok, "the drop must leave an index it does not own")
	assert.Empty(t, owner)
}

// TestCollectionService_RecreatedCollectionDoesNotShareAnIncarnation pins
// the incarnation in the owner: a collection dropped and created again under
// the same name records a different owner.
func TestCollectionService_RecreatedCollectionDoesNotShareAnIncarnation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestCollectionServiceWithIndex()
	req := &CreateCollectionRequest{Name: "users", Database: "mydb", Indexes: []IndexDefinition{{FieldName: "email"}}}
	first, err := svc.CreateCollection(ctx, req)
	require.NoError(t, err)
	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "users"}))
	second, err := svc.CreateCollection(ctx, req)
	require.NoError(t, err)

	assert.NotEmpty(t, first.IndexOwner)
	assert.NotEqual(t, first.IndexOwner, second.IndexOwner)
	assert.True(t, strings.HasPrefix(second.IndexOwner, "collection:mydb/users#"))
}

// TestCollectionService_DropCollection_LegacyRecordDropsDeclaredIndexes
// pins the reading of metadata written before ownership existed: its indexes
// were created ownerless, so its drop removes the names it declares.
func TestCollectionService_DropCollection_LegacyRecordDropsDeclaredIndexes(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	idxSvc := newMemoryIndexSvc()
	svc := NewCollectionServiceWithIndex(kv, idxSvc)
	require.NoError(t, idxSvc.CreateIndex(ctx, "mydb.old.email", "email", index.IndexConfig{}))
	putMetadata(t, kv, &CollectionInfo{
		Name: "old", Database: "mydb", IDStrategy: IDStrategyUnixNano,
		Indexes: []IndexDefinition{{FieldName: "email"}},
	})

	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "old"}))
	assert.Empty(t, idxSvc.names())
}

// ---------------------------------------------------------------------------
// go-qrdb#425: the dropping state and the write fence
// ---------------------------------------------------------------------------

// markStored rewrites a collection's stored metadata into state, the way a
// concurrent drop's first write does.
func markStored(t *testing.T, kv kvService, database, name, state string) {
	t.Helper()
	ctx := context.Background()
	raw, err := kv.Get(ctx, collectionMetadataKey(database, name))
	require.NoError(t, err)
	var info CollectionInfo
	require.NoError(t, json.Unmarshal(raw, &info))
	info.State = state
	info.StateSince = time.Now()
	putMetadata(t, kv, &info)
}

// TestCollectionService_InsertFencedByConcurrentDropMark lands a drop's
// DROPPING mark between an insert's write and the insert's return. The write
// committed after the drop could have listed it, so the insert must retract
// it and refuse -- otherwise it outlives the drop.
func TestCollectionService_InsertFencedByConcurrentDropMark(t *testing.T) {
	ctx := context.Background()
	kv := newHookKVSvc()
	svc := NewCollectionService(kv)
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Name: "users", Database: "mydb", IDStrategy: IDStrategyIncrement})
	require.NoError(t, err)

	kv.afterDocWrite = func(string) { markStored(t, kv, "mydb", "users", CollectionStateDropping) }
	_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{
		Database: "mydb", Collection: "users", Document: map[string]interface{}{"_id": float64(7), "k": "v"},
	})
	var notActive *CollectionNotActiveError
	require.ErrorAs(t, err, &notActive)
	assert.Equal(t, CollectionStateDropping, notActive.State)
	assert.Empty(t, kv.listAll(collectionDataPrefix), "the fenced-out write must be retracted")
}

// TestCollectionService_UpdateFencedByCompletedDrop lets a whole drop finish
// between an update's write and its return: the metadata is gone, so the
// update's document would be an orphan no name reaches.
func TestCollectionService_UpdateFencedByCompletedDrop(t *testing.T) {
	ctx := context.Background()
	kv := newHookKVSvc()
	svc := NewCollectionService(kv)
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Name: "users", Database: "mydb"})
	require.NoError(t, err)
	resp, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "mydb", Collection: "users", Document: map[string]interface{}{"k": "v"}})
	require.NoError(t, err)

	kv.afterDocWrite = func(string) {
		require.NoError(t, kv.memoryKVSvc.Delete(ctx, collectionMetadataKey("mydb", "users")))
	}
	_, err = svc.UpdateDocument(ctx, &UpdateDocumentRequest{
		Database: "mydb", Collection: "users", ID: resp.ID, Document: map[string]interface{}{"k": "v2"},
	})
	var notFound *CollectionNotFoundError
	require.ErrorAs(t, err, &notFound)
	assert.Empty(t, kv.listAll(collectionDataPrefix), "the orphaned update must be retracted")
}

// TestCollectionService_SchemaMergeDoesNotRevertDropping lands a drop's mark
// between an insert's fence and its schema merge. The merge used to be a
// plain Put of the copy it read first, which wrote the collection back to
// ACTIVE.
func TestCollectionService_SchemaMergeDoesNotRevertDropping(t *testing.T) {
	ctx := context.Background()
	kv := newHookKVSvc()
	svc := NewCollectionService(kv)
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Name: "users", Database: "mydb"})
	require.NoError(t, err)

	kv.beforeSwap = func(string) { markStored(t, kv, "mydb", "users", CollectionStateDropping) }
	_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "mydb", Collection: "users", Document: map[string]interface{}{"new_field": "v"}})
	require.NoError(t, err, "the write itself committed before the mark")

	info, err := svc.GetCollectionInfo(ctx, "mydb", "users")
	require.NoError(t, err)
	assert.Equal(t, CollectionStateDropping, info.State, "the schema merge must not revert the drop's mark")
}

// TestCollectionService_DropOfDroppingCollectionResumes pins that re-issuing
// a drop on a DROPPING collection finishes it rather than conflicting.
func TestCollectionService_DropOfDroppingCollectionResumes(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	svc := NewCollectionService(kv)
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Name: "users", Database: "mydb"})
	require.NoError(t, err)
	markStored(t, kv, "mydb", "users", CollectionStateDropping)

	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "users"}))
	_, err = svc.GetCollectionInfo(ctx, "mydb", "users")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// The sweeper
// ---------------------------------------------------------------------------

// TestCollectionService_SweepStuckCollections finishes a stale DROPPING
// collection and unwinds a stale CREATING one, and leaves alone both a fresh
// transitional collection -- whose operation may still be running -- and an
// active one.
func TestCollectionService_SweepStuckCollections(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	idxSvc := newMemoryIndexSvc()
	svc := NewCollectionServiceWithIndex(kv, idxSvc)
	base := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return base }

	for _, name := range []string{"dropping", "creating", "fresh", "live"} {
		_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
			Name: name, Database: "mydb", Indexes: []IndexDefinition{{FieldName: "email"}},
		})
		require.NoError(t, err)
		_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "mydb", Collection: name, Document: map[string]interface{}{"email": name}})
		require.NoError(t, err)
	}
	setState := func(name, state string, since time.Time) {
		raw, err := kv.Get(ctx, collectionMetadataKey("mydb", name))
		require.NoError(t, err)
		var info CollectionInfo
		require.NoError(t, json.Unmarshal(raw, &info))
		info.State, info.StateSince = state, since
		putMetadata(t, kv, &info)
	}
	setState("dropping", CollectionStateDropping, base.Add(-time.Hour))
	setState("creating", CollectionStateCreating, base.Add(-time.Hour))
	setState("fresh", CollectionStateCreating, base.Add(-time.Second))
	prefixes := map[string]string{}
	for _, gone := range []string{"dropping", "creating"} {
		prefixes[gone] = docPrefixForTest(t, svc, "mydb", gone)
	}

	res, err := svc.SweepStuckCollections(ctx, "mydb", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Swept)
	assert.Equal(t, DocumentBodyBytes(map[string]interface{}{"email": "dropping"})+DocumentBodyBytes(map[string]interface{}{"email": "creating"}), res.FreedBytes,
		"the sweep reports the body bytes of exactly the documents it removed")

	list, err := svc.ListCollections(ctx, &ListCollectionsRequest{Database: "mydb"})
	require.NoError(t, err)
	states := map[string]string{}
	for _, c := range list.Collections {
		states[c.Name] = c.State
	}
	assert.Equal(t, map[string]string{"fresh": CollectionStateCreating, "live": CollectionStateActive}, states)
	assert.Equal(t, []string{"mydb.fresh.email", "mydb.live.email"}, idxSvc.names())
	for _, gone := range []string{"dropping", "creating"} {
		assert.Empty(t, kv.listAll(prefixes[gone]), gone)
	}
}

// TestCollectionService_SweepStuckCollections_Errors covers the refusals and
// the per-collection failure that does not stop the pass.
func TestCollectionService_SweepStuckCollections_Errors(t *testing.T) {
	ctx := context.Background()
	svc := newTestCollectionService()
	_, err := svc.SweepStuckCollections(ctx, "", time.Minute)
	var empty *CollectionEmptyDatabaseError
	require.ErrorAs(t, err, &empty)

	kv := &failingKVSvc{memoryKVSvc: newMemoryKVSvc()}
	svc = NewCollectionService(kv)
	putMetadata(t, kv, &CollectionInfo{Name: "a", Database: "mydb", State: CollectionStateDropping})
	putMetadata(t, kv, &CollectionInfo{Name: "b", Database: "mydb", State: CollectionStateDropping})
	kv.listErr = errors.New("list failed")
	res, err := svc.SweepStuckCollections(ctx, "mydb", 0)
	assert.Equal(t, 0, res.Swept)
	var meta *CollectionMetadataError
	require.ErrorAs(t, err, &meta)
	assert.Equal(t, "list-documents", meta.Op)
}

// ---------------------------------------------------------------------------
// go-qrdb#497: the guarded upsert
// ---------------------------------------------------------------------------

func TestCollectionService_UpsertDocument_InsertThenReplace(t *testing.T) {
	ctx := context.Background()
	svc := newTestCollectionService()
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Name: "users", Database: "mydb", IDStrategy: IDStrategyIncrement})
	require.NoError(t, err)

	first, err := svc.UpsertDocument(ctx, &UpsertDocumentRequest{Database: "mydb", Collection: "users", ID: 10, Document: map[string]interface{}{"v": "one"}})
	require.NoError(t, err)
	assert.False(t, first.Replaced)
	assert.Nil(t, first.Previous)

	second, err := svc.UpsertDocument(ctx, &UpsertDocumentRequest{Database: "mydb", Collection: "users", ID: 10, Document: map[string]interface{}{"v": "two"}})
	require.NoError(t, err)
	assert.True(t, second.Replaced)
	assert.Equal(t, "one", second.Previous["v"])

	got, err := svc.GetDocument(ctx, &GetDocumentRequest{Database: "mydb", Collection: "users", ID: 10})
	require.NoError(t, err)
	assert.Equal(t, "two", got.Document["v"])
	assert.Equal(t, second.Revision, got.Revision)

	// The explicit ID reserved the increment counter.
	ins, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "mydb", Collection: "users", Document: map[string]interface{}{"v": "gen"}})
	require.NoError(t, err)
	assert.Greater(t, ins.ID, uint64(10))
}

func TestCollectionService_UpsertDocument_IfRevision(t *testing.T) {
	ctx := context.Background()
	svc := newTestCollectionService()
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Name: "users", Database: "mydb"})
	require.NoError(t, err)

	req := func(rev string, v string) *UpsertDocumentRequest {
		return &UpsertDocumentRequest{Database: "mydb", Collection: "users", ID: 5, Document: map[string]interface{}{"v": v}, IfRevision: rev}
	}
	var conflict *CollectionDocumentConflictError
	_, err = svc.UpsertDocument(ctx, req("anything", "x"))
	require.ErrorAs(t, err, &conflict, "a guarded replace of an absent document is refused")

	_, err = svc.UpsertDocument(ctx, req("", "base"))
	require.NoError(t, err)
	read, err := svc.GetDocument(ctx, &GetDocumentRequest{Database: "mydb", Collection: "users", ID: 5})
	require.NoError(t, err)

	_, err = svc.UpsertDocument(ctx, req("stale", "x"))
	require.ErrorAs(t, err, &conflict)

	out, err := svc.UpsertDocument(ctx, req(read.Revision, "next"))
	require.NoError(t, err)
	assert.True(t, out.Replaced)

	_, err = svc.UpsertDocument(ctx, req(read.Revision, "lost"))
	require.ErrorAs(t, err, &conflict, "the revision read before the replace is stale now")
}

func TestCollectionService_UpsertDocument_Validation(t *testing.T) {
	ctx := context.Background()
	svc := newTestCollectionService()
	_, err := svc.UpsertDocument(ctx, &UpsertDocumentRequest{Database: "mydb", Collection: "users", Document: map[string]interface{}{"v": 1}})
	var invalidID *CollectionInvalidDocumentIDError
	require.ErrorAs(t, err, &invalidID)
	_, err = svc.UpsertDocument(ctx, &UpsertDocumentRequest{Database: "mydb", Collection: "users", ID: 1})
	var emptyDoc *CollectionEmptyDocumentError
	require.ErrorAs(t, err, &emptyDoc)
	_, err = svc.UpsertDocument(ctx, &UpsertDocumentRequest{Database: "mydb", Collection: "users", ID: 1, Document: map[string]interface{}{"v": 1}})
	var notFound *CollectionNotFoundError
	require.ErrorAs(t, err, &notFound)
	_, err = svc.UpsertDocument(ctx, &UpsertDocumentRequest{Collection: "users", ID: 1, Document: map[string]interface{}{"v": 1}})
	require.Error(t, err)
}

// TestCollectionService_UpsertDocument_ConcurrentWritersLoseNothingSilently
// is the #497 property at the service seam. N writers upsert one ID at once.
// Every call succeeds; exactly one reports an insert; and every value written
// is accounted for exactly once -- either displaced by a replace that REPORTED
// displacing it, or the survivor. A write lost with nobody told would appear
// in neither place.
//
// Mutation-checked: with guardedUpsert's CompareAndSwap replaced by an
// unguarded Put, two writers read the same prior value and both report
// displacing it, so a value goes unaccounted for and this test fails.
func TestCollectionService_UpsertDocument_ConcurrentWritersLoseNothingSilently(t *testing.T) {
	ctx := context.Background()
	svc := newTestCollectionService()
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Name: "users", Database: "mydb"})
	require.NoError(t, err)

	const writers = 32
	results := make([]*UpsertDocumentResponse, writers)
	errs := make([]error, writers)
	var start, done sync.WaitGroup
	start.Add(1)
	for i := 0; i < writers; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			results[i], errs[i] = svc.UpsertDocument(ctx, &UpsertDocumentRequest{
				Database: "mydb", Collection: "users", ID: 1,
				Document: map[string]interface{}{"writer": float64(i)},
			})
		}(i)
	}
	start.Done()
	done.Wait()

	inserts := 0
	displaced := map[float64]int{}
	for i := 0; i < writers; i++ {
		var conflict *CollectionDocumentConflictError
		if errors.As(errs[i], &conflict) {
			// Sustained contention may exhaust the bounded retry; a writer
			// told so has not lost a value silently. It is excluded from the
			// accounting because it wrote nothing.
			continue
		}
		require.NoError(t, errs[i])
		if !results[i].Replaced {
			inserts++
			continue
		}
		displaced[results[i].Previous["writer"].(float64)]++
	}
	assert.Equal(t, 1, inserts, "exactly one writer found the ID free")

	final, err := svc.GetDocument(ctx, &GetDocumentRequest{Database: "mydb", Collection: "users", ID: 1})
	require.NoError(t, err)
	survivor := final.Document["writer"].(float64)
	for i := 0; i < writers; i++ {
		if errs[i] != nil {
			continue
		}
		v := float64(i)
		if v == survivor {
			assert.Zero(t, displaced[v], "the survivor cannot also have been displaced")
			continue
		}
		assert.Equal(t, 1, displaced[v], "writer %d's value must be displaced exactly once, by a replace that reported it", i)
	}
}

// ---------------------------------------------------------------------------
// Guarded delete
// ---------------------------------------------------------------------------

func TestCollectionService_DeleteDocument_IfRevision(t *testing.T) {
	ctx := context.Background()
	svc := newTestCollectionService()
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Name: "users", Database: "mydb"})
	require.NoError(t, err)
	ins, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "mydb", Collection: "users", Document: map[string]interface{}{"v": "a"}})
	require.NoError(t, err)

	var conflict *CollectionDocumentConflictError
	_, err = svc.DeleteDocument(ctx, &DeleteDocumentRequest{Database: "mydb", Collection: "users", ID: ins.ID, IfRevision: "stale"})
	require.ErrorAs(t, err, &conflict)
	_, err = svc.GetDocument(ctx, &GetDocumentRequest{Database: "mydb", Collection: "users", ID: ins.ID})
	require.NoError(t, err, "a refused guarded delete removes nothing")

	out, err := svc.DeleteDocument(ctx, &DeleteDocumentRequest{Database: "mydb", Collection: "users", ID: ins.ID, IfRevision: ins.Revision})
	require.NoError(t, err)
	assert.True(t, out.Deleted)
}
