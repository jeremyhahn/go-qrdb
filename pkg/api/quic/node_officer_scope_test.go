// Copyright 2026 Jeremy Hahn
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

// The node barrier's Security Officer registry over a real QUIC connection:
// real dispatch authorization and a REAL rbac.RBACAuthorizer over the REAL
// built-in roles. The only double is the authenticator, standing in for the
// mTLS handshake.
//
// No build constraint: generateCredentialE2ETLS and credentialE2ERoundTrip
// (handlers_credentials_e2e_test.go) are unconstrained.

package quic

import (
	"crypto/tls"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
)

// TestNodeOfficerScope: the node barrier is a platform singleton, so only the
// system tenant may register its unseal or recovery officers or list them. A
// tenant's SO, and a tenant Admin its own tenant granted the officer
// permissions, are refused before the barrier service is reached.
func TestNodeOfficerScope(t *testing.T) {
	register, err := proto.Marshal(&pb.BarrierOfficerRegisterRequest{Username: "planted", CsrPem: []byte("csr")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ops := []struct {
		method  string
		payload []byte
		hits    func(b *authtest.Barrier) int64
	}{
		{"barrier.OfficerRegister", register, (*authtest.Barrier).OfficerRegistrations},
		{"barrier.RecoveryOfficerRegister", register, (*authtest.Barrier).RecoveryOfficerRegistrations},
		{"barrier.OfficerList", nil, (*authtest.Barrier).OfficerReads},
		{"barrier.OfficerClaimStatus", nil, (*authtest.Barrier).OfficerReads},
	}
	for _, op := range ops {
		roles := authtest.BuiltInRoles(t, tenantid.System, authtest.NodeOfficerTenant)
		for _, c := range authtest.NodeOfficerCallers(t, roles) {
			t.Run(op.method+"/"+c.Name, func(t *testing.T) {
				caller := authtest.NewFixedAuthenticator(c.Identity)
				barrier := &authtest.Barrier{}
				serverCfg, clientCfg := generateCredentialE2ETLS(t)
				serverCfg.ClientAuth = tls.RequireAnyClientCert
				clientCfg.Certificates = serverCfg.Certificates
				srv := NewServer(nil, nil, "127.0.0.1:0", WithTLS(serverCfg),
					WithAuthMiddleware(authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller))),
					WithBarrierService(barrier))
				if err := srv.Start(); err != nil {
					t.Fatalf("start server: %v", err)
				}
				t.Cleanup(srv.Stop)

				status, _ := credentialE2ERoundTrip(t, srv.Addr(), clientCfg, op.method, op.payload)
				want, calls := uint16(wire.StatusPermissionDenied), int64(0)
				if c.Admitted {
					want, calls = wire.StatusOK, 1
				}
				if status != want {
					t.Fatalf("status = %s, want %s", wire.StatusName(status), wire.StatusName(want))
				}
				if got := op.hits(barrier); got != calls {
					t.Fatalf("barrier calls = %d, want %d", got, calls)
				}
			})
		}
	}
}
