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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// The header's database is bound as the canonical tenant.DatabaseFromContext
// scope too, so context-scoped services (the RAG store) see the same database
// on REST as on QUIC and MCP.
func TestScopeMiddleware_BindsCanonicalDatabaseScope(t *testing.T) {
	cases := map[string]string{"analytics": "analytics", "": tenant.DefaultDatabase}
	for header, want := range cases {
		var canonical string
		handler := scopeMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			canonical = tenant.DatabaseFromContext(r.Context())
		}))
		req := httptest.NewRequest(http.MethodGet, "/kv/key", nil)
		if header != "" {
			req.Header.Set(HeaderDatabase, header)
		}
		handler.ServeHTTP(httptest.NewRecorder(), req)
		if canonical != want {
			t.Errorf("header %q: tenant.DatabaseFromContext = %q, want %q", header, canonical, want)
		}
	}
}
