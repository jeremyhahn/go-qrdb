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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// moverMVCCService is an MVCC service that can move a prefix: it validates
// the request as the service does, records it, and answers rev or err.
type moverMVCCService struct {
	mockMVCCService
	rev int64
	err error
	got *service.MovePrefixRequest
}

func (m *moverMVCCService) MovePrefix(_ context.Context, req *service.MovePrefixRequest) (int64, error) {
	if err := service.ValidateMovePrefix(req); err != nil {
		return 0, err
	}
	if m.err != nil {
		return 0, m.err
	}
	m.got = req
	return m.rev, nil
}

func postMovePrefix(t *testing.T, svc service.MVCCKVServicer, body string) *httptest.ResponseRecorder {
	t.Helper()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(svc))
	req := httptest.NewRequest(http.MethodPost, "/kv/move-prefix", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	return rec
}

func TestMovePrefix_ReturnsTheRevision(t *testing.T) {
	svc := &moverMVCCService{rev: 42}
	// src "db/stage/", dst "db/pages/", a put of the binary value 00 ff and a
	// mod_revision compare, every byte field base64.
	rec := postMovePrefix(t, svc, `{"src":"ZGIvc3RhZ2Uv","dst":"ZGIvcGFnZXMv","expected_keys":2,`+
		`"ops":[{"type":"put","key":"ZGIvbWV0YQ==","value":"AP8="}],`+
		`"compares":[{"key":"ZGIvbWV0YQ==","target":"mod_revision","result":"equal","mod_revision":7}]}`)
	assertStatus(t, rec, http.StatusOK)

	var resp movePrefixResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Revision != 42 {
		t.Fatalf("revision = %d, want 42", resp.Revision)
	}
	if svc.got == nil || svc.got.Src != "db/stage/" || svc.got.Dst != "db/pages/" || svc.got.ExpectedKeys != 2 ||
		len(svc.got.Ops) != 1 || svc.got.Ops[0].Type != service.OpPut || string(svc.got.Ops[0].Value) != "\x00\xff" ||
		len(svc.got.Compares) != 1 || svc.got.Compares[0].Target != service.CompareModRevision || svc.got.Compares[0].ModRevision != 7 {
		t.Fatalf("service got %+v", svc.got)
	}
}

func TestMovePrefix_CountMismatchIsAConflict(t *testing.T) {
	svc := &moverMVCCService{err: &service.CommandConflictError{Message: "live count 3, expected 2"}}
	rec := postMovePrefix(t, svc, `{"src":"YS8=","dst":"Yi8=","expected_keys":2}`)
	assertStatus(t, rec, http.StatusConflict)
	if svc.got != nil {
		t.Fatal("a refused move recorded a write")
	}
}

func TestMovePrefix_InvalidRequestIsInvalidArgument(t *testing.T) {
	// dst "a/b/" lies under src "a/".
	rec := postMovePrefix(t, &moverMVCCService{}, `{"src":"YS8=","dst":"YS9iLw=="}`)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestMovePrefix_UnknownOpTypeIsInvalidArgument(t *testing.T) {
	rec := postMovePrefix(t, &moverMVCCService{}, `{"src":"YS8=","dst":"Yi8=","ops":[{"type":"upsert","key":"Yw=="}]}`)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestMovePrefix_InnerWithoutMoverIsUnimplemented(t *testing.T) {
	rec := postMovePrefix(t, &mockMVCCService{}, `{"src":"YS8=","dst":"Yi8="}`)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestMovePrefix_BadBody(t *testing.T) {
	rec := postMovePrefix(t, &moverMVCCService{}, `{`)
	assertStatus(t, rec, http.StatusBadRequest)
}
