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

package middleware

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// soGateMethod is a gRPC method whose (qrdb.v1.permission) option is
// seal:rekey, which is in SOPermissions.
const soGateMethod = "/qrdb.v1.QRDBService/BarrierRekeyStart"

func newTestSOGate(t *testing.T) (*SOGate, string) {
	t.Helper()
	roles, soRoleID := seededSORoles(t)
	f := newSORealFixture(t)
	return NewSOGate(roles, f.verifier, audit.NewRingBuffer(16), audit.FailClosed, nil), soRoleID
}

func officerCtx(soRoleID string) context.Context {
	return auth.WithIdentity(context.Background(), &auth.Identity{
		Subject: "officer", TenantID: soGateTenant, Roles: []string{soRoleID},
	})
}

func TestSOGate_PermissionOutsideSOPermissionsPasses(t *testing.T) {
	gate, _ := newTestSOGate(t)
	// No identity, no factor: a non-SO permission is not the gate's business.
	if err := gate.Enforce(context.Background(), permission.KVGet, "/kv", "get"); err != nil {
		t.Fatalf("Enforce(kv:get) = %v, want nil", err)
	}
}

func TestSOGate_NoFactorBoundFailsClosed(t *testing.T) {
	gate, soRoleID := newTestSOGate(t)
	err := gate.Enforce(officerCtx(soRoleID), permission.SealRekey, "r", "a")
	var fe *SOFactorError
	if !errors.As(err, &fe) || fe.Reason != SOReasonInsecureTransport {
		t.Fatalf("err = %v, want the insecure-transport refusal", err)
	}
}

func TestSOGate_ErrorsClassifyAsEveryTransportMapsThem(t *testing.T) {
	cases := []struct {
		err   error
		class AuthFailureClass
		http  int
		grpc  codes.Code
	}{
		{&SOFactorError{Reason: SOReasonPINRequired}, AuthFailureUnauthenticated, http.StatusUnauthorized, codes.Unauthenticated},
		{&SORoleRequiredError{}, AuthFailureUnauthorized, http.StatusForbidden, codes.PermissionDenied},
		{&SORoleUnresolvedError{Err: errors.New("store down")}, AuthFailureUnavailable, http.StatusServiceUnavailable, codes.Unavailable},
		{&SOPINUnavailableError{Err: errors.New("sealer wiped")}, AuthFailureUnavailable, http.StatusServiceUnavailable, codes.Unavailable},
	}
	for _, tc := range cases {
		if got := ClassifyAuthError(tc.err); got != tc.class {
			t.Errorf("%T: class %v, want %v", tc.err, got, tc.class)
		}
		if got := HTTPStatus(tc.err, 0); got != tc.http {
			t.Errorf("%T: HTTP %d, want %d", tc.err, got, tc.http)
		}
		if got := status.Code(GRPCError(tc.err, codes.Unknown)); got != tc.grpc {
			t.Errorf("%T: gRPC %v, want %v", tc.err, got, tc.grpc)
		}
		if tc.err.Error() == "" {
			t.Errorf("%T: empty message", tc.err)
		}
	}
	unresolved := &SORoleUnresolvedError{Err: context.DeadlineExceeded}
	if !errors.Is(unresolved, context.DeadlineExceeded) {
		t.Fatal("SORoleUnresolvedError does not unwrap to its cause")
	}
}

// grpcCtx is an incoming gRPC context from a peer over TLS (or not), carrying
// pin in the SO PIN metadata entry when non-empty.
func grpcCtx(base context.Context, tlsPeer bool, pin string) context.Context {
	p := &peer.Peer{Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}}
	if tlsPeer {
		p.AuthInfo = credentials.TLSInfo{}
	}
	ctx := peer.NewContext(base, p)
	if pin != "" {
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(SOPINMetadataKey, pin))
	}
	return ctx
}

func TestSOGateUnaryInterceptor(t *testing.T) {
	gate, soRoleID := newTestSOGate(t)
	intercept := SOGateUnaryInterceptor(gate)
	info := &grpc.UnaryServerInfo{FullMethod: soGateMethod}
	handled := func(context.Context, any) (any, error) { return "ok", nil }

	if _, err := intercept(grpcCtx(officerCtx(soRoleID), true, soGatePIN), nil, info, handled); err != nil {
		t.Fatalf("officer over TLS with PIN: %v", err)
	}
	if _, err := intercept(grpcCtx(officerCtx(soRoleID), false, soGatePIN), nil, info, handled); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("PIN over a plaintext connection: %v, want Unauthenticated", err)
	}
	if _, err := intercept(grpcCtx(officerCtx(soRoleID), true, ""), nil, info, handled); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no PIN: %v, want Unauthenticated", err)
	}
	// An unmapped method is not the gate's business.
	if _, err := intercept(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/x.Y/Z"}, handled); err != nil {
		t.Fatalf("unmapped method: %v", err)
	}
}

type soTestStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *soTestStream) Context() context.Context { return s.ctx }

func TestSOGateStreamInterceptor(t *testing.T) {
	gate, soRoleID := newTestSOGate(t)
	intercept := SOGateStreamInterceptor(gate)
	info := &grpc.StreamServerInfo{FullMethod: soGateMethod}
	var seen context.Context
	handler := func(_ any, ss grpc.ServerStream) error { seen = ss.Context(); return nil }

	if err := intercept(nil, &soTestStream{ctx: grpcCtx(officerCtx(soRoleID), true, soGatePIN)}, info, handler); err != nil {
		t.Fatalf("officer over TLS with PIN: %v", err)
	}
	if soFactorFromContext(seen).PIN == nil {
		t.Fatal("the handler's stream context lost the bound factor")
	}
	other := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "x", TenantID: soGateTenant})
	if err := intercept(nil, &soTestStream{ctx: grpcCtx(other, true, soGatePIN)}, info, handler); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("non-SO caller: %v, want PermissionDenied", err)
	}
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// sealedPIN seals pin the way NewSOFactor does, for a factor a test builds by
// hand to reach a state NewSOFactor never produces.
func sealedPIN(t *testing.T, pin string) *secure.Secret {
	t.Helper()
	s, err := secure.NewSecret([]byte(pin))
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	return s
}

// requireDestroyed fails unless the sealed PIN can no longer be opened.
func requireDestroyed(t *testing.T, what string, s *secure.Secret) {
	t.Helper()
	buf, err := s.Open()
	if err == nil {
		buf.Destroy()
		t.Fatalf("%s: the sealed PIN survived", what)
	}
	var nilSecret *secure.NilSecretError
	if !errors.As(err, &nilSecret) {
		t.Fatalf("%s: Open = %v, want *secure.NilSecretError", what, err)
	}
}

// TestNewSOFactor: the presented PIN is sealed only over TLS, and the
// caller's plaintext is wiped on every path.
func TestNewSOFactor(t *testing.T) {
	defer secure.GuardBorrows(t.Fatalf)()

	pinBytes := []byte(soGatePIN)
	f := NewSOFactor(pinBytes, true, "192.0.2.1:1")
	defer f.Destroy()
	if !allZero(pinBytes) {
		t.Fatal("NewSOFactor left the caller's plaintext in place")
	}
	if !f.Secure || f.ClientIP != "192.0.2.1:1" || f.sealErr != nil {
		t.Fatalf("factor = %+v", f)
	}
	got, err := openSOPIN(f.PIN)
	if err != nil || string(got) != soGatePIN {
		t.Fatalf("openSOPIN = %q, %v; want the presented PIN", got, err)
	}
	secure.Wipe(got)

	for name, tc := range map[string]struct {
		pin      []byte
		isSecure bool
	}{
		"not over TLS": {[]byte(soGatePIN), false},
		"no PIN":       {nil, true},
		"empty PIN":    {[]byte{}, true},
	} {
		f := NewSOFactor(tc.pin, tc.isSecure, "")
		if f.PIN != nil || f.sealErr != nil || f.Secure != tc.isSecure {
			t.Fatalf("%s: factor = %+v, want no PIN held", name, f)
		}
		if !allZero(tc.pin) {
			t.Fatalf("%s: the plaintext was not wiped", name)
		}
	}
}

// TestSOGate_EnforceDestroysThePresentedPIN: the server's sealed PIN is
// destroyed once the gate has decided, whether it admitted the request or
// refused it.
func TestSOGate_EnforceDestroysThePresentedPIN(t *testing.T) {
	defer secure.GuardBorrows(t.Fatalf)()
	gate, soRoleID := newTestSOGate(t)
	for _, pinText := range []string{soGatePIN, "wrong"} {
		f := NewSOFactor([]byte(pinText), true, "")
		_ = gate.Enforce(WithSOFactor(officerCtx(soRoleID), f), permission.SealRekey, "r", "a")
		requireDestroyed(t, "PIN "+pinText, f.PIN)
	}
	// Refusals that never reach the verifier destroy it too: a caller without
	// the SO role, and a factor that did not arrive over TLS.
	notSO := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "x", TenantID: soGateTenant})
	for name, tc := range map[string]struct {
		ctx      context.Context
		isSecure bool
	}{
		"not the SO":   {notSO, true},
		"not over TLS": {officerCtx(soRoleID), false},
	} {
		f := SOFactor{PIN: sealedPIN(t, soGatePIN), Secure: tc.isSecure}
		if err := gate.Enforce(WithSOFactor(tc.ctx, f), permission.SealRekey, "r", "a"); err == nil {
			t.Fatalf("%s: admitted", name)
		}
		requireDestroyed(t, name, f.PIN)
	}
}

// TestWipeSOFactor: a transport that bound a factor and did not reach the
// gate -- a request whose permission is not SO-gated -- still destroys it.
func TestWipeSOFactor(t *testing.T) {
	f := NewSOFactor([]byte("1234"), true, "")
	WipeSOFactor(WithSOFactor(context.Background(), f))
	requireDestroyed(t, "WipeSOFactor", f.PIN)
	WipeSOFactor(WithSOFactor(context.Background(), f)) // destroyed twice: no-op
	WipeSOFactor(context.Background())                  // no factor bound: no-op
}

// TestSOGate_UnholdablePINIsUnavailable: a PIN the node could not seal, or
// can no longer open, is refused as *SOPINUnavailableError -- 503, retryable
// -- and never audited as an invalid PIN: the officer's PIN was never judged.
func TestSOGate_UnholdablePINIsUnavailable(t *testing.T) {
	defer secure.GuardBorrows(t.Fatalf)()
	roles, soRoleID := seededSORoles(t)
	sink := audit.NewRingBuffer(16)
	gate := NewSOGate(roles, newSORealFixture(t).verifier, sink, audit.FailClosed, nil)

	destroyed := sealedPIN(t, soGatePIN)
	destroyed.Destroy()
	sealFailure := errors.New("sealer wiped")
	for name, f := range map[string]SOFactor{
		"seal failed": {Secure: true, sealErr: sealFailure},
		"open failed": {Secure: true, PIN: destroyed},
	} {
		err := gate.Enforce(WithSOFactor(officerCtx(soRoleID), f), permission.SealRekey, "r", "a")
		var unavailable *SOPINUnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("%s: err = %v, want *SOPINUnavailableError", name, err)
		}
		if ClassifyAuthError(err) != AuthFailureUnavailable {
			t.Fatalf("%s: class %v, want unavailable", name, ClassifyAuthError(err))
		}
	}
	if !errors.Is(&SOPINUnavailableError{Err: sealFailure}, sealFailure) {
		t.Fatal("SOPINUnavailableError does not unwrap to its cause")
	}
	if events := sink.Snapshot(); len(events) != 0 {
		t.Fatalf("an unheld PIN was audited: %+v", events)
	}
}

// TestSOFactorFromHTTPRequest: REST and MCP read the SO PIN header only off a
// TLS request.
func TestSOFactorFromHTTPRequest(t *testing.T) {
	defer secure.GuardBorrows(t.Fatalf)()
	r := httptest.NewRequest(http.MethodPost, "https://db.example.com/v1/barrier/rekey", nil)
	r.Header.Set(SOPINHeader, soGatePIN)

	r.TLS = &tls.ConnectionState{}
	f := SOFactorFromHTTPRequest(r)
	defer f.Destroy()
	if !f.Secure || f.ClientIP != r.RemoteAddr {
		t.Fatalf("TLS factor = %+v", f)
	}
	got, err := openSOPIN(f.PIN)
	if err != nil || string(got) != soGatePIN {
		t.Fatalf("TLS factor PIN = %q, %v; want the header", got, err)
	}
	secure.Wipe(got)

	r.TLS = nil
	if f := SOFactorFromHTTPRequest(r); f.Secure || f.PIN != nil {
		t.Fatalf("plaintext factor = %+v, want insecure and no PIN held", f)
	}

	r.TLS = &tls.ConnectionState{}
	r.Header.Del(SOPINHeader)
	if f := SOFactorFromHTTPRequest(r); !f.Secure || f.PIN != nil {
		t.Fatalf("PIN-less TLS factor = %+v, want secure and no PIN", f)
	}
}
