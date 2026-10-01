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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// This file covers what an explicitly supplied document _id does to a
// collection (go-qrdb#447): it reserves the collection's ID counter against
// itself, and it never writes over a document that already holds it.
//
// The replicated wiring here is the production one -- newIncrementCollectionService
// builds a real SequenceService over a store that evaluates compare-and-swap
// preconditions the way the state machine does -- so SequenceServicer.Advance
// is exercised as implemented, not as stubbed.

// insertDoc inserts doc and returns the ID the service assigned.
func insertDoc(t *testing.T, svc *CollectionService, database, collection string, doc map[string]interface{}) uint64 {
	t.Helper()
	resp, err := svc.InsertDocument(context.Background(), &InsertDocumentRequest{
		Database:   database,
		Collection: collection,
		Document:   doc,
	})
	require.NoError(t, err)
	return resp.ID
}

// createCollection creates a collection with the given strategy.
func createCollection(t *testing.T, svc *CollectionService, database, name, strategy, fieldHash string) {
	t.Helper()
	_, err := svc.CreateCollection(context.Background(), &CreateCollectionRequest{
		Database:   database,
		Name:       name,
		IDStrategy: strategy,
		FieldHash:  fieldHash,
	})
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// The defect
// ---------------------------------------------------------------------------

// TestCollectionService_ExplicitIDReservesTheCounter is the failing-first
// test for go-qrdb#447.
//
// Measured on the commit before this change, with exactly this wiring:
//
//	insert {"_id": 1, "who": "explicit"}   -> id 1
//	insert {"who": "auto"}                 -> id 1        <-- reissued
//	get id 1                               -> map[who:auto]
//	list                                   -> 1 document
//
// Two successful inserts, one surviving document, no error anywhere.
func TestCollectionService_ExplicitIDReservesTheCounter(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newIncrementCollectionService()
	createCollection(t, svc, "mydb", "users", IDStrategyIncrement, "")

	explicitID := insertDoc(t, svc, "mydb", "users", map[string]interface{}{"_id": 1, "who": "explicit"})
	require.Equal(t, uint64(1), explicitID, "an explicit _id must be honoured verbatim")

	generatedID := insertDoc(t, svc, "mydb", "users", map[string]interface{}{"who": "auto"})
	assert.Equal(t, uint64(2), generatedID, "the counter must have been advanced past the explicit id")

	got, err := svc.GetDocument(ctx, &GetDocumentRequest{Database: "mydb", Collection: "users", ID: 1})
	require.NoError(t, err)
	assert.Equal(t, "explicit", got.Document["who"], "the explicitly inserted document must still be there")

	list, err := svc.ListDocuments(ctx, &ListDocumentsRequest{Database: "mydb", Collection: "users"})
	require.NoError(t, err)
	assert.Equal(t, int64(2), list.TotalCount, "two successful inserts must leave two documents")
}

// TestCollectionService_ExplicitIDReservesTheCounter_ProcessLocalFallback
// runs the same measurement against the OTHER implementation of
// IDStrategyIncrement -- the process-local dao.AtomicIncrementGenerator used
// when no SequenceServicer is wired (the dedicated-store bundle, see
// pkg/server/database_barrier_services.go).
//
// The two must not disagree about this. go-qrdb#411 found them disagreeing
// about what a recreated collection does, and the only wiring that matched
// the documentation was the one that cannot deliver unique IDs at all.
func TestCollectionService_ExplicitIDReservesTheCounter_ProcessLocalFallback(t *testing.T) {
	svc := NewCollectionService(newMemoryKVSvc())
	createCollection(t, svc, "mydb", "users", IDStrategyIncrement, "")

	require.Equal(t, uint64(1), insertDoc(t, svc, "mydb", "users", map[string]interface{}{"_id": 1, "who": "explicit"}))
	assert.Equal(t, uint64(2), insertDoc(t, svc, "mydb", "users", map[string]interface{}{"who": "auto"}))
}

// TestCollectionService_ExplicitID_BothIncrementWiringsAgree pins the two
// implementations to the same observable sequence of IDs across a mixed run
// of explicit and generated inserts, rather than checking each separately
// and hoping the expectations were written consistently.
func TestCollectionService_ExplicitID_BothIncrementWiringsAgree(t *testing.T) {
	run := func(svc *CollectionService) []uint64 {
		createCollection(t, svc, "mydb", "users", IDStrategyIncrement, "")
		return []uint64{
			insertDoc(t, svc, "mydb", "users", map[string]interface{}{"n": 0}),
			insertDoc(t, svc, "mydb", "users", map[string]interface{}{"_id": 5, "n": 1}),
			insertDoc(t, svc, "mydb", "users", map[string]interface{}{"n": 2}),
			insertDoc(t, svc, "mydb", "users", map[string]interface{}{"_id": 3, "n": 3}),
			insertDoc(t, svc, "mydb", "users", map[string]interface{}{"n": 4}),
		}
	}

	replicated, _, _ := newIncrementCollectionService()
	fallback := NewCollectionService(newMemoryKVSvc())

	replicatedIDs := run(replicated)
	fallbackIDs := run(fallback)

	assert.Equal(t, []uint64{1, 5, 6, 3, 7}, replicatedIDs,
		"an explicit id below the watermark must not lower it, and one above it must raise it")
	assert.Equal(t, replicatedIDs, fallbackIDs,
		"the replicated and process-local implementations of IDStrategyIncrement must agree about an explicit _id")
}

// TestCollectionService_RecreateThenReimport is the workflow go-qrdb#411
// created and go-qrdb#447 made dangerous: drop a collection, recreate it
// under the same name (the counter now restarts at 1), re-import the
// documents carrying their original IDs, then insert without one.
func TestCollectionService_RecreateThenReimport(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newIncrementCollectionService()

	createCollection(t, svc, "mydb", "users", IDStrategyIncrement, "")
	for i := 1; i <= 3; i++ {
		insertDoc(t, svc, "mydb", "users", map[string]interface{}{"n": i})
	}
	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "users"}))

	createCollection(t, svc, "mydb", "users", IDStrategyIncrement, "")
	for i := 1; i <= 3; i++ {
		require.Equal(t, uint64(i), insertDoc(t, svc, "mydb", "users",
			map[string]interface{}{"_id": i, "imported": true}))
	}

	assert.Equal(t, uint64(4), insertDoc(t, svc, "mydb", "users", map[string]interface{}{"fresh": true}),
		"the first generated id after a re-import must clear every re-imported id")

	list, err := svc.ListDocuments(ctx, &ListDocumentsRequest{Database: "mydb", Collection: "users"})
	require.NoError(t, err)
	assert.Equal(t, int64(4), list.TotalCount, "no re-imported document may have been overwritten")
}

// ---------------------------------------------------------------------------
// The guard: an insert may not destroy a stored document silently
// ---------------------------------------------------------------------------

// TestCollectionService_InsertDocument_ExplicitIDCollisionIsRefused covers
// every ID strategy, because an explicit _id is a claim on an identity on
// all three of them -- it is never a strategy's generated value. Measured
// before the change: all three silently overwrote and reported success.
func TestCollectionService_InsertDocument_ExplicitIDCollisionIsRefused(t *testing.T) {
	for _, tc := range []struct {
		strategy  string
		fieldHash string
	}{
		{IDStrategyUnixNano, ""},
		{IDStrategyIncrement, ""},
		{IDStrategyFieldHash, "name"},
	} {
		t.Run(tc.strategy, func(t *testing.T) {
			ctx := context.Background()
			svc, _, _ := newIncrementCollectionService()
			createCollection(t, svc, "mydb", "c", tc.strategy, tc.fieldHash)

			require.Equal(t, uint64(7), insertDoc(t, svc, "mydb", "c",
				map[string]interface{}{"_id": 7, "name": "first", "who": "first"}))

			_, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
				Database: "mydb", Collection: "c",
				Document: map[string]interface{}{"_id": 7, "name": "second", "who": "second"},
			})
			var exists *CollectionDocumentExistsError
			require.ErrorAs(t, err, &exists, "a second insert claiming a taken _id must be refused")
			assert.True(t, exists.Explicit, "the error must record that the caller supplied the id")
			assert.Equal(t, uint64(7), exists.ID)
			assert.Equal(t, "mydb", exists.Database)
			assert.Equal(t, "c", exists.Collection)

			got, err := svc.GetDocument(ctx, &GetDocumentRequest{Database: "mydb", Collection: "c", ID: 7})
			require.NoError(t, err)
			assert.Equal(t, "first", got.Document["who"], "the refused insert must not have written anything")
		})
	}
}

// TestCollectionService_InsertDocument_FieldHashReinsertStaysIdempotent is
// the one collision the guard must NOT refuse. docs/collections/architecture.md
// promises field_hash for the case where "the document has a natural key and
// re-inserting it must be idempotent"; the ID is derived from that key, so
// resolving to an occupied ID is the strategy working, not data loss.
func TestCollectionService_InsertDocument_FieldHashReinsertStaysIdempotent(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newIncrementCollectionService()
	createCollection(t, svc, "mydb", "c", IDStrategyFieldHash, "name")

	first := insertDoc(t, svc, "mydb", "c", map[string]interface{}{"name": "bob", "v": float64(1)})
	second := insertDoc(t, svc, "mydb", "c", map[string]interface{}{"name": "bob", "v": float64(2)})
	require.Equal(t, first, second, "the same natural key must resolve to the same document id")

	got, err := svc.GetDocument(ctx, &GetDocumentRequest{Database: "mydb", Collection: "c", ID: first})
	require.NoError(t, err)
	assert.Equal(t, float64(2), got.Document["v"], "the re-insert must have replaced the document")

	list, err := svc.ListDocuments(ctx, &ListDocumentsRequest{Database: "mydb", Collection: "c"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), list.TotalCount)
}

// TestCollectionService_InsertDocument_GeneratedIDCollisionIsRefused proves
// the guard also covers a GENERATED id on a strategy that may not overwrite.
// It is reached by planting a document under the id the counter is about to
// hand out -- which is what the world looked like before this change, and
// what a restored backup or a hand-written key still looks like.
func TestCollectionService_InsertDocument_GeneratedIDCollisionIsRefused(t *testing.T) {
	ctx := context.Background()
	svc, kv, _ := newIncrementCollectionService()
	createCollection(t, svc, "mydb", "users", IDStrategyIncrement, "")

	docKey := docKeyForTest(t, svc, "mydb", "users", 1)
	require.NoError(t, kv.Put(ctx, docKey, []byte(`{"id":"1","who":"planted"}`)))

	_, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Database: "mydb", Collection: "users",
		Document: map[string]interface{}{"who": "auto"},
	})
	var exists *CollectionDocumentExistsError
	require.ErrorAs(t, err, &exists)
	assert.False(t, exists.Explicit, "the id came from the collection's allocator, not the caller")
	assert.Equal(t, uint64(1), exists.ID)

	got, err := svc.GetDocument(ctx, &GetDocumentRequest{Database: "mydb", Collection: "users", ID: 1})
	require.NoError(t, err)
	assert.Equal(t, "planted", got.Document["who"])
}

// TestCollectionService_InsertDocument_ExplicitIDInAFreshCollectionSucceeds
// is the other half of the guard: refusing a collision must not turn into
// refusing explicit IDs, which is the re-import workflow.
func TestCollectionService_InsertDocument_ExplicitIDInAFreshCollectionSucceeds(t *testing.T) {
	for _, tc := range []struct {
		strategy  string
		fieldHash string
	}{
		{IDStrategyUnixNano, ""},
		{IDStrategyIncrement, ""},
		{IDStrategyFieldHash, "name"},
	} {
		t.Run(tc.strategy, func(t *testing.T) {
			svc, _, _ := newIncrementCollectionService()
			createCollection(t, svc, "mydb", "c", tc.strategy, tc.fieldHash)
			assert.Equal(t, uint64(99), insertDoc(t, svc, "mydb", "c",
				map[string]interface{}{"_id": 99, "name": "n"}))
		})
	}
}

// ---------------------------------------------------------------------------
// mayOverwrite
// ---------------------------------------------------------------------------

func TestMayOverwrite(t *testing.T) {
	for _, tc := range []struct {
		name     string
		strategy string
		explicit bool
		want     bool
	}{
		{"field_hash generated is the documented idempotent re-insert", IDStrategyFieldHash, false, true},
		{"field_hash explicit is a claim, not a natural key", IDStrategyFieldHash, true, false},
		{"increment generated must never land on a live document", IDStrategyIncrement, false, false},
		{"increment explicit", IDStrategyIncrement, true, false},
		{"unix_nano generated collides only on a same-nanosecond insert", IDStrategyUnixNano, false, false},
		{"unix_nano explicit", IDStrategyUnixNano, true, false},
		{"an unknown strategy is never allowed to overwrite", "no-such-strategy", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, mayOverwrite(tc.strategy, tc.explicit))
		})
	}
}

// ---------------------------------------------------------------------------
// reserveExplicitDocumentID
// ---------------------------------------------------------------------------

// TestCollectionService_ReserveExplicitDocumentID_RaisesTheReplicatedCounter
// asserts the effect on the sequence itself rather than on a later insert,
// so a regression names the counter instead of the symptom.
func TestCollectionService_ReserveExplicitDocumentID_RaisesTheReplicatedCounter(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	seqStore := newFakeCatalogStore()
	seq := NewSequenceService(seqStore, seqStore)
	svc := NewCollectionService(kv).WithSequenceService(seq)

	info := &CollectionInfo{Database: "mydb", Name: "users", IDStrategy: IDStrategyIncrement}
	require.NoError(t, svc.reserveExplicitDocumentID(ctx, info, 40))

	current, err := seq.Peek(ctx, "mydb", collectionSequenceName(info))
	require.NoError(t, err)
	assert.Equal(t, uint64(40), current)

	// Advance is defined to be a no-op once the counter is at or past the
	// target, so a lower explicit id must not lower the watermark.
	require.NoError(t, svc.reserveExplicitDocumentID(ctx, info, 7))
	current, err = seq.Peek(ctx, "mydb", collectionSequenceName(info))
	require.NoError(t, err)
	assert.Equal(t, uint64(40), current, "an explicit id below the watermark must not lower it")
}

// TestCollectionService_ReserveExplicitDocumentID_NoCounterToReserve covers
// the strategies whose IDs are not drawn from a counter. They must reserve
// nothing at all -- not "reserve zero", nothing: a sequence key created for
// a unix_nano collection would be a leak with no owner.
func TestCollectionService_ReserveExplicitDocumentID_NoCounterToReserve(t *testing.T) {
	for _, strategy := range []string{IDStrategyUnixNano, IDStrategyFieldHash, "no-such-strategy"} {
		t.Run(strategy, func(t *testing.T) {
			ctx := context.Background()
			kv := newMemoryKVSvc()
			seqStore := newFakeCatalogStore()
			svc := NewCollectionService(kv).WithSequenceService(NewSequenceService(seqStore, seqStore))

			info := &CollectionInfo{Database: "mydb", Name: "c", IDStrategy: strategy, FieldHash: "name"}
			require.NoError(t, svc.reserveExplicitDocumentID(ctx, info, 500))

			_, err := seqStore.Get(ctx, tableSequenceKey("mydb", collectionSequenceName(info)))
			var notFound *KeyNotFoundError
			assert.ErrorAs(t, err, &notFound, "a strategy with no counter must not create a sequence key")
		})
	}
}

// TestCollectionService_ReserveExplicitDocumentID_SequenceFailureFailsTheInsert
// is the failure direction: if the counter cannot be raised, the insert must
// not proceed. Proceeding would store the document under an id the collection
// is still free to hand out, which is the defect itself.
func TestCollectionService_ReserveExplicitDocumentID_SequenceFailureFailsTheInsert(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	seqStore := newFakeCatalogStore()
	svc := NewCollectionService(kv).WithSequenceService(NewSequenceService(seqStore, seqStore))
	createCollection(t, svc, "mydb", "users", IDStrategyIncrement, "")

	seqStore.injectGetRevisionErr = assert.AnError
	_, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Database: "mydb", Collection: "users",
		Document: map[string]interface{}{"_id": 9, "who": "explicit"},
	})
	var metaErr *CollectionMetadataError
	require.ErrorAs(t, err, &metaErr)
	assert.Equal(t, "reserve-id", metaErr.Op)

	_, getErr := svc.GetDocument(ctx, &GetDocumentRequest{Database: "mydb", Collection: "users", ID: 9})
	var notFound *CollectionDocumentNotFoundError
	assert.ErrorAs(t, getErr, &notFound, "an insert whose reservation failed must not have stored anything")
}

// ---------------------------------------------------------------------------
// storeNewDocument
// ---------------------------------------------------------------------------

// failingGuardedKVSvc is the in-memory KV service with one key's guarded write
// replaced by a transport-shaped failure. It embeds the real double rather
// than reimplementing it, so every other verb behaves exactly as production's
// caller sees it.
type failingGuardedKVSvc struct {
	*memoryKVSvc
	failKey string
	err     error
}

func (f *failingGuardedKVSvc) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if key == f.failKey {
		return false, f.err
	}
	return f.memoryKVSvc.PutIfAbsent(ctx, key, value)
}

// TestCollectionService_StoreNewDocument_GuardedWhenOverwriteRefused proves
// the refusal is the backend declining to store rather than a check this
// service performed: the document is written with PutIfAbsent, and a second
// claim on the same ID is refused by the write itself.
func TestCollectionService_StoreNewDocument_GuardedWhenOverwriteRefused(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	svc := NewCollectionService(kv)
	createCollection(t, svc, "mydb", "users", IDStrategyIncrement, "")
	info, err := svc.loadCollectionInfo(ctx, "mydb", "users")
	require.NoError(t, err)
	docKey := collectionDocumentKey(info, 1)
	req := &InsertDocumentRequest{Database: "mydb", Collection: "users"}
	c := svc.resolveCodec(info)

	resp, err := svc.storeNewDocument(ctx, info, req, docKey, []byte(`{"id":"1","who":"first"}`), 1, true, c)
	require.NoError(t, err)
	assert.False(t, resp.Replaced)

	_, err = svc.storeNewDocument(ctx, info, req, docKey, []byte(`{"id":"1","who":"second"}`), 1, true, c)
	var exists *CollectionDocumentExistsError
	require.ErrorAs(t, err, &exists)
	assert.Equal(t, uint64(1), exists.ID)
	assert.True(t, exists.Explicit)

	stored, err := kv.Get(ctx, docKey)
	require.NoError(t, err)
	assert.Contains(t, string(stored), "first", "the refused claim must not have replaced the stored document")
}

// TestCollectionService_StoreNewDocument_OverwriteAllowedIsGuardedUpsert
// covers the other branch: a field_hash re-insert is the documented
// idempotent update, written with the guarded upsert so the insert reports
// exactly the document it displaced (go-qrdb#456 B-4). It is synchronous, so a
// requested WriteMode changes nothing about it.
func TestCollectionService_StoreNewDocument_OverwriteAllowedIsGuardedUpsert(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	svc := NewCollectionService(kv)
	createCollection(t, svc, "mydb", "hashed", IDStrategyFieldHash, "email")
	info, err := svc.loadCollectionInfo(ctx, "mydb", "hashed")
	require.NoError(t, err)
	docKey := collectionDocumentKey(info, 1)
	c := svc.resolveCodec(info)
	encode := func(who string) []byte {
		data, merr := marshalDocument(&dao.DynamicEntity{ID: 1, Fields: map[string]interface{}{"who": who}}, c)
		require.NoError(t, merr)
		return data
	}

	req := &InsertDocumentRequest{Database: "mydb", Collection: "hashed"}
	first, err := svc.storeNewDocument(ctx, info, req, docKey, encode("first"), 1, false, c)
	require.NoError(t, err)
	assert.False(t, first.Replaced, "the first insert of a natural key stores a new document")
	assert.Nil(t, first.Previous)

	second, err := svc.storeNewDocument(ctx, info, req, docKey, encode("second"), 1, false, c)
	require.NoError(t, err)
	assert.True(t, second.Replaced)
	assert.Equal(t, "first", second.Previous["who"], "the displaced document is reported exactly")

	modeReq := &InsertDocumentRequest{Database: "mydb", Collection: "hashed", WriteMode: writemode.WALDurable}
	third, err := svc.storeNewDocument(ctx, info, modeReq, docKey, encode("third"), 1, false, c)
	require.NoError(t, err)
	assert.Equal(t, "second", third.Previous["who"])

	stored, err := kv.Get(ctx, docKey)
	require.NoError(t, err)
	assert.Contains(t, string(stored), "third")
}

// TestCollectionService_StoreNewDocument_OverwriteWriteFailuresAreTyped: an
// idempotent re-insert whose guarded write fails surfaces as a typed upsert
// failure -- one whose outcome is unknown, so a quota keeps its reservation --
// rather than a partially applied insert reporting success.
func TestCollectionService_StoreNewDocument_OverwriteWriteFailuresAreTyped(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	failing := &failingGuardedKVSvc{memoryKVSvc: kv, err: assert.AnError}
	svc := NewCollectionService(failing)
	createCollection(t, svc, "mydb", "hashed", IDStrategyFieldHash, "email")
	info, err := svc.loadCollectionInfo(ctx, "mydb", "hashed")
	require.NoError(t, err)
	docKey := collectionDocumentKey(info, 1)
	failing.failKey = docKey

	_, err = svc.storeNewDocument(ctx, info, &InsertDocumentRequest{Database: "mydb", Collection: "hashed"}, docKey, []byte(`{"who":"first"}`), 1, false, svc.resolveCodec(info))
	var metaErr *CollectionMetadataError
	require.ErrorAs(t, err, &metaErr)
	assert.Equal(t, "upsert-document", metaErr.Op)
	assert.True(t, metaErr.OutcomeUnknown())
}

// TestCollectionService_StoreNewDocument_WriteFailureIsNotAbsence is the
// property that makes the guard worth having: a guarded write that FAILS is
// never read as "no document here", because that would wave through exactly
// the overwrite the guard exists to prevent.
func TestCollectionService_StoreNewDocument_WriteFailureIsNotAbsence(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	failing := &failingGuardedKVSvc{memoryKVSvc: kv, err: assert.AnError}
	svc := NewCollectionService(failing)

	createCollection(t, svc, "mydb", "users", IDStrategyIncrement, "")
	docKey := docKeyForTest(t, svc, "mydb", "users", 1)
	failing.failKey = docKey
	require.NoError(t, kv.Put(ctx, docKey, []byte(`{"id":"1","who":"stored"}`)))

	_, insertErr := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Database: "mydb", Collection: "users",
		Document: map[string]interface{}{"_id": 1, "who": "overwriter"},
	})
	var metaErr *CollectionMetadataError
	require.ErrorAs(t, insertErr, &metaErr)
	assert.Equal(t, "insert-document", metaErr.Op)

	stored, err := kv.Get(ctx, docKey)
	require.NoError(t, err)
	assert.Contains(t, string(stored), "stored", "the document must be untouched")
}

// ---------------------------------------------------------------------------
// CollectionDocumentExistsError
// ---------------------------------------------------------------------------

func TestCollectionDocumentExistsError_Error(t *testing.T) {
	explicit := &CollectionDocumentExistsError{Database: "mydb", Collection: "users", ID: 7, Explicit: true}
	assert.Equal(t, "service: document already exists: mydb/users/7 (explicit id)", explicit.Error())

	generated := &CollectionDocumentExistsError{Database: "mydb", Collection: "users", ID: 7}
	assert.Equal(t, "service: document already exists: mydb/users/7 (generated id)", generated.Error())
}
