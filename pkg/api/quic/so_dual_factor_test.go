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
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

const (
	soTestTenant  = tenantid.System
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
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles), authmw.WithAuthenticator(caller))
	var gate *authmw.SOGate
	if gated {
		gate = authmw.NewSOGate(roles, authtest.SOVerifier(t, soTestPIN, soTestTenant, soOtherTenant), audit.NewRingBuffer(64), audit.FailClosed, nil)
	}
	barrier := &authtest.Barrier{PINs: authtest.BarrierPINBackend(t, soTestPIN)}
	serverCfg, clientCfg := generateCredentialE2ETLS(t)
	serverCfg.ClientAuth = tls.RequireAnyClientCert
	clientCfg.Certificates = serverCfg.Certificates
	srv := NewServer(nil, nil, "127.0.0.1:0",
		WithTLS(serverCfg), WithAuthMiddleware(mw), WithBarrierService(barrier), WithSOGate(gate),
		WithPINBackend(authtest.BarrierPINBackend(t, soTestPIN)))
	if err := srv.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(srv.Stop)
	return &soFixture{addr: srv.Addr(), clientCfg: clientCfg, caller: caller, barrier: barrier}
}

// rekey sends one barrier.RekeyStart frame as id, with pin in the request's so_pin.
func (f *soFixture) rekey(t *testing.T, id *auth.Identity, pin string) uint16 {
	t.Helper()
	f.caller.Set(id)
	payload, err := proto.Marshal(&pb.BarrierRekeyStartRequest{Domain: "unseal", Threshold: 2, SoPin: pin})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	status, _ := credentialE2ERoundTrip(t, f.addr, f.clientCfg, "barrier.RekeyStart", payload)
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
		// With the gate off the barrier's own SO PIN check still applies, so the
		// request carries the PIN; the caller's lack of the SO role is what the
		// gate would have refused.
		{"gate off serves the same non-SO caller", false, token, soTestPIN, wire.StatusOK, 1},
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
	empty, _ := proto.Marshal(&pb.BarrierRekeyStartRequest{})
	if soPINFromPayload("barrier.RekeyStart", empty) != nil {
		t.Fatal("an empty so_pin yielded a PIN")
	}
}

// TestSOPINFromPayload_CopiesOnlyThePIN: from a change-passphrase request the
// gate copies the PIN and nothing else. Its one allocation is the returned
// PIN, so no copy of Current or Next exists for the gate to leave unwiped,
// and the PIN does not alias the payload the handler decodes next.
func TestSOPINFromPayload_CopiesOnlyThePIN(t *testing.T) {
	payload, err := proto.Marshal(&pb.BarrierChangePassphraseRequest{
		Current: []byte("current-passphrase"),
		Next:    []byte("next-passphrase"),
		SoPin:   "1234",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	pin := soPINFromPayload("barrier.ChangePassphrase", payload)
	if string(pin) != "1234" {
		t.Fatalf("pin = %q, want 1234", pin)
	}
	clear(pin)
	var decoded pb.BarrierChangePassphraseRequest
	if err := proto.Unmarshal(payload, &decoded); err != nil || decoded.GetSoPin() != "1234" {
		t.Fatalf("wiping the returned PIN changed the payload: %v, %q", err, decoded.GetSoPin())
	}
	if allocs := testing.AllocsPerRun(100, func() {
		soPINFromPayload("barrier.ChangePassphrase", payload)
	}); allocs != 1 {
		t.Fatalf("allocations = %v, want 1 (the PIN copy alone)", allocs)
	}
}

// TestSOPINFromPayload_WireRules: the scan reads the payload as
// proto.Unmarshal would, and yields no PIN wherever Unmarshal would fail.
func TestSOPINFromPayload_WireRules(t *testing.T) {
	const method = "barrier.ChangePassphrase"
	valid, err := proto.Marshal(&pb.BarrierChangePassphraseRequest{Current: []byte("c"), SoPin: "1234"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	pinField := soPINCarriers[method].Fields().ByName(soPINField).Number()
	unknown := protowire.AppendVarint(protowire.AppendTag(append([]byte(nil), valid...), 999, protowire.VarintType), 7)
	if got := string(soPINFromPayload(method, unknown)); got != "1234" {
		t.Fatalf("an unknown field hid the PIN: %q", got)
	}
	appendPIN := func(typ protowire.Type) []byte {
		return protowire.AppendTag(append([]byte(nil), valid...), pinField, typ)
	}
	cases := []struct {
		name    string
		payload []byte
		want    string // "" means no PIN
	}{
		{"unknown field", unknown, "1234"},
		{"repeated so_pin, the last wins", protowire.AppendString(appendPIN(protowire.BytesType), "5678"), "5678"},
		{"so_pin of another wire type is an unknown field", protowire.AppendVarint(appendPIN(protowire.VarintType), 1), "1234"},
		{"truncated", valid[:len(valid)-1], ""},
		{"bad tag", append(append([]byte(nil), valid...), 0x80), ""},
		{"truncated so_pin", appendPIN(protowire.BytesType), ""},
		{"not UTF-8", protowire.AppendBytes(protowire.AppendTag(nil, pinField, protowire.BytesType), []byte{0xff, 0xfe}), ""},
	}
	for _, tc := range cases {
		if got := string(soPINFromPayload(method, tc.payload)); got != tc.want {
			t.Errorf("%s: pin = %q, want %q", tc.name, got, tc.want)
		}
		var decoded pb.BarrierChangePassphraseRequest
		if err := proto.Unmarshal(tc.payload, &decoded); err == nil && decoded.GetSoPin() != tc.want || err != nil && tc.want != "" {
			t.Errorf("%s: proto.Unmarshal reads pin %q (err %v), the scan %q", tc.name, decoded.GetSoPin(), err, tc.want)
		}
	}
}

// TestSODualFactor_GateWithoutAuthFailsClosed: with nothing to authenticate
// the caller there is no SO to admit, so the gate refuses rather than being
// skipped (go-qrdb#569 N5).
func TestSODualFactor_GateWithoutAuthFailsClosed(t *testing.T) {
	roles := authtest.BuiltInRoles(t, soTestTenant)
	gate := authmw.NewSOGate(roles, authtest.SOVerifier(t, soTestPIN, soTestTenant), audit.NewRingBuffer(64), audit.FailClosed, nil)
	barrier := &authtest.Barrier{PINs: authtest.BarrierPINBackend(t, soTestPIN)}
	serverCfg, clientCfg := generateCredentialE2ETLS(t)
	serverCfg.ClientAuth = tls.RequireAnyClientCert
	clientCfg.Certificates = serverCfg.Certificates
	srv := NewServer(nil, nil, "127.0.0.1:0", WithTLS(serverCfg), WithBarrierService(barrier), WithSOGate(gate),
		WithPINBackend(authtest.BarrierPINBackend(t, soTestPIN)))
	if err := srv.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(srv.Stop)
	payload, err := proto.Marshal(&pb.BarrierRekeyStartRequest{Domain: "unseal", Threshold: 2, SoPin: soTestPIN})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	status, _ := credentialE2ERoundTrip(t, srv.Addr(), clientCfg, "barrier.RekeyStart", payload)
	if barrier.Rekeys() != 0 {
		t.Fatal("the gate was skipped for want of an auth middleware")
	}
	if status != wire.StatusPermissionDenied {
		t.Fatalf("status = %s, want %s", wire.StatusName(status), wire.StatusName(wire.StatusPermissionDenied))
	}
}
