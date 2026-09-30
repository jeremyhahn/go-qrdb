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

package embedded

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

var errResolve = errors.New("resolution refused")

// recordingRegistry resolves every engine for every (tenant, database) to
// one test double and records each resolution. With fail set it refuses
// every resolution instead.
type recordingRegistry struct {
	mu    sync.Mutex
	fail  bool
	asked []string
}

func (r *recordingRegistry) note(tenantID, db string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, tenantID+"/"+db)
	if r.fail {
		return errResolve
	}
	return nil
}

func (r *recordingRegistry) KVService(tn, db string) (service.KVServicer, error) {
	return newMockKVService(), r.note(tn, db)
}
func (r *recordingRegistry) MVCCService(tn, db string) (service.MVCCKVServicer, error) {
	return &mockMVCCService{}, r.note(tn, db)
}
func (r *recordingRegistry) WatchService(tn, db string) (service.WatchServicer, error) {
	return &mockWatchService{}, r.note(tn, db)
}
func (r *recordingRegistry) LeaseService(tn, db string) (service.LeaseServicer, error) {
	return &mockLeaseService{}, r.note(tn, db)
}
func (r *recordingRegistry) IndexService(tn, db string) (service.IndexServicer, error) {
	return &mockIndexService{}, r.note(tn, db)
}
func (r *recordingRegistry) TimeSeriesService(tn, db string) (service.TimeSeriesServicer, error) {
	return &mockTSService{}, r.note(tn, db)
}
func (r *recordingRegistry) VectorService(tn, db string) (service.VectorServicer, error) {
	return &mockVectorService{}, r.note(tn, db)
}
func (r *recordingRegistry) TransactionService(tn, db string) (service.TransactionServicer, error) {
	return &mockTxService{}, r.note(tn, db)
}
func (r *recordingRegistry) BatchService(tn, db string) (service.BatchServicer, error) {
	return &fullBatchService{}, r.note(tn, db)
}
func (r *recordingRegistry) TableService(tn, db string) (service.TableServicer, error) {
	return nil, r.note(tn, db)
}
func (r *recordingRegistry) SequenceService(tn, db string) (service.SequenceServicer, error) {
	return nil, r.note(tn, db)
}
func (r *recordingRegistry) PrivilegeService(tn, db string) (service.PrivilegeServicer, error) {
	return nil, r.note(tn, db)
}
func (r *recordingRegistry) CollectionService(tn, db string) (service.CollectionServicer, error) {
	return &mockCollectionService{}, r.note(tn, db)
}
func (r *recordingRegistry) LockService(tn, db string) (service.LockServicer, error) {
	return &mockLockService{}, r.note(tn, db)
}

var _ service.Registry = (*recordingRegistry)(nil)

// scopedAdapters returns every data-plane adapter over reg for database db.
func scopedAdapters(reg service.Registry, db string) map[string]any {
	s := &databaseScope{registry: reg, database: db}
	return map[string]any{
		"kv": &scopedKV{scope: s}, "mvcc": &scopedMVCC{scope: s}, "transaction": &scopedTransaction{scope: s},
		"batch": &scopedBatch{scope: s}, "watch": &scopedWatch{scope: s}, "lease": &scopedLease{scope: s},
		"index": &scopedIndex{scope: s}, "timeseries": &scopedTimeSeries{scope: s}, "vector": &scopedVector{scope: s},
		"collection": &scopedCollection{scope: s}, "lock": &scopedLock{scope: s},
	}
}

// callWithZeroArgs calls method with the context first and zero values for
// every other parameter, recovering a panic in the resolved double (whose
// behaviour for zero arguments is not what these tests are about).
func callWithZeroArgs(ctx context.Context, m reflect.Value) (out []reflect.Value) {
	typ := m.Type()
	args := make([]reflect.Value, typ.NumIn())
	for i := range args {
		args[i] = reflect.Zero(typ.In(i))
	}
	if typ.NumIn() > 0 && typ.In(0) == reflect.TypeOf((*context.Context)(nil)).Elem() {
		args[0] = reflect.ValueOf(ctx)
	}
	defer func() { _ = recover() }()
	if typ.IsVariadic() {
		return m.CallSlice(args)
	}
	return m.Call(args)
}

// Every adapter method resolves its engine for (the context's tenant, the
// scoped database) -- the no-context time-series calls refuse instead, since
// they cannot know the tenant -- and, when the registry refuses, returns that refusal instead of touching
// any service.
func TestScopedAdapters_ResolvePerCallAndPropagateRefusal(t *testing.T) {
	ctx := tenant.WithTenantID(context.Background(), "acme")
	for _, fail := range []bool{false, true} {
		reg := &recordingRegistry{fail: fail}
		for name, adapter := range scopedAdapters(reg, "ledger") {
			v := reflect.ValueOf(adapter)
			for i := 0; i < v.NumMethod(); i++ {
				method := v.Type().Method(i).Name
				before := len(reg.asked)
				out := callWithZeroArgs(ctx, v.Method(i))
				takesCtx := v.Method(i).Type().NumIn() > 0 && v.Method(i).Type().In(0) == reflect.TypeOf((*context.Context)(nil)).Elem()
				if !takesCtx {
					// Without a context the caller's tenant is unknown, so
					// the call is refused rather than resolved for a
					// guessed one.
					if len(reg.asked) != before {
						t.Errorf("%s.%s resolved %v without a context", name, method, reg.asked[before:])
					}
					if err, _ := out[len(out)-1].Interface().(error); !errors.Is(err, errScopedNeedsContext) {
						t.Errorf("%s.%s returned %v, want the needs-context refusal", name, method, err)
					}
					continue
				}
				if len(reg.asked) != before+1 {
					t.Fatalf("%s.%s resolved %d times, want once", name, method, len(reg.asked)-before)
				}
				if got := reg.asked[len(reg.asked)-1]; got != "acme/ledger" {
					t.Errorf("%s.%s resolved %q, want acme/ledger", name, method, got)
				}
				if fail {
					if len(out) == 0 {
						t.Fatalf("%s.%s panicked instead of returning the refusal", name, method)
					}
					if err, _ := out[len(out)-1].Interface().(error); !errors.Is(err, errResolve) {
						t.Errorf("%s.%s returned %v, want the registry's refusal", name, method, err)
					}
				}
			}
		}
	}
}

// RAG calls carry the scoped database in the context.
func TestScopedRAG_BindsDatabase(t *testing.T) {
	inner := &ragDatabaseRecorder{}
	s := &scopedRAG{inner: inner, database: "ledger"}
	ctx := context.Background()
	_, _ = s.IngestDocument(ctx, &service.IngestDocumentRequest{})
	_, _ = s.Query(ctx, &service.RAGQueryRequest{})
	_, _ = s.DeleteDocument(ctx, &service.DeleteRAGDocumentRequest{})
	_, _ = s.ListDocuments(ctx, &service.ListRAGDocumentsRequest{})
	if len(inner.seen) != 4 {
		t.Fatalf("inner saw %d calls, want 4", len(inner.seen))
	}
	for _, db := range inner.seen {
		if db != "ledger" {
			t.Fatalf("inner saw database %q, want ledger", db)
		}
	}
	if err := s.Close(); err != nil || inner.closed {
		t.Fatalf("Close = %v, closed inner = %v; a scoped transport must not close the node's RAG service", err, inner.closed)
	}
}

// ragDatabaseRecorder records the database each call's context carries.
type ragDatabaseRecorder struct {
	seen   []string
	closed bool
}

func (r *ragDatabaseRecorder) IngestDocument(ctx context.Context, _ *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
	r.seen = append(r.seen, tenant.DatabaseFromContext(ctx))
	return &service.IngestDocumentResponse{}, nil
}

func (r *ragDatabaseRecorder) Query(ctx context.Context, _ *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
	r.seen = append(r.seen, tenant.DatabaseFromContext(ctx))
	return &service.RAGQueryResponse{}, nil
}

func (r *ragDatabaseRecorder) DeleteDocument(ctx context.Context, _ *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
	r.seen = append(r.seen, tenant.DatabaseFromContext(ctx))
	return &service.DeleteRAGDocumentResponse{}, nil
}

func (r *ragDatabaseRecorder) ListDocuments(ctx context.Context, _ *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
	r.seen = append(r.seen, tenant.DatabaseFromContext(ctx))
	return &service.ListRAGDocumentsResponse{}, nil
}

func (r *ragDatabaseRecorder) Close() error {
	r.closed = true
	return nil
}

var _ service.RAGServicer = (*ragDatabaseRecorder)(nil)
