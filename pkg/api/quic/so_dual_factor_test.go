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

// The SO dual-factor gate over a real QUIC connection: real dispatch
// authorization, a REAL rbac.RBACAuthorizer over the REAL built-in roles, the
// REAL SOAuthenticator. The only double is the authenticator, standing in for
// the mTLS handshake.
//
// No build constraint: generateCredentialE2ETLS and credentialE2ERoundTrip
// (handlers_credentials_e2e_test.go) are unconstrained.

package quic

import (
	"crypto/tls"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

const (
	soTestTenant  = tenant.SystemTenantID
	soOtherTenant = "acme"
	soTestPIN     = "correct horse battery staple"
)

type soFixture struct {
	addr      string
	clientCfg *tls.Config
	caller    *authtest.FixedAuthenticator
	barrier   *authtest.Barrier
}

func newSOFixture(t *testing.T, gated bool) *soFixture {
	t.Helper()
	caller := authtest.NewFixedAuthenticator(authtest.SOIdentity(soTestTenant, "officer"))
	roles := authtest.BuiltInRoles(t, soTestTenant, soOtherTenant)
	mw := authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller))
	var gate *authmw.SOGate
	if gated {
		gate = authmw.NewSOGate(roles, authtest.SOVerifier(t, soTestPIN, soTestTenant, soOtherTenant), audit.NewRingBuffer(64), audit.FailClosed, nil)
	}
	barrier := &authtest.Barrier{}
	serverCfg, clientCfg := generateCredentialE2ETLS(t)
	serverCfg.ClientAuth = tls.RequireAnyClientCert
	clientCfg.Certificates = serverCfg.Certificates
	srv := NewServer(nil, nil, "127.0.0.1:0",
		WithTLS(serverCfg), WithAuthMiddleware(mw), WithBarrierService(barrier), WithSOGate(gate))
	if err := srv.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(srv.Stop)
	return &soFixture{addr: srv.Addr(), clientCfg: clientCfg, caller: caller, barrier: barrier}
}

// rekey sends one barrier.Rekey frame as id, with pin in the request's so_pin.
func (f *soFixture) rekey(t *testing.T, id *auth.Identity, pin string) uint16 {
	t.Helper()
	f.caller.Set(id)
	payload, err := proto.Marshal(&pb.BarrierRekeyRequest{Threshold: 2, Total: 3, SoPin: pin})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	status, _ := credentialE2ERoundTrip(t, f.addr, f.clientCfg, "barrier.Rekey", payload)
	return status
}

func TestSODualFactor(t *testing.T) {
	officer := authtest.SOIdentity(soTestTenant, "officer")
	token := authtest.GrantedNonSOIdentity(soTestTenant, "token", permission.SealRekey)
	cases := []struct {
		name   string
		gated  bool
		id     *auth.Identity
		pin    string
		status uint16
		rekeys int64
	}{
		{"officer with PIN is served", true, officer, soTestPIN, wire.StatusOK, 1},
		{"officer without PIN is refused", true, officer, "", wire.StatusUnauthenticated, 0},
		{"officer with wrong PIN is refused", true, officer, "wrong", wire.StatusUnauthenticated, 0},
		{"authorized non-SO caller is refused", true, token, soTestPIN, wire.StatusPermissionDenied, 0},
		// The node barrier is a platform singleton: a tenant's SO with its own
		// tenant's valid PIN must not rekey it (go-qrdb#569 N2).
		{"a tenant's SO is refused on the node barrier", true, authtest.SOIdentity(soOtherTenant, "acme-officer"), soTestPIN, wire.StatusPermissionDenied, 0},
		{"gate off serves the same non-SO caller", false, token, "", wire.StatusOK, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSOFixture(t, tc.gated)
			if got := f.rekey(t, tc.id, tc.pin); got != tc.status {
				t.Fatalf("status = %s, want %s", wire.StatusName(got), wire.StatusName(tc.status))
			}
			if got := f.barrier.Rekeys(); got != tc.rekeys {
				t.Fatalf("rekeys = %d, want %d", got, tc.rekeys)
			}
		})
	}
}

// TestSODualFactor_RecoveryDeleteCarriesThePIN: barrier.RecoveryDelete used to
// take an empty request, so on QUIC it had nowhere to carry the SO PIN and the
// gate refused even the officer. Its request now has so_pin (field 1). The
// payload is encoded by hand so the test reads the same before and after the
// message existed.
func TestSODualFactor_RecoveryDeleteCarriesThePIN(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pin     string
		status  uint16
		deletes int64
	}{
		{"with PIN", soTestPIN, wire.StatusOK, 1},
		{"without PIN", "", wire.StatusUnauthenticated, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSOFixture(t, true)
			var payload []byte
			if tc.pin != "" {
				payload = protowire.AppendTag(nil, 1, protowire.BytesType)
				payload = protowire.AppendString(payload, tc.pin)
			}
			status, _ := credentialE2ERoundTrip(t, f.addr, f.clientCfg, "barrier.RecoveryDelete", payload)
			if status != tc.status {
				t.Fatalf("status = %s, want %s", wire.StatusName(status), wire.StatusName(tc.status))
			}
			if got := f.barrier.RecoveryDeletes(); got != tc.deletes {
				t.Fatalf("deletes = %d, want %d", got, tc.deletes)
			}
		})
	}
}

// TestSOGate_EveryGatedQUICMethodHasACarrier pins soPINCarriers to the method
// registry: every QUIC method whose permission the gate covers has a PIN
// carrier, and every carrier names a gated, registered method. A newly gated
// method fails here until its request carries so_pin.
func TestSOGate_EveryGatedQUICMethodHasACarrier(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	gated := 0
	for method, perm := range srv.methodPerms.Entries() {
		if _, ok := authmw.SOPermissions[perm]; !ok {
			continue
		}
		gated++
		if _, carried := soPINCarriers[method]; !carried {
			t.Errorf("%s (%s) is SO-gated and has no PIN carrier", method, perm)
		}
	}
	for method := range soPINCarriers {
		if perm, ok := srv.methodPerms.Lookup(method); !ok {
			t.Errorf("carrier for unregistered method %s", method)
		} else if _, gatedPerm := authmw.SOPermissions[perm]; !gatedPerm {
			t.Errorf("carrier for %s, whose permission %s is not SO-gated", method, perm)
		}
	}
	if gated == 0 {
		t.Fatal("no SO-gated QUIC method found; the registry was not populated")
	}
}

func TestSOPINFromPayload(t *testing.T) {
	payload, err := proto.Marshal(&pb.CeremonySignCSRRequest{SoPin: "1234"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(soPINFromPayload("ceremony.SignCSR", payload)); got != "1234" {
		t.Fatalf("pin = %q, want 1234", got)
	}
	if soPINFromPayload("barrier.Status", payload) != nil {
		t.Fatal("a method without a carrier yielded a PIN")
	}
	if soPINFromPayload("ceremony.SignCSR", []byte{0xff, 0xff}) != nil {
		t.Fatal("an undecodable payload yielded a PIN")
	}
	empty, _ := proto.Marshal(&pb.BarrierRekeyRequest{})
	if soPINFromPayload("barrier.Rekey", empty) != nil {
		t.Fatal("an empty so_pin yielded a PIN")
	}
}
