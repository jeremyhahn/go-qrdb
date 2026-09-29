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
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// racingDocs runs a hook once, just after the first GetDocument returns -- the
// moment between the quota wrapper measuring a document and its write.
type racingDocs struct {
	service.CollectionServicer
	hook func()
}

func (r *racingDocs) GetDocument(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
	resp, err := r.CollectionServicer.GetDocument(ctx, req)
	if hook := r.hook; hook != nil {
		r.hook = nil
		hook()
	}
	return resp, err
}

// raceHarness builds a metered collection holding a small document and a
// larger one, so a double release shows as usage below the larger one's size
// rather than being hidden by the accountant's floor at zero.
func raceHarness(t *testing.T) (*quotaCollectionService, *racingDocs, *quota.Accountant, uint64, int64) {
	t.Helper()
	ctx := context.Background()
	kv := newMockKVService()
	racing := &racingDocs{CollectionServicer: service.NewCollectionService(kv)}
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaCollectionService(racing, acct, "t1", 1<<20)
	if _, err := q.CreateCollection(ctx, &service.CreateCollectionRequest{Database: "db", Name: "docs"}); err != nil {
		t.Fatal(err)
	}
	small, err := q.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "db", Collection: "docs", Document: map[string]interface{}{"_id": 1.0, "v": "a"}})
	if err != nil {
		t.Fatal(err)
	}
	large := map[string]interface{}{"_id": 2.0, "v": "0123456789012345678901234567890123456789"}
	if _, err := q.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "db", Collection: "docs", Document: large}); err != nil {
		t.Fatal(err)
	}
	return q, racing, acct, small.ID, service.DocumentBodyBytes(large)
}

// getHookKV runs a hook once, just after the Nth read of a document key --
// chosen so it lands inside the service's own read-then-delete, the window a
// measurement taken beforehand cannot see.
type getHookKV struct {
	*mockKVService
	mu   sync.Mutex
	n    int
	at   int
	hook func()
}

func (g *getHookKV) Get(ctx context.Context, key string) ([]byte, error) {
	out, err := g.mockKVService.Get(ctx, key)
	if strings.HasPrefix(key, "__col:") {
		g.mu.Lock()
		g.n++
		var hook func()
		if g.n == g.at {
			hook, g.hook = g.hook, nil
		}
		g.mu.Unlock()
		if hook != nil {
			hook()
		}
	}
	return out, err
}

// TestQuotaCollection_ConcurrentDeletesReleaseOnce lands a second delete of
// the same document inside the first delete's own read-then-delete: after
// the service read the document and before it removed it. Both deletes then
// find the document; only the one that removed it may release it.
//
// Mutation-checked: with an unguarded delete both calls release the small
// document and the tenant's usage falls below what it holds.
func TestQuotaCollection_ConcurrentDeletesReleaseOnce(t *testing.T) {
	ctx := context.Background()
	kv := &getHookKV{mockKVService: newMockKVService()}
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaCollectionService(service.NewCollectionService(kv), acct, "t1", 1<<20)
	if _, err := q.CreateCollection(ctx, &service.CreateCollectionRequest{Database: "db", Name: "docs"}); err != nil {
		t.Fatal(err)
	}
	small, err := q.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "db", Collection: "docs", Document: map[string]interface{}{"_id": 1.0, "v": "a"}})
	if err != nil {
		t.Fatal(err)
	}
	large := map[string]interface{}{"_id": 2.0, "v": "0123456789012345678901234567890123456789"}
	if _, err := q.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "db", Collection: "docs", Document: large}); err != nil {
		t.Fatal(err)
	}

	kv.mu.Lock()
	kv.n = 0
	kv.at = 2 // 1: the wrapper's measurement; 2: the service's own read
	kv.hook = func() {
		if _, err := q.DeleteDocument(ctx, &service.DeleteDocumentRequest{Database: "db", Collection: "docs", ID: small.ID}); err != nil {
			t.Errorf("the delete that won: %v", err)
		}
	}
	kv.mu.Unlock()

	_, err = q.DeleteDocument(ctx, &service.DeleteDocumentRequest{Database: "db", Collection: "docs", ID: small.ID})
	var notFound *service.CollectionDocumentNotFoundError
	if err != nil && !errors.As(err, &notFound) {
		t.Fatalf("the delete that lost: %v", err)
	}
	if got, want := acct.Usage("t1"), service.DocumentBodyBytes(large); got != want {
		t.Fatalf("usage = %d, want %d: the deleted document was released more than once", got, want)
	}
}

// TestQuotaCollection_ConcurrentUpdatesReleaseWhatTheyDisplaced lands a
// second update between the first update's measurement and its write. Each
// update must release the document IT displaced, so the usage ends at the
// surviving bodies exactly.
func TestQuotaCollection_ConcurrentUpdatesReleaseWhatTheyDisplaced(t *testing.T) {
	ctx := context.Background()
	q, racing, acct, id, largeBytes := raceHarness(t)
	first := map[string]interface{}{"v": "first-writer"}
	second := map[string]interface{}{"v": "second"}
	racing.hook = func() {
		if _, err := q.UpdateDocument(ctx, &service.UpdateDocumentRequest{Database: "db", Collection: "docs", ID: id, Document: second}); err != nil {
			t.Fatalf("the interleaved update: %v", err)
		}
	}
	if _, err := q.UpdateDocument(ctx, &service.UpdateDocumentRequest{Database: "db", Collection: "docs", ID: id, Document: first}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got, want := acct.Usage("t1"), largeBytes+service.DocumentBodyBytes(first); got != want {
		t.Fatalf("usage = %d, want %d", got, want)
	}
}

// TestQuotaCollection_UnreadableDocumentReleasesNothing covers the fallback
// the wrapper takes when it cannot measure: the service decides, and nothing
// is released.
func TestQuotaCollection_UnreadableDocumentReleasesNothing(t *testing.T) {
	ctx := context.Background()
	q, _, acct, _, _ := raceHarness(t)
	before := acct.Usage("t1")
	var notFound *service.CollectionDocumentNotFoundError
	if _, err := q.DeleteDocument(ctx, &service.DeleteDocumentRequest{Database: "db", Collection: "docs", ID: 99}); !errors.As(err, &notFound) {
		t.Fatalf("delete of a missing document: %v", err)
	}
	if _, err := q.UpdateDocument(ctx, &service.UpdateDocumentRequest{Database: "db", Collection: "docs", ID: 99, Document: map[string]interface{}{"v": 1}}); !errors.As(err, &notFound) {
		t.Fatalf("update of a missing document: %v", err)
	}
	if acct.Usage("t1") != before {
		t.Fatalf("usage moved from %d to %d", before, acct.Usage("t1"))
	}
	// A caller-supplied revision that no longer matches is refused by the
	// service, unchanged.
	var conflict *service.CollectionDocumentConflictError
	if _, err := q.DeleteDocument(ctx, &service.DeleteDocumentRequest{Database: "db", Collection: "docs", ID: 1, IfRevision: "stale"}); !errors.As(err, &conflict) {
		t.Fatalf("stale guarded delete: %v", err)
	}
}

// alwaysConflicting answers every guarded write with a lost race.
type alwaysConflicting struct {
	service.CollectionServicer
}

func (a *alwaysConflicting) UpsertDocument(_ context.Context, req *service.UpsertDocumentRequest) (*service.UpsertDocumentResponse, error) {
	return nil, &service.CollectionDocumentConflictError{ID: req.ID}
}

func (a *alwaysConflicting) DeleteDocument(_ context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
	return nil, &service.CollectionDocumentConflictError{ID: req.ID}
}

// TestQuotaCollection_SustainedContentionIsReported covers the bound on the
// re-read loop: a writer that keeps losing is told so, and its reservation
// is released.
func TestQuotaCollection_SustainedContentionIsReported(t *testing.T) {
	ctx := context.Background()
	kv := newMockKVService()
	inner := service.NewCollectionService(kv)
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaCollectionService(&alwaysConflicting{CollectionServicer: inner}, acct, "t1", 1<<20)
	if _, err := q.CreateCollection(ctx, &service.CreateCollectionRequest{Database: "db", Name: "docs"}); err != nil {
		t.Fatal(err)
	}
	ins, err := inner.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "db", Collection: "docs", Document: map[string]interface{}{"v": "a"}})
	if err != nil {
		t.Fatal(err)
	}
	var conflict *service.CollectionDocumentConflictError
	if _, err := q.UpdateDocument(ctx, &service.UpdateDocumentRequest{Database: "db", Collection: "docs", ID: ins.ID, Document: map[string]interface{}{"v": "b"}}); !errors.As(err, &conflict) {
		t.Fatalf("update: %v", err)
	}
	if _, err := q.DeleteDocument(ctx, &service.DeleteDocumentRequest{Database: "db", Collection: "docs", ID: ins.ID}); !errors.As(err, &conflict) {
		t.Fatalf("delete: %v", err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage = %d, want 0: the lost update's reservation must be released", got)
	}
}
