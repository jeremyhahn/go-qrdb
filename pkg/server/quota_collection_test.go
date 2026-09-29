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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// quotaBundle resolves a bundle for tenant t1 with a MaxStorageBytes limit
// through applyQuota, over one shared KV that the collection service writes
// documents to.
func quotaBundle(t *testing.T, limit int64) (*DatabaseServices, *quota.Accountant) {
	t.Helper()
	kv := newMockKVService()
	acct, _ := newTestAccountant(t, nil)
	r := &restServiceRegistry{accountant: acct, systemTenant: tenantid.System}
	svcs := &DatabaseServices{KV: kv, Collection: service.NewCollectionService(kv)}
	info := &tenant.TenantInfo{ID: "t1", Quotas: &quota.Quotas{MaxStorageBytes: limit}}
	return r.applyQuota(svcs, "t1", info), acct
}

// TestQuotaCollection_DocumentWriteMeetsTheLimitAKVWriteDoes is the
// measurement #456 asked for. A tenant at its MaxStorageBytes limit is
// refused a KV write; the same bytes written as a document were admitted,
// because the collection service wrote through the KV servicer it was built
// over, which the quota wrapper never wrapped.
//
// Mutation-checked: without the Collection wrap in applyQuota the document
// insert succeeds and this test fails.
func TestQuotaCollection_DocumentWriteMeetsTheLimitAKVWriteDoes(t *testing.T) {
	ctx := context.Background()
	out, _ := quotaBundle(t, 64)
	if _, err := out.Collection.CreateCollection(ctx, &service.CreateCollectionRequest{Database: "db", Name: "docs"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := out.KV.Put(ctx, "fill", make([]byte, 60)); err != nil {
		t.Fatalf("fill: %v", err)
	}

	var exceeded *quota.ExceededError
	if err := out.KV.Put(ctx, "more", make([]byte, 20)); !errors.As(err, &exceeded) {
		t.Fatalf("the KV write over the limit must be refused, got %v", err)
	}
	_, err := out.Collection.InsertDocument(ctx, &service.InsertDocumentRequest{
		Database: "db", Collection: "docs", Document: map[string]interface{}{"payload": "0123456789"},
	})
	if !errors.As(err, &exceeded) {
		t.Fatalf("the same bytes as a document must be refused too, got %v", err)
	}
}

// TestQuotaCollection_AccountsBodyBytes walks a document through its life
// and checks the tenant is charged its body bytes and no more.
func TestQuotaCollection_AccountsBodyBytes(t *testing.T) {
	ctx := context.Background()
	out, acct := quotaBundle(t, 1<<20)
	coll := out.Collection
	if _, err := coll.CreateCollection(ctx, &service.CreateCollectionRequest{Database: "db", Name: "docs"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	small := map[string]interface{}{"v": "a"}
	large := map[string]interface{}{"v": "abcdefghij"}
	ins, err := coll.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "db", Collection: "docs", Document: small})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got, want := acct.Usage("t1"), service.DocumentBodyBytes(small); got != want {
		t.Fatalf("after insert usage = %d, want %d", got, want)
	}

	if _, err := coll.UpdateDocument(ctx, &service.UpdateDocumentRequest{Database: "db", Collection: "docs", ID: ins.ID, Document: large}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got, want := acct.Usage("t1"), service.DocumentBodyBytes(large); got != want {
		t.Fatalf("after update usage = %d, want %d (the replaced body is released)", got, want)
	}

	if _, err := coll.UpsertDocument(ctx, &service.UpsertDocumentRequest{Database: "db", Collection: "docs", ID: ins.ID, Document: small}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got, want := acct.Usage("t1"), service.DocumentBodyBytes(small); got != want {
		t.Fatalf("after upsert usage = %d, want %d", got, want)
	}

	if _, err := coll.DeleteDocument(ctx, &service.DeleteDocumentRequest{Database: "db", Collection: "docs", ID: ins.ID}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("after delete usage = %d, want 0", got)
	}

	// A refused insert is released: the collection does not exist.
	if _, err := coll.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "db", Collection: "absent", Document: large}); err == nil {
		t.Fatal("insert into a missing collection must fail")
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("a refused insert must release its reservation, usage = %d", got)
	}

	// A drop frees what its documents held.
	for i := 0; i < 3; i++ {
		doc := map[string]interface{}{"_id": float64(100 + i), "v": "abcdefghij"}
		if _, err := coll.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "db", Collection: "docs", Document: doc}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if err := coll.DropCollection(ctx, &service.DropCollectionRequest{Database: "db", Name: "docs"}); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("after drop usage = %d, want 0", got)
	}
}

// failingCollection is a CollectionServicer whose document writes fail with a
// chosen error, to drive the retain/release rule.
type failingCollection struct {
	service.CollectionServicer
	err error
}

func (f *failingCollection) InsertDocument(context.Context, *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
	return nil, f.err
}

func (f *failingCollection) UpsertDocument(context.Context, *service.UpsertDocumentRequest) (*service.UpsertDocumentResponse, error) {
	return nil, f.err
}

// TestQuotaCollection_UnknownOutcomeRetainsReservation pins the three-way
// rule: a write whose own storage call failed may have stored the document,
// so its bytes stay reserved; a failure decided before the write releases.
func TestQuotaCollection_UnknownOutcomeRetainsReservation(t *testing.T) {
	ctx := context.Background()
	doc := map[string]interface{}{"v": "x"}
	n := service.DocumentBodyBytes(doc)
	for op, retained := range map[string]bool{"insert-document": true, "upsert-document": true, "fence": true, "retract-write": true, "marshal-document": false, "reserve-id": false, "load-metadata": false} {
		acct, _ := newTestAccountant(t, nil)
		q := newQuotaCollectionService(&failingCollection{err: &service.CollectionMetadataError{Op: op, Err: errors.New("x")}}, acct, "t1", 1<<20)
		_, _ = q.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "db", Collection: "c", Document: doc})
		_, _ = q.UpsertDocument(ctx, &service.UpsertDocumentRequest{Database: "db", Collection: "c", ID: 1, Document: doc})
		want := int64(0)
		if retained {
			want = 2 * n
		}
		if got := acct.Usage("t1"); got != want {
			t.Errorf("op %q: usage = %d, want %d", op, got, want)
		}
	}
}

// TestQuotaCollection_SweepForwards pins that the wrapper does not hide the
// sweeper, and that a surface that cannot sweep says so.
func TestQuotaCollection_SweepForwards(t *testing.T) {
	ctx := context.Background()
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaCollectionService(service.NewCollectionService(newMockKVService()), acct, "t1", 1)
	if _, err := q.SweepStuckCollections(ctx, "db", time.Minute); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	q = newQuotaCollectionService(&failingCollection{}, acct, "t1", 1)
	var unsupported *service.CapabilityUnsupportedError
	if _, err := q.SweepStuckCollections(ctx, "db", time.Minute); !errors.As(err, &unsupported) {
		t.Fatalf("expected CapabilityUnsupportedError, got %v", err)
	}
}

func TestApplyQuota_WrapsCollection(t *testing.T) {
	out, _ := quotaBundle(t, 100)
	if _, ok := out.Collection.(*quotaCollectionService); !ok {
		t.Fatalf("expected Collection wrapped with *quotaCollectionService, got %T", out.Collection)
	}
}
