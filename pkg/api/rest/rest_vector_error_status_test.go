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

// TestVectorHandlers_ServiceErrorsReachTheirStatus is go-qrdb#331's
// verification on REST: each condition the vector service now reports as a
// typed error reaches a caller as its status instead of 500. The service half
// — that VectorService really returns these types — is pinned in pkg/service;
// the other transports are pinned by pkg/api/errclass's parity test.
func TestVectorHandlers_ServiceErrorsReachTheirStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		svc    *mockVectorService
		method string
		path   string
		body   string
		want   int
	}{
		{
			name: "missing namespace",
			svc: &mockVectorService{getNamespaceInfoFn: func(context.Context, *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
				return nil, &service.NamespaceNotFoundError{Namespace: "ns"}
			}},
			method: http.MethodGet, path: "/vector/namespace/info?namespace=ns",
			want: http.StatusNotFound,
		},
		{
			name: "dimension mismatch",
			svc: &mockVectorService{upsertVectorFn: func(context.Context, *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
				return nil, &service.DimensionMismatchError{Expected: 3, Actual: 2}
			}},
			method: http.MethodPost, path: "/vector/upsert",
			body: `{"namespace":"ns","id":1,"vector":[1.0,2.0]}`,
			want: http.StatusBadRequest,
		},
		{
			name: "duplicate namespace",
			svc: &mockVectorService{createNamespaceFn: func(context.Context, *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
				return nil, &service.NamespaceExistsError{Namespace: "ns"}
			}},
			method: http.MethodPost, path: "/vector/namespace",
			body: `{"namespace":"ns","dimension":3}`,
			want: http.StatusConflict,
		},
		{
			name: "strict isolation, no tenant",
			svc: &mockVectorService{createNamespaceFn: func(context.Context, *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
				return nil, &service.TenantRequiredError{Op: "CreateNamespace"}
			}},
			method: http.MethodPost, path: "/vector/namespace",
			body: `{"namespace":"ns","dimension":3}`,
			want: http.StatusForbidden,
		},
		{
			name: "malformed request",
			svc: &mockVectorService{createNamespaceFn: func(context.Context, *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
				return nil, &service.InvalidVectorError{Reason: "dimension must be greater than 0"}
			}},
			method: http.MethodPost, path: "/vector/namespace",
			body: `{"namespace":"ns","dimension":3}`,
			want: http.StatusBadRequest,
		},
		{
			name: "closed service",
			svc: &mockVectorService{getNamespaceInfoFn: func(context.Context, *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
				return nil, &service.VectorServiceClosedError{}
			}},
			method: http.MethodGet, path: "/vector/namespace/info?namespace=ns",
			want: http.StatusServiceUnavailable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newVectorServer(tc.svc)
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			srv.dispatch(rec, req)
			assertStatus(t, rec, tc.want)
		})
	}
}
