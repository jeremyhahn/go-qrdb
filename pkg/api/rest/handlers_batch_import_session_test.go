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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestHandleBatchImportBegin_Success(t *testing.T) {
	var captured service.ImportOptions
	batchSvc := &mockBatchService{
		importBeginFn: func(_ context.Context, opts service.ImportOptions) (string, error) {
			captured = opts
			return "sess-123", nil
		},
	}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	body := `{"format":"csv","key_field":"id","value_mode":"row","skip_header":true}`
	req := httptest.NewRequest(http.MethodPost, "/batch/import/begin", bytes.NewReader([]byte(body)))
	rr := httptest.NewRecorder()
	srv.handleBatchImportBegin(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp batchImportBeginResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.SessionID != "sess-123" {
		t.Errorf("SessionID = %q, want sess-123", resp.SessionID)
	}
	if captured.Format != "csv" || captured.KeyField != "id" || captured.ValueMode != "row" || !captured.SkipHeader {
		t.Errorf("captured opts = %+v, want format=csv key_field=id value_mode=row skip_header=true", captured)
	}
}

func TestHandleBatchImportBegin_ServiceError(t *testing.T) {
	batchSvc := &mockBatchService{
		importBeginFn: func(context.Context, service.ImportOptions) (string, error) {
			return "", &service.ImportQuotaExceededError{Kind: "sessions", Limit: 1}
		},
	}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := httptest.NewRequest(http.MethodPost, "/batch/import/begin", bytes.NewReader([]byte(`{}`)))
	rr := httptest.NewRecorder()
	srv.handleBatchImportBegin(rr, req)

	if rr.Code == http.StatusOK {
		t.Fatalf("expected a non-200 status, got 200: %s", rr.Body.String())
	}
}

func TestHandleBatchImportAppend_Success(t *testing.T) {
	var gotSession string
	var gotSeq uint64
	var gotChunk []byte
	batchSvc := &mockBatchService{
		importAppendFn: func(_ context.Context, sessionID string, seq uint64, chunk []byte) error {
			gotSession, gotSeq, gotChunk = sessionID, seq, chunk
			return nil
		},
	}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := httptest.NewRequest(http.MethodPost, "/batch/import/sess-123/append?seq=0", bytes.NewReader([]byte("id,v\n1,a\n")))
	rr := httptest.NewRecorder()
	srv.handleBatchImportAppend(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
	if gotSession != "sess-123" {
		t.Errorf("sessionID = %q, want sess-123", gotSession)
	}
	if gotSeq != 0 {
		t.Errorf("seq = %d, want 0", gotSeq)
	}
	if string(gotChunk) != "id,v\n1,a\n" {
		t.Errorf("chunk = %q, want %q", gotChunk, "id,v\n1,a\n")
	}
}

func TestHandleBatchImportAppend_MissingSeq(t *testing.T) {
	batchSvc := &mockBatchService{}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := httptest.NewRequest(http.MethodPost, "/batch/import/sess-123/append", bytes.NewReader([]byte("x")))
	rr := httptest.NewRecorder()
	srv.handleBatchImportAppend(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleBatchImportAppend_WrongVerbInPathRefused(t *testing.T) {
	batchSvc := &mockBatchService{}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	// A path whose trailing segment is not "append" must not be accepted
	// as one -- importSessionIDFromPath's verb check is what is under
	// test here.
	req := httptest.NewRequest(http.MethodPost, "/batch/import/sess-123/commit?seq=0", bytes.NewReader([]byte("x")))
	rr := httptest.NewRecorder()
	srv.handleBatchImportAppend(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleBatchImportCommit_Success(t *testing.T) {
	var gotSession string
	batchSvc := &mockBatchService{
		importCommitFn: func(_ context.Context, sessionID string) (*service.ImportResult, error) {
			gotSession = sessionID
			return &service.ImportResult{Total: 3, Succeeded: 3}, nil
		},
	}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := httptest.NewRequest(http.MethodPost, "/batch/import/sess-123/commit", nil)
	rr := httptest.NewRecorder()
	srv.handleBatchImportCommit(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if gotSession != "sess-123" {
		t.Errorf("sessionID = %q, want sess-123", gotSession)
	}
	var resp batchImportResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 3 || resp.Succeeded != 3 {
		t.Errorf("resp = %+v, want Total=3 Succeeded=3", resp)
	}
}

func TestHandleBatchImportCommit_NotFound(t *testing.T) {
	batchSvc := &mockBatchService{
		importCommitFn: func(context.Context, string) (*service.ImportResult, error) {
			return nil, &service.ImportSessionNotFoundError{SessionID: "sess-123"}
		},
	}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := httptest.NewRequest(http.MethodPost, "/batch/import/sess-123/commit", nil)
	rr := httptest.NewRecorder()
	srv.handleBatchImportCommit(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleBatchImportAbort_Success(t *testing.T) {
	var gotSession string
	batchSvc := &mockBatchService{
		importAbortFn: func(_ context.Context, sessionID string) error {
			gotSession = sessionID
			return nil
		},
	}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := httptest.NewRequest(http.MethodPost, "/batch/import/sess-123/abort", nil)
	rr := httptest.NewRecorder()
	srv.handleBatchImportAbort(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
	if gotSession != "sess-123" {
		t.Errorf("sessionID = %q, want sess-123", gotSession)
	}
}

func TestHandleBatchImportAbort_OwnerMismatchRefused(t *testing.T) {
	batchSvc := &mockBatchService{
		importAbortFn: func(context.Context, string) error {
			return &service.ImportSessionOwnerError{SessionID: "sess-123"}
		},
	}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := httptest.NewRequest(http.MethodPost, "/batch/import/sess-123/abort", nil)
	rr := httptest.NewRecorder()
	srv.handleBatchImportAbort(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestImportSessionIDFromPath(t *testing.T) {
	tests := []struct {
		path, verb string
		wantID     string
		wantOK     bool
	}{
		{"/batch/import/sess-123/append", "append", "sess-123", true},
		{"/batch/import/sess-123/commit", "commit", "sess-123", true},
		{"/batch/import/sess-123/abort", "abort", "sess-123", true},
		{"/batch/import/sess-123/commit", "append", "", false},
		{"/batch/import//append", "append", "", false},
		{"/batch/import/sess-123", "append", "", false},
	}
	for _, tt := range tests {
		id, ok := importSessionIDFromPath(tt.path, tt.verb)
		if id != tt.wantID || ok != tt.wantOK {
			t.Errorf("importSessionIDFromPath(%q, %q) = (%q, %v), want (%q, %v)",
				tt.path, tt.verb, id, ok, tt.wantID, tt.wantOK)
		}
	}
}

func TestHandleBatchImport_NoBatchServiceIsNotImplemented(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0")
	cases := []struct {
		name    string
		handler http.HandlerFunc
		path    string
	}{
		{"begin", srv.handleBatchImportBegin, "/batch/import/begin"},
		{"append", srv.handleBatchImportAppend, "/batch/import/s1/append?seq=0"},
		{"commit", srv.handleBatchImportCommit, "/batch/import/s1/commit"},
		{"abort", srv.handleBatchImportAbort, "/batch/import/s1/abort"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			tc.handler(rr, httptest.NewRequest(http.MethodPost, tc.path, nil))
			if rr.Code != http.StatusNotImplemented {
				t.Fatalf("status = %d, want 501: %s", rr.Code, rr.Body.String())
			}
		})
	}
}

// TestHandleBatchImport_MalformedSessionPathRefused proves a path whose verb
// does not match the handler, or whose session segment is empty, is refused
// before the service is consulted.
func TestHandleBatchImport_MalformedSessionPathRefused(t *testing.T) {
	called := false
	batchSvc := &mockBatchService{
		importAppendFn: func(context.Context, string, uint64, []byte) error { called = true; return nil },
		importCommitFn: func(context.Context, string) (*service.ImportResult, error) {
			called = true
			return &service.ImportResult{}, nil
		},
		importAbortFn: func(context.Context, string) error { called = true; return nil },
	}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))
	cases := []struct {
		name    string
		handler http.HandlerFunc
		path    string
	}{
		{"append empty session", srv.handleBatchImportAppend, "/batch/import//append?seq=0"},
		{"commit wrong verb", srv.handleBatchImportCommit, "/batch/import/s1/abort"},
		{"abort no verb", srv.handleBatchImportAbort, "/batch/import/s1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			tc.handler(rr, httptest.NewRequest(http.MethodPost, tc.path, nil))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rr.Code, rr.Body.String())
			}
		})
	}
	if called {
		t.Fatal("service reached despite a malformed session path")
	}
}

func TestHandleBatchImportBegin_MalformedBodyRefused(t *testing.T) {
	batchSvc := &mockBatchService{
		importBeginFn: func(context.Context, service.ImportOptions) (string, error) {
			t.Fatal("ImportBegin reached with a malformed body")
			return "", nil
		},
	}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))
	rr := httptest.NewRecorder()
	srv.handleBatchImportBegin(rr, httptest.NewRequest(http.MethodPost, "/batch/import/begin", bytes.NewBufferString(`{"format":`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// failingReader fails every Read, standing in for a client that drops the
// connection mid-body.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestHandleBatchImportAppend_BodyReadErrorRefused(t *testing.T) {
	batchSvc := &mockBatchService{
		importAppendFn: func(context.Context, string, uint64, []byte) error {
			t.Fatal("ImportAppend reached with an unreadable body")
			return nil
		},
	}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))
	rr := httptest.NewRecorder()
	srv.handleBatchImportAppend(rr, httptest.NewRequest(http.MethodPost, "/batch/import/s1/append?seq=0", failingReader{}))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestHandleBatchImportAppend_SequenceErrorMapsTo400(t *testing.T) {
	batchSvc := &mockBatchService{
		importAppendFn: func(_ context.Context, id string, seq uint64, _ []byte) error {
			return &service.ImportSequenceError{SessionID: id, Got: seq, Want: 1}
		},
	}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))
	rr := httptest.NewRecorder()
	srv.handleBatchImportAppend(rr, httptest.NewRequest(http.MethodPost, "/batch/import/s1/append?seq=5", bytes.NewBufferString("k,v\n")))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleBatchImportCommit_ReportsRowErrors(t *testing.T) {
	batchSvc := &mockBatchService{
		importCommitFn: func(context.Context, string) (*service.ImportResult, error) {
			return &service.ImportResult{
				Total: 3, Succeeded: 1, Failed: 2,
				Errors: []service.ImportError{{Row: 2, Key: "b", Message: "bad value"}, {Row: 3, Message: "missing key"}},
			}, nil
		},
	}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))
	rr := httptest.NewRecorder()
	srv.handleBatchImportCommit(rr, httptest.NewRequest(http.MethodPost, "/batch/import/s1/commit", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	var resp batchImportResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 3 || resp.Succeeded != 1 || resp.Failed != 2 || len(resp.Errors) != 2 {
		t.Fatalf("response = %+v", resp)
	}
	if resp.Errors[0] != (batchImportErrorJSON{Row: 2, Key: "b", Message: "bad value"}) {
		t.Fatalf("errors[0] = %+v", resp.Errors[0])
	}
}
