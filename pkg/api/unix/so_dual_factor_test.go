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

// The SO dual-factor gate over the Unix socket. The scenarios live in
// grpctest, shared with the gRPC/TCP transport.
//
// No build constraint: newMockKVService, mockAdminService, tempSocketPath,
// selfSignedTLSConfig and dialUnixTLS are unconstrained (see
// tenant_isolation_test.go).

package unix

import (
	"testing"

	"google.golang.org/grpc"

	"github.com/jeremyhahn/go-qrdb/pkg/api/grpc/grpctest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestSODualFactor(t *testing.T) {
	grpctest.SODualFactor(t, startSOServer)
}

// TestNodeOfficerScope: a tenant's identity may not register or list the
// node barrier's Security Officers over the Unix socket.
func TestNodeOfficerScope(t *testing.T) {
	grpctest.NodeOfficerScope(t, startSOServer)
}

// startSOServer is the grpctest.SOStartFunc for the Unix socket.
func startSOServer(t *testing.T, mw *authmw.AuthMiddleware, gate *authmw.SOGate, barrier service.BarrierServicer) *grpc.ClientConn {
	t.Helper()
	socketPath := tempSocketPath(t)
	tlsCfg := selfSignedTLSConfig(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath,
		WithTLS(tlsCfg), WithAuthMiddleware(mw), WithBarrierService(barrier), WithSOGate(gate))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	conn := dialUnixTLS(t, socketPath, tlsCfg)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}
