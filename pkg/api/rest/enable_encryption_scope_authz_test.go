// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rest

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestHandleBarrierEnableEncryption_AuthorizesPerScope drives
// POST /v1/barrier/enable-encryption through the full handler chain, real
// RBAC included, for every caller in authtest.EnableEncryptionScopeCases
// (go-qrdb#791): the node's barrier needs seal:enable_encryption, a named
// tenant's barrier needs tenant_barrier:enable_encryption for that tenant. A
// refused call answers 403 and never reaches the migrator; an admitted one
// reaches it with the scope the body named.
func TestHandleBarrierEnableEncryption_AuthorizesPerScope(t *testing.T) {
	for _, c := range authtest.EnableEncryptionScopeCases() {
		t.Run(c.Name, func(t *testing.T) {
			roles := authtest.BuiltInRoles(t)
			mig := &captureMigrator{ch: make(chan migrateCall, 1)}
			srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
				WithAuthMiddleware(authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles),
					authmw.WithAuthenticator(authtest.NewFixedAuthenticator(c.Caller)))),
				WithBarrierService(&mockBarrierService{}), WithEncryptionMigrator(mig))
			body, err := json.Marshal(map[string]any{"tenant_id": c.TenantID, "strategy": "passphrase", "passphrase": []byte("pw")})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/barrier/enable-encryption", strings.NewReader(string(body)))
			req.TLS = &tls.ConnectionState{}
			rec := httptest.NewRecorder()
			srv.server.Handler.ServeHTTP(rec, req)

			if !c.Admit {
				if rec.Code != http.StatusForbidden {
					t.Fatalf("status = %d body=%q, want 403", rec.Code, rec.Body.String())
				}
				if n := len(mig.ch); n != 0 {
					t.Fatalf("a refused call reached the migrator %d time(s)", n)
				}
				return
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%q, want 200", rec.Code, rec.Body.String())
			}
			want := service.BarrierScope{Kind: service.ScopeSystem}
			if c.TenantID != "" {
				want = service.BarrierScope{Kind: service.ScopeTenant, TenantID: c.TenantID}
			}
			select {
			case call := <-mig.ch:
				if call.scope != want {
					t.Fatalf("migrator scope = %+v, want %+v", call.scope, want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("an admitted call did not reach the migrator")
			}
		})
	}
}
