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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Fault injection for the collection lifecycle.
//
// Every guarded write the lifecycle performs has three outcomes -- stored,
// refused, unknown -- and each is handled differently. These tests reach the
// refused and unknown branches the happy-path tests cannot.
// ---------------------------------------------------------------------------

// errStorage is the fault these tests inject.
var errStorage = errors.New("storage unavailable")

// faultingKVSvc is the faithful in-memory KV with per-operation fault hooks.
// A hook returning (handled=true) replaces the operation's result; otherwise
// the real operation runs.
type faultingKVSvc struct {
	*memoryKVSvc
	onCAS func(key string) (handled, swapped bool, err error)
	onCAD func(key string) (handled, deleted bool, err error)
	onPIA func(key string) (handled, stored bool, err error)
	onGet func(key string) (handled bool, value []byte, err error)
	onDel func(key string) error
}

var _ kvService = (*faultingKVSvc)(nil)

func newFaultingKVSvc() *faultingKVSvc {
	return &faultingKVSvc{memoryKVSvc: newResolvedKVSvc()}
}

func (f *faultingKVSvc) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if f.onCAS != nil {
		if handled, swapped, err := f.onCAS(key); handled {
			return swapped, err
		}
	}
	return f.memoryKVSvc.CompareAndSwap(ctx, key, expected, next)
}

func (f *faultingKVSvc) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if f.onCAD != nil {
		if handled, deleted, err := f.onCAD(key); handled {
			return deleted, err
		}
	}
	return f.memoryKVSvc.CompareAndDelete(ctx, key, expected)
}

func (f *faultingKVSvc) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if f.onPIA != nil {
		if handled, stored, err := f.onPIA(key); handled {
			return stored, err
		}
	}
	return f.memoryKVSvc.PutIfAbsent(ctx, key, value)
}

func (f *faultingKVSvc) Get(ctx context.Context, key string) ([]byte, error) {
	if f.onGet != nil {
		if handled, value, err := f.onGet(key); handled {
			return value, err
		}
	}
	return f.memoryKVSvc.Get(ctx, key)
}

func (f *faultingKVSvc) Delete(ctx context.Context, key string) error {
	if f.onDel != nil {
		if err := f.onDel(key); err != nil {
			return err
		}
	}
	return f.memoryKVSvc.Delete(ctx, key)
}

func isMeta(key string) bool { return strings.HasPrefix(key, collectionMetadataPrefix) }
func isDoc(key string) bool  { return strings.HasPrefix(key, collectionDataPrefix) }

func metadataOp(t *testing.T, err error) string {
	t.Helper()
	var meta *CollectionMetadataError
	require.ErrorAs(t, err, &meta)
	return meta.Op
}

// failingSecondIndex builds a service whose index service fails its second
// create, over kv.
func failingSecondIndex(kv kvService) (*CollectionService, *nthCreateFailIndexSvc) {
	idx := &nthCreateFailIndexSvc{memoryIndexSvc: newMemoryIndexSvc(), failOn: 2, err: errors.New("boom")}
	return NewCollectionServiceWithIndex(kv, idx), idx
}

var twoIndexes = &CreateCollectionRequest{
	Name: "users", Database: "mydb", Indexes: []IndexDefinition{{FieldName: "a"}, {FieldName: "b"}},
}

func TestCreateCollection_UnwindFaults(t *testing.T) {
	ctx := context.Background()

	t.Run("mark fails: the collection is left to the sweeper", func(t *testing.T) {
		kv := newFaultingKVSvc()
		kv.onCAS = func(key string) (bool, bool, error) { return isMeta(key), false, errStorage }
		svc, _ := failingSecondIndex(kv)
		_, err := svc.CreateCollection(ctx, twoIndexes)
		assert.Equal(t, "create-index", metadataOp(t, err))
		assert.ErrorIs(t, err, errStorage, "the unwind's failure is joined to the cause")
		info, gerr := svc.GetCollectionInfo(ctx, "mydb", "users")
		require.NoError(t, gerr)
		assert.Equal(t, CollectionStateCreating, info.State)
	})

	t.Run("record already moved on: this incarnation removes its own indexes", func(t *testing.T) {
		kv := newFaultingKVSvc()
		kv.onCAS = func(key string) (bool, bool, error) { return isMeta(key), false, nil }
		svc, idx := failingSecondIndex(kv)
		_, err := svc.CreateCollection(ctx, twoIndexes)
		assert.Equal(t, "create-index", metadataOp(t, err))
		assert.NotErrorIs(t, err, errStorage)
		// Index "a" was built before "b" failed. Whoever moved the record on
		// may have torn down before "a" existed, so the create removes it.
		_, exists := idx.ownerOf("mydb.users.a")
		assert.False(t, exists, "the index this create built must not survive it")
		ledger, lerr := kv.memoryKVSvc.Scan(ctx, collectionIndexLedgerPrefix)
		require.NoError(t, lerr)
		assert.Empty(t, ledger, "its index ledger entry goes with the indexes")
	})

	t.Run("record already moved on and the index drop fails: joined", func(t *testing.T) {
		kv := newFaultingKVSvc()
		kv.onCAS = func(key string) (bool, bool, error) { return isMeta(key), false, nil }
		idx := &nthCreateFailIndexSvc{memoryIndexSvc: newMemoryIndexSvc(), failOn: 2, err: errors.New("boom")}
		svc := NewCollectionServiceWithIndex(kv, &dropFailIndexSvc{nthCreateFailIndexSvc: idx})
		_, err := svc.CreateCollection(ctx, twoIndexes)
		assert.Equal(t, "create-index", metadataOp(t, err))
		assert.ErrorIs(t, err, errStorage)
	})

	t.Run("teardown fails: joined and left dropping", func(t *testing.T) {
		kv := newFaultingKVSvc()
		kv.onCAD = func(key string) (bool, bool, error) { return isMeta(key), false, errStorage }
		svc, _ := failingSecondIndex(kv)
		_, err := svc.CreateCollection(ctx, twoIndexes)
		assert.ErrorIs(t, err, errStorage)
		info, gerr := svc.GetCollectionInfo(ctx, "mydb", "users")
		require.NoError(t, gerr)
		assert.Equal(t, CollectionStateDropping, info.State)
	})
}

func TestCreateCollection_WriteFaults(t *testing.T) {
	ctx := context.Background()

	kv := newFaultingKVSvc()
	kv.onPIA = func(key string) (bool, bool, error) { return isMeta(key), false, errStorage }
	_, err := NewCollectionService(kv).CreateCollection(ctx, &CreateCollectionRequest{Name: "c", Database: "db"})
	assert.Equal(t, "save", metadataOp(t, err))

	kv = newFaultingKVSvc()
	kv.onCAS = func(key string) (bool, bool, error) { return isMeta(key), false, errStorage }
	_, err = NewCollectionService(kv).CreateCollection(ctx, &CreateCollectionRequest{Name: "c", Database: "db"})
	assert.Equal(t, "activate", metadataOp(t, err))
}

func TestDropCollection_MarkFaults(t *testing.T) {
	ctx := context.Background()

	kv := newFaultingKVSvc()
	svc := NewCollectionService(kv)
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Name: "c", Database: "db"})
	require.NoError(t, err)

	kv.onCAS = func(key string) (bool, bool, error) { return isMeta(key), false, errStorage }
	assert.Equal(t, "mark-dropping", metadataOp(t, svc.DropCollection(ctx, &DropCollectionRequest{Name: "c", Database: "db"})))

	// A mark that loses every swap is a conflict, reported, not looped on.
	kv.onCAS = func(key string) (bool, bool, error) { return isMeta(key), false, nil }
	var conflict *CollectionStateConflictError
	require.ErrorAs(t, svc.DropCollection(ctx, &DropCollectionRequest{Name: "c", Database: "db"}), &conflict)
	assert.Equal(t, "mark-dropping", conflict.Op)
}

func TestSweepStuckCollections_Faults(t *testing.T) {
	ctx := context.Background()
	kv := newFaultingKVSvc()
	svc := NewCollectionService(kv)
	putMetadata(t, kv, &CollectionInfo{Name: "stuck", Database: "db", State: CollectionStateCreating, StateSince: time.Now().Add(-time.Hour)})
	require.NoError(t, kv.Put(ctx, collectionMetadataKey("db", "garbled"), []byte("{not json")))

	// The mark that unwinds a stuck create fails: reported, the pass continues.
	kv.onCAS = func(key string) (bool, bool, error) { return isMeta(key), false, errStorage }
	res, err := svc.SweepStuckCollections(ctx, "db", time.Minute)
	assert.Equal(t, 0, res.Swept)
	assert.Equal(t, "mark-dropping", metadataOp(t, err))

	// The record moved on since the scan: skipped, no failure.
	kv.onCAS = func(key string) (bool, bool, error) { return isMeta(key), false, nil }
	res, err = svc.SweepStuckCollections(ctx, "db", time.Minute)
	assert.Equal(t, 0, res.Swept)
	assert.NoError(t, err)

	// The scan itself fails.
	scanFail := &scanFailKV{memoryKVSvc: newResolvedKVSvc()}
	_, err = NewCollectionService(scanFail).SweepStuckCollections(ctx, "db", time.Minute)
	assert.Equal(t, "list", metadataOp(t, err))
}

// scanFailKV fails every Scan.
type scanFailKV struct{ *memoryKVSvc }

func (s *scanFailKV) Scan(context.Context, string) (map[string][]byte, error) {
	return nil, errStorage
}

func TestUpsertDocument_Faults(t *testing.T) {
	ctx := context.Background()
	setup := func() (*faultingKVSvc, *CollectionService) {
		kv := newFaultingKVSvc()
		svc := NewCollectionService(kv)
		_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Name: "c", Database: "db"})
		require.NoError(t, err)
		return kv, svc
	}
	req := func() *UpsertDocumentRequest {
		return &UpsertDocumentRequest{Database: "db", Collection: "c", ID: 9, Document: map[string]interface{}{"v": "x"}}
	}

	kv, svc := setup()
	kv.onGet = func(key string) (bool, []byte, error) { return isDoc(key), nil, errStorage }
	_, err := svc.UpsertDocument(ctx, req())
	assert.Equal(t, "check-document", metadataOp(t, err))

	kv, svc = setup()
	kv.onPIA = func(key string) (bool, bool, error) { return isDoc(key), false, errStorage }
	_, err = svc.UpsertDocument(ctx, req())
	assert.Equal(t, "upsert-document", metadataOp(t, err))

	// A PutIfAbsent that always loses, with nothing ever readable, runs out.
	kv, svc = setup()
	kv.onPIA = func(key string) (bool, bool, error) { return isDoc(key), false, nil }
	var conflict *CollectionDocumentConflictError
	_, err = svc.UpsertDocument(ctx, req())
	require.ErrorAs(t, err, &conflict)

	kv, svc = setup()
	_, err = svc.UpsertDocument(ctx, req())
	require.NoError(t, err)
	kv.onCAS = func(key string) (bool, bool, error) { return isDoc(key), false, errStorage }
	_, err = svc.UpsertDocument(ctx, req())
	assert.Equal(t, "upsert-document", metadataOp(t, err))

	// A guarded replace whose swap loses is a conflict at once, not a retry.
	got, err := svc.GetDocument(ctx, &GetDocumentRequest{Database: "db", Collection: "c", ID: 9})
	require.NoError(t, err)
	kv.onCAS = func(key string) (bool, bool, error) { return isDoc(key), false, nil }
	guarded := req()
	guarded.IfRevision = got.Revision
	_, err = svc.UpsertDocument(ctx, guarded)
	require.ErrorAs(t, err, &conflict)

	// An unguarded one whose swap keeps losing runs out.
	_, err = svc.UpsertDocument(ctx, req())
	require.ErrorAs(t, err, &conflict)

	// A stored document the collection's codec cannot decode is reported.
	kv.onCAS = nil
	require.NoError(t, kv.memoryKVSvc.Put(ctx, docKeyForTest(t, svc, "db", "c", 9), []byte("{not json")))
	_, err = svc.UpsertDocument(ctx, req())
	assert.Equal(t, "unmarshal-document", metadataOp(t, err))
}

func TestDeleteDocument_GuardFaults(t *testing.T) {
	ctx := context.Background()
	kv := newFaultingKVSvc()
	svc := NewCollectionService(kv)
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Name: "c", Database: "db"})
	require.NoError(t, err)
	ins, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"v": 1.0}})
	require.NoError(t, err)
	req := &DeleteDocumentRequest{Database: "db", Collection: "c", ID: ins.ID, IfRevision: ins.Revision}

	kv.onCAD = func(key string) (bool, bool, error) { return isDoc(key), false, errStorage }
	_, err = svc.DeleteDocument(ctx, req)
	assert.Equal(t, "delete-document", metadataOp(t, err))

	kv.onCAD = func(key string) (bool, bool, error) { return isDoc(key), false, nil }
	var conflict *CollectionDocumentConflictError
	_, err = svc.DeleteDocument(ctx, req)
	require.ErrorAs(t, err, &conflict)
}

func TestFenceWrite_Faults(t *testing.T) {
	ctx := context.Background()

	// The re-read fails for a reason other than absence: the write stands and
	// the failure is reported.
	kv := newFaultingKVSvc()
	svc := NewCollectionService(kv)
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Name: "c", Database: "db"})
	require.NoError(t, err)
	reads := 0
	kv.onGet = func(key string) (bool, []byte, error) {
		if !isMeta(key) {
			return false, nil, nil
		}
		reads++
		if reads == 2 {
			return true, nil, errStorage
		}
		return false, nil, nil
	}
	_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"_id": 5.0, "v": 1.0}})
	assert.Equal(t, "fence", metadataOp(t, err))
	_, gerr := kv.memoryKVSvc.Get(ctx, docKeyForTest(t, svc, "db", "c", 5))
	assert.NoError(t, gerr, "an unknown answer must not retract a write into a live collection")

	// The retraction itself fails: reported with the refusal.
	kv2 := newHookKVSvc()
	svc2 := NewCollectionService(&retractFailKV{hookKVSvc: kv2})
	_, err = svc2.CreateCollection(ctx, &CreateCollectionRequest{Name: "c", Database: "db"})
	require.NoError(t, err)
	kv2.afterDocWrite = func(string) { markStored(t, kv2, "db", "c", CollectionStateDropping) }
	_, err = svc2.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"v": 1.0}})
	assert.Equal(t, "retract-write", metadataOp(t, err))
	var notActive *CollectionNotActiveError
	assert.ErrorAs(t, err, &notActive, "the refusal travels with the retraction failure")
}

// retractFailKV fails every compare-and-delete.
type retractFailKV struct{ *hookKVSvc }

func (r *retractFailKV) CompareAndDelete(context.Context, string, []byte) (bool, error) {
	return false, errStorage
}

func TestLoadCollectionRecord_Answers(t *testing.T) {
	ctx := context.Background()
	kv := newFaultingKVSvc()
	svc := NewCollectionService(kv)

	kv.onGet = func(key string) (bool, []byte, error) { return true, nil, nil }
	var notFound *CollectionNotFoundError
	_, err := svc.GetCollectionInfo(ctx, "db", "c")
	require.ErrorAs(t, err, &notFound, "an absent value with no error is absence")

	kv.onGet = func(key string) (bool, []byte, error) { return true, nil, errStorage }
	_, err = svc.GetCollectionInfo(ctx, "db", "c")
	assert.Equal(t, "load-metadata", metadataOp(t, err), "a failed read is not reported as absence")
}

func TestUpdateSchema_RereadsALostSwap(t *testing.T) {
	ctx := context.Background()
	kv := newFaultingKVSvc()
	svc := NewCollectionService(kv)
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Name: "c", Database: "db"})
	require.NoError(t, err)

	// The first schema swap loses to a concurrent merge; the re-read and
	// re-merge land both fields.
	lost := false
	kv.onCAS = func(key string) (bool, bool, error) {
		if !isMeta(key) || lost {
			return false, false, nil
		}
		lost = true
		info, _, lerr := svc.loadCollectionRecord(ctx, "db", "c")
		require.NoError(t, lerr)
		info.Schema = map[string]string{"concurrent": "VARCHAR"}
		putMetadata(t, kv, info)
		return true, false, nil
	}
	_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"mine": "x"}})
	require.NoError(t, err)
	info, err := svc.GetCollectionInfo(ctx, "db", "c")
	require.NoError(t, err)
	assert.Contains(t, info.Schema, "mine")
	assert.Contains(t, info.Schema, "concurrent")

	// A swap that errors gives up quietly: the schema is best effort.
	kv.onCAS = func(key string) (bool, bool, error) { return isMeta(key), false, errStorage }
	_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"other": "x"}})
	require.NoError(t, err)
}

func TestCollectionService_ClockDefaultsToWallTime(t *testing.T) {
	svc := &CollectionService{}
	assert.WithinDuration(t, time.Now(), svc.clock(), time.Minute)
}

// TestUpdateDocument_RefusedOnADroppingCollection: every document verb,
// delete included, refuses a collection being dropped. The teardown removes
// every document itself, counting exactly the ones it removed.
func TestUpdateDocument_RefusedOnADroppingCollection(t *testing.T) {
	ctx := context.Background()
	kv := newResolvedKVSvc()
	svc := NewCollectionService(kv)
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Name: "c", Database: "db"})
	require.NoError(t, err)
	ins, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"v": 1.0}})
	require.NoError(t, err)
	markStored(t, kv, "db", "c", CollectionStateDropping)

	var notActive *CollectionNotActiveError
	_, err = svc.UpdateDocument(ctx, &UpdateDocumentRequest{Database: "db", Collection: "c", ID: ins.ID, Document: map[string]interface{}{"v": 2.0}})
	require.ErrorAs(t, err, &notActive)
	_, err = svc.DeleteDocument(ctx, &DeleteDocumentRequest{Database: "db", Collection: "c", ID: ins.ID})
	require.ErrorAs(t, err, &notActive)
	_, err = kv.Get(ctx, docKeyForTest(t, svc, "db", "c", ins.ID))
	require.NoError(t, err, "a refused delete removes nothing")
}

// dropFailIndexSvc fails every owner-checked drop.
type dropFailIndexSvc struct {
	*nthCreateFailIndexSvc
}

func (d *dropFailIndexSvc) DropOwnedIndex(context.Context, string, string) error {
	return errStorage
}
