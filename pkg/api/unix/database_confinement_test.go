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

// No cross-tenant database access, gRPC over the Unix socket. The scenarios
// live in grpctest, shared with the TCP transport.

package unix

import (
	"testing"

	"google.golang.org/grpc"

	"github.com/jeremyhahn/go-qrdb/pkg/api/grpc/grpctest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

func TestDatabaseConfinement(t *testing.T) {
	grpctest.DatabaseConfinement(t, func(t *testing.T, mw *authmw.AuthMiddleware, tenants tenant.TenantServicer, db service.DatabaseServicer) *grpc.ClientConn {
		t.Helper()
		socketPath := tempSocketPath(t)
		tlsCfg := selfSignedTLSConfig(t)
		srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath,
			WithTLS(tlsCfg), WithAuthMiddleware(mw),
			WithTenantService(tenants), WithDatabaseService(db))
		if err := srv.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(srv.Stop)
		conn := dialUnixTLS(t, socketPath, tlsCfg)
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	})
}
