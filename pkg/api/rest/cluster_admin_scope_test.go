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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestClusterAdminScope: the cluster-membership routes name no tenant and act
// on shards every tenant shares. The built-in Admin and SO roles grant them in
// every tenant, so under ScopeSelf any tenant's Admin could remove a replica
// from the system shard and break quorum for everyone, or drain the node. Only
// the system tenant's holders may use them.
func TestClusterAdminScope(t *testing.T) {
	ops := []struct{ method, path, body string }{
		{http.MethodPost, "/membership/add", `{"shard_id":1,"replica_id":9,"address":"127.0.0.1:1"}`},
		{http.MethodPost, "/membership/remove", `{"shard_id":1,"replica_id":2}`},
		{http.MethodPost, "/snapshot", `{"shard_id":1}`},
		{http.MethodPost, "/drain", ``},
		// The node keyspace's compaction policy: the platform's, and its
		// compaction discards every tenant's raw history on the node.
		{http.MethodGet, "/admin/node-compaction", ``},
		{http.MethodPut, "/admin/node-compaction", `{"compaction":{"mode":"revision","retainRevisions":10}}`},
	}
	roles := authtest.BuiltInRoles(t, tenantid.System, authtest.ClusterAdminTenant)
	for _, op := range ops {
		for _, c := range authtest.ClusterAdminCallers(t) {
			t.Run(op.method+" "+op.path+"/"+c.Name, func(t *testing.T) {
				caller := authtest.NewFixedAuthenticator(c.Identity)
				admin := &authtest.Admin{AdminServicer: &mockAdminService{}}
				srv := NewServer(nil, admin, "127.0.0.1:0",
					WithAuthMiddleware(authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller))))
				req := httptest.NewRequest(op.method, op.path, strings.NewReader(op.body))
				req.TLS = &tls.ConnectionState{}
				rec := httptest.NewRecorder()
				srv.server.Handler.ServeHTTP(rec, req)

				want, calls := http.StatusForbidden, int64(0)
				if c.Admitted {
					want, calls = http.StatusOK, 1
				}
				if rec.Code != want {
					t.Fatalf("status=%d body=%q, want %d", rec.Code, rec.Body.String(), want)
				}
				if got := admin.ClusterChanges(); got != calls {
					t.Fatalf("admin calls = %d, want %d", got, calls)
				}
			})
		}
	}
}
