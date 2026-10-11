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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Collection index lifecycle: what CreateCollection creates, DropCollection
// must destroy.
//
// The engine's index-create handler is idempotent BY NAME: re-creating an
// existing name is a no-op and the ORIGINAL definition wins. So an index the
// drop leaves behind is not inert -- it silently overrides the definition a
// later CreateCollection asks for on the same (database, collection, field),
// and a unique constraint requested by the second collection is never
// enforced. That consequence is proved end to end against the real engine in
// pkg/engine (TestMVCCEngine_OrphanedCollectionIndex_*); these tests pin the
// service-layer behaviour that prevents it.
// ---------------------------------------------------------------------------

func TestCollectionService_DropCollection_DropsSecondaryIndexes(t *testing.T) {
	svc, idxSvc := newTestCollectionServiceWithIndex()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "users",
		Database: "mydb",
		Indexes: []IndexDefinition{
			{FieldName: "email", Unique: true},
			{FieldName: "team"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"mydb.users.email", "mydb.users.team"}, idxSvc.names(),
		"CreateCollection should have registered both indexes")

	for _, doc := range []map[string]interface{}{
		{"email": "alice@example.com", "team": "core"},
		{"email": "bob@example.com", "team": "core"},
	} {
		_, insErr := svc.InsertDocument(ctx, &InsertDocumentRequest{
			Database:   "mydb",
			Collection: "users",
			Document:   doc,
		})
		require.NoError(t, insErr)
	}

	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{
		Name:     "users",
		Database: "mydb",
	}))

	assert.Empty(t, idxSvc.names(),
		"DropCollection must leave no secondary index behind")
	assert.ElementsMatch(t,
		[]string{"mydb.users.email", "mydb.users.team"}, idxSvc.dropCalls(),
		"DropCollection must drop exactly the indexes CreateCollection created")
}

// TestCollectionService_DropCollection_DropsIndexesBeforeMetadata pins the
// teardown ORDER. The metadata key is the drop's own commit record: while it
// is present the collection still resolves, so a drop interrupted after the
// documents or after the indexes can simply be re-issued and will finish.
// Deleting the metadata first would strand the index definitions with nothing
// left in the store naming them.
func TestCollectionService_DropCollection_DropsIndexesBeforeMetadata(t *testing.T) {
	kv := newResolvedKVSvc()
	idxSvc := newMemoryIndexSvc()
	svc := NewCollectionServiceWithIndex(kv, idxSvc)
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "users",
		Database: "mydb",
		Indexes:  []IndexDefinition{{FieldName: "email"}},
	})
	require.NoError(t, err)

	metaKey := collectionMetadataKey("mydb", "users")
	observed := &metadataProbeKV{memoryKVSvc: kv, metaKey: metaKey, idxSvc: idxSvc}
	svc = NewCollectionServiceWithIndex(observed, idxSvc)

	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{
		Name:     "users",
		Database: "mydb",
	}))

	require.True(t, observed.metaDeleted, "metadata key should have been deleted")
	assert.Equal(t, []string{"mydb.users.email"}, observed.dropsAtMetaDelete,
		"every index must already be dropped when the metadata key is deleted")
}

// metadataProbeKV wraps the in-memory KV service and snapshots the index
// service's drop log at the moment the collection metadata key is deleted.
type metadataProbeKV struct {
	*memoryKVSvc
	metaKey           string
	idxSvc            *memoryIndexSvc
	metaDeleted       bool
	dropsAtMetaDelete []string
}

// CompareAndDelete is how the teardown removes the metadata: guarded by the
// exact DROPPING record it started from.
func (p *metadataProbeKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if key == p.metaKey {
		p.metaDeleted = true
		p.dropsAtMetaDelete = p.idxSvc.dropCalls()
	}
	return p.memoryKVSvc.CompareAndDelete(ctx, key, expected)
}

// TestCollectionService_DropCollection_IndexDropFailureIsFatal pins the
// failure policy: a drop that cannot tear an index down reports the failure
// instead of returning success over an orphan. The metadata key survives, so
// the collection still resolves and the drop is retryable.
func TestCollectionService_DropCollection_IndexDropFailureIsFatal(t *testing.T) {
	kv := newResolvedKVSvc()
	idxSvc := &failingIndexSvc{}
	svc := NewCollectionServiceWithIndex(kv, idxSvc)
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "users",
		Database: "mydb",
		Indexes:  []IndexDefinition{{FieldName: "email"}},
	})
	require.NoError(t, err)

	idxSvc.dropErr = errors.New("raft: not leader")

	err = svc.DropCollection(ctx, &DropCollectionRequest{Name: "users", Database: "mydb"})
	require.Error(t, err)

	var metaErr *CollectionMetadataError
	require.ErrorAs(t, err, &metaErr)
	assert.Equal(t, "drop-index", metaErr.Op)
	assert.Equal(t, []string{"mydb.users.email"}, idxSvc.dropAttempts)

	// The collection still resolves, so the operator can retry the drop.
	_, err = svc.GetCollectionInfo(ctx, "mydb", "users")
	require.NoError(t, err, "a failed drop must leave the collection retrievable and its drop retryable")

	// And the retry succeeds once the index service recovers.
	idxSvc.dropErr = nil
	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Name: "users", Database: "mydb"}))
	_, err = svc.GetCollectionInfo(ctx, "mydb", "users")
	require.Error(t, err, "the retried drop must remove the collection")
}

// TestCollectionService_DropCollection_WithoutIndexService covers the
// NewCollectionService construction, which has no index service at all,
// dropping metadata that declares an index anyway -- a record another
// composition wrote. The drop has nothing to drop indexes with and still
// completes.
func TestCollectionService_DropCollection_WithoutIndexService(t *testing.T) {
	kv := newResolvedKVSvc()
	svc := NewCollectionService(kv)
	ctx := context.Background()

	legacy := []byte(`{"name":"users","database":"mydb","id_strategy":"unix_nano",` +
		`"indexes":[{"field_name":"email"}],"document_count":0,"created_at":"2026-01-01T00:00:00Z"}`)
	require.NoError(t, kv.Put(ctx, collectionMetadataKey("mydb", "users"), legacy))

	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{
		Name:     "users",
		Database: "mydb",
	}))
}

// TestCollectionService_DropCollection_NoIndexesDefined asserts a collection
// with no index definitions makes no DropIndex calls at all.
func TestCollectionService_DropCollection_NoIndexesDefined(t *testing.T) {
	svc, idxSvc := newTestCollectionServiceWithIndex()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "plain",
		Database: "mydb",
	})
	require.NoError(t, err)

	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{
		Name:     "plain",
		Database: "mydb",
	}))
	assert.Empty(t, idxSvc.dropCalls())
}

// TestCollectionService_DropCollection_CorruptMetadata asserts that metadata
// which cannot be parsed fails the drop with a typed error rather than
// half-dropping the collection. The index definitions live in that metadata,
// so a drop that cannot read it cannot know what it owns -- and proceeding
// would produce exactly the silent orphan this change removes. Every other
// CollectionService verb already refuses a collection in this state.
func TestCollectionService_DropCollection_CorruptMetadata(t *testing.T) {
	kv := newResolvedKVSvc()
	idxSvc := newMemoryIndexSvc()
	svc := NewCollectionServiceWithIndex(kv, idxSvc)
	ctx := context.Background()

	require.NoError(t, kv.Put(ctx, collectionMetadataKey("mydb", "broken"), []byte("{not json")))

	err := svc.DropCollection(ctx, &DropCollectionRequest{Name: "broken", Database: "mydb"})
	require.Error(t, err)

	var metaErr *CollectionMetadataError
	require.ErrorAs(t, err, &metaErr)
	assert.Equal(t, "unmarshal-metadata", metaErr.Op)
	assert.Empty(t, idxSvc.dropCalls())
}

// ---------------------------------------------------------------------------
// collectionIndexName
// ---------------------------------------------------------------------------

func TestCollectionIndexName(t *testing.T) {
	tests := []struct {
		name       string
		database   string
		collection string
		field      string
		want       string
	}{
		{"simple", "mydb", "users", "email", "mydb.users.email"},
		{"default database", "system", "audit", "actor", "system.audit.actor"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, collectionIndexName(tc.database, tc.collection, tc.field))
		})
	}
}

// TestCollectionIndexName_MatchesCreateAndDrop asserts the create and drop
// paths derive the SAME name, which is the property the orphan defect broke.
func TestCollectionIndexName_MatchesCreateAndDrop(t *testing.T) {
	svc, idxSvc := newTestCollectionServiceWithIndex()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "users",
		Database: "mydb",
		Indexes:  []IndexDefinition{{FieldName: "email"}},
	})
	require.NoError(t, err)
	created := idxSvc.names()

	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Name: "users", Database: "mydb"}))
	assert.Equal(t, created, idxSvc.dropCalls())
}

// faultKV wraps the in-memory KV service and injects one failure, so a test
// can interrupt DropCollection at a chosen stage and inspect what survived.
type faultKV struct {
	*memoryKVSvc
	listErr      error // returned from List when the prefix matches listPrefix
	listPrefix   string
	deleteErr    error // returned from Delete when the key matches deleteKey
	deleteKey    string
	deleteSuffix string // ...or when the key carries this suffix
}

func (f *faultKV) List(ctx context.Context, prefix string) ([]string, error) {
	if f.listErr != nil && prefix == f.listPrefix {
		return nil, f.listErr
	}
	return f.memoryKVSvc.List(ctx, prefix)
}

// Scan is how the teardown enumerates documents (with their values, which
// its guarded deletes compare against), so the listing fault applies to it.
func (f *faultKV) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	if f.listErr != nil && prefix == f.listPrefix {
		return nil, f.listErr
	}
	return f.memoryKVSvc.Scan(ctx, prefix)
}

func (f *faultKV) deleteFault(key string) error {
	if f.deleteErr != nil {
		if key == f.deleteKey {
			return f.deleteErr
		}
		if f.deleteSuffix != "" && strings.HasSuffix(key, f.deleteSuffix) {
			return f.deleteErr
		}
	}
	return nil
}

func (f *faultKV) Delete(ctx context.Context, key string) error {
	if err := f.deleteFault(key); err != nil {
		return err
	}
	return f.memoryKVSvc.Delete(ctx, key)
}

// CompareAndDelete is how the teardown removes documents and the metadata,
// so the delete fault applies to it too.
func (f *faultKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if err := f.deleteFault(key); err != nil {
		return false, err
	}
	return f.memoryKVSvc.CompareAndDelete(ctx, key, expected)
}

// TestCollectionService_DropCollection_InterruptedAtEachStage walks the three
// storage faults DropCollection can hit and asserts the property the teardown
// order exists to provide: whatever fails, the metadata key survives, so the
// collection still resolves and re-issuing the drop finishes it.
func TestCollectionService_DropCollection_InterruptedAtEachStage(t *testing.T) {
	boom := errors.New("storage: unavailable")
	metaKey := collectionMetadataKey("mydb", "users")

	tests := []struct {
		name string
		// fault configures the injected failure on a freshly populated store.
		fault  func(kv *faultKV, docPrefix string)
		wantOp string
		// wantDropsBeforeFailure is the index names already dropped when the
		// failure surfaced.
		wantDropsBeforeFailure []string
	}{
		{
			name: "listing the documents fails",
			fault: func(kv *faultKV, docPrefix string) {
				kv.listErr = boom
				kv.listPrefix = docPrefix
			},
			wantOp:                 "list-documents",
			wantDropsBeforeFailure: nil,
		},
		{
			name: "deleting a document fails",
			fault: func(kv *faultKV, docPrefix string) {
				kv.deleteErr = boom
				kv.deleteSuffix = "/1"
			},
			wantOp:                 "delete-document",
			wantDropsBeforeFailure: nil,
		},
		{
			name: "deleting the metadata fails",
			fault: func(kv *faultKV, docPrefix string) {
				kv.deleteErr = boom
				kv.deleteKey = metaKey
			},
			wantOp: "delete-metadata",
			// The indexes are torn down BEFORE the metadata, so by the time
			// this fault fires they are already gone -- and the retry drops
			// them again as an idempotent no-op.
			wantDropsBeforeFailure: []string{"mydb.users.email"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			kv := &faultKV{memoryKVSvc: newResolvedKVSvc()}
			idxSvc := newMemoryIndexSvc()
			svc := NewCollectionServiceWithIndex(kv, idxSvc)

			_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
				Name:     "users",
				Database: "mydb",
				Indexes:  []IndexDefinition{{FieldName: "email"}},
			})
			require.NoError(t, err)
			_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{
				Database:   "mydb",
				Collection: "users",
				Document:   map[string]interface{}{"_id": 1, "email": "alice@example.com"},
			})
			require.NoError(t, err)

			tc.fault(kv, docPrefixForTest(t, svc, "mydb", "users"))

			err = svc.DropCollection(ctx, &DropCollectionRequest{Name: "users", Database: "mydb"})
			require.Error(t, err)
			var metaErr *CollectionMetadataError
			require.ErrorAs(t, err, &metaErr)
			assert.Equal(t, tc.wantOp, metaErr.Op)
			assert.ErrorIs(t, err, boom)
			assert.Equal(t, tc.wantDropsBeforeFailure, idxSvc.dropCalls())

			// The metadata key survived, so the drop is retryable.
			_, err = svc.GetCollectionInfo(ctx, "mydb", "users")
			require.NoError(t, err)

			// Clear the fault and retry: the drop completes.
			kv.listErr, kv.deleteErr = nil, nil
			require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Name: "users", Database: "mydb"}))
			assert.Empty(t, idxSvc.names())
			_, err = svc.GetCollectionInfo(ctx, "mydb", "users")
			require.Error(t, err)
		})
	}
}

// TestCollectionService_DropCollection_ToleratesAlreadyGoneIndex covers the
// retry path against an index service that follows the canonical
// index.IndexServicer contract, which permits DropIndex to report
// IndexNotFoundError for a name that is not registered. A teardown asked to
// remove something already absent has got what it asked for; anything else
// would wedge the retry short of the metadata delete.
func TestCollectionService_DropCollection_ToleratesAlreadyGoneIndex(t *testing.T) {
	kv := newResolvedKVSvc()
	idxSvc := &failingIndexSvc{}
	svc := NewCollectionServiceWithIndex(kv, idxSvc)
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "users",
		Database: "mydb",
		Indexes:  []IndexDefinition{{FieldName: "email"}},
	})
	require.NoError(t, err)

	idxSvc.dropErr = &IndexNotFoundError{Name: "mydb.users.email"}

	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Name: "users", Database: "mydb"}),
		"an index that is already gone must not fail the drop")
	assert.Equal(t, []string{"mydb.users.email"}, idxSvc.dropAttempts)

	_, err = svc.GetCollectionInfo(ctx, "mydb", "users")
	require.Error(t, err, "the drop must have completed through to the metadata")
}

// TestIndexAlreadyGone is the unit-level companion: only a not-found result
// counts as an index that was already gone.
func TestIndexAlreadyGone(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"not found", &IndexNotFoundError{Name: "a.b.c"}, true},
		{"wrapped not found", &CollectionMetadataError{Op: "x", Err: &IndexNotFoundError{Name: "a.b.c"}}, true},
		{"not leader", &NotLeaderError{ShardID: 1}, false},
		{"opaque", errors.New("raft: timeout"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, indexAlreadyGone(tc.err))
		})
	}
}

// nthDropFailIndexSvc fails the Nth DropIndex call and succeeds on the rest,
// so a test can interrupt the drop PARTWAY THROUGH a multi-index collection.
type nthDropFailIndexSvc struct {
	*memoryIndexSvc
	failOn int
	n      int
	err    error
}

func (f *nthDropFailIndexSvc) DropIndex(ctx context.Context, name string) error {
	f.n++
	if f.n == f.failOn {
		return f.err
	}
	return f.memoryIndexSvc.DropIndex(ctx, name)
}

// DropOwnedIndex counts toward the same Nth failure: a collection that
// records ownership drops its indexes with it.
func (f *nthDropFailIndexSvc) DropOwnedIndex(ctx context.Context, name, owner string) error {
	f.n++
	if f.n == f.failOn {
		return f.err
	}
	return f.memoryIndexSvc.DropOwnedIndex(ctx, name, owner)
}

// TestCollectionService_DropCollection_PartialIndexDropIsWiderThanOneWrite
// pins the widest interruption state the teardown ordering admits. With
// several indexes and a failure on a later one, the drop returns leaving the
// collection PARTIALLY indexed until someone -- or the sweeper -- retries.
//
// It used to leave that collection LIVE, accepting writes with its declared
// unique constraints unenforced, and this test asserted the insert
// succeeded. Since go-qrdb#425 the drop marks the collection DROPPING before
// it tears anything down, so the same interrupted state refuses the write
// and reports itself.
func TestCollectionService_DropCollection_PartialIndexDropIsWiderThanOneWrite(t *testing.T) {
	ctx := context.Background()
	idxSvc := &nthDropFailIndexSvc{
		memoryIndexSvc: newMemoryIndexSvc(),
		failOn:         2,
		err:            errors.New("raft: not leader"),
	}
	svc := NewCollectionServiceWithIndex(newResolvedKVSvc(), idxSvc)

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "users",
		Database: "mydb",
		Indexes: []IndexDefinition{
			{FieldName: "a"}, {FieldName: "b"}, {FieldName: "c"},
		},
	})
	require.NoError(t, err)
	require.Len(t, idxSvc.names(), 3)

	err = svc.DropCollection(ctx, &DropCollectionRequest{Name: "users", Database: "mydb"})
	require.Error(t, err)
	var metaErr *CollectionMetadataError
	require.ErrorAs(t, err, &metaErr)
	assert.Equal(t, "drop-index", metaErr.Op)

	// The first index is gone and the other two remain, but the collection
	// is no longer live: it reports DROPPING and refuses the write that used
	// to land here with its unique constraints unenforced.
	assert.Equal(t, []string{"mydb.users.b", "mydb.users.c"}, idxSvc.names())
	info, err := svc.GetCollectionInfo(ctx, "mydb", "users")
	require.NoError(t, err)
	assert.Len(t, info.Indexes, 3, "the metadata still declares all three")
	assert.Equal(t, CollectionStateDropping, info.State)
	_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{
		Database:   "mydb",
		Collection: "users",
		Document:   map[string]interface{}{"a": "no-longer-writable"},
	})
	var notActive *CollectionNotActiveError
	require.ErrorAs(t, err, &notActive, "a dropping collection must refuse writes")
	assert.Equal(t, CollectionStateDropping, notActive.State)

	// The retry converges: the already-dropped index is dropped again as a
	// no-op and the drop completes.
	idxSvc.failOn = 0
	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Name: "users", Database: "mydb"}))
	assert.Empty(t, idxSvc.names())
	_, err = svc.GetCollectionInfo(ctx, "mydb", "users")
	require.Error(t, err)
}
