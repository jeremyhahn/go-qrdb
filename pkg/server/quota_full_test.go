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

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// quotaKVService — write accounting, read passthroughs, error release
// ---------------------------------------------------------------------------

func TestQuotaKVService_PutWithModeAccountsAndReleases(t *testing.T) {
	inner := newMockKVService()
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaKVService(inner, acct, "t1", 100)
	ctx := context.Background()

	if err := q.PutWithMode(ctx, "a", make([]byte, 40), writemode.Mode(0)); err != nil {
		t.Fatalf("PutWithMode: %v", err)
	}
	if got := acct.Usage("t1"); got != 40 {
		t.Fatalf("usage = %d, want 40", got)
	}
	// Over-limit write is rejected and never reaches the store.
	err := q.PutWithMode(ctx, "b", make([]byte, 80), writemode.Mode(0))
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("PutWithMode over-limit err = %v, want ExceededError", err)
	}
}

func TestQuotaKVService_PutInnerErrorReleases(t *testing.T) {
	inner := &failingKV{mockKVService: newMockKVService(), putErr: errBoom}
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaKVService(inner, acct, "t1", 1000)
	ctx := context.Background()

	if err := q.Put(ctx, "a", make([]byte, 50)); !errors.Is(err, errBoom) {
		t.Fatalf("Put err = %v, want errBoom", err)
	}
	// The reservation is released because the inner write failed.
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage after failed put = %d, want 0 (released)", got)
	}

	if err := q.PutWithMode(ctx, "a", make([]byte, 50), writemode.Mode(0)); !errors.Is(err, errBoom) {
		t.Fatalf("PutWithMode err = %v, want errBoom", err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage after failed PutWithMode = %d, want 0 (released)", got)
	}
}

func TestQuotaKVService_DeleteWithModeSubtracts(t *testing.T) {
	inner := newMockKVService()
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaKVService(inner, acct, "t1", 1000)
	ctx := context.Background()

	if err := q.Put(ctx, "a", make([]byte, 200)); err != nil {
		t.Fatal(err)
	}
	if err := q.DeleteWithMode(ctx, "a", writemode.Mode(0)); err != nil {
		t.Fatal(err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage after DeleteWithMode = %d, want 0", got)
	}
}

func TestQuotaKVService_DeleteInnerErrorKeepsAccounting(t *testing.T) {
	inner := &failingKV{mockKVService: newMockKVService(), delErr: errBoom}
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaKVService(inner, acct, "t1", 1000)
	ctx := context.Background()

	// priorSize reads via GetStale on the underlying mock store.
	inner.store["a"] = make([]byte, 50)
	acct.Reserve(ctx, "t1", 1000, 50)

	if err := q.Delete(ctx, "a"); !errors.Is(err, errBoom) {
		t.Fatalf("Delete err = %v, want errBoom", err)
	}
	if err := q.DeleteWithMode(ctx, "a", writemode.Mode(0)); !errors.Is(err, errBoom) {
		t.Fatalf("DeleteWithMode err = %v, want errBoom", err)
	}
	// A failed delete must NOT subtract bytes.
	if got := acct.Usage("t1"); got != 50 {
		t.Fatalf("usage after failed delete = %d, want 50 (unchanged)", got)
	}
}

func TestQuotaKVService_EntityAccounting(t *testing.T) {
	inner := newMockKVService()
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaKVService(inner, acct, "t1", 100)
	ctx := context.Background()

	// PutEntity accounts len(req.Data).
	if _, err := q.PutEntity(ctx, &service.PutEntityRequest{ID: "e1", Data: make([]byte, 60)}); err != nil {
		t.Fatalf("PutEntity: %v", err)
	}
	if got := acct.Usage("t1"); got != 60 {
		t.Fatalf("usage after PutEntity = %d, want 60", got)
	}
	// Over-limit PutEntity is rejected.
	_, err := q.PutEntity(ctx, &service.PutEntityRequest{ID: "e2", Data: make([]byte, 80)})
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("PutEntity over-limit err = %v, want ExceededError", err)
	}

	// DeleteEntity subtracts the prior size (read from the store via GetStale).
	inner.store["e1"] = make([]byte, 60)
	if _, err := q.DeleteEntity(ctx, &service.DeleteEntityRequest{ID: "e1"}); err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage after DeleteEntity = %d, want 0", got)
	}
}

func TestQuotaKVService_PutEntityInnerErrorReleases(t *testing.T) {
	inner := &failingKV{mockKVService: newMockKVService(), putEntErr: errBoom}
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaKVService(inner, acct, "t1", 1000)
	ctx := context.Background()

	if _, err := q.PutEntity(ctx, &service.PutEntityRequest{ID: "e", Data: make([]byte, 30)}); !errors.Is(err, errBoom) {
		t.Fatalf("PutEntity err = %v, want errBoom", err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage after failed PutEntity = %d, want 0 (released)", got)
	}
}

func TestQuotaKVService_DeleteEntityInnerError(t *testing.T) {
	inner := &failingKV{mockKVService: newMockKVService(), delEntErr: errBoom}
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaKVService(inner, acct, "t1", 1000)
	if _, err := q.DeleteEntity(context.Background(), &service.DeleteEntityRequest{ID: "e"}); !errors.Is(err, errBoom) {
		t.Fatalf("DeleteEntity err = %v, want errBoom", err)
	}
}

func TestQuotaKVService_ReadPassthroughs(t *testing.T) {
	inner := newMockKVService()
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaKVService(inner, acct, "t1", 1000)
	ctx := context.Background()
	inner.store["k"] = []byte("v")

	if v, err := q.Get(ctx, "k"); err != nil || string(v) != "v" {
		t.Fatalf("Get = %q, %v", v, err)
	}
	if _, err := q.List(ctx, ""); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := q.Scan(ctx, ""); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if v, err := q.GetStale(ctx, "k"); err != nil || string(v) != "v" {
		t.Fatalf("GetStale = %q, %v", v, err)
	}
	if _, err := q.ListStale(ctx, ""); err != nil {
		t.Fatalf("ListStale: %v", err)
	}
	if _, err := q.ScanStale(ctx, ""); err != nil {
		t.Fatalf("ScanStale: %v", err)
	}
	if v, err := q.GetWithConsistency(ctx, "k", consistency.ReadOptions{}); err != nil || string(v) != "v" {
		t.Fatalf("GetWithConsistency = %q, %v", v, err)
	}
	if _, err := q.ListWithConsistency(ctx, "", consistency.ReadOptions{}); err != nil {
		t.Fatalf("ListWithConsistency: %v", err)
	}
	if _, err := q.ScanWithConsistency(ctx, "", consistency.ReadOptions{}); err != nil {
		t.Fatalf("ScanWithConsistency: %v", err)
	}
	if ok, err := q.ExistsWithConsistency(ctx, "k", consistency.ReadOptions{}); err != nil || !ok {
		t.Fatalf("ExistsWithConsistency = %v, %v", ok, err)
	}
	if _, err := q.GetEntity(ctx, &service.GetEntityRequest{ID: "k"}); err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if _, err := q.ListEntities(ctx, &service.ListEntitiesRequest{}); err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
}

// ---------------------------------------------------------------------------
// quotaMVCCService — lease-bearing write accounting + read passthroughs
// ---------------------------------------------------------------------------

func TestQuotaMVCCService_LeaseWriteAccounting(t *testing.T) {
	inner := newMVCCRec()
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaMVCCService(inner, acct, "t1", 100)
	ctx := context.Background()

	if _, err := q.PutWithLease(ctx, "k", make([]byte, 60), 1); err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}
	if got := acct.Usage("t1"); got != 60 {
		t.Fatalf("usage = %d, want 60", got)
	}
	// Over-limit lease write is rejected.
	_, err := q.PutWithLease(ctx, "k2", make([]byte, 80), 1)
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("PutWithLease over-limit err = %v, want ExceededError", err)
	}

	// PutWithLeaseAndMode accounts the value length too.
	if _, err := q.PutWithLeaseAndMode(ctx, "k3", make([]byte, 40), 1, writemode.Mode(0)); err != nil {
		t.Fatalf("PutWithLeaseAndMode: %v", err)
	}
	if got := acct.Usage("t1"); got != 100 {
		t.Fatalf("usage = %d, want 100", got)
	}
}

func TestQuotaMVCCService_LeaseWriteInnerErrorReleases(t *testing.T) {
	inner := newMVCCRec()
	inner.err = errBoom
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaMVCCService(inner, acct, "t1", 1000)
	ctx := context.Background()

	if _, err := q.PutWithLease(ctx, "k", make([]byte, 50), 1); !errors.Is(err, errBoom) {
		t.Fatalf("PutWithLease err = %v, want errBoom", err)
	}
	if _, err := q.PutWithLeaseAndMode(ctx, "k", make([]byte, 50), 1, writemode.Mode(0)); !errors.Is(err, errBoom) {
		t.Fatalf("PutWithLeaseAndMode err = %v, want errBoom", err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage after failed lease writes = %d, want 0 (released)", got)
	}
}

func TestQuotaMVCCService_ReadPassthroughs(t *testing.T) {
	inner := newMVCCRec()
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaMVCCService(inner, acct, "t1", 1000)
	ctx := context.Background()

	if _, err := q.GetRevision(ctx, "k"); err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if _, err := q.GetRevisionWithConsistency(ctx, "k", consistency.ReadOptions{}); err != nil {
		t.Fatalf("GetRevisionWithConsistency: %v", err)
	}
	if _, err := q.GetAtRevision(ctx, "k", 1); err != nil {
		t.Fatalf("GetAtRevision: %v", err)
	}
	if _, err := q.GetAtRevisionWithConsistency(ctx, "k", 1, consistency.ReadOptions{}); err != nil {
		t.Fatalf("GetAtRevisionWithConsistency: %v", err)
	}
	if _, _, err := q.Range(ctx, []byte("a"), []byte("z")); err != nil {
		t.Fatalf("Range: %v", err)
	}
	if _, _, err := q.RangeWithConsistency(ctx, []byte("a"), []byte("z"), consistency.ReadOptions{}); err != nil {
		t.Fatalf("RangeWithConsistency: %v", err)
	}
	if err := q.Compact(ctx, 1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if rev, err := q.CurrentRevision(ctx); err != nil || rev != 42 {
		t.Fatalf("CurrentRevision = %d, %v", rev, err)
	}
}

// ---------------------------------------------------------------------------
// failingKV — a KV mock whose mutating methods can be forced to error.
// ---------------------------------------------------------------------------

type failingKV struct {
	*mockKVService
	putErr    error
	delErr    error
	putEntErr error
	delEntErr error
}

func (m *failingKV) Put(ctx context.Context, key string, value []byte) error {
	if m.putErr != nil {
		return m.putErr
	}
	return m.mockKVService.Put(ctx, key, value)
}
func (m *failingKV) PutWithMode(ctx context.Context, key string, value []byte, mode writemode.Mode) error {
	if m.putErr != nil {
		return m.putErr
	}
	return m.mockKVService.PutWithMode(ctx, key, value, mode)
}
func (m *failingKV) Delete(ctx context.Context, key string) error {
	if m.delErr != nil {
		return m.delErr
	}
	return m.mockKVService.Delete(ctx, key)
}
func (m *failingKV) DeleteWithMode(ctx context.Context, key string, mode writemode.Mode) error {
	if m.delErr != nil {
		return m.delErr
	}
	return m.mockKVService.DeleteWithMode(ctx, key, mode)
}
func (m *failingKV) PutEntity(ctx context.Context, req *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	if m.putEntErr != nil {
		return nil, m.putEntErr
	}
	return m.mockKVService.PutEntity(ctx, req)
}
func (m *failingKV) DeleteEntity(ctx context.Context, req *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	if m.delEntErr != nil {
		return nil, m.delEntErr
	}
	return m.mockKVService.DeleteEntity(ctx, req)
}
