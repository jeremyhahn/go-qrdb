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

package server

import (
	"context"
	"encoding/json"
	"math/rand"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// lockedKV is the mock KV made safe for concurrent callers: every verb,
// guarded ones included, is atomic under one lock, as a Raft-applied write
// is.
type lockedKV struct {
	mu sync.Mutex
	m  *mockKVService
}

func (l *lockedKV) Put(ctx context.Context, k string, v []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.m.Put(ctx, k, v)
}

func (l *lockedKV) PutIfAbsent(ctx context.Context, k string, v []byte) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.m.PutIfAbsent(ctx, k, v)
}

func (l *lockedKV) CompareAndSwap(ctx context.Context, k string, e, n []byte) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.m.CompareAndSwap(ctx, k, e, n)
}

func (l *lockedKV) CompareAndDelete(ctx context.Context, k string, e []byte) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.m.CompareAndDelete(ctx, k, e)
}

func (l *lockedKV) Get(ctx context.Context, k string) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.m.Get(ctx, k)
}

func (l *lockedKV) Delete(ctx context.Context, k string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.m.Delete(ctx, k)
}

func (l *lockedKV) List(ctx context.Context, p string) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.m.List(ctx, p)
}

func (l *lockedKV) Scan(ctx context.Context, p string) (map[string][]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.m.Scan(ctx, p)
}

func (l *lockedKV) PutWithMode(ctx context.Context, k string, v []byte, _ writemode.Mode) error {
	return l.Put(ctx, k, v)
}

func (l *lockedKV) DeleteWithMode(ctx context.Context, k string, _ writemode.Mode) error {
	return l.Delete(ctx, k)
}

func (l *lockedKV) GetWithConsistency(ctx context.Context, k string, _ consistency.ReadOptions) ([]byte, error) {
	return l.Get(ctx, k)
}

func (l *lockedKV) ScanWithConsistency(ctx context.Context, p string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return l.Scan(ctx, p)
}

// countingInner counts the collection writes the quota wrapper makes that
// are NOT guarded -- a plain update, or a delete with no revision -- because
// such a write removes or replaces a document the wrapper cannot then
// account for exactly.
type countingInner struct {
	*service.CollectionService
	mu                  sync.Mutex
	plainUpd, plainDels int
}

func (c *countingInner) UpdateDocument(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
	resp, err := c.CollectionService.UpdateDocument(ctx, req)
	if err == nil {
		c.mu.Lock()
		c.plainUpd++
		c.mu.Unlock()
	}
	return resp, err
}

func (c *countingInner) DeleteDocument(ctx context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
	resp, err := c.CollectionService.DeleteDocument(ctx, req)
	if err == nil && req.IfRevision == "" {
		c.mu.Lock()
		c.plainDels++
		c.mu.Unlock()
	}
	return resp, err
}

// storedBodyBytes sums the body bytes of every stored document.
func storedBodyBytes(t *testing.T, kv *lockedKV) int64 {
	t.Helper()
	all, err := kv.Scan(context.Background(), "__col:")
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, raw := range all {
		var e dao.DynamicEntity
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatal(err)
		}
		total += service.DocumentBodyBytes(e.Fields)
	}
	return total
}

// runConcurrentCRUD drives workers goroutines of random inserts, updates,
// upserts and deletes over four document IDs through q.
func runConcurrentCRUD(q *quotaCollectionService, strategy string, workers int, seed int64) {
	ctx := context.Background()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			r := rand.New(rand.NewSource(seed))
			for i := 0; i < 300; i++ {
				id := uint64(r.Intn(4) + 1)
				body := strings.Repeat("x", r.Intn(40)+1)
				doc := map[string]interface{}{"k": string(rune('a' + r.Intn(3))), "v": body}
				switch r.Intn(5) {
				case 0:
					if strategy == service.IDStrategyFieldHash {
						_, _ = q.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "db", Collection: "c", Document: doc})
					} else {
						_, _ = q.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"_id": float64(id), "v": body}})
					}
				case 1:
					_, _ = q.UpdateDocument(ctx, &service.UpdateDocumentRequest{Database: "db", Collection: "c", ID: id, Document: doc})
				case 2:
					_, _ = q.UpsertDocument(ctx, &service.UpsertDocumentRequest{Database: "db", Collection: "c", ID: id, Document: doc})
				case 3:
					_, _ = q.DeleteDocument(ctx, &service.DeleteDocumentRequest{Database: "db", Collection: "c", ID: id})
				case 4:
					l, err := q.ListDocuments(ctx, &service.ListDocumentsRequest{Database: "db", Collection: "c"})
					if err == nil && len(l.Documents) > 0 {
						_, _ = q.DeleteDocument(ctx, &service.DeleteDocumentRequest{Database: "db", Collection: "c", ID: l.Documents[r.Intn(len(l.Documents))].ID})
					}
				}
			}
		}(seed + int64(w))
	}
	wg.Wait()
}

// TestQuotaCollection_ConcurrentCRUDAccountsExactly: under concurrent
// ordinary CRUD, with no drop in play, the tenant's usage equals the body
// bytes actually stored -- to the byte -- and the wrapper never makes an
// unguarded write it could not account for. The wrapper used to fall back to
// the plain update or delete when its read found no document, and a document
// written in between was then replaced or removed with nothing released: a
// measured usage of 1086 against 38 stored.
func TestQuotaCollection_ConcurrentCRUDAccountsExactly(t *testing.T) {
	for round := 0; round < 20; round++ {
		strategy := service.IDStrategyUnixNano
		if round%2 == 1 {
			strategy = service.IDStrategyFieldHash
		}
		kv := &lockedKV{m: newMockKVService()}
		inner := &countingInner{CollectionService: service.NewCollectionService(kv)}
		acct, _ := newTestAccountant(t, nil)
		q := newQuotaCollectionService(inner, acct, "t1", 1<<30)
		if _, err := q.CreateCollection(context.Background(), &service.CreateCollectionRequest{Database: "db", Name: "c", IDStrategy: strategy, FieldHash: "k"}); err != nil {
			t.Fatal(err)
		}
		runConcurrentCRUD(q, strategy, 6, int64(round*100))

		if inner.plainUpd != 0 || inner.plainDels != 0 {
			t.Fatalf("round %d: the wrapper made %d unguarded updates and %d unguarded deletes", round, inner.plainUpd, inner.plainDels)
		}
		if usage, actual := acct.Usage("t1"), storedBodyBytes(t, kv); usage != actual {
			t.Fatalf("round %d (%s): usage = %d, stored = %d", round, strategy, usage, actual)
		}
	}
}

// TestQuotaCollection_ConcurrentCRUDWithDropsNeverUnderCounts: with drops and
// re-creates racing the CRUD, the only drift left is the documented
// fail-closed one -- the tenant may be charged for more than is stored, never
// for less.
func TestQuotaCollection_ConcurrentCRUDWithDropsNeverUnderCounts(t *testing.T) {
	ctx := context.Background()
	for round := 0; round < 10; round++ {
		kv := &lockedKV{m: newMockKVService()}
		acct, _ := newTestAccountant(t, nil)
		q := newQuotaCollectionService(service.NewCollectionService(kv), acct, "t1", 1<<30)
		create := func() {
			_, _ = q.CreateCollection(ctx, &service.CreateCollectionRequest{Database: "db", Name: "c"})
		}
		create()
		done := make(chan struct{})
		go func() {
			defer close(done)
			for i := 0; i < 20; i++ {
				_, _ = q.DropCollectionMetered(ctx, &service.DropCollectionRequest{Database: "db", Name: "c"})
				create()
			}
		}()
		runConcurrentCRUD(q, service.IDStrategyUnixNano, 4, int64(round*100))
		<-done
		if _, err := q.SweepStuckCollections(ctx, "db", -1); err != nil {
			t.Fatal(err)
		}
		if usage, actual := acct.Usage("t1"), storedBodyBytes(t, kv); usage < actual {
			t.Fatalf("round %d: under-count: usage = %d, stored = %d", round, usage, actual)
		}
	}
}
