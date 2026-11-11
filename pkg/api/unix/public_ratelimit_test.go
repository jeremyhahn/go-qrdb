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

package unix

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/api/publicverbs"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// TestPublicHealth_RateLimited_Unix drives public Health through the Unix
// server's own admission chain with a limiter whose admin:health quota is
// one: the first unauthenticated call is served, the second is refused
// ResourceExhausted before the handler runs.
func TestPublicHealth_RateLimited_Unix(t *testing.T) {
	limiter := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = limiter.Close() })
	mw := authmw.New(dispatchDenyAuthorizer{},
		authmw.WithRoleResolver(authtest.BuiltInRoles(t)),
		authmw.WithRateLimiter(limiter),
		authmw.WithRateLimitPolicies(authtest.OneToken(publicverbs.Table[publicverbs.VerbHealth].RateLimit)))
	if err := mw.RegisterPublicGRPCMethods(publicverbs.GRPCFullMethods(pb.QRDBService_ServiceDesc.ServiceName)); err != nil {
		t.Fatalf("RegisterPublicGRPCMethods: %v", err)
	}
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-public-ratelimit.sock", WithAuthMiddleware(mw))
	unary, _ := srv.admissionChains()

	info := &grpc.UnaryServerInfo{FullMethod: "/" + pb.QRDBService_ServiceDesc.ServiceName + "/" + publicverbs.Table[publicverbs.VerbHealth].GRPC}
	served := 0
	handler := func(context.Context, any) (any, error) { served++; return &pb.HealthResponse{}, nil }

	if _, err := unary(context.Background(), nil, info, handler); err != nil {
		t.Fatalf("first Health: %v", err)
	}
	_, err := unary(context.Background(), nil, info, handler)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second Health: err = %v, want ResourceExhausted", err)
	}
	if served != 1 {
		t.Fatalf("handler served %d calls, want 1", served)
	}
}
