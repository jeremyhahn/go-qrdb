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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/authfactorstest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestAuthFactorVerify_CallerFactorNameCannotOverrideEnrollment is go-qrdb#577
// on the gRPC transport (and so on Unix, which registers the same
// AuthFactorService): a challenge for a strict-factor enrollment, verified
// under the name of a permissive factor, is refused as InvalidArgument and the
// permissive factor is never consulted. The real service backs the server.
func TestAuthFactorVerify_CallerFactorNameCannotOverrideEnrollment(t *testing.T) {
	ctx := context.Background()
	strict := authfactorstest.New("piv")
	weak := authfactorstest.NewPermissive("presence")
	svc, err := service.NewAuthFactorService(service.AuthFactorServiceConfig{
		Factors: []authfactors.Factor{strict, weak},
		KV:      kvstore.NewMemoryKVStore(),
	})
	if err != nil {
		t.Fatalf("NewAuthFactorService: %v", err)
	}
	srv := &authFactorServer{svc: svc}

	enr, err := srv.EnrollFactor(ctx, &pb.EnrollFactorRequest{FactorName: "piv", Subject: "custodian"})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	challenge := func() *pb.ChallengeFactorResponse {
		c, err := srv.ChallengeFactor(ctx, &pb.ChallengeFactorRequest{EnrollmentId: enr.GetEnrollmentId()})
		if err != nil {
			t.Fatalf("ChallengeFactor: %v", err)
		}
		return c
	}

	c := challenge()
	_, err = srv.VerifyFactor(ctx, &pb.VerifyFactorRequest{
		FactorName: "presence", ChallengeId: c.GetChallengeId(), Value: []byte("anything"),
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("verify through the permissive factor: code %s (%v), want InvalidArgument", status.Code(err), err)
	}
	if weak.Verifies() != 0 {
		t.Fatalf("the caller-named factor's Verify ran %d times", weak.Verifies())
	}

	c = challenge()
	if _, err := srv.VerifyFactor(ctx, &pb.VerifyFactorRequest{
		FactorName: "piv", ChallengeId: c.GetChallengeId(), Value: strict.Respond(c.GetNonce()),
	}); err != nil {
		t.Fatalf("verify through the enrolled factor: %v", err)
	}
}
