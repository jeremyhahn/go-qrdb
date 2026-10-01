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

	"github.com/jeremyhahn/go-qrdb/pkg/codec"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// afterDocWriteKV is the faithful in-memory KV with a one-shot hook that runs
// just after a document is stored by a guarded insert: the moment a writer has
// written and not yet fenced.
type afterDocWriteKV struct {
	*memoryKVSvc
	mu            sync.Mutex
	afterDocWrite func()
	cadFails      int // CompareAndDelete of a document fails once this many succeed
	cadDone       int
}

var _ kvService = (*afterDocWriteKV)(nil)

func (k *afterDocWriteKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	stored, err := k.memoryKVSvc.PutIfAbsent(ctx, key, value)
	if stored && strings.HasPrefix(key, collectionDataPrefix) {
		if hook := takeHook(&k.mu, &k.afterDocWrite); hook != nil {
			hook()
		}
	}
	return stored, err
}

func (k *afterDocWriteKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if k.cadFails > 0 && strings.HasPrefix(key, collectionDataPrefix) {
		k.mu.Lock()
		done := k.cadDone
		k.cadDone++
		k.mu.Unlock()
		if done >= k.cadFails {
			return false, errStorage
		}
	}
	return k.memoryKVSvc.CompareAndDelete(ctx, key, expected)
}

// TestKeyspace_DropReportsExactlyTheBytesItRemoved: the metered drop reports
// the body bytes of the documents its own deletes removed -- the measure a
// write is charged by -- and a document somebody else removed first is not
// counted.
func TestKeyspace_DropReportsExactlyTheBytesItRemoved(t *testing.T) {
	ctx := context.Background()
	svc := NewCollectionService(newMemoryKVSvc())
	createCollection(t, svc, "db", "c", IDStrategyIncrement, "")
	docs := []map[string]interface{}{{"k": "one"}, {"k": "twotwo"}, {"k": "three", "n": 3.0}}
	var want int64
	var ids []uint64
	for _, d := range docs {
		resp, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: d})
		require.NoError(t, err)
		ids = append(ids, resp.ID)
		want += DocumentBodyBytes(d)
	}
	// One document is deleted before the drop; the drop must not count it.
	_, err := svc.DeleteDocument(ctx, &DeleteDocumentRequest{Database: "db", Collection: "c", ID: ids[0]})
	require.NoError(t, err)
	want -= DocumentBodyBytes(docs[0])

	freed, err := svc.DropCollectionMetered(ctx, &DropCollectionRequest{Database: "db", Name: "c"})
	require.NoError(t, err)
	assert.Equal(t, want, freed)
}

// TestKeyspace_PartialDropReportsWhatItRemoved: a teardown that fails part-way
// still reports the documents it removed before failing, so a quota releases
// them rather than holding them forever.
func TestKeyspace_PartialDropReportsWhatItRemoved(t *testing.T) {
	ctx := context.Background()
	kv := &afterDocWriteKV{memoryKVSvc: newMemoryKVSvc()}
	svc := NewCollectionService(kv)
	createCollection(t, svc, "db", "c", IDStrategyIncrement, "")
	doc := map[string]interface{}{"k": "same"}
	for i := 0; i < 3; i++ {
		_, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: doc})
		require.NoError(t, err)
	}
	kv.cadFails = 2

	freed, err := svc.DropCollectionMetered(ctx, &DropCollectionRequest{Database: "db", Name: "c"})
	assert.Equal(t, "delete-document", metadataOp(t, err))
	assert.Equal(t, 2*DocumentBodyBytes(doc), freed)

	kv.cadFails = 0
	freed, err = svc.DropCollectionMetered(ctx, &DropCollectionRequest{Database: "db", Name: "c"})
	require.NoError(t, err)
	assert.Equal(t, DocumentBodyBytes(doc), freed, "the resumed drop counts only what it removed")
}

// TestKeyspace_TeardownStopsOnACancelledContext: a teardown bounded by a
// deadline stops at its next batch check and reports what it removed.
func TestKeyspace_TeardownStopsOnACancelledContext(t *testing.T) {
	svc := NewCollectionService(newMemoryKVSvc())
	createCollection(t, svc, "db", "c", IDStrategyIncrement, "")
	_, err := svc.InsertDocument(context.Background(), &InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"k": "v"}})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	info, raw, err := svc.markDropping(context.Background(), "db", "c")
	require.NoError(t, err)

	freed, err := svc.teardownCollection(ctx, info, raw)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, freed)
	_, err = svc.GetCollectionInfo(context.Background(), "db", "c")
	require.NoError(t, err, "the drop is left DROPPING for a retry or the sweeper")
}

// TestKeyspace_StaleWriteReclaimsTheDeadSequence: a stale insert on an
// increment collection allocates from -- and so recreates -- the dead
// incarnation's sequence after its teardown deleted it. The refusal reclaims
// it, and the process-local generator cached for the dead incarnation.
func TestKeyspace_StaleWriteReclaimsTheDeadSequence(t *testing.T) {
	kv := &keyspaceKV{memoryKVSvc: newMemoryKVSvc()}
	seqStore := newFakeCatalogStore()
	svc := NewCollectionService(kv).WithSequenceService(NewSequenceService(seqStore, seqStore))
	createCollection(t, svc, "db", "c", IDStrategyIncrement, "")
	ctx := context.Background()
	dead := storedCollectionForTest(t, svc, "db", "c")
	kv.afterMetaGet = func() { dropAndRecreate(t, svc, IDStrategyIncrement, "", map[string]interface{}{"k": "live"}) }

	_, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"k": "stale"}})
	var conflict *CollectionStateConflictError
	require.ErrorAs(t, err, &conflict)

	_, perr := seqStore.Get(ctx, tableSequenceKey("db", collectionSequenceName(dead)))
	var notFound *KeyNotFoundError
	assert.ErrorAs(t, perr, &notFound, "the dead incarnation's recreated counter is reclaimed")
	_, cached := svc.idGens.Load(collectionIDGenKey(dead))
	assert.False(t, cached)
	assert.Equal(t, "live", liveDoc(t, svc, 1)["k"])
}

// TestKeyspace_RetractionThatFindsItsWriteGoneIsSuperseded: a write whose
// collection is dropped between the write and its fence -- and whose document
// the drop's teardown removed and counted -- is refused as superseded, so a
// quota keeps the write's reservation instead of releasing the bytes twice.
func TestKeyspace_RetractionThatFindsItsWriteGoneIsSuperseded(t *testing.T) {
	ctx := context.Background()
	kv := &afterDocWriteKV{memoryKVSvc: newMemoryKVSvc()}
	svc := NewCollectionService(kv)
	createCollection(t, svc, "db", "c", IDStrategyUnixNano, "")
	doc := map[string]interface{}{"k": "v"}
	var freed int64
	kv.afterDocWrite = func() {
		var err error
		freed, err = svc.DropCollectionMetered(ctx, &DropCollectionRequest{Database: "db", Name: "c"})
		require.NoError(t, err)
	}

	_, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: doc})
	var superseded *CollectionWriteSupersededError
	require.ErrorAs(t, err, &superseded)
	var notFound *CollectionNotFoundError
	assert.ErrorAs(t, err, &notFound, "the refusal is still the refusal")
	assert.Equal(t, notFound.Error(), superseded.Error())
	assert.Equal(t, DocumentBodyBytes(doc), freed, "the teardown counted the write it removed")
}

// TestKeyspace_RetractionSequenceFailureIsReported: a reclamation of the dead
// incarnation's counter that fails is reported with the refusal; the refusal
// is still what the caller matches.
func TestKeyspace_RetractionSequenceFailureIsReported(t *testing.T) {
	ctx := context.Background()
	kv := &keyspaceKV{memoryKVSvc: newMemoryKVSvc()}
	seqStore := newFakeCatalogStore()
	rec := &recordingSequenceService{SequenceServicer: NewSequenceService(seqStore, seqStore)}
	svc := NewCollectionService(kv).WithSequenceService(rec)
	createCollection(t, svc, "db", "c", IDStrategyIncrement, "")
	kv.afterMetaGet = func() {
		dropAndRecreate(t, svc, IDStrategyIncrement, "", map[string]interface{}{"k": "live"})
		rec.mu.Lock()
		rec.deleteErr = errStorage
		rec.mu.Unlock()
	}

	_, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"k": "stale"}})
	assert.Equal(t, "retract-sequence", metadataOp(t, err))
	var meta *CollectionMetadataError
	require.ErrorAs(t, err, &meta)
	assert.False(t, meta.OutcomeUnknown(), "the write itself was retracted")
	var conflict *CollectionStateConflictError
	assert.ErrorAs(t, err, &conflict)
	assert.ErrorIs(t, err, errStorage)
}

// TestKeyspace_LegacyCollectionKeepsItsUnsuffixedKeyspace: metadata written
// before incarnations existed is read, written and dropped where its
// documents and counter already are -- the unsuffixed prefix and sequence
// name -- with no migration.
func TestKeyspace_LegacyCollectionKeepsItsUnsuffixedKeyspace(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	seqStore := newFakeCatalogStore()
	seq := NewSequenceService(seqStore, seqStore)
	svc := NewCollectionService(kv).WithSequenceService(seq)
	putMetadata(t, kv, &CollectionInfo{Name: "old", Database: "db", IDStrategy: IDStrategyIncrement})
	require.NoError(t, kv.Put(ctx, "__col:db:old/5", []byte(`{"id":"5","k":"legacy"}`)))
	require.NoError(t, seq.Advance(ctx, "db", "__collection:old", 5))

	got, err := svc.GetDocument(ctx, &GetDocumentRequest{Database: "db", Collection: "old", ID: 5})
	require.NoError(t, err)
	assert.Equal(t, "legacy", got.Document["k"])
	ins, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "old", Document: map[string]interface{}{"k": "next"}})
	require.NoError(t, err)
	assert.Equal(t, uint64(6), ins.ID, "the legacy counter is the one advanced")
	_, err = kv.Get(ctx, "__col:db:old/6")
	require.NoError(t, err, "a legacy collection's new document lands in its existing keyspace")

	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "db", Name: "old"}))
	assert.Empty(t, kv.listAll("__col:db:old/"))
	_, err = seqStore.Get(ctx, tableSequenceKey("db", "__collection:old"))
	var notFound *KeyNotFoundError
	assert.ErrorAs(t, err, &notFound)
}

// TestKeyspace_DeleteRefusedOnANonActiveCollection: a delete is refused on a
// collection that is not ACTIVE, and removes nothing.
func TestKeyspace_DeleteRefusedOnANonActiveCollection(t *testing.T) {
	for _, state := range []string{CollectionStateCreating, CollectionStateDropping} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			kv := newMemoryKVSvc()
			svc := NewCollectionService(kv)
			createCollection(t, svc, "db", "c", IDStrategyIncrement, "")
			ins, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"k": "v"}})
			require.NoError(t, err)
			markStored(t, kv, "db", "c", state)

			_, err = svc.DeleteDocument(ctx, &DeleteDocumentRequest{Database: "db", Collection: "c", ID: ins.ID})
			var notActive *CollectionNotActiveError
			require.ErrorAs(t, err, &notActive)
			assert.Equal(t, state, notActive.State)
			_, err = kv.Get(ctx, docKeyForTest(t, svc, "db", "c", ins.ID))
			assert.NoError(t, err)
		})
	}
}

// TestKeyspace_FieldHashReinsertReportsTheDisplacedDocument: the insert
// response of a field_hash re-insert names the document it replaced.
func TestKeyspace_FieldHashReinsertReportsTheDisplacedDocument(t *testing.T) {
	ctx := context.Background()
	svc := NewCollectionService(newMemoryKVSvc())
	createCollection(t, svc, "db", "u", IDStrategyFieldHash, "email")
	first, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "u", Document: map[string]interface{}{"email": "a@example.com", "n": "1"}})
	require.NoError(t, err)
	assert.False(t, first.Replaced)
	second, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "u", Document: map[string]interface{}{"email": "a@example.com", "n": "2"}})
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)
	assert.True(t, second.Replaced)
	assert.Equal(t, map[string]interface{}{"email": "a@example.com", "n": "1"}, second.Previous)
	assert.NotEqual(t, first.Revision, second.Revision)
}

// TestCollectionRecordConsistent covers every refusal of a record the
// sweeper would act on.
func TestCollectionRecordConsistent(t *testing.T) {
	owner := index.CollectionOwner("db", "c", "inc")
	defs := []IndexDefinition{{FieldName: "f"}}
	tests := []struct {
		name string
		key  string
		info CollectionInfo
		ok   bool
	}{
		{"consistent", collectionMetadataKey("db", "c"), CollectionInfo{Database: "db", Name: "c", Incarnation: "inc", IndexOwner: owner, Indexes: defs}, true},
		{"no indexes, no owner", collectionMetadataKey("db", "c"), CollectionInfo{Database: "db", Name: "c", Incarnation: "inc"}, true},
		{"legacy", collectionMetadataKey("db", "c"), CollectionInfo{Database: "db", Name: "c", Indexes: defs}, true},
		{"another database", collectionMetadataKey("other", "c"), CollectionInfo{Database: "other", Name: "c"}, false},
		{"stored under another name", collectionMetadataKey("db", "x"), CollectionInfo{Database: "db", Name: "c"}, false},
		{"no name", collectionMetadataKey("db", ""), CollectionInfo{Database: "db"}, false},
		{"owner from another incarnation", collectionMetadataKey("db", "c"), CollectionInfo{Database: "db", Name: "c", Incarnation: "other", IndexOwner: owner, Indexes: defs}, false},
		{"owner without indexes", collectionMetadataKey("db", "c"), CollectionInfo{Database: "db", Name: "c", Incarnation: "inc", IndexOwner: owner}, false},
		{"legacy claiming an owner", collectionMetadataKey("db", "c"), CollectionInfo{Database: "db", Name: "c", IndexOwner: owner, Indexes: defs}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := collectionRecordConsistent(tc.key, "db", &tc.info)
			if tc.ok {
				assert.NoError(t, err)
				return
			}
			var inconsistent *CollectionInconsistentRecordError
			require.ErrorAs(t, err, &inconsistent)
			assert.Contains(t, inconsistent.Error(), "is inconsistent")
		})
	}
}

// TestLedgerEntryConsistent covers every refusal of an index-ledger entry the
// sweeper would act on.
func TestLedgerEntryConsistent(t *testing.T) {
	good := func() collectionIndexLedger {
		return collectionIndexLedger{
			Database: "db", Name: "c", Incarnation: "inc",
			Owner: index.CollectionOwner("db", "c", "inc"), Indexes: []string{"db.c.f"},
		}
	}
	tests := []struct {
		name   string
		key    string
		mutate func(*collectionIndexLedger)
		ok     bool
	}{
		{"consistent", collectionIndexLedgerKey("db", "inc"), func(*collectionIndexLedger) {}, true},
		{"another database", collectionIndexLedgerKey("db", "inc"), func(e *collectionIndexLedger) { e.Database = "other" }, false},
		{"stored under another incarnation", collectionIndexLedgerKey("db", "x"), func(*collectionIndexLedger) {}, false},
		{"no incarnation", collectionIndexLedgerKey("db", ""), func(e *collectionIndexLedger) { e.Incarnation = "" }, false},
		{"no name", collectionIndexLedgerKey("db", "inc"), func(e *collectionIndexLedger) { e.Name = "" }, false},
		{"foreign owner", collectionIndexLedgerKey("db", "inc"), func(e *collectionIndexLedger) { e.Owner = index.CollectionOwner("db", "v", "inc") }, false},
		{"another collection's index", collectionIndexLedgerKey("db", "inc"), func(e *collectionIndexLedger) { e.Indexes = []string{"db.v.f"} }, false},
		{"an index with no field", collectionIndexLedgerKey("db", "inc"), func(e *collectionIndexLedger) { e.Indexes = []string{"db.c."} }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := good()
			tc.mutate(&entry)
			err := ledgerEntryConsistent(tc.key, "db", &entry)
			if tc.ok {
				assert.NoError(t, err)
				return
			}
			var inconsistent *CollectionInconsistentRecordError
			assert.ErrorAs(t, err, &inconsistent)
		})
	}
}

// TestKeyspace_SweeperStillFinishesConsistentRecordsBesideAnInconsistentOne:
// a refused record does not stop the pass.
func TestKeyspace_SweeperStillFinishesConsistentRecordsBesideAnInconsistentOne(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	svc := NewCollectionService(kv)
	stale := time.Now().Add(-time.Hour)
	putMetadata(t, kv, &CollectionInfo{Name: "bad", Database: "db", State: CollectionStateDropping, StateSince: stale, Incarnation: "i", IndexOwner: "someone"})
	putMetadata(t, kv, &CollectionInfo{Name: "good", Database: "db", State: CollectionStateDropping, StateSince: stale, Incarnation: "j"})

	res, err := svc.SweepStuckCollections(ctx, "db", time.Minute)
	var inconsistent *CollectionInconsistentRecordError
	require.ErrorAs(t, err, &inconsistent)
	assert.Equal(t, "bad", inconsistent.Collection)
	assert.Equal(t, 1, res.Swept)
	raw, gerr := kv.Get(ctx, collectionMetadataKey("db", "bad"))
	require.NoError(t, gerr)
	var left CollectionInfo
	require.NoError(t, json.Unmarshal(raw, &left))
	assert.Equal(t, CollectionStateDropping, left.State)
	assert.False(t, errors.Is(err, context.Canceled))
}

// TestDocumentBodyBytes pins the one measure writes are charged and
// reclamations released by: the JSON body without the identity key.
func TestDocumentBodyBytes(t *testing.T) {
	assert.Zero(t, DocumentBodyBytes(nil), "an empty document costs nothing")
	with := DocumentBodyBytes(map[string]interface{}{"_id": 7, "v": "x"})
	without := DocumentBodyBytes(map[string]interface{}{"v": "x"})
	assert.Equal(t, without, with, "the identity is not a stored field")
	assert.Zero(t, DocumentBodyBytes(map[string]interface{}{"bad": make(chan int)}), "an unencodable document measures zero")
}

// TestStoredDocumentBodyBytes: a stored document is measured by its decoded
// body, and one that cannot be decoded by its stored size.
func TestStoredDocumentBodyBytes(t *testing.T) {
	svc := NewCollectionService(newMemoryKVSvc())
	c := svc.resolveCodec(&CollectionInfo{Database: "db"})
	data, err := marshalDocument(&dao.DynamicEntity{ID: 3, Fields: map[string]interface{}{"v": "x"}}, c)
	require.NoError(t, err)
	assert.Equal(t, DocumentBodyBytes(map[string]interface{}{"v": "x"}), storedDocumentBodyBytes(data, c))
	assert.Equal(t, int64(len("{not json")), storedDocumentBodyBytes([]byte("{not json"), c))
}

// TestKeyspace_TeardownDoesNotCountADocumentRemovedUnderIt: a guarded delete
// decided while the collection was still active lands between the teardown's
// scan and its delete of that document. The delete released the document
// itself, so the teardown -- whose compare-and-delete then removes nothing --
// must not count it again.
func TestKeyspace_TeardownDoesNotCountADocumentRemovedUnderIt(t *testing.T) {
	ctx := context.Background()
	kv := &keyspaceKV{memoryKVSvc: newMemoryKVSvc()}
	svc := NewCollectionService(kv)
	createCollection(t, svc, "db", "c", IDStrategyIncrement, "")
	doc := map[string]interface{}{"k": "v"}
	for i := 0; i < 2; i++ {
		_, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: doc})
		require.NoError(t, err)
	}
	prefix := docPrefixForTest(t, svc, "db", "c")
	kv.beforeDocDelete = func() {
		// The in-flight delete removes both documents first.
		for _, key := range kv.listAll(prefix) {
			require.NoError(t, kv.memoryKVSvc.Delete(ctx, key))
		}
	}

	freed, err := svc.DropCollectionMetered(ctx, &DropCollectionRequest{Database: "db", Name: "c"})
	require.NoError(t, err)
	assert.Zero(t, freed, "documents another delete removed are that delete's to release")
}

// TestCreateCollection_RefusesTheIncarnationSeparator: '#' separates a name
// from its incarnation in every per-incarnation key, so a new name containing
// it is refused before anything is stored -- which makes a new collection's
// keys impossible to confuse with another's by construction.
func TestCreateCollection_RefusesTheIncarnationSeparator(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	svc := NewCollectionService(kv)
	for _, name := range []string{"a#0123456789abcdef", "#", "trailing#"} {
		_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Database: "db", Name: name})
		var invalid *CollectionInvalidNameError
		require.ErrorAs(t, err, &invalid, name)
		assert.Equal(t, name, invalid.Name)
		assert.Contains(t, invalid.Error(), `must not contain "#"`)
	}
	assert.Empty(t, kv.listAll(collectionMetadataPrefix), "a refused name stores nothing")
}

// TestLegacyCollectionWithTheSeparatorKeepsWorking: a collection that
// already exists under a name containing '#' -- written before the refusal --
// is still read, written and dropped.
func TestLegacyCollectionWithTheSeparatorKeepsWorking(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	svc := NewCollectionService(kv)
	putMetadata(t, kv, &CollectionInfo{Name: "old#x", Database: "db", IDStrategy: IDStrategyUnixNano})

	ins, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "old#x", Document: map[string]interface{}{"k": "v"}})
	require.NoError(t, err)
	got, err := svc.GetDocument(ctx, &GetDocumentRequest{Database: "db", Collection: "old#x", ID: ins.ID})
	require.NoError(t, err)
	assert.Equal(t, "v", got.Document["k"])
	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "db", Name: "old#x"}))
	assert.Empty(t, kv.listAll("__col:db:old#x/"))
}

// TestDocumentBodyBytes_ChargeEqualsRelease: for every document the service
// accepts, the bytes a write is charged (measured on the request) equal the
// bytes its reclamation releases (measured on what was stored), under every
// codec built into this binary that can encode a document map.
func TestDocumentBodyBytes_ChargeEqualsRelease(t *testing.T) {
	docs := []map[string]interface{}{
		{"a": 1.0, "s": "x"},
		{"n": map[string]interface{}{"x": 1.0, "y": "z"}, "arr": []interface{}{1.0, "two", true}},
		{"big": 12345678901234.0, "f": 0.1, "b": false},
		{"_id": 3.0, "k": "v"},
		{"fields": map[string]interface{}{"a": 1.0}, "other": "x"},
	}
	checked := 0
	for _, name := range []string{codec.TypeJSON, codec.TypeMsgPack, codec.TypeBSON, codec.TypeYAML} {
		c, err := codec.ResolveCodec(name)
		if err != nil {
			continue // not built into this binary
		}
		for i, d := range docs {
			fields := documentFields(d)
			require.NoError(t, validateStoredFields(fields))
			raw, err := marshalDocument(&dao.DynamicEntity{ID: 3, Fields: fields}, c)
			require.NoError(t, err)
			assert.Equal(t, DocumentBodyBytes(d), storedDocumentBodyBytes(raw, c), "%s doc %d", name, i)
			checked++
		}
	}
	assert.NotZero(t, checked, "the JSON codec is always built")
}

// cancelOnDocDeleteKV cancels a context the moment the first document is
// compare-and-deleted: a teardown whose deadline expires mid-way.
type cancelOnDocDeleteKV struct {
	*memoryKVSvc
	cancel context.CancelFunc
}

var _ kvService = (*cancelOnDocDeleteKV)(nil)

func (k *cancelOnDocDeleteKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	deleted, err := k.memoryKVSvc.CompareAndDelete(ctx, key, expected)
	if strings.HasPrefix(key, collectionDataPrefix) {
		k.cancel()
	}
	return deleted, err
}

// TestKeyspace_TeardownChecksItsContextBeforeEveryDelete: a teardown whose
// context ends stops before its very next delete, not at the next batch
// boundary, and reports exactly what it removed.
func TestKeyspace_TeardownChecksItsContextBeforeEveryDelete(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	kv := &cancelOnDocDeleteKV{memoryKVSvc: newMemoryKVSvc(), cancel: cancel}
	svc := NewCollectionService(kv)
	createCollection(t, svc, "db", "c", IDStrategyIncrement, "")
	doc := map[string]interface{}{"k": "v"}
	for i := 0; i < 3; i++ {
		_, err := svc.InsertDocument(context.Background(), &InsertDocumentRequest{Database: "db", Collection: "c", Document: doc})
		require.NoError(t, err)
	}
	prefix := docPrefixForTest(t, svc, "db", "c")

	freed, err := svc.DropCollectionMetered(ctx, &DropCollectionRequest{Database: "db", Name: "c"})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, DocumentBodyBytes(doc), freed, "exactly one document was removed")
	assert.Len(t, kv.listAll(prefix), 2, "the teardown stopped before its next delete")
}
