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

package quic

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestMapServiceError_MemberIdentity pins every member-identity refusal to
// the wire status matching gRPC's code for it.
func TestMapServiceError_MemberIdentity(t *testing.T) {
	cases := []struct {
		cause error
		want  uint16
	}{
		{&cluster.MemberIdentityFormatError{Identity: "x"}, wire.StatusInvalidArgument},
		{&cluster.MemberIdentityConflictError{ReplicaID: 2}, wire.StatusFailedPrecondition},
		{&cluster.MemberIdentityUnknownError{ReplicaID: 4}, wire.StatusFailedPrecondition},
		{&cluster.MembershipReadError{ShardID: 1, Err: errors.New("no leader")}, wire.StatusUnavailable},
	}
	for _, tc := range cases {
		got, _ := mapServiceError(&service.MemberIdentityError{ReplicaID: 4, Err: tc.cause})
		if got != tc.want {
			t.Errorf("%T: status %d, want %d", tc.cause, got, tc.want)
		}
	}
}

// TestHandleAddMember_CarriesIdentity pins that admin.AddMember hands the
// request's member identity to the admin service.
func TestHandleAddMember_CarriesIdentity(t *testing.T) {
	adminSvc := &mockAdminService{}
	srv := newTestServer(&mockKVService{}, adminSvc)

	identity := "spki:" + strings.Repeat("ab", 32)
	payload, err := proto.Marshal(&pb.AddMemberRequest{ShardId: 1, ReplicaId: 4, RaftAddress: "10.0.0.4:5000", MemberIdentity: identity})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := srv.handleAddMember(context.Background(), payload); err != nil {
		t.Fatalf("handleAddMember: %v", err)
	}
	if adminSvc.addIdentity != identity {
		t.Fatalf("admin service received identity %q, want %q", adminSvc.addIdentity, identity)
	}
}
