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
// Incarnation-scoped keyspaces.
//
// Every piece of a collection's per-incarnation state -- its documents, its
// ID sequence, its indexes -- lives in a keyspace named by the incarnation,
// so nothing still working on a dead incarnation can reach a live one. Each
// test below was written against the shared-keyspace implementation and
// failed there; see the report for the recorded red runs.
// ---------------------------------------------------------------------------

// keyspaceKV is the faithful in-memory KV with one-shot hooks at moments a
// stale operation is exposed: just after the creating record is claimed, and
// just before the first document key is deleted.
type keyspaceKV struct {
	*memoryKVSvc
	mu              sync.Mutex
	afterMetaClaim  func()
	beforeDocDelete func()
	afterMetaGet    func()
}

var _ kvService = (*keyspaceKV)(nil)

func (k *keyspaceKV) take(h *func()) func() {
	k.mu.Lock()
	defer k.mu.Unlock()
	hook := *h
	*h = nil
	return hook
}

func (k *keyspaceKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	stored, err := k.memoryKVSvc.PutIfAbsent(ctx, key, value)
	if stored && strings.HasPrefix(key, collectionMetadataPrefix) {
		if hook := k.take(&k.afterMetaClaim); hook != nil {
			hook()
		}
	}
	return stored, err
}

func (k *keyspaceKV) Delete(ctx context.Context, key string) error {
	if strings.HasPrefix(key, collectionDataPrefix) {
		if hook := k.take(&k.beforeDocDelete); hook != nil {
			hook()
		}
	}
	return k.memoryKVSvc.Delete(ctx, key)
}

func (k *keyspaceKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if strings.HasPrefix(key, collectionDataPrefix) {
		if hook := k.take(&k.beforeDocDelete); hook != nil {
			hook()
		}
	}
	return k.memoryKVSvc.CompareAndDelete(ctx, key, expected)
}

func (k *keyspaceKV) Get(ctx context.Context, key string) ([]byte, error) {
	out, err := k.memoryKVSvc.Get(ctx, key)
	if strings.HasPrefix(key, collectionMetadataPrefix) {
		if hook := k.take(&k.afterMetaGet); hook != nil {
			hook()
		}
	}
	return out, err
}

func keyspaceHarness(t *testing.T, strategy, fieldHash string) (*keyspaceKV, *CollectionService, *SequenceService) {
	t.Helper()
	kv := &keyspaceKV{memoryKVSvc: newResolvedKVSvc()}
	seqStore := newFakeCatalogStore()
	seq := NewSequenceService(seqStore, seqStore)
	svc := NewCollectionService(kv).WithSequenceService(seq)
	_, err := svc.CreateCollection(context.Background(), &CreateCollectionRequest{Database: "db", Name: "c", IDStrategy: strategy, FieldHash: fieldHash})
	require.NoError(t, err)
	return kv, svc, seq
}

// dropAndRecreate drops c to completion, creates it again with the same
// strategy and writes the given document into the new incarnation.
func dropAndRecreate(t *testing.T, svc *CollectionService, strategy, fieldHash string, doc map[string]interface{}) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "db", Name: "c"}))
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Database: "db", Name: "c", IDStrategy: strategy, FieldHash: fieldHash})
	require.NoError(t, err)
	_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: doc})
	require.NoError(t, err)
}

func liveDoc(t *testing.T, svc *CollectionService, id uint64) map[string]interface{} {
	t.Helper()
	got, err := svc.GetDocument(context.Background(), &GetDocumentRequest{Database: "db", Collection: "c", ID: id})
	require.NoError(t, err, "the new incarnation's document %d must survive", id)
	return got.Document
}

// TestKeyspace_StaleUpdateCannotTouchTheNewIncarnation: an update decided
// against incarnation 1 lands after the name was dropped and created again.
// With a shared keyspace it overwrote incarnation 2's document 1 and its
// fence's retraction then deleted it.
func TestKeyspace_StaleUpdateCannotTouchTheNewIncarnation(t *testing.T) {
	kv, svc, _ := keyspaceHarness(t, IDStrategyIncrement, "")
	_, err := svc.InsertDocument(context.Background(), &InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"_id": 1.0, "k": "old"}})
	require.NoError(t, err)
	kv.afterMetaGet = func() {
		dropAndRecreate(t, svc, IDStrategyIncrement, "", map[string]interface{}{"_id": 1.0, "k": "live"})
	}

	_, err = svc.UpdateDocument(context.Background(), &UpdateDocumentRequest{Database: "db", Collection: "c", ID: 1, Document: map[string]interface{}{"k": "stale"}})
	require.Error(t, err, "a write decided against a dead incarnation is refused")
	assert.Equal(t, "live", liveDoc(t, svc, 1)["k"])
}

// TestKeyspace_StaleUpsertCannotTouchTheNewIncarnation is the same for the
// guarded upsert.
func TestKeyspace_StaleUpsertCannotTouchTheNewIncarnation(t *testing.T) {
	kv, svc, _ := keyspaceHarness(t, IDStrategyIncrement, "")
	kv.afterMetaGet = func() {
		dropAndRecreate(t, svc, IDStrategyIncrement, "", map[string]interface{}{"_id": 1.0, "k": "live"})
	}

	_, err := svc.UpsertDocument(context.Background(), &UpsertDocumentRequest{Database: "db", Collection: "c", ID: 1, Document: map[string]interface{}{"k": "stale"}})
	require.Error(t, err)
	assert.Equal(t, "live", liveDoc(t, svc, 1)["k"])
}

// TestKeyspace_StaleFieldHashInsertCannotTouchTheNewIncarnation: a
// field_hash insert is an overwrite of the natural key's document, and a
// stale one used to overwrite -- then retract -- the new incarnation's.
func TestKeyspace_StaleFieldHashInsertCannotTouchTheNewIncarnation(t *testing.T) {
	kv, svc, _ := keyspaceHarness(t, IDStrategyFieldHash, "email")
	doc := map[string]interface{}{"email": "a@example.com", "k": "live"}
	kv.afterMetaGet = func() { dropAndRecreate(t, svc, IDStrategyFieldHash, "email", doc) }

	resp, err := svc.InsertDocument(context.Background(), &InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"email": "a@example.com", "k": "stale"}})
	require.Error(t, err)
	_ = resp
	list, err := svc.ListDocuments(context.Background(), &ListDocumentsRequest{Database: "db", Collection: "c"})
	require.NoError(t, err)
	require.Len(t, list.Documents, 1, "the new incarnation's document must survive")
	assert.Equal(t, "live", list.Documents[0].Document["k"])
}

// TestKeyspace_StaleCreateCannotTouchTheNewIncarnation: a create whose name
// is dropped and created again -- and written to -- just after it claimed the
// name. The stale create used to purge the prefix both incarnations shared.
func TestKeyspace_StaleCreateCannotTouchTheNewIncarnation(t *testing.T) {
	kv := &keyspaceKV{memoryKVSvc: newResolvedKVSvc()}
	svc := NewCollectionService(kv)
	ctx := context.Background()
	kv.afterMetaClaim = func() {
		require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "db", Name: "c"}))
		_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Database: "db", Name: "c"})
		require.NoError(t, err)
		_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"_id": 5.0, "k": "live"}})
		require.NoError(t, err)
	}
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Database: "db", Name: "c"})
	require.Error(t, err, "the stale create loses its activation")
	assert.Equal(t, "live", liveDoc(t, svc, 5)["k"])
}

// TestKeyspace_ResumedTeardownCannotTouchTheNewIncarnation is N1: a teardown
// paused between its last check and its next document delete while the name
// is dropped, created again and written with a byte-identical document. A
// shared keyspace let the compare-and-delete match the new document's bytes.
func TestKeyspace_ResumedTeardownCannotTouchTheNewIncarnation(t *testing.T) {
	kv, svc, seq := keyspaceHarness(t, IDStrategyIncrement, "")
	ctx := context.Background()
	same := map[string]interface{}{"_id": 1.0, "k": "v"}
	_, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: same})
	require.NoError(t, err)
	staleDropping(t, kv, "db", "c")
	kv.beforeDocDelete = func() {
		info, raw, lerr := svc.loadCollectionRecord(ctx, "db", "c")
		require.NoError(t, lerr)
		// Finish the drop out from under the sweep, then create the name
		// again with the byte-identical document and one generated one.
		require.NoError(t, deleteMetadataForTest(ctx, kv, info, raw))
		_, cerr := svc.CreateCollection(ctx, &CreateCollectionRequest{Database: "db", Name: "c", IDStrategy: IDStrategyIncrement})
		require.NoError(t, cerr)
		_, ierr := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: same})
		require.NoError(t, ierr)
		_, ierr = svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"k": "gen"}})
		require.NoError(t, ierr)
	}

	_, _ = svc.SweepStuckCollections(ctx, "db", time.Minute)
	assert.Equal(t, "v", liveDoc(t, svc, 1)["k"])
	assert.Equal(t, "gen", liveDoc(t, svc, 2)["k"])
	info, err := svc.GetCollectionInfo(ctx, "db", "c")
	require.NoError(t, err)
	assert.Equal(t, CollectionStateActive, info.State)
	next, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"k": "next"}})
	require.NoError(t, err)
	assert.Equal(t, uint64(3), next.ID, "the new incarnation's counter must not be reset")
	_ = seq
}

// deleteMetadataForTest removes a collection's metadata record, the last
// step of a drop some other actor finished.
func deleteMetadataForTest(ctx context.Context, kv kvService, info *CollectionInfo, raw []byte) error {
	_, err := kv.CompareAndDelete(ctx, collectionMetadataKey(info.Database, info.Name), raw)
	return err
}

// TestKeyspace_LegacyTeardownDropsIndexesAsTheExplicitOwner is N3: metadata
// written before ownership names indexes that were created ownerless; its
// teardown drops them claiming the explicit owner, so an index a collection
// now owns under the same name survives.
func TestKeyspace_LegacyTeardownDropsIndexesAsTheExplicitOwner(t *testing.T) {
	ctx := context.Background()
	kv := newResolvedKVSvc()
	idx := newMemoryIndexSvc()
	svc := NewCollectionServiceWithIndex(kv, idx)
	putMetadata(t, kv, &CollectionInfo{
		Name: "old", Database: "db", IDStrategy: IDStrategyUnixNano,
		Indexes: []IndexDefinition{{FieldName: "a"}, {FieldName: "b"}},
	})
	require.NoError(t, idx.CreateIndex(ctx, "db.old.a", "a", index.IndexConfig{}))
	owner := index.CollectionOwner("db", "old", "newer")
	require.NoError(t, idx.CreateIndex(ctx, "db.old.b", "b", index.IndexConfig{Owner: owner}))

	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "db", Name: "old"}))
	_, exists := idx.ownerOf("db.old.a")
	assert.False(t, exists, "the legacy collection's own ownerless index is dropped")
	got, exists := idx.ownerOf("db.old.b")
	require.True(t, exists, "an index a collection owns under the legacy name survives")
	assert.Equal(t, owner, got)
}

// TestKeyspace_SweeperRefusesAnInconsistentRecord is N5: a DROPPING record
// whose index owner does not derive from its own (database, name,
// incarnation) is not torn down on the sweeper's authority.
func TestKeyspace_SweeperRefusesAnInconsistentRecord(t *testing.T) {
	ctx := context.Background()
	kv := newResolvedKVSvc()
	idx := newMemoryIndexSvc()
	svc := NewCollectionServiceWithIndex(kv, idx)
	foreign := index.CollectionOwner("db", "other", "x")
	require.NoError(t, idx.CreateIndex(ctx, "db.c.f", "f", index.IndexConfig{Owner: foreign}))
	putMetadata(t, kv, &CollectionInfo{
		Name: "c", Database: "db", State: CollectionStateDropping, StateSince: time.Now().Add(-time.Hour),
		Incarnation: "mine", IndexOwner: foreign, Indexes: []IndexDefinition{{FieldName: "f"}},
	})
	_, err := svc.SweepStuckCollections(ctx, "db", time.Minute)
	require.Error(t, err, "an inconsistent record is reported, not torn down")
	_, exists := idx.ownerOf("db.c.f")
	assert.True(t, exists)
	_, gerr := svc.GetCollectionInfo(ctx, "db", "c")
	assert.NoError(t, gerr, "the record is left for an operator")
}

// TestKeyspace_SweeperRefusesAnInconsistentLedgerEntry is N5 for the index
// ledger: an entry whose owner or index names do not derive from its own
// (database, name, incarnation) drops nothing.
func TestKeyspace_SweeperRefusesAnInconsistentLedgerEntry(t *testing.T) {
	ctx := context.Background()
	kv := newResolvedKVSvc()
	idx := newMemoryIndexSvc()
	svc := NewCollectionServiceWithIndex(kv, idx)
	victimOwner := index.CollectionOwner("db", "victim", "live")
	require.NoError(t, idx.CreateIndex(ctx, "db.victim.f", "f", index.IndexConfig{Owner: victimOwner}))
	entry, err := json.Marshal(&collectionIndexLedger{
		Database: "db", Name: "gone", Incarnation: "dead", Owner: victimOwner,
		Indexes: []string{"db.victim.f"}, CreatedAt: time.Now().Add(-time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, kv.Put(ctx, collectionIndexLedgerKey("db", "dead"), entry))

	_, err = svc.SweepStuckCollections(ctx, "db", time.Minute)
	require.Error(t, err)
	_, exists := idx.ownerOf("db.victim.f")
	assert.True(t, exists, "an index the entry does not derive may not be dropped on its word")
}
