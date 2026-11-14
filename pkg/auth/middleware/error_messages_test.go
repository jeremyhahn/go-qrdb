// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
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
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/peer"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// Every refusal names what failed, and the operator-access refusals classify
// as the failure they wrap.
func TestErrorMessages_NameTheFailure(t *testing.T) {
	cause := errors.New("store down")
	for _, tc := range []struct {
		err  error
		want []string
	}{
		{&AwaitingAdminCredentialError{}, []string{"awaiting admin credential", "qrdb init"}},
		{&TenantOwnerUnresolvedError{TenantID: "acme"}, []string{`"acme"`}},
		{&TenantOwnerUnresolvedError{TenantID: "acme", Err: cause}, []string{`"acme"`, "store down"}},
		{&TenantStatusUnavailableError{TenantID: "acme", Err: cause}, []string{"acme", "store down"}},
		{&CertSerialUnresolvableError{Serial: "0a1b"}, []string{"0a1b"}},
		{&FirstInitCertificateMismatchError{TenantID: "acme"}, []string{"acme", "its first admin", "<no peer certificate>"}},
		{&FirstInitCertificateMismatchError{TenantID: "acme", Username: "alice", PresentedThumbprint: "ff00"}, []string{"its first admin alice", "ff00"}},
		{&FirstInitNotOnboardedError{TenantID: "acme"}, []string{"acme", "no first admin"}},
		{&OperatorAccessRefusedError{TenantID: "acme", Policy: tenant.ClusterIsolationPolicy("strict")}, []string{`"acme"`, `"strict"`}},
		{&OperatorAccessUnresolvedError{TenantID: "acme"}, []string{`"acme"`, "unresolved"}},
		{&OperatorAccessUnresolvedError{TenantID: "acme", Err: cause}, []string{"store down"}},
		{&OperatorAccessUnauditedError{TenantID: "acme", Trail: "tenant"}, []string{`"acme"`, "tenant trail"}},
		{&OperatorAccessUnauditedError{TenantID: "acme", Trail: "operator", Err: cause}, []string{"operator trail", "store down"}},
	} {
		msg := tc.err.Error()
		for _, w := range tc.want {
			if !strings.Contains(msg, w) {
				t.Errorf("%T.Error() = %q; want it to contain %q", tc.err, msg, w)
			}
		}
	}
	if !errors.Is(&OperatorAccessRefusedError{}, auth.ErrUnauthorized) {
		t.Error("OperatorAccessRefusedError does not classify as unauthorized")
	}
	if !errors.Is(&OperatorAccessUnresolvedError{Err: cause}, cause) {
		t.Error("OperatorAccessUnresolvedError does not unwrap its cause")
	}
	if !errors.Is(&OperatorAccessUnauditedError{Err: cause}, cause) {
		t.Error("OperatorAccessUnauditedError does not unwrap its cause")
	}
}

// The gRPC remote-address interceptor carries the peer's address into the
// handler's context, and leaves a peerless context unchanged.
func TestRemoteAddrUnaryInterceptor(t *testing.T) {
	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4242}
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: addr})
	_, err := RemoteAddrUnaryInterceptor(ctx, nil, &grpc.UnaryServerInfo{}, func(ctx context.Context, _ any) (any, error) {
		if got, ok := auth.RemoteAddrFromContext(ctx); !ok || got != addr.String() {
			t.Errorf("remote addr = %q, %v; want %q", got, ok, addr.String())
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := auth.RemoteAddrFromContext(InjectGRPCRemoteAddr(context.Background())); ok {
		t.Error("a peerless context gained a remote address")
	}
}

// StrictSeparation reports the configured posture; a nil middleware reports
// the standard one.
func TestStrictSeparation(t *testing.T) {
	var nilMW *AuthMiddleware
	if nilMW.StrictSeparation() {
		t.Error("nil middleware reports strict separation")
	}
	if !(&AuthMiddleware{strictSeparation: true}).StrictSeparation() {
		t.Error("strict middleware reports the standard posture")
	}
}

// A public QUIC method is never permission-mapped, and neither registration
// may be repeated or crossed.
func TestQUICMethodRegistry_RegisterPublic(t *testing.T) {
	r := NewQUICMethodRegistry()
	r.RegisterPublic("health")
	if !r.IsPublic("health") {
		t.Fatal("health is not public")
	}
	if _, ok := r.Lookup("health"); ok {
		t.Fatal("a public method is permission-mapped")
	}
	r.Register("query", "db:read")
	mustPanic(t, "duplicate public", func() { r.RegisterPublic("health") })
	mustPanic(t, "mapped then public", func() { r.RegisterPublic("query") })
	mustPanic(t, "public then mapped", func() { r.Register("health", "db:read") })
}

func mustPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: no panic", name)
		}
	}()
	fn()
}

// A middleware with no public limiter wired admits a public request.
func TestLimitPublicHTTP_UnwiredAdmits(t *testing.T) {
	var nilMW *AuthMiddleware
	r := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	if !nilMW.LimitPublicHTTP(httptest.NewRecorder(), r, "public:health") {
		t.Fatal("nil middleware refused a public request")
	}
	r = r.WithContext(auth.WithRemoteAddr(r.Context(), "192.0.2.1:5555"))
	if !(&AuthMiddleware{}).LimitPublicHTTP(httptest.NewRecorder(), r, "public:health") {
		t.Fatal("unwired limiter refused a public request")
	}
	restRouteNoop(httptest.NewRecorder(), r)
}
