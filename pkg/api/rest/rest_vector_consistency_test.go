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

//go:build !integration

package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Vector Get with valid consistency level
// ---------------------------------------------------------------------------

func TestHandleVectorGet_WithConsistency_Success(t *testing.T) {
	var captured *service.GetVectorRequest
	vecSvc := &mockVectorService{
		getVectorFn: func(_ context.Context, req *service.GetVectorRequest) (*service.GetVectorResponse, error) {
			captured = req
			return &service.GetVectorResponse{
				ID:     req.ID,
				Vector: []float32{1.0, 2.0},
				Found:  true,
			}, nil
		},
	}
	srv := newVectorServer(vecSvc)

	req := httptest.NewRequest(http.MethodGet, "/vector/get?namespace=ns&id=1&consistency=2", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if captured == nil {
		t.Fatal("GetVector was not called")
	}
	if captured.Consistency != 2 {
		t.Fatalf("consistency = %d, want 2", captured.Consistency)
	}
}

func TestHandleVectorGet_InvalidConsistency_NonNumeric(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})

	req := httptest.NewRequest(http.MethodGet, "/vector/get?namespace=ns&id=1&consistency=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}
