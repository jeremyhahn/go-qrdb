//go:build !integration

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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestVectorHandlers_ShardIDRefused asserts that a vector request body
// carrying shard_id is refused as an unknown field before the service sees
// it: the database's binding decides the vector shard (OD-14 A), and the
// vector bodies decode strictly as database create does (OD-13).
func TestVectorHandlers_ShardIDRefused(t *testing.T) {
	called := false
	srv := newVectorServer(&mockVectorService{
		createNamespaceFn: func(_ context.Context, _ *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
			called = true
			return &service.CreateNamespaceResponse{Success: true}, nil
		},
		upsertVectorFn: func(_ context.Context, _ *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
			called = true
			return &service.UpsertVectorResponse{Success: true}, nil
		},
	})
	cases := []struct {
		name string
		path string
		body string
	}{
		{"namespace create", "/vector/namespace", `{"namespace":"ns","dimension":3,"shard_id":7}`},
		{"upsert", "/vector/upsert", `{"namespace":"ns","id":1,"vector":[1.0,2.0,3.0],"shard_id":7}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			srv.dispatch(rec, req)
			assertStatus(t, rec, http.StatusBadRequest)
			if !strings.Contains(rec.Body.String(), `unknown field \"shard_id\"`) {
				t.Fatalf("body = %s, want an unknown field \"shard_id\" refusal", rec.Body.String())
			}
			if called {
				t.Fatal("the service was called for a body carrying shard_id")
			}
		})
	}
}
