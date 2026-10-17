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

package rest

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// closedStoreErrors are the shapes a closed store arrives in at this layer.
// The service wraps it on the way up — ReadError on a read, ProposeError on a
// write, BatchError inside a batch — and each of those wrappers has its own
// arm in mapHTTPError that would claim the error first if the closed-store
// rule did not sit above them.
var closedStoreErrors = map[string]error{
	"bare sentinel":       storage.ErrStorageClosed,
	"storage ClosedError": &storage.ClosedError{},
	"storage OpError":     &storage.OpError{Op: "set", Err: &storage.ClosedError{}},
	"storage BatchError":  &storage.BatchError{Op: "commit", Err: &storage.ClosedError{}},
	"backend closed":      &backend.BackendClosedError{},
	"service ReadError":   &service.ReadError{Err: &storage.OpError{Op: "get", Err: &storage.ClosedError{}}},
	"service ProposeError": &service.ProposeError{
		Err: &storage.BatchError{Op: "commit", Err: &storage.ClosedError{}},
	},
	"service BatchError": &service.BatchError{
		Err: &storage.BatchError{Op: "commit", Err: &storage.ClosedError{}},
	},
}

// mapHTTPError is the single table every REST verb but two consults, so the
// closed-store rule belongs in it rather than at one call site. It must beat
// the wrapper arms below it — service.BatchError, for one, answers 500, and
// the first cut of this rule lived only in writeProposeFailure and therefore
// reached writes alone (go-qrdb#371).
func TestMapHTTPError_ClosedStorageIsServiceUnavailable(t *testing.T) {
	for name, err := range closedStoreErrors {
		t.Run(name, func(t *testing.T) {
			if got := mapHTTPError(err); got != http.StatusServiceUnavailable {
				t.Fatalf("mapHTTPError = %d, want %d", got, http.StatusServiceUnavailable)
			}
		})
	}
}

// The rule must not widen: an error that merely travels in the same wrapper
// as a closed store keeps the status it had, or the predicate has become a
// catch-all and a genuine fault reads as an orderly shutdown.
func TestMapHTTPError_ClosedStorageRuleDoesNotWiden(t *testing.T) {
	cases := map[string]struct {
		err  error
		want int
	}{
		"key not found":         {&service.KeyNotFoundError{Key: "k"}, http.StatusNotFound},
		"unrelated storage op":  {&storage.OpError{Op: "set", Err: storage.ErrKeyNotFound}, http.StatusInternalServerError},
		"service batch failure": {&service.BatchError{Err: storage.ErrEmptyKey}, http.StatusInternalServerError},
		"not leader":            {&service.NotLeaderError{ShardID: 1}, http.StatusServiceUnavailable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := mapHTTPError(tc.err); got != tc.want {
				t.Fatalf("mapHTTPError = %d, want %d", got, tc.want)
			}
		})
	}
}

// The write path reads its closed-store status from mapHTTPError rather than
// repeating the number.
func TestWriteProposeFailure_ClosedStorageIsServiceUnavailable(t *testing.T) {
	for name, err := range closedStoreErrors {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeProposeFailure(rec, err)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
			}
		})
	}
}

// The closed-store rule must not swallow the two classifications that already
// sit on the write path: an unencodable request is still 400, and an
// unrecognised failure is still 500.
func TestWriteProposeFailure_ClosedStorageRuleLeavesTheOthersAlone(t *testing.T) {
	rec := httptest.NewRecorder()
	writeProposeFailure(rec, unencodableKeyError())
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unencodable request: status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	rec = httptest.NewRecorder()
	writeProposeFailure(rec, &storage.OpError{Op: "set", Err: storage.ErrKeyNotFound})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("unrelated storage failure: status = %d, want %d",
			rec.Code, http.StatusInternalServerError)
	}
}

// writeReadFailure is the read path's counterpart. A read meeting a dying node
// is the same fact as a write meeting one, so it gets the same status.
func TestWriteReadFailure_ClosedStorageIsServiceUnavailable(t *testing.T) {
	for name, err := range closedStoreErrors {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeReadFailure(rec, err)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
			}
		})
	}
}

// Every other read failure keeps the 500 and the ReadError wrapper it had.
// The helper is narrow on purpose: only a closed store and an unknown leader
// (not_leader_failure_test.go) move to 503, the same two on both verbs.
func TestWriteReadFailure_LeavesOtherReadFailuresAt500(t *testing.T) {
	for name, err := range map[string]error{
		"unrelated": &service.ReadError{Err: storage.ErrEmptyKey},
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeReadFailure(rec, err)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
			}
			if !strings.Contains(rec.Body.String(), "read") {
				t.Errorf("body = %q, want it to keep the ReadError wrapper", rec.Body.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// End to end through the handlers, which is the assertion that matters: a
// helper can be right while the handler that should call it hardcodes 500.
// handleKVGet, handleKVList and handleKVScan each did.
// ---------------------------------------------------------------------------

// closedStoreReadError is what KVService.translateReadError produces when the
// host's read met a closed store.
func closedStoreReadError() error {
	return &service.ReadError{Err: &storage.OpError{Op: "get", Err: &storage.ClosedError{}}}
}

// closedStoreWriteError is what KVService.translateProposeError produces when
// the apply met a closed store.
func closedStoreWriteError() error {
	return &service.ProposeError{Err: &storage.BatchError{Op: "commit", Err: &storage.ClosedError{}}}
}

// TestKVEndpointsAnswerAClosedStoreWith503 drives every KV verb against a
// service whose store has been closed and asserts they agree.
//
// Before this, PUT answered 503 and GET answered 500 — the write/read
// asymmetry go-qrdb#371 exists to remove.
func TestKVEndpointsAnswerAClosedStoreWith503(t *testing.T) {
	t.Run("GET /kv/{key}", func(t *testing.T) {
		kvSvc := &captureKVService{}
		kvSvc.getFn = func(context.Context, string) ([]byte, error) { return nil, closedStoreReadError() }
		srv := newCaptureSrv(kvSvc, nil)

		rec := httptest.NewRecorder()
		srv.handleKVGet(rec, httptest.NewRequest(http.MethodGet, "/kv/k", nil), "k")
		assertStatus(t, rec, http.StatusServiceUnavailable)
	})

	t.Run("GET /kv/list", func(t *testing.T) {
		kvSvc := &captureKVService{}
		kvSvc.listFn = func(context.Context, string) ([]string, error) { return nil, closedStoreReadError() }
		srv := newCaptureSrv(kvSvc, nil)

		rec := httptest.NewRecorder()
		srv.handleKVList(rec, httptest.NewRequest(http.MethodGet, "/kv/list?prefix=p", nil))
		assertStatus(t, rec, http.StatusServiceUnavailable)
	})

	t.Run("GET /kv/scan", func(t *testing.T) {
		kvSvc := &captureKVService{}
		kvSvc.scanFn = func(context.Context, string) (map[string][]byte, error) {
			return nil, closedStoreReadError()
		}
		srv := newCaptureSrv(kvSvc, nil)

		rec := httptest.NewRecorder()
		srv.handleKVScan(rec, httptest.NewRequest(http.MethodGet, "/kv/scan?prefix=p", nil))
		assertStatus(t, rec, http.StatusServiceUnavailable)
	})

	t.Run("PUT /kv/{key}", func(t *testing.T) {
		kvSvc := &captureKVService{}
		kvSvc.putFn = func(context.Context, string, []byte) error { return closedStoreWriteError() }
		srv := newCaptureSrv(kvSvc, nil)

		rec := httptest.NewRecorder()
		srv.handleKVPut(rec, httptest.NewRequest(http.MethodPut, "/kv/k", bytes.NewReader([]byte("v"))), "k")
		assertStatus(t, rec, http.StatusServiceUnavailable)
	})

	t.Run("PUT /kv/{key}?lease-id", func(t *testing.T) {
		mvccSvc := &captureMVCCService{}
		mvccSvc.putWithLeaseFn = func(context.Context, string, []byte, int64) (*kv.KeyValue, error) {
			return nil, closedStoreWriteError()
		}
		srv := newCaptureSrv(&captureKVService{}, mvccSvc)

		rec := httptest.NewRecorder()
		srv.handleKVPut(rec,
			httptest.NewRequest(http.MethodPut, "/kv/k?lease-id=1", bytes.NewReader([]byte("v"))), "k")
		assertStatus(t, rec, http.StatusServiceUnavailable)
	})

	t.Run("DELETE /kv/{key}", func(t *testing.T) {
		kvSvc := &captureKVService{}
		kvSvc.deleteFn = func(context.Context, string) error { return closedStoreWriteError() }
		srv := newCaptureSrv(kvSvc, nil)

		rec := httptest.NewRecorder()
		srv.handleKVDelete(rec, httptest.NewRequest(http.MethodDelete, "/kv/k", nil), "k")
		assertStatus(t, rec, http.StatusServiceUnavailable)
	})

	t.Run("POST /batch", func(t *testing.T) {
		batchSvc := &mockBatchService{
			executeFn: func(context.Context, []service.Op) ([]service.OpResponse, error) {
				return nil, &service.BatchError{
					Err: &storage.BatchError{Op: "commit", Err: &storage.ClosedError{}},
				}
			},
		}
		srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithBatch(batchSvc))

		body := `{"ops":[{"type":"put","key":"k1","value":"v1"}]}`
		rec := httptest.NewRecorder()
		srv.dispatch(rec, httptest.NewRequest(http.MethodPost, "/batch", strings.NewReader(body)))
		assertStatus(t, rec, http.StatusServiceUnavailable)
	})
}
