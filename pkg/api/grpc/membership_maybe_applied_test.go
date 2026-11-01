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

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestMapServiceError_MaybeAppliedMembershipIsUnavailable pins a member change
// whose outcome is unknown -- submitted, then its leader stepped down or Raft
// timed out -- to codes.Unavailable, the class REST answers 503 (main run
// 70834).
func TestMapServiceError_MaybeAppliedMembershipIsUnavailable(t *testing.T) {
	for name, cause := range map[string]error{
		"leader stepped down": &quicraft.NotLeaderError{ShardID: 1},
		"raft timeout":        quicraft.ErrTimeout,
	} {
		for _, op := range []string{"add", "promote", "remove"} {
			err := mapServiceError(&service.MembershipError{Op: op, Err: &quicraft.MaybeAppliedError{Err: cause}})
			if got := status.Code(err); got != codes.Unavailable {
				t.Errorf("%s, %s: code = %s, want %s", name, op, got, codes.Unavailable)
			}
		}
	}
}

// TestGRPC_RemoveMember_MaybeAppliedIsUnavailable drives RemoveMember end to
// end over the gRPC server: the ambiguous remove reaches the client as
// Unavailable, never Internal.
func TestGRPC_RemoveMember_MaybeAppliedIsUnavailable(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.removeMemberErr = &service.MembershipError{Op: "remove", Err: &quicraft.MaybeAppliedError{
		Err: &quicraft.NotLeaderError{ShardID: 1},
	}}
	client, cleanup := startTestServer(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := client.RemoveMember(context.Background(), &pb.RemoveMemberRequest{ShardId: 1, ReplicaId: 4})
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("code = %s, want %s (err %v)", got, codes.Unavailable, err)
	}
}
