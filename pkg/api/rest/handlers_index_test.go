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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestHandleIndexList_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodGet, "/index/list", nil)
	rec := httptest.NewRecorder()
	s.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleIndexList_Success(t *testing.T) {
	svc := &mockIndexService{
		listIndexesFn: func(_ context.Context) ([]string, error) {
			return []string{"by-name", "by-age"}, nil
		},
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0", WithIndex(svc))
	req := httptest.NewRequest(http.MethodGet, "/index/list", nil)
	rec := httptest.NewRecorder()
	s.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp indexListResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Names) != 2 || resp.Names[0] != "by-name" || resp.Names[1] != "by-age" {
		t.Errorf("names = %v, want [by-name by-age]", resp.Names)
	}
}

func TestHandleIndexList_ServiceError(t *testing.T) {
	svc := &mockIndexService{
		listIndexesFn: func(_ context.Context) ([]string, error) {
			return nil, &service.IndexNotFoundError{Name: "idx"}
		},
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0", WithIndex(svc))
	req := httptest.NewRequest(http.MethodGet, "/index/list", nil)
	rec := httptest.NewRecorder()
	s.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}
