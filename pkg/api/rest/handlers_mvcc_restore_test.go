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
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestHandleRestoreToRevision_Success(t *testing.T) {
	var captured *service.RestoreRevisionRequest
	mvccSvc := &mockMVCCService{
		restoreFn: func(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
			captured = req
			return &service.RestoreRevisionResult{
				Revision: 5,
				Changes: []service.RestoreKeyChange{
					{Key: "a", Action: service.RestoreActionRevert, FromRevision: 9, ToRevision: 5, ValueBytes: 3},
					{Key: "b", Action: service.RestoreActionRecreate, ToRevision: 5, ValueBytes: 1},
					{Key: "c", Action: service.RestoreActionDelete, FromRevision: 9},
				},
				Applied: 3,
				Skipped: 1,
			}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	prefixB64 := base64.StdEncoding.EncodeToString([]byte("prefix"))
	body := `{"revision":5,"prefix":"` + prefixB64 + `"}`
	req := httptest.NewRequest(http.MethodPost, "/kv/restore", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp restoreRevisionResponse
	decodeJSON(t, rec, &resp)
	if resp.Revision != 5 {
		t.Fatalf("revision = %d, want 5", resp.Revision)
	}
	if resp.Applied != 3 || resp.Skipped != 1 {
		t.Fatalf("applied=%d skipped=%d, want applied=3 skipped=1", resp.Applied, resp.Skipped)
	}
	if len(resp.Changes) != 3 {
		t.Fatalf("len(changes) = %d, want 3", len(resp.Changes))
	}
	if !bytes.Equal(resp.Changes[0].Key, []byte("a")) || resp.Changes[0].Action != service.RestoreActionRevert {
		t.Fatalf("unexpected first change: %+v", resp.Changes[0])
	}
	if resp.Changes[1].Action != service.RestoreActionRecreate {
		t.Fatalf("unexpected second change action: %v", resp.Changes[1].Action)
	}
	if resp.Changes[2].Action != service.RestoreActionDelete {
		t.Fatalf("unexpected third change action: %v", resp.Changes[2].Action)
	}

	// Binary round-trip: the base64 prefix must decode to the exact bytes
	// the service layer receives.
	if captured == nil || captured.Prefix != "prefix" {
		t.Fatalf("expected service prefix %q, got %+v", "prefix", captured)
	}
}

func TestHandleRestoreToRevision_DryRun(t *testing.T) {
	var captured *service.RestoreRevisionRequest
	mvccSvc := &mockMVCCService{
		restoreFn: func(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
			captured = req
			return &service.RestoreRevisionResult{
				Revision: 5,
				Changes:  []service.RestoreKeyChange{{Key: "a", Action: service.RestoreActionRevert, ToRevision: 5}},
				DryRun:   true,
			}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	prefixB64 := base64.StdEncoding.EncodeToString([]byte("prefix"))
	body := `{"revision":5,"prefix":"` + prefixB64 + `","dry_run":true}`
	req := httptest.NewRequest(http.MethodPost, "/kv/restore", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if captured == nil || !captured.DryRun {
		t.Fatal("expected service to receive DryRun=true")
	}

	var resp restoreRevisionResponse
	decodeJSON(t, rec, &resp)
	if !resp.DryRun {
		t.Fatal("expected dry_run=true in response")
	}
	if resp.Applied != 0 {
		t.Fatalf("applied = %d, want 0 on a dry run", resp.Applied)
	}
}

func TestHandleRestoreToRevision_ForceWithConflicts(t *testing.T) {
	var captured *service.RestoreRevisionRequest
	mvccSvc := &mockMVCCService{
		restoreFn: func(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
			captured = req
			return &service.RestoreRevisionResult{
				Revision: 5,
				Changes: []service.RestoreKeyChange{
					{Key: "a", Action: service.RestoreActionRevert, FromRevision: 9, ToRevision: 5},
					{Key: "b", Action: service.RestoreActionRevert, FromRevision: 9, ToRevision: 5},
				},
				Conflicts: []service.RestoreKeyChange{
					{Key: "b", Action: service.RestoreActionRevert, FromRevision: 9, ToRevision: 5},
				},
				Applied:    1,
				Conflicted: 1,
			}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	keyA := base64.StdEncoding.EncodeToString([]byte("a"))
	keyB := base64.StdEncoding.EncodeToString([]byte("b"))
	body := `{"revision":5,"keys":["` + keyA + `","` + keyB + `"],"force":true}`
	req := httptest.NewRequest(http.MethodPost, "/kv/restore", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if captured == nil || !captured.Force {
		t.Fatal("expected service to receive Force=true")
	}
	if len(captured.Keys) != 2 || captured.Keys[0] != "a" || captured.Keys[1] != "b" {
		t.Fatalf("expected service keys [a b], got %v", captured.Keys)
	}

	var resp restoreRevisionResponse
	decodeJSON(t, rec, &resp)
	if resp.Applied != 1 || resp.Conflicted != 1 {
		t.Fatalf("applied=%d conflicted=%d, want applied=1 conflicted=1", resp.Applied, resp.Conflicted)
	}
	if len(resp.Conflicts) != 1 || !bytes.Equal(resp.Conflicts[0].Key, []byte("b")) {
		t.Fatalf("expected conflict on key %q, got %+v", "b", resp.Conflicts)
	}
}

func TestHandleRestoreToRevision_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPost, "/kv/restore", strings.NewReader(`{"revision":1}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleRestoreToRevision_BadBody(t *testing.T) {
	mvccSvc := &mockMVCCService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodPost, "/kv/restore", strings.NewReader("not-json"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// TestHandleRestoreToRevision_GuardErrors pins the HTTP status each restore
// guard error maps to.
func TestHandleRestoreToRevision_GuardErrors(t *testing.T) {
	errSentinel := errors.New("sentinel")

	tests := []struct {
		name string
		err  error
		want int
	}{
		{"ScopeEmpty", &service.RestoreScopeEmptyError{}, http.StatusBadRequest},
		{"ScopeAmbiguous", &service.RestoreScopeAmbiguousError{}, http.StatusBadRequest},
		{"RevisionInvalid", &service.RestoreRevisionInvalidError{Revision: -1}, http.StatusBadRequest},
		{"RevisionFuture", &service.RestoreRevisionFutureError{Requested: 99, Current: 5}, http.StatusBadRequest},
		{"ScopeTooLarge", &service.RestoreScopeTooLargeError{Limit: 10000, Scope: "prefix"}, http.StatusBadRequest},
		{"UnboundedPrefix", &service.RestoreUnboundedPrefixError{Prefix: "\xff"}, http.StatusBadRequest},
		{"InternalKey", &service.RestoreInternalKeyError{Prefix: "__sys:"}, http.StatusForbidden},
		{"ChangeIndexUnavailable", &service.RestoreChangeIndexUnavailableError{Err: errSentinel}, http.StatusNotImplemented},
		{"BackendRequired", &service.MVCCBackendRequiredError{Err: errSentinel}, http.StatusNotImplemented},
		{"RevisionCompacted", &service.RevisionCompactedError{Requested: 1, Compacted: 5}, http.StatusRequestedRangeNotSatisfiable},
		{"ApplyError", &service.RestoreApplyError{Key: "a", Action: service.RestoreActionRevert, Applied: 1, Err: errSentinel}, http.StatusInternalServerError},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mvccSvc := &mockMVCCService{
				restoreFn: func(_ context.Context, _ *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
					return nil, tc.err
				},
			}
			srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

			req := httptest.NewRequest(http.MethodPost, "/kv/restore", strings.NewReader(`{"revision":1,"prefix":"a2V5"}`))
			rec := httptest.NewRecorder()
			srv.dispatch(rec, req)

			assertStatus(t, rec, tc.want)
		})
	}
}

// TestHandleRestoreToRevision_BinaryRoundTrip proves an arbitrary byte-valued
// key (including invalid UTF-8) survives the base64 wire encoding exactly.
func TestHandleRestoreToRevision_BinaryRoundTrip(t *testing.T) {
	binaryKey := []byte{0x00, 0xff, 0xfe, 'k', 0x80, 0x81}
	var captured *service.RestoreRevisionRequest
	mvccSvc := &mockMVCCService{
		restoreFn: func(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
			captured = req
			return &service.RestoreRevisionResult{Revision: 1}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	payload := restoreRevisionRequest{
		Revision: 1,
		Keys:     [][]byte{binaryKey},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/kv/restore", bytes.NewReader(data))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if captured == nil || len(captured.Keys) != 1 || captured.Keys[0] != string(binaryKey) {
		t.Fatalf("expected service to receive binary key %q, got %+v", binaryKey, captured)
	}

	var resp restoreRevisionResponse
	decodeJSON(t, rec, &resp)
}
