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

// No build constraint: newMockKVService, mockAdminService, tempSocketPath,
// selfSignedTLSConfig and dialUnixTLS are unconstrained (see
// tenant_isolation_test.go).

package unix

import (
	"testing"

	"google.golang.org/grpc"

	"github.com/jeremyhahn/go-qrdb/pkg/api/grpc/grpctest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// TestClusterAdminScope: a tenant's Admin or SO may not add or remove a
// replica, request a snapshot or drain the node over the Unix socket.
func TestClusterAdminScope(t *testing.T) {
	grpctest.ClusterAdminScope(t, func(t *testing.T, mw *authmw.AuthMiddleware, admin *authtest.Admin) *grpc.ClientConn {
		t.Helper()
		admin.AdminServicer = &mockAdminService{}
		socketPath := tempSocketPath(t)
		tlsCfg := selfSignedTLSConfig(t)
		srv := NewServer(newMockKVService(), admin, socketPath, WithTLS(tlsCfg), WithAuthMiddleware(mw))
		if err := srv.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(srv.Stop)
		conn := dialUnixTLS(t, socketPath, tlsCfg)
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	})
}
