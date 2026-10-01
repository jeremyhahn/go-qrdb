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
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file covers collection teardown of the replicated ID sequence
// (go-qrdb#411). CollectionService and SequenceService are backed by
// separate stores here because they consume different interfaces --
// kvService and sequenceKVService/TransactionServicer -- and they address
// disjoint keyspaces in production too (__sys:collections/ versus __seq:),
// so the split loses nothing the tests need.

// newIncrementCollectionService returns a CollectionService wired exactly
// the way production wires it for IDStrategyIncrement (see
// pkg/server/tenant_host.go and pkg/server/lifecycle), together with the
// document store and the sequence store so a test can inspect both.
func newIncrementCollectionService() (*CollectionService, *memoryKVSvc, *fakeCatalogStore) {
	kv := newMemoryKVSvc()
	seqStore := newFakeCatalogStore()
	svc := NewCollectionService(kv).WithSequenceService(NewSequenceService(seqStore, seqStore))
	return svc, kv, seqStore
}

// createIncrementCollection creates an IDStrategyIncrement collection and
// inserts n auto-ID documents into it, returning the IDs it was handed.
func createIncrementCollection(t *testing.T, svc *CollectionService, database, name string, n int) []uint64 {
	t.Helper()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Database:   database,
		Name:       name,
		IDStrategy: IDStrategyIncrement,
	})
	require.NoError(t, err)

	ids := make([]uint64, 0, n)
	for i := 0; i < n; i++ {
		resp, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
			Database:   database,
			Collection: name,
			Document:   map[string]interface{}{"n": i},
		})
		require.NoError(t, err)
		ids = append(ids, resp.ID)
	}
	return ids
}

// TestCollectionService_DropCollection_ReclaimsTheIncrementSequence is the
// failing-first test for go-qrdb#411: before the fix the counter key
// survived the drop, and nothing in the system would ever name it again.
func TestCollectionService_DropCollection_ReclaimsTheIncrementSequence(t *testing.T) {
	ctx := context.Background()
	svc, _, seqStore := newIncrementCollectionService()

	createIncrementCollection(t, svc, "mydb", "users", 3)
	seqKey := tableSequenceKey("mydb", seqNameForTest(t, svc, "mydb", "users"))
	_, err := seqStore.Get(ctx, seqKey)
	require.NoError(t, err, "inserting with IDStrategyIncrement must have created the counter key")

	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "users"}))

	_, err = seqStore.Get(ctx, seqKey)
	var notFound *KeyNotFoundError
	require.ErrorAs(t, err, &notFound, "dropping a collection must reclaim its replicated ID sequence, not leave a key per dropped collection behind forever")
}

// TestCollectionService_DropCollection_RecreatedNameRestartsIDsAtOne is the
// name-reuse question, answered by measurement.
//
// BEFORE the fix this collection's first document was ID 4: the counter
// survived the drop and the recreated collection inherited its high-water
// mark, so `increment` delivered neither of the two properties
// docs/collections/architecture.md promises it ("dense, small, predictable
// IDs"). It RESUMED -- it did not reset -- so no ID was ever reissued and
// this was never a collision defect, only a broken promise plus the leak.
func TestCollectionService_DropCollection_RecreatedNameRestartsIDsAtOne(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newIncrementCollectionService()

	first := createIncrementCollection(t, svc, "mydb", "users", 3)
	require.Equal(t, []uint64{1, 2, 3}, first)

	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "users"}))

	second := createIncrementCollection(t, svc, "mydb", "users", 3)
	assert.Equal(t, []uint64{1, 2, 3}, second, "a collection recreated under a dropped name must not inherit the dropped collection's counter")
}

// TestCollectionService_DropCollection_IncrementAgreesAcrossBothWirings
// pins the divergence the leak produced. IDStrategyIncrement has two
// implementations -- the replicated SequenceServicer (production) and the
// process-local fallback -- and before the fix they disagreed about what a
// recreated name does: the fallback restarted at 1 because DropCollection
// drops its cache entry, while the replicated path resumed. The only
// wiring that matched the documented behaviour was the one that cannot
// deliver unique IDs across a restart or across replicas.
func TestCollectionService_DropCollection_IncrementAgreesAcrossBothWirings(t *testing.T) {
	ctx := context.Background()

	replicated, _, _ := newIncrementCollectionService()
	createIncrementCollection(t, replicated, "mydb", "users", 3)
	require.NoError(t, replicated.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "users"}))
	replicatedIDs := createIncrementCollection(t, replicated, "mydb", "users", 2)

	fallback := NewCollectionService(newMemoryKVSvc())
	createIncrementCollection(t, fallback, "mydb", "users", 3)
	require.NoError(t, fallback.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "users"}))
	fallbackIDs := createIncrementCollection(t, fallback, "mydb", "users", 2)

	assert.Equal(t, fallbackIDs, replicatedIDs, "the replicated and process-local implementations of IDStrategyIncrement must agree on what a recreated collection does")
}

// TestCollectionService_DropCollection_DocumentsGoBeforeTheSequence is the
// ordering that matters most: a counter restarted while documents still
// exist would reissue an ID a live document already holds and silently
// overwrite it. The recording servicer asserts the collection is already
// empty at the moment its sequence is deleted.
func TestCollectionService_DropCollection_DocumentsGoBeforeTheSequence(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	seqStore := newFakeCatalogStore()

	var docPrefix string
	rec := &recordingSequenceService{
		SequenceServicer: NewSequenceService(seqStore, seqStore),
		onDelete: func() {
			keys, err := kv.List(ctx, docPrefix)
			require.NoError(t, err)
			assert.Empty(t, keys, "every document must be deleted before the sequence is, or a restarted counter can collide with a live document")
		},
	}
	svc := NewCollectionService(kv).WithSequenceService(rec)

	createIncrementCollection(t, svc, "mydb", "users", 4)
	docPrefix = docPrefixForTest(t, svc, "mydb", "users")
	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "users"}))
	assert.Equal(t, 1, rec.deletes())
}

// TestCollectionService_DropCollection_SequenceGoesBeforeTheMetadata is the
// other half of the ordering: the metadata is what makes the collection
// resolvable, so deleting it first would strand the counter where no
// re-issued drop could reach it (the retry would fail with
// CollectionNotFoundError before getting this far).
func TestCollectionService_DropCollection_SequenceGoesBeforeTheMetadata(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	seqStore := newFakeCatalogStore()

	rec := &recordingSequenceService{
		SequenceServicer: NewSequenceService(seqStore, seqStore),
		onDelete: func() {
			raw, err := kv.Get(ctx, collectionMetadataKey("mydb", "users"))
			require.NoError(t, err, "the metadata must outlive the sequence delete so an interrupted drop stays retryable")
			assert.NotEmpty(t, raw)
		},
	}
	svc := NewCollectionService(kv).WithSequenceService(rec)

	createIncrementCollection(t, svc, "mydb", "users", 1)
	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "users"}))
	assert.Equal(t, 1, rec.deletes())
}

// TestCollectionService_DropCollection_SequenceDeleteFailureFailsTheDrop
// pins the choice not to skip past a sequence that cannot be reclaimed:
// reporting success over an unreclaimed counter is the defect itself, and
// the surviving metadata is what makes the retry possible.
func TestCollectionService_DropCollection_SequenceDeleteFailureFailsTheDrop(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	seqStore := newFakeCatalogStore()
	sentinel := errors.New("sequence store unavailable")

	rec := &recordingSequenceService{
		SequenceServicer: NewSequenceService(seqStore, seqStore),
		deleteErr:        sentinel,
	}
	svc := NewCollectionService(kv).WithSequenceService(rec)

	createIncrementCollection(t, svc, "mydb", "users", 2)

	err := svc.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "users"})
	var metaErr *CollectionMetadataError
	require.ErrorAs(t, err, &metaErr)
	assert.Equal(t, "delete-sequence", metaErr.Op)
	require.ErrorIs(t, err, sentinel)

	raw, err := kv.Get(ctx, collectionMetadataKey("mydb", "users"))
	require.NoError(t, err, "a failed drop must leave the metadata so the operation is retryable")
	assert.NotEmpty(t, raw)
}

// TestCollectionService_DropCollection_RetryAfterSequenceFailureCompletes
// is the point of leaving the metadata behind: re-issuing the drop finishes
// it, which only works because deleting an already-absent sequence is
// success.
func TestCollectionService_DropCollection_RetryAfterSequenceFailureCompletes(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	seqStore := newFakeCatalogStore()

	rec := &recordingSequenceService{
		SequenceServicer: NewSequenceService(seqStore, seqStore),
		deleteErr:        errors.New("transient"),
		deleteErrOnce:    true,
	}
	svc := NewCollectionService(kv).WithSequenceService(rec)

	createIncrementCollection(t, svc, "mydb", "users", 2)
	seqName := seqNameForTest(t, svc, "mydb", "users")
	require.Error(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "users"}))
	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "users"}))

	_, err := kv.Get(ctx, collectionMetadataKey("mydb", "users"))
	var notFound *KeyNotFoundError
	require.ErrorAs(t, err, &notFound)

	_, err = seqStore.Get(ctx, tableSequenceKey("mydb", seqName))
	require.ErrorAs(t, err, &notFound)
}

// TestCollectionService_DropCollection_SiblingCollectionSequencesSurvive
// guards the sequence name's collection scoping: dropping one collection
// must not disturb another's counter, in the same database or another one.
func TestCollectionService_DropCollection_SiblingCollectionSequencesSurvive(t *testing.T) {
	ctx := context.Background()
	svc, _, seqStore := newIncrementCollectionService()
	seq := NewSequenceService(seqStore, seqStore)

	createIncrementCollection(t, svc, "mydb", "users", 3)
	createIncrementCollection(t, svc, "mydb", "orders", 5)
	createIncrementCollection(t, svc, "otherdb", "users", 7)

	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "users"}))

	orders, err := seq.Peek(ctx, "mydb", seqNameForTest(t, svc, "mydb", "orders"))
	require.NoError(t, err)
	assert.Equal(t, uint64(5), orders)

	otherUsers, err := seq.Peek(ctx, "otherdb", seqNameForTest(t, svc, "otherdb", "users"))
	require.NoError(t, err)
	assert.Equal(t, uint64(7), otherUsers)
}

// TestCollectionService_DropCollection_NonIncrementCollectionProposesNothing
// pins the cost of issuing the delete unconditionally rather than reading
// the strategy out of the metadata: a collection that never allocated a
// counter costs one read and proposes nothing.
func TestCollectionService_DropCollection_NonIncrementCollectionProposesNothing(t *testing.T) {
	ctx := context.Background()
	svc, _, seqStore := newIncrementCollectionService()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Database: "mydb", Name: "events"})
	require.NoError(t, err)
	_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{
		Database: "mydb", Collection: "events", Document: map[string]interface{}{"a": 1},
	})
	require.NoError(t, err)

	before := seqStore.executeCallsSnapshot()
	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "events"}))
	assert.Equal(t, before, seqStore.executeCallsSnapshot())
}

// TestCollectionService_DropCollection_WithoutSequenceServiceIsUnchanged
// covers the nil-seqSvc branch: a service built without WithSequenceService
// drops exactly what it always did.
func TestCollectionService_DropCollection_WithoutSequenceServiceIsUnchanged(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	svc := NewCollectionService(kv)

	createIncrementCollection(t, svc, "mydb", "users", 2)
	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "users"}))

	_, err := kv.Get(ctx, collectionMetadataKey("mydb", "users"))
	var notFound *KeyNotFoundError
	require.ErrorAs(t, err, &notFound)
}

// TestCollectionService_DropCollection_MissingCollectionNeverTouchesSequence
// keeps the not-found guard ahead of every reclamation step, so a typo can
// never reclaim a live collection's counter.
func TestCollectionService_DropCollection_MissingCollectionNeverTouchesSequence(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	seqStore := newFakeCatalogStore()
	rec := &recordingSequenceService{SequenceServicer: NewSequenceService(seqStore, seqStore)}
	svc := NewCollectionService(kv).WithSequenceService(rec)

	err := svc.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "absent"})
	var notFound *CollectionNotFoundError
	require.ErrorAs(t, err, &notFound)
	assert.Zero(t, rec.deletes())
}

// TestCollectionService_DropCollection_DocumentFailureLeavesTheSequence is
// the failure-boundary proof of the documents-before-sequence ordering: a
// drop that cannot reclaim every document must not have restarted the
// counter, or the retry would insert over a document that is still there.
func TestCollectionService_DropCollection_DocumentFailureLeavesTheSequence(t *testing.T) {
	ctx := context.Background()
	kv := &failingKVSvc{memoryKVSvc: newMemoryKVSvc()}
	seqStore := newFakeCatalogStore()
	rec := &recordingSequenceService{SequenceServicer: NewSequenceService(seqStore, seqStore)}
	svc := NewCollectionService(kv).WithSequenceService(rec)

	createIncrementCollection(t, svc, "mydb", "users", 3)
	kv.deleteErr = errors.New("storage unavailable")

	err := svc.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "users"})
	var metaErr *CollectionMetadataError
	require.ErrorAs(t, err, &metaErr)
	assert.Equal(t, "delete-document", metaErr.Op)
	assert.Zero(t, rec.deletes(), "the sequence must not be reclaimed while documents survive")

	current, err := NewSequenceService(seqStore, seqStore).Peek(ctx, "mydb", seqNameForTest(t, svc, "mydb", "users"))
	require.NoError(t, err)
	assert.Equal(t, uint64(3), current, "the counter must still be past every surviving document's ID")
}

// TestCollectionService_DropCollection_ListFailureIsTyped covers the
// enumeration step: a drop that cannot even list the documents reports that
// rather than proceeding to reclaim anything.
func TestCollectionService_DropCollection_ListFailureIsTyped(t *testing.T) {
	ctx := context.Background()
	kv := &failingKVSvc{memoryKVSvc: newMemoryKVSvc()}
	seqStore := newFakeCatalogStore()
	rec := &recordingSequenceService{SequenceServicer: NewSequenceService(seqStore, seqStore)}
	svc := NewCollectionService(kv).WithSequenceService(rec)

	createIncrementCollection(t, svc, "mydb", "users", 1)
	kv.listErr = errors.New("storage unavailable")

	err := svc.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "users"})
	var metaErr *CollectionMetadataError
	require.ErrorAs(t, err, &metaErr)
	assert.Equal(t, "list-documents", metaErr.Op)
	assert.Zero(t, rec.deletes())
}

// TestCollectionService_DropCollection_MetadataFailureIsTyped covers the
// last step. The sequence is already gone at this point, which is exactly
// the state the chosen ordering accepts: a live but EMPTY collection whose
// counter has restarted, with nothing left to collide with.
func TestCollectionService_DropCollection_MetadataFailureIsTyped(t *testing.T) {
	ctx := context.Background()
	kv := &failingKVSvc{memoryKVSvc: newMemoryKVSvc()}
	seqStore := newFakeCatalogStore()
	svc := NewCollectionService(kv).WithSequenceService(NewSequenceService(seqStore, seqStore))

	createIncrementCollection(t, svc, "mydb", "users", 0)
	kv.deleteErr = errors.New("storage unavailable")

	err := svc.DropCollection(ctx, &DropCollectionRequest{Database: "mydb", Name: "users"})
	var metaErr *CollectionMetadataError
	require.ErrorAs(t, err, &metaErr)
	assert.Equal(t, "delete-metadata", metaErr.Op)

	keys, err := kv.List(ctx, docPrefixForTest(t, svc, "mydb", "users"))
	require.NoError(t, err)
	assert.Empty(t, keys, "the collection the retry will finish dropping is empty, so a restarted counter collides with nothing")
}

// ---------------------------------------------------------------------------
// failingKVSvc
// ---------------------------------------------------------------------------

// failingKVSvc wraps memoryKVSvc, turning Delete and List into injectable
// failures so DropCollection's error branches can be reached. Every other
// method is promoted from the embedded store.
type failingKVSvc struct {
	*memoryKVSvc

	deleteErr error
	listErr   error
}

// Compile-time interface check, beside the type, so the double cannot
// silently stop satisfying the interface it stands in for.
var _ kvService = (*failingKVSvc)(nil)

func (f *failingKVSvc) Delete(ctx context.Context, key string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return f.memoryKVSvc.Delete(ctx, key)
}

func (f *failingKVSvc) List(ctx context.Context, prefix string) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.memoryKVSvc.List(ctx, prefix)
}

// Scan fails like List for a document prefix -- the teardown enumerates
// documents with it -- and passes through for the metadata scans the sweeper
// and ListCollections issue.
func (f *failingKVSvc) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	if f.listErr != nil && strings.HasPrefix(prefix, collectionDataPrefix) {
		return nil, f.listErr
	}
	return f.memoryKVSvc.Scan(ctx, prefix)
}

// CompareAndDelete fails like Delete: the teardown removes documents and the
// metadata with it.
func (f *failingKVSvc) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if f.deleteErr != nil {
		return false, f.deleteErr
	}
	return f.memoryKVSvc.CompareAndDelete(ctx, key, expected)
}

// ---------------------------------------------------------------------------
// recordingSequenceService
// ---------------------------------------------------------------------------

// recordingSequenceService wraps a real SequenceServicer, counting Delete
// calls, running a hook at the moment of each one so a test can observe the
// surrounding state, and optionally failing them.
type recordingSequenceService struct {
	SequenceServicer

	mu        sync.Mutex
	onDelete  func()
	deleteErr error
	// deleteErrOnce makes deleteErr fire on the first Delete only, so a
	// test can exercise the retry that follows a transient failure.
	deleteErrOnce bool
	deleteCalls   int
}

// Compile-time interface check, beside the type, so the double cannot
// silently stop satisfying the interface it stands in for.
var _ SequenceServicer = (*recordingSequenceService)(nil)

func (r *recordingSequenceService) Delete(ctx context.Context, database, name string) error {
	r.mu.Lock()
	r.deleteCalls++
	hook := r.onDelete
	err := r.deleteErr
	if err != nil && r.deleteErrOnce {
		r.deleteErr = nil
	}
	r.mu.Unlock()

	if hook != nil {
		hook()
	}
	if err != nil {
		return err
	}
	return r.SequenceServicer.Delete(ctx, database, name)
}

func (r *recordingSequenceService) deletes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.deleteCalls
}
