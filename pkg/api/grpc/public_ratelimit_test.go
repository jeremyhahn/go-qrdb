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

package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/publicverbs"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// TestPublicHealth_RateLimited_GRPC drives public Health through the TCP
// gRPC server's own admission chain with a limiter whose admin:health
// quota is one: the first unauthenticated call is served, the second is
// refused ResourceExhausted before the handler runs.
func TestPublicHealth_RateLimited_GRPC(t *testing.T) {
	limiter := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = limiter.Close() })
	mw := authmw.New(denyAllAuthorizer{},
		authmw.WithRoleResolver(authtest.BuiltInRoles(t)),
		authmw.WithRateLimiter(limiter),
		authmw.WithRateLimitPolicies(authtest.OneToken(publicverbs.Table[publicverbs.VerbHealth].RateLimit)))
	if err := mw.RegisterPublicGRPCMethods(publicverbs.GRPCFullMethods(pb.QRDBService_ServiceDesc.ServiceName)); err != nil {
		t.Fatalf("RegisterPublicGRPCMethods: %v", err)
	}
	s := &Server{}
	WithAuthMiddleware(mw)(s)
	unary, _ := s.admissionChains()

	info := &grpc.UnaryServerInfo{FullMethod: "/" + pb.QRDBService_ServiceDesc.ServiceName + "/" + publicverbs.Table[publicverbs.VerbHealth].GRPC}
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: fakeAddr("192.0.2.7:4242")})
	served := 0
	handler := func(context.Context, any) (any, error) { served++; return &pb.HealthResponse{}, nil }

	if _, err := unary(ctx, nil, info, handler); err != nil {
		t.Fatalf("first Health: %v", err)
	}
	_, err := unary(ctx, nil, info, handler)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second Health: err = %v, want ResourceExhausted", err)
	}
	if served != 1 {
		t.Fatalf("handler served %d calls, want 1", served)
	}
}

type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }
