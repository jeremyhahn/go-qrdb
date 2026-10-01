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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// dropHook runs a hook once, just before the wrapped service drops. It is a
// metered drop surface exactly when the service it wraps is one.
type dropHook struct {
	*service.CollectionService
	beforeDrop func()
}

var _ service.CollectionDropMeter = (*dropHook)(nil)

func (d *dropHook) fire() {
	if h := d.beforeDrop; h != nil {
		d.beforeDrop = nil
		h()
	}
}

func (d *dropHook) DropCollection(ctx context.Context, req *service.DropCollectionRequest) error {
	d.fire()
	return d.CollectionService.DropCollection(ctx, req)
}

func (d *dropHook) DropCollectionMetered(ctx context.Context, req *service.DropCollectionRequest) (int64, error) {
	d.fire()
	return d.CollectionService.DropCollectionMetered(ctx, req)
}

// TestQuotaCollection_DropDoesNotReleaseAConcurrentDeleteTwice lands a delete
// between the drop's measurement and the drop. The drop used to release the
// bytes it measured beforehand -- including the document the delete had
// already released. It now releases what its teardown removed.
func TestQuotaCollection_DropDoesNotReleaseAConcurrentDeleteTwice(t *testing.T) {
	ctx := context.Background()
	kv := newMockKVService()
	hook := &dropHook{CollectionService: service.NewCollectionService(kv)}
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaCollectionService(hook, acct, "t1", 1<<20)
	other := newQuotaCollectionService(hook, acct, "t1", 1<<20)
	for _, n := range []string{"keep", "docs"} {
		if _, err := q.CreateCollection(ctx, &service.CreateCollectionRequest{Database: "db", Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	keep := map[string]interface{}{"v": "0123456789012345678901234567890123456789"}
	if _, err := q.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "db", Collection: "keep", Document: keep}); err != nil {
		t.Fatal(err)
	}
	doc, err := q.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "db", Collection: "docs", Document: map[string]interface{}{"v": "abcdefghij"}})
	if err != nil {
		t.Fatal(err)
	}
	hook.beforeDrop = func() {
		if _, err := other.DeleteDocument(ctx, &service.DeleteDocumentRequest{Database: "db", Collection: "docs", ID: doc.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.DropCollection(ctx, &service.DropCollectionRequest{Database: "db", Name: "docs"}); err != nil {
		t.Fatal(err)
	}
	if got, want := acct.Usage("t1"), service.DocumentBodyBytes(keep); got != want {
		t.Fatalf("usage = %d, want %d: the deleted document was released twice", got, want)
	}
}

// TestQuotaCollection_FieldHashReinsertIsChargedOnce: a field_hash re-insert
// of the same natural key overwrites one document, so the tenant is charged
// for one document, not one per insert.
func TestQuotaCollection_FieldHashReinsertIsChargedOnce(t *testing.T) {
	ctx := context.Background()
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaCollectionService(service.NewCollectionService(newMockKVService()), acct, "t1", 1<<20)
	if _, err := q.CreateCollection(ctx, &service.CreateCollectionRequest{Database: "db", Name: "u", IDStrategy: service.IDStrategyFieldHash, FieldHash: "email"}); err != nil {
		t.Fatal(err)
	}
	doc := map[string]interface{}{"email": "a@example.com", "n": "x"}
	for i := 0; i < 5; i++ {
		if _, err := q.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "db", Collection: "u", Document: doc}); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := acct.Usage("t1"), service.DocumentBodyBytes(doc); got != want {
		t.Fatalf("usage = %d, want %d: one stored document charged %d times", got, want, got/want)
	}
}
