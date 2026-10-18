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

package grpc

import (
	"context"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// countingAuthenticator refuses every caller and counts how often it was
// asked: a call it sees is a call that reached authentication, and so the
// user store behind it.
type countingAuthenticator struct{ calls atomic.Int64 }

func (a *countingAuthenticator) Authenticate(context.Context) (*auth.Identity, error) {
	a.calls.Add(1)
	return nil, auth.ErrUnauthenticated
}

func (a *countingAuthenticator) Name() string { return "counting" }

// dispatchServer is a regular listener's admission chains: authentication,
// the regular sealed-window gate over node (the node barrier with the
// cluster seal folded in) and the dispatch over barrier (the bare node
// barrier), as pkg/server composes them.
func dispatchServer(t testing.TB, authn auth.Authenticator, node, barrier *fakeSealProbe) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor) {
	s := &Server{}
	WithAuthMiddleware(authmw.New(denyAllAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(authn)))(s)
	WithSealedWindowProbe(node)(s)
	WithSealedWindowDispatch(barrier)(s)
	return s.admissionChains()
}

// TestSealedWindowDispatch_ResealedListenerServesAsBootListener: while the
// node barrier is sealed, a regular listener serves the sealed-window verbs
// anonymously and refuses everything else with the sealed Unavailable, and
// neither reaches authentication -- exactly what a node that booted sealed
// serves.
func TestSealedWindowDispatch_ResealedListenerServesAsBootListener(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		want      codes.Code
		wantMsg   string
		anonymous bool
	}{
		{name: "allowlisted verb", method: pb.QRDBService_BarrierUnseal_FullMethodName, want: codes.OK, anonymous: true},
		{name: "barrier status", method: pb.QRDBService_BarrierStatus_FullMethodName, want: codes.OK, anonymous: true},
		{name: "health", method: pb.QRDBService_Health_FullMethodName, want: codes.Unavailable, wantMsg: "barrier sealed"},
		{name: "data verb", method: pb.QRDBService_Get_FullMethodName, want: codes.Unavailable, wantMsg: "barrier sealed"},
		{name: "authenticated-only verb", method: pb.QRDBService_BarrierClusterUnseal_FullMethodName, want: codes.Unavailable, wantMsg: "barrier sealed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authn := &countingAuthenticator{}
			unary, _ := dispatchServer(t, authn, newProbe(true), newProbe(true))
			var reached, anonymous bool
			handler := func(ctx context.Context, _ any) (any, error) {
				reached, anonymous = true, sealedwindow.Anonymous(ctx)
				return nil, nil
			}
			_, err := unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: tt.method}, handler)
			st, _ := status.FromError(err)
			if st.Code() != tt.want || (tt.wantMsg != "" && st.Message() != tt.wantMsg) {
				t.Fatalf("err = %v, want %v %q", err, tt.want, tt.wantMsg)
			}
			if reached != (tt.want == codes.OK) || anonymous != tt.anonymous {
				t.Fatalf("handler reached %v anonymous %v, want %v %v", reached, anonymous, tt.want == codes.OK, tt.anonymous)
			}
			if n := authn.calls.Load(); n != 0 {
				t.Fatalf("authentication ran %d times while the barrier was sealed", n)
			}
		})
	}
}

// TestSealedWindowDispatch_StreamWhileSealed: a stream on a re-sealed
// regular listener is refused with the sealed Unavailable before
// authentication.
func TestSealedWindowDispatch_StreamWhileSealed(t *testing.T) {
	authn := &countingAuthenticator{}
	_, stream := dispatchServer(t, authn, newProbe(true), newProbe(true))
	err := stream(nil, &ctxServerStream{ctx: context.Background()}, &grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/Watch"}, dummyStreamHandler)
	if status.Code(err) != codes.Unavailable || authn.calls.Load() != 0 {
		t.Fatalf("err = %v, authentications %d; want Unavailable and none", err, authn.calls.Load())
	}
}

// TestSealedWindowDispatch_HeldNodeRefusesBarrierStatus: a node held or cut
// off by the cluster seal has its own barrier unsealed, so barrier status does
// not take the sealed window's anonymous path: it runs the regular chain,
// authentication first, and the regular gate refuses it
// (TestSealedWindow_Unary_BarrierStatusOnlyOnTheListener); operators read the
// seal record from a serving node.
func TestSealedWindowDispatch_HeldNodeRefusesBarrierStatus(t *testing.T) {
	authn := &countingAuthenticator{}
	unary, _ := dispatchServer(t, authn, newProbe(true), newProbe(false))
	reached := false
	handler := func(context.Context, any) (any, error) { reached = true; return nil, nil }
	_, err := unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: pb.QRDBService_BarrierStatus_FullMethodName}, handler)
	if status.Code(err) != codes.Unauthenticated || reached {
		t.Fatalf("err = %v, handler reached %v; want Unauthenticated and not served", err, reached)
	}
	if n := authn.calls.Load(); n != 1 {
		t.Fatalf("authentications = %d, want 1", n)
	}
}

// TestSealedWindowDispatch_UnsealedBarrierAuthenticates: with the node
// barrier unsealed every call is authenticated -- including a node the
// cluster seal holds, whose barrier-cluster-unseal is authenticated-only.
func TestSealedWindowDispatch_UnsealedBarrierAuthenticates(t *testing.T) {
	for _, tt := range []struct {
		name       string
		nodeSealed bool
	}{
		{name: "unsealed node"},
		{name: "cluster-held node", nodeSealed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			authn := &countingAuthenticator{}
			unary, stream := dispatchServer(t, authn, newProbe(tt.nodeSealed), newProbe(false))
			_, err := unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: pb.QRDBService_BarrierClusterUnseal_FullMethodName}, dummyHandler)
			if status.Code(err) != codes.Unauthenticated {
				t.Fatalf("unary err = %v, want Unauthenticated", err)
			}
			err = stream(nil, &ctxServerStream{ctx: context.Background()}, &grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/Watch"}, dummyStreamHandler)
			if status.Code(err) != codes.Unauthenticated {
				t.Fatalf("stream err = %v, want Unauthenticated", err)
			}
			if n := authn.calls.Load(); n != 2 {
				t.Fatalf("authentications = %d, want 2", n)
			}
		})
	}
}

// TestSealedWindowDispatch_FollowsTheBarrier: the dispatch samples the
// barrier on every call, so a node that unseals returns to authentication
// and one that re-seals leaves it.
func TestSealedWindowDispatch_FollowsTheBarrier(t *testing.T) {
	authn := &countingAuthenticator{}
	node, barrier := newProbe(false), newProbe(false)
	unary, _ := dispatchServer(t, authn, node, barrier)
	info := &grpc.UnaryServerInfo{FullMethod: pb.QRDBService_BarrierUnseal_FullMethodName}
	if _, err := unary(context.Background(), nil, info, dummyHandler); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unsealed: %v, want Unauthenticated", err)
	}
	node.seal()
	barrier.seal()
	if _, err := unary(context.Background(), nil, info, dummyHandler); err != nil {
		t.Fatalf("re-sealed: %v", err)
	}
	if n := authn.calls.Load(); n != 1 {
		t.Fatalf("authentications = %d, want 1 (the unsealed call only)", n)
	}
}

// TestSealedWindowDispatch_PicksPathPerCall pins the dispatch interceptors
// themselves.
func TestSealedWindowDispatch_PicksPathPerCall(t *testing.T) {
	probe := newProbe(true)
	var took string
	pick := func(name string) grpc.UnaryServerInterceptor {
		return func(context.Context, any, *grpc.UnaryServerInfo, grpc.UnaryHandler) (any, error) {
			took = name
			return nil, nil
		}
	}
	pickStream := func(name string) grpc.StreamServerInterceptor {
		return func(any, grpc.ServerStream, *grpc.StreamServerInfo, grpc.StreamHandler) error {
			took = name
			return nil
		}
	}
	unary := SealedWindowDispatchUnaryInterceptor(probe, pick("sealed"), pick("regular"))
	stream := SealedWindowDispatchStreamInterceptor(probe, pickStream("sealed"), pickStream("regular"))
	for _, sealed := range []bool{true, false} {
		probe.sealed.Store(sealed)
		want := map[bool]string{true: "sealed", false: "regular"}[sealed]
		_, _ = unary(context.Background(), nil, &grpc.UnaryServerInfo{}, dummyHandler)
		if took != want {
			t.Errorf("unary, sealed %v: took %q", sealed, took)
		}
		_ = stream(nil, nil, &grpc.StreamServerInfo{}, dummyStreamHandler)
		if took != want {
			t.Errorf("stream, sealed %v: took %q", sealed, took)
		}
	}
}
