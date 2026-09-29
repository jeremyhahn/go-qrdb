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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// ---------------------------------------------------------------------------
// Incarnations: a collection dropped and created again is a different
// collection, and nothing still working on the old one may touch the new one.
// ---------------------------------------------------------------------------

// raceKV is the faithful in-memory KV with one-shot hooks at the moments a
// slow teardown or a slow writer is exposed: just after a teardown scans the
// documents, just before the metadata is compare-and-deleted, and just after
// the first metadata read. Each hook runs once, with no lock held.
type raceKV struct {
	*memoryKVSvc
	mu            sync.Mutex
	afterDocScan  func()
	beforeMetaCAD func()
	afterMetaGet  func()
}

var _ kvService = (*raceKV)(nil)

func takeHook(mu *sync.Mutex, h *func()) func() {
	mu.Lock()
	defer mu.Unlock()
	hook := *h
	*h = nil
	return hook
}

func (r *raceKV) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	out, err := r.memoryKVSvc.Scan(ctx, prefix)
	if strings.HasPrefix(prefix, collectionDataPrefix) {
		if hook := takeHook(&r.mu, &r.afterDocScan); hook != nil {
			hook()
		}
	}
	return out, err
}

func (r *raceKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if strings.HasPrefix(key, collectionMetadataPrefix) {
		if hook := takeHook(&r.mu, &r.beforeMetaCAD); hook != nil {
			hook()
		}
	}
	return r.memoryKVSvc.CompareAndDelete(ctx, key, expected)
}

func (r *raceKV) Get(ctx context.Context, key string) ([]byte, error) {
	out, err := r.memoryKVSvc.Get(ctx, key)
	if strings.HasPrefix(key, collectionMetadataPrefix) {
		if hook := takeHook(&r.mu, &r.afterMetaGet); hook != nil {
			hook()
		}
	}
	return out, err
}

// staleDropping rewrites a stored collection into a DROPPING state entered an
// hour ago -- a drop that crashed after its first write.
func staleDropping(t *testing.T, kv kvService, database, name string) {
	t.Helper()
	raw, err := kv.Get(context.Background(), collectionMetadataKey(database, name))
	require.NoError(t, err)
	var info CollectionInfo
	require.NoError(t, json.Unmarshal(raw, &info))
	info.State = CollectionStateDropping
	info.StateSince = time.Now().Add(-time.Hour)
	putMetadata(t, kv, &info)
}

// recreate drops the collection to completion and creates it again, with the
// document the old incarnation held -- byte-identical, so only a guard that
// knows WHICH collection it is tearing down can tell them apart -- and one
// generated document.
func recreate(t *testing.T, svc *CollectionService) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "db", Name: "c"}))
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Database: "db", Name: "c", IDStrategy: IDStrategyIncrement})
	require.NoError(t, err)
	_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"_id": 1.0, "k": "v"}})
	require.NoError(t, err)
	_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"k": "new"}})
	require.NoError(t, err)
}

// assertNewCollectionIntact checks the re-created collection survived a
// teardown of its predecessor untouched.
func assertNewCollectionIntact(t *testing.T, svc *CollectionService, seq SequenceServicer) {
	t.Helper()
	ctx := context.Background()
	info, err := svc.GetCollectionInfo(ctx, "db", "c")
	require.NoError(t, err, "the new collection's metadata must survive")
	assert.Equal(t, CollectionStateActive, info.State)
	for _, id := range []uint64{1, 2} {
		_, err := svc.GetDocument(ctx, &GetDocumentRequest{Database: "db", Collection: "c", ID: id})
		assert.NoError(t, err, "document %d of the new collection must survive", id)
	}
	current, err := seq.(*SequenceService).Peek(ctx, "db", seqNameForTest(t, svc, "db", "c"))
	require.NoError(t, err)
	assert.Equal(t, uint64(2), current, "the new collection's counter must not be reset")
}

func newIncarnationHarness(t *testing.T) (*raceKV, *CollectionService, SequenceServicer) {
	t.Helper()
	kv := &raceKV{memoryKVSvc: newMemoryKVSvc()}
	seqStore := newFakeCatalogStore()
	seq := NewSequenceService(seqStore, seqStore)
	svc := NewCollectionService(kv).WithSequenceService(seq)
	_, err := svc.CreateCollection(context.Background(), &CreateCollectionRequest{Database: "db", Name: "c", IDStrategy: IDStrategyIncrement})
	require.NoError(t, err)
	_, err = svc.InsertDocument(context.Background(), &InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"_id": 1.0, "k": "v"}})
	require.NoError(t, err)
	return kv, svc, seq
}

// TestTeardown_ResumingSweepLeavesARecreatedCollectionAlone is the data-loss
// case: a sweep of a stale DROPPING collection stalls after scanning its
// documents; meanwhile the collection is dropped by someone else, created
// again and written to. The resumed sweep must touch nothing of the new
// collection -- not its documents, not its counter, not its metadata.
//
// The new collection's documents and counter live in its own incarnation's
// keyspace, which the resumed sweep never names: it reclaims only the dead
// incarnation's prefix and sequence.
func TestTeardown_ResumingSweepLeavesARecreatedCollectionAlone(t *testing.T) {
	kv, svc, seq := newIncarnationHarness(t)
	staleDropping(t, kv, "db", "c")
	kv.afterDocScan = func() { recreate(t, svc) }

	res, err := svc.SweepStuckCollections(context.Background(), "db", time.Minute)
	require.NoError(t, err, "a teardown finished by somebody else is not a failure")
	assert.Equal(t, 0, res.Swept)
	assertNewCollectionIntact(t, svc, seq)
}

// TestTeardown_MetadataDeleteIsGuardedByTheRecord lands the re-create in the
// last window: after every check, just before the metadata delete. The
// delete is a compare-and-delete of the DROPPING record the teardown started
// from, so it cannot remove the new collection's metadata.
//
// Mutation-checked: with a plain Delete the new collection's metadata is
// removed and this test fails.
func TestTeardown_MetadataDeleteIsGuardedByTheRecord(t *testing.T) {
	kv, svc, _ := newIncarnationHarness(t)
	staleDropping(t, kv, "db", "c")
	var recreated bool
	kv.beforeMetaCAD = func() {
		// Finish the teardown's own record out from under it, then create
		// the name again. The nested drop's metadata delete finds the hook
		// already taken.
		info, raw, err := svc.loadCollectionRecord(context.Background(), "db", "c")
		require.NoError(t, err)
		_, terr := svc.teardownCollection(context.Background(), info, raw)
		require.NoError(t, terr)
		_, err = svc.CreateCollection(context.Background(), &CreateCollectionRequest{Database: "db", Name: "c"})
		require.NoError(t, err)
		recreated = true
	}

	_, err := svc.SweepStuckCollections(context.Background(), "db", time.Minute)
	require.NoError(t, err)
	require.True(t, recreated)
	info, err := svc.GetCollectionInfo(context.Background(), "db", "c")
	require.NoError(t, err, "the re-created collection's metadata must survive")
	assert.Equal(t, CollectionStateActive, info.State)
}

// TestFence_WriteDecidedAgainstAnEarlierIncarnationIsRetracted lets a
// collection be dropped and created again between an insert's decision (the
// metadata it loaded) and its write. The new collection is ACTIVE, so only
// the incarnation tells the fence the write was decided against something
// else.
//
// Mutation-checked: without the incarnation comparison the write lands in
// the new collection and this test fails.
func TestFence_WriteDecidedAgainstAnEarlierIncarnationIsRetracted(t *testing.T) {
	kv, svc, _ := newIncarnationHarness(t)
	var newIncarnation string
	kv.afterMetaGet = func() {
		ctx := context.Background()
		require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "db", Name: "c"}))
		info, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Database: "db", Name: "c"})
		require.NoError(t, err)
		newIncarnation = info.Incarnation
	}

	_, err := svc.InsertDocument(context.Background(), &InsertDocumentRequest{
		Database: "db", Collection: "c", Document: map[string]interface{}{"_id": 7.0, "k": "stale"},
	})
	var conflict *CollectionStateConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, "write", conflict.Op)
	require.NotEmpty(t, newIncarnation)
	_, gerr := svc.GetDocument(context.Background(), &GetDocumentRequest{Database: "db", Collection: "c", ID: 7})
	var notFound *CollectionDocumentNotFoundError
	assert.ErrorAs(t, gerr, &notFound, "the stale write must not appear in the new collection")
}

// TestCreate_EarlierKeyspacesAreUnreachable covers the orphan a crashed
// writer can leave: a document that committed into a dropped incarnation's
// keyspace after its teardown scanned it. A new incarnation of the name reads
// and lists only its own keyspace, so neither that orphan nor a document
// under the legacy unsuffixed prefix appears in it -- with no purge, and so
// nothing a stale create could purge from the live collection.
func TestCreate_EarlierKeyspacesAreUnreachable(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	svc := NewCollectionService(kv)
	dead := &CollectionInfo{Database: "db", Name: "c", Incarnation: "0123456789abcdef"}
	legacy := &CollectionInfo{Database: "db", Name: "c"}
	require.NoError(t, kv.Put(ctx, collectionDocumentKey(dead, 9), []byte(`{"id":"9","k":"orphan"}`)))
	require.NoError(t, kv.Put(ctx, collectionDocumentKey(legacy, 8), []byte(`{"id":"8","k":"legacy"}`)))

	info, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Database: "db", Name: "c"})
	require.NoError(t, err)
	assert.NotEqual(t, dead.documentPrefix(), info.documentPrefix())
	list, err := svc.ListDocuments(ctx, &ListDocumentsRequest{Database: "db", Collection: "c"})
	require.NoError(t, err)
	assert.Empty(t, list.Documents, "no earlier keyspace's document may appear in a new collection")
	for _, id := range []uint64{8, 9} {
		_, gerr := svc.GetDocument(ctx, &GetDocumentRequest{Database: "db", Collection: "c", ID: id})
		var notFound *CollectionDocumentNotFoundError
		assert.ErrorAs(t, gerr, &notFound)
	}
	got, err := svc.GetCollectionInfo(ctx, "db", "c")
	require.NoError(t, err)
	assert.Zero(t, got.DocumentCount)
}

// TestCreate_EveryCollectionHasAnIncarnation pins that the incarnation is
// drawn for every create, not only for collections with indexes, and that a
// re-created name draws a new one.
func TestCreate_EveryCollectionHasAnIncarnation(t *testing.T) {
	ctx := context.Background()
	svc := NewCollectionService(newMemoryKVSvc())
	first, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Database: "db", Name: "c"})
	require.NoError(t, err)
	require.NotEmpty(t, first.Incarnation)
	assert.Empty(t, first.IndexOwner, "a collection without indexes owns none")
	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "db", Name: "c"}))
	second, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Database: "db", Name: "c"})
	require.NoError(t, err)
	assert.NotEqual(t, first.Incarnation, second.Incarnation)
}

// TestValidateIndexDefinitions_RefusesADuplicateField pins that two
// definitions naming one field -- which derive one index name, so the second
// would silently be the first -- are refused before anything is stored.
func TestValidateIndexDefinitions_RefusesADuplicateField(t *testing.T) {
	ctx := context.Background()
	svc, idx := newTestCollectionServiceWithIndex()
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Database: "db", Name: "c",
		Indexes: []IndexDefinition{{FieldName: "email"}, {FieldName: "email", Unique: true}},
	})
	var invalid *CollectionInvalidIndexError
	require.ErrorAs(t, err, &invalid)
	assert.Contains(t, invalid.Error(), "more than once")
	assert.Empty(t, idx.names())
	_, err = svc.GetCollectionInfo(ctx, "db", "c")
	require.Error(t, err)
}

// TestSweep_RemovesIndexesAnOrphanedLedgerNames covers a creator that died
// after building an index and after its collection's metadata was removed.
// Only the index ledger still names the index's owner; the sweeper drops it
// once the entry is stale and no metadata names its incarnation, and leaves
// a live incarnation's ledger alone.
//
// Mutation-checked: without the ledger sweep the orphaned index survives.
func TestSweep_RemovesIndexesAnOrphanedLedgerNames(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	idx := newMemoryIndexSvc()
	svc := NewCollectionServiceWithIndex(kv, idx)

	live, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Database: "db", Name: "live", Indexes: []IndexDefinition{{FieldName: "f"}}})
	require.NoError(t, err)

	orphanOwner := index.CollectionOwner("db", "gone", "dead")
	require.NoError(t, idx.CreateIndex(ctx, "db.gone.f", "f", index.IndexConfig{Owner: orphanOwner}))
	entry, err := json.Marshal(&collectionIndexLedger{
		Database: "db", Name: "gone", Incarnation: "dead", Owner: orphanOwner,
		Indexes: []string{"db.gone.f"}, CreatedAt: time.Now().Add(-time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, kv.Put(ctx, collectionIndexLedgerKey("db", "dead"), entry))

	// A fresh orphan entry is not touched yet: its creator may still be running.
	svc.now = func() time.Time { return time.Now().Add(-2 * time.Hour) }
	res, err := svc.SweepStuckCollections(ctx, "db", time.Minute)
	require.NoError(t, err)
	assert.Zero(t, res.Swept)
	svc.now = time.Now

	res, err = svc.SweepStuckCollections(ctx, "db", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Swept)
	_, exists := idx.ownerOf("db.gone.f")
	assert.False(t, exists, "the orphaned index must be dropped")
	_, err = kv.Get(ctx, collectionIndexLedgerKey("db", "dead"))
	assert.Error(t, err, "the orphan's ledger entry goes with it")

	owner, exists := idx.ownerOf("db.live.f")
	require.True(t, exists, "a live incarnation's index is left alone")
	assert.Equal(t, live.IndexOwner, owner)
	_, err = kv.Get(ctx, collectionIndexLedgerKey("db", live.Incarnation))
	assert.NoError(t, err)

	// Dropping the live collection removes its own ledger entry.
	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "db", Name: "live"}))
	_, err = kv.Get(ctx, collectionIndexLedgerKey("db", live.Incarnation))
	assert.Error(t, err)
}
