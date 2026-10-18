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

package unix

import (
	"context"
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

// dispatchAuthenticator refuses every caller and counts how often it was
// asked: a call it sees is a call that reached authentication, and so the
// user store behind it.
type dispatchAuthenticator struct{ calls int }

func (a *dispatchAuthenticator) Authenticate(context.Context) (*auth.Identity, error) {
	a.calls++
	return nil, auth.ErrUnauthenticated
}

func (a *dispatchAuthenticator) Name() string { return "counting" }

// dispatchDenyAuthorizer is never reached: dispatchAuthenticator admits no
// one.
type dispatchDenyAuthorizer struct{}

func (dispatchDenyAuthorizer) Authorize(context.Context, *auth.Identity, string, string) error {
	return auth.ErrUnauthorized
}

// dispatchStream is a grpc.ServerStream with a background context.
type dispatchStream struct{ grpc.ServerStream }

func (dispatchStream) Context() context.Context { return context.Background() }

// TestSealedWindowDispatch_Unix: the Unix socket's regular listener serves
// a sealed node exactly as the boot-time listener does -- the sealed-window
// verbs anonymously, everything else refused with the sealed Unavailable,
// neither authenticated -- and authenticates every call once the node
// barrier is unsealed, a cluster-held node included.
func TestSealedWindowDispatch_Unix(t *testing.T) {
	tests := []struct {
		name                string
		nodeSealed, barrier bool
		method              string
		want                codes.Code
		anonymous           bool
		authentications     int
	}{
		{name: "sealed, allowlisted", nodeSealed: true, barrier: true, method: pb.QRDBService_BarrierUnseal_FullMethodName, want: codes.OK, anonymous: true},
		{name: "sealed, barrier status", nodeSealed: true, barrier: true, method: pb.QRDBService_BarrierStatus_FullMethodName, want: codes.OK, anonymous: true},
		{name: "cluster-held, barrier status", nodeSealed: true, method: pb.QRDBService_BarrierStatus_FullMethodName, want: codes.Unauthenticated, authentications: 1},
		{name: "sealed, data verb", nodeSealed: true, barrier: true, method: pb.QRDBService_Get_FullMethodName, want: codes.Unavailable},
		{name: "sealed, authenticated-only verb", nodeSealed: true, barrier: true, method: pb.QRDBService_BarrierClusterUnseal_FullMethodName, want: codes.Unavailable},
		{name: "unsealed", method: pb.QRDBService_BarrierStatus_FullMethodName, want: codes.Unauthenticated, authentications: 1},
		{name: "cluster-held", nodeSealed: true, method: pb.QRDBService_BarrierClusterUnseal_FullMethodName, want: codes.Unauthenticated, authentications: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authn := &dispatchAuthenticator{}
			srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-dispatch.sock",
				WithAuthMiddleware(authmw.New(dispatchDenyAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(authn))),
				WithSealedWindowProbe(&fakeSealedProbe{sealed: tt.nodeSealed}),
				WithSealedWindowDispatch(&fakeSealedProbe{sealed: tt.barrier}))
			unary, stream := srv.admissionChains()

			var anonymous bool
			handler := func(ctx context.Context, _ any) (any, error) {
				anonymous = sealedwindow.Anonymous(ctx)
				return nil, nil
			}
			_, err := unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: tt.method}, handler)
			if status.Code(err) != tt.want || anonymous != tt.anonymous {
				t.Fatalf("err = %v anonymous %v, want %v %v", err, anonymous, tt.want, tt.anonymous)
			}
			if authn.calls != tt.authentications {
				t.Fatalf("authentications = %d, want %d", authn.calls, tt.authentications)
			}
			if tt.barrier {
				err = stream(nil, dispatchStream{}, &grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/Watch"}, func(any, grpc.ServerStream) error { return nil })
				if status.Code(err) != codes.Unavailable || authn.calls != 0 {
					t.Fatalf("stream while sealed: %v, authentications %d", err, authn.calls)
				}
			}
		})
	}
}
