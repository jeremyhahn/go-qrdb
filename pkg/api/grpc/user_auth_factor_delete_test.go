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
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

func TestUserAuthFactor_Delete_Success(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	resp, err := pb.NewUserAuthFactorServiceClient(conn).Delete(context.Background(), &pb.UserAuthFactorDeleteRequest{
		FactorId: "factor-1",
		Force:    true,
	})
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if resp.GetStatus() != "deleted" {
		t.Fatalf("status = %q, want deleted", resp.GetStatus())
	}
	if svc.deletedID != "factor-1" || !svc.deletedForce {
		t.Fatalf("service saw (%q, %v), want (factor-1, true)", svc.deletedID, svc.deletedForce)
	}
}

func TestUserAuthFactor_Delete_Error(t *testing.T) {
	cases := map[string]struct {
		err  error
		want codes.Code
	}{
		"not found": {err: &auth.FactorNotFoundError{FactorID: "x"}, want: codes.NotFound},
		"revoked":   {err: &auth.FactorRevokedError{FactorID: "x"}, want: codes.FailedPrecondition},
		"last":      {err: &auth.LastFactorError{UserID: 1, FactorID: "x"}, want: codes.FailedPrecondition},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc := newFullMockUserAuthFactorSvc()
			svc.deleteErr = tc.err
			conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
			defer cleanup()

			_, err := pb.NewUserAuthFactorServiceClient(conn).Delete(context.Background(), &pb.UserAuthFactorDeleteRequest{
				FactorId: "x",
			})
			if st, _ := status.FromError(err); st.Code() != tc.want {
				t.Fatalf("code = %s, want %s (err %v)", st.Code(), tc.want, err)
			}
		})
	}
}
