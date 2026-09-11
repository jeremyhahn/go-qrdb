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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	memorystorage "github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// gatedStorage is a REAL memory engine with one addition: onGet is invoked at
// the top of Get for a single nominated key, once.
//
// It exists because a 64-way contention test at the InsertDocument level does
// NOT distinguish a guarded write from a read-then-write: each insert does
// several store round trips before its document write, so the callers spread
// out and the window is almost never actually hit. A test that passes against
// the defect is worse than no test -- it is a green assertion that the defect
// is correct, which is how three of them reached main here (go-qrdb#489). This
// type makes the window deterministic by holding one insert INSIDE it while a
// second one runs.
type gatedStorage struct {
	storage.Storage

	armedKey string
	once     sync.Once
	onGet    func()
}

// Get delegates first and runs the hook AFTER the read, which is where the
// window actually is: the caller has now made its observation and has not yet
// acted on it. Running the hook before the read would let the competitor's
// write be picked up by this very read, and a read-then-write would look
// correct -- the test would pass against the defect.
func (g *gatedStorage) Get(key []byte) ([]byte, error) {
	v, err := g.Storage.Get(key)
	if string(key) == g.armedKey {
		g.once.Do(func() {
			if g.onGet != nil {
				g.onGet()
			}
		})
	}
	return v, err
}

// This file exercises the dedicated-store path end to end with no test double
// anywhere in the write: a real memory engine, a real database.StandaloneBackend
// over it, the real BackendKVService the bundle builds (see
// buildDedicatedStoreServices), and where the subject is a collection, the real
// CollectionService.
//
// A double that reports "PutIfAbsent stored" proves nothing about atomicity, so
// every assertion here is made against production types under genuine
// contention (go-qrdb#466, go-qrdb#489).

// claimants is the number of concurrent callers each contention test uses. It
// is well above GOMAXPROCS on any runner so the goroutines genuinely overlap.
const claimants = 64

// newDedicatedStoreKV builds the KV service the dedicated-store bundle builds:
// NewNamedBackendKVService over a StandaloneBackend over a real memory engine.
func newDedicatedStoreKV(t *testing.T) *BackendKVService {
	t.Helper()
	store := memorystorage.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	return NewNamedBackendKVService("mydb", database.NewStandaloneBackend(store))
}

func TestDedicatedStore_PutIfAbsentStoresExactlyOnce(t *testing.T) {
	ctx := context.Background()
	kv := newDedicatedStoreKV(t)

	var stored atomic.Int64
	var winner atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < claimants; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ok, err := kv.PutIfAbsent(ctx, "single-use-token", []byte(strconv.Itoa(i)))
			if err != nil {
				t.Errorf("PutIfAbsent: unexpected error %v", err)
				return
			}
			if ok {
				stored.Add(1)
				winner.Store(int64(i))
			}
		}(i)
	}
	close(start)
	wg.Wait()

	require.Equal(t, int64(1), stored.Load(), "a single-use token was issued more than once")
	got, err := kv.Get(ctx, "single-use-token")
	require.NoError(t, err)
	assert.Equal(t, strconv.FormatInt(winner.Load(), 10), string(got))
}

func TestDedicatedStore_CompareAndSwapSwapsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	kv := newDedicatedStoreKV(t)
	require.NoError(t, kv.Put(ctx, "k", []byte("v0")))

	var swapped atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < claimants; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ok, err := kv.CompareAndSwap(ctx, "k", []byte("v0"), []byte("v"+strconv.Itoa(i)))
			if err != nil {
				t.Errorf("CompareAndSwap: unexpected error %v", err)
				return
			}
			if ok {
				swapped.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	assert.Equal(t, int64(1), swapped.Load(), "more than one caller believed it won the swap")
}

func TestDedicatedStore_CompareAndDeleteDeletesExactlyOnce(t *testing.T) {
	ctx := context.Background()
	kv := newDedicatedStoreKV(t)
	require.NoError(t, kv.Put(ctx, "lock", []byte("held")))

	var deleted atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < claimants; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := kv.CompareAndDelete(ctx, "lock", []byte("held"))
			if err != nil {
				t.Errorf("CompareAndDelete: unexpected error %v", err)
				return
			}
			if ok {
				deleted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.Equal(t, int64(1), deleted.Load(), "more than one caller believed it released the lock")
}

// TestRemoteBackendKV_StillRefusesGuardedWrites keeps the refusal honest. The
// capability is discovered by assertion precisely because RemoteBackend cannot
// honour it without a new wire operation, so widening the standalone backend
// must not have widened this one by accident.
func TestRemoteBackendKV_StillRefusesGuardedWrites(t *testing.T) {
	ctx := context.Background()
	svc := NewBackendKVService(database.NewRemoteBackend(nil))

	calls := map[string]func() (bool, error){
		"PutIfAbsent":      func() (bool, error) { return svc.PutIfAbsent(ctx, "k", []byte("v")) },
		"CompareAndSwap":   func() (bool, error) { return svc.CompareAndSwap(ctx, "k", []byte("a"), []byte("b")) },
		"CompareAndDelete": func() (bool, error) { return svc.CompareAndDelete(ctx, "k", []byte("a")) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			ok, err := call()
			assert.False(t, ok)
			assert.True(t, errors.Is(err, database.ErrAtomicWriteUnsupported),
				"error %v no longer names the missing capability", err)
			assert.True(t, errors.Is(err, ErrNotStored),
				"a refused capability provably stored nothing and must say so")
		})
	}
}

// ---------------------------------------------------------------------------
// The level go-qrdb#447 and go-qrdb#475 care about
// ---------------------------------------------------------------------------

// newDedicatedStoreCollections builds the collection service the dedicated-store
// bundle builds: NewCollectionService over the same BackendKVService, with no
// secondary index and no sequence service, exactly as buildDedicatedStoreServices
// wires it.
func newDedicatedStoreCollections(t *testing.T) (*CollectionService, *BackendKVService) {
	t.Helper()
	kv := newDedicatedStoreKV(t)
	return NewCollectionService(kv), kv
}

// TestDedicatedStoreCollection_SecondClaimInsideTheWindowIsRefused is the
// defect go-qrdb#447 found and could not close, made deterministic: a second
// insert carrying the same explicit _id runs entirely INSIDE the first one's
// document-write window. Under the read-then-write this replaced, both observed
// the ID free, both wrote, and one document was gone with no error anywhere.
//
// Nothing here is a double. The engine is a real memory store with a hook on
// one key's read, the backend is database.StandaloneBackend, the KV service is
// the one buildDedicatedStoreServices builds, and the collection service is the
// real one.
func TestDedicatedStoreCollection_SecondClaimInsideTheWindowIsRefused(t *testing.T) {
	ctx := context.Background()
	inner := memorystorage.NewStorage()
	t.Cleanup(func() { _ = inner.Close() })

	gated := &gatedStorage{Storage: inner}
	kv := NewNamedBackendKVService("mydb", database.NewStandaloneBackend(gated))
	svc := NewCollectionService(kv)
	createCollection(t, svc, "mydb", "users", IDStrategyIncrement, "")
	gated.armedKey = collectionDataEntityType("mydb", "users") + "/7"

	claim := func(who string) error {
		_, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
			Database:   "mydb",
			Collection: "users",
			Document:   map[string]interface{}{"_id": 7, "who": who},
		})
		return err
	}

	var secondErr error
	second := make(chan struct{})
	gated.onGet = func() {
		go func() {
			defer close(second)
			secondErr = claim("second")
		}()
		// The second claim is given every chance to complete while the first
		// is still inside its window. Under a guarded write it cannot: it
		// blocks until the first one's write has landed.
		select {
		case <-second:
		case <-time.After(200 * time.Millisecond):
		}
	}

	firstErr := claim("first")
	<-second

	var exists *CollectionDocumentExistsError
	switch {
	case firstErr == nil && secondErr == nil:
		t.Fatal("both claims on the same explicit _id succeeded: one document was overwritten and neither caller was told")
	case firstErr == nil:
		require.ErrorAs(t, secondErr, &exists, "the losing claim must be refused, not failed obscurely")
	case secondErr == nil:
		require.ErrorAs(t, firstErr, &exists, "the losing claim must be refused, not failed obscurely")
	default:
		t.Fatalf("neither claim succeeded: first=%v second=%v", firstErr, secondErr)
	}

	got, err := svc.GetDocument(ctx, &GetDocumentRequest{Database: "mydb", Collection: "users", ID: 7})
	require.NoError(t, err)
	assert.Equal(t, uint64(7), got.ID)
}

// TestDedicatedStoreCollection_ConcurrentExplicitIDClaimsStoreOnce is the same
// property under ordinary load rather than a held-open window: many callers,
// one ID, one survivor. It complements the deterministic test above; it does
// not replace it, because on its own it passes against the read-then-write it
// replaced.
func TestDedicatedStoreCollection_ConcurrentExplicitIDClaimsStoreOnce(t *testing.T) {
	ctx := context.Background()
	svc, _ := newDedicatedStoreCollections(t)
	createCollection(t, svc, "mydb", "users", IDStrategyIncrement, "")

	var inserted atomic.Int64
	var refused atomic.Int64
	var winner atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < claimants; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
				Database:   "mydb",
				Collection: "users",
				Document:   map[string]interface{}{"_id": 7, "who": "claimant" + strconv.Itoa(i)},
			})
			switch {
			case err == nil:
				inserted.Add(1)
				winner.Store(int64(i))
			default:
				var exists *CollectionDocumentExistsError
				if !errors.As(err, &exists) {
					t.Errorf("InsertDocument: unexpected error %v", err)
					return
				}
				refused.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	require.Equal(t, int64(1), inserted.Load(), "more than one concurrent insert claimed the same explicit _id")
	require.Equal(t, int64(claimants-1), refused.Load(), "every losing claim must be told, not silently dropped")

	// The surviving document belongs to the caller that was told it won.
	got, err := svc.GetDocument(ctx, &GetDocumentRequest{Database: "mydb", Collection: "users", ID: 7})
	require.NoError(t, err)
	assert.Contains(t, docPayload(t, got), "claimant"+strconv.FormatInt(winner.Load(), 10),
		"the stored document is not the one whose insert reported success")
}

// TestDedicatedStoreCollection_ConcurrentGeneratedIDsAllSurvive is the other
// half: the guard must refuse only real collisions. Distinct generated IDs are
// not a collision, so every concurrent insert stores its own document.
func TestDedicatedStoreCollection_ConcurrentGeneratedIDsAllSurvive(t *testing.T) {
	ctx := context.Background()
	svc, _ := newDedicatedStoreCollections(t)
	createCollection(t, svc, "mydb", "events", IDStrategyIncrement, "")

	ids := make(chan uint64, claimants)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < claimants; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			resp, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
				Database:   "mydb",
				Collection: "events",
				Document:   map[string]interface{}{"seq": i},
			})
			if err != nil {
				t.Errorf("InsertDocument: unexpected error %v", err)
				return
			}
			ids <- resp.ID
		}(i)
	}
	close(start)
	wg.Wait()
	close(ids)

	seen := make(map[uint64]struct{}, claimants)
	for id := range ids {
		if _, dup := seen[id]; dup {
			t.Fatalf("document ID %d was handed out twice", id)
		}
		seen[id] = struct{}{}
	}
	assert.Len(t, seen, claimants, "every insert must have stored its own document")
}

// docPayload renders a retrieved document as a string for substring matching,
// without depending on the codec's field ordering.
func docPayload(t *testing.T, resp *GetDocumentResponse) string {
	t.Helper()
	var b strings.Builder
	for k, v := range resp.Document {
		b.WriteString(k)
		b.WriteString("=")
		switch tv := v.(type) {
		case string:
			b.WriteString(tv)
		default:
			b.WriteString(strconv.FormatUint(resp.ID, 10))
		}
		b.WriteString(";")
	}
	return b.String()
}
