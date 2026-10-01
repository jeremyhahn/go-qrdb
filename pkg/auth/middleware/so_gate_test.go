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
	"errors"
	"net"
	"net/http"
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
)

// soGateMethod is a gRPC method whose (qrdb.v1.permission) option is
// seal:rekey, which is in SOPermissions.
const soGateMethod = "/qrdb.v1.QRDBService/BarrierRekey"

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

// TestSOGate_EnforceWipesThePresentedPIN: the server's copy of the PIN is
// wiped once the gate has decided, whether it admitted the request or
// refused it.
func TestSOGate_EnforceWipesThePresentedPIN(t *testing.T) {
	gate, soRoleID := newTestSOGate(t)
	for _, pinText := range []string{soGatePIN, "wrong"} {
		pinBytes := []byte(pinText)
		ctx := WithSOFactor(officerCtx(soRoleID), SOFactor{PIN: pinBytes, Secure: true})
		_ = gate.Enforce(ctx, permission.SealRekey, "r", "a")
		if !allZero(pinBytes) {
			t.Fatalf("PIN %q survived Enforce", pinText)
		}
	}
	// Refusals that never reach the verifier wipe it too: a caller without
	// the SO role, and a factor that did not arrive over TLS.
	for name, ctx := range map[string]func([]byte) context.Context{
		"not the SO": func(p []byte) context.Context {
			return WithSOFactor(auth.WithIdentity(context.Background(), &auth.Identity{Subject: "x", TenantID: soGateTenant}), SOFactor{PIN: p, Secure: true})
		},
		"not over TLS": func(p []byte) context.Context {
			return WithSOFactor(officerCtx(soRoleID), SOFactor{PIN: p})
		},
	} {
		pinBytes := []byte(soGatePIN)
		if err := gate.Enforce(ctx(pinBytes), permission.SealRekey, "r", "a"); err == nil {
			t.Fatalf("%s: admitted", name)
		}
		if !allZero(pinBytes) {
			t.Fatalf("%s: PIN survived a refusal", name)
		}
	}
}

// TestWipeSOFactor: a transport that bound a factor and did not reach the
// gate -- a request whose permission is not SO-gated -- still wipes it.
func TestWipeSOFactor(t *testing.T) {
	pinBytes := []byte("1234")
	ctx := WithSOFactor(context.Background(), SOFactor{PIN: pinBytes, Secure: true})
	WipeSOFactor(ctx)
	if !allZero(pinBytes) {
		t.Fatal("WipeSOFactor left the PIN in place")
	}
	WipeSOFactor(context.Background()) // no factor bound: no-op
}
