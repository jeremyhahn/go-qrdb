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

// Sealing the node barrier over a real QUIC connection: the configured
// barrier SO PIN is required -- the handler used to ignore the request body,
// so the PIN was skipped -- and a tenant's Admin is refused.
//
// No build constraint: generateCredentialE2ETLS and credentialE2ERoundTrip
// (handlers_credentials_e2e_test.go) are unconstrained.

package quic

import (
	"crypto/tls"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

const sealTestPIN = "barrier-so-pin-1234"

// sealOverQUIC seals as id, with soPIN in the request's so_pin field (field
// 1, a string; built from a well-known type so the test reads the same
// against the request message and the empty body it replaced).
func sealOverQUIC(t *testing.T, id *auth.Identity, soPIN string) (uint16, *authtest.Barrier) {
	t.Helper()
	caller := authtest.NewFixedAuthenticator(id)
	roles := authtest.BuiltInRoles(t, tenant.SystemTenantID, "acme")
	mw := authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller))
	barrier := &authtest.Barrier{}
	serverCfg, clientCfg := generateCredentialE2ETLS(t)
	serverCfg.ClientAuth = tls.RequireAnyClientCert
	clientCfg.Certificates = serverCfg.Certificates
	srv := NewServer(nil, nil, "127.0.0.1:0", WithTLS(serverCfg), WithAuthMiddleware(mw),
		WithBarrierService(barrier), WithPINBackend(authtest.BarrierPINBackend(t, sealTestPIN)))
	if err := srv.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(srv.Stop)
	var payload []byte
	if soPIN != "" {
		var err error
		if payload, err = proto.Marshal(wrapperspb.String(soPIN)); err != nil {
			t.Fatalf("marshal: %v", err)
		}
	}
	status, _ := credentialE2ERoundTrip(t, srv.Addr(), clientCfg, "barrier.Seal", payload)
	return status, barrier
}

func TestBarrierSeal(t *testing.T) {
	operator := authtest.AdminIdentity(tenant.SystemTenantID, "root")
	cases := []struct {
		name   string
		id     *auth.Identity
		pin    string
		status uint16
		seals  int64
	}{
		{"seal with the barrier SO PIN is served", operator, sealTestPIN, wire.StatusOK, 1},
		{"seal without the barrier SO PIN is refused", operator, "", wire.StatusUnauthenticated, 0},
		{"seal with a wrong barrier SO PIN is refused", operator, "wrong", wire.StatusUnauthenticated, 0},
		{"a tenant's Admin may not seal the node barrier", authtest.AdminIdentity("acme", "acme-admin"), sealTestPIN, wire.StatusPermissionDenied, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, barrier := sealOverQUIC(t, tc.id, tc.pin)
			if got := barrier.Seals(); got != tc.seals {
				t.Fatalf("seals = %d, want %d", got, tc.seals)
			}
			if status != tc.status {
				t.Fatalf("status = %s, want %s", wire.StatusName(status), wire.StatusName(tc.status))
			}
		})
	}
}
