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
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// memberIdentityCauses is one cause per refusal kind.
var memberIdentityCauses = map[service.MemberIdentityRefusal]error{
	service.IdentityInvalid:               &cluster.MemberIdentityFormatError{Identity: "x"},
	service.IdentityConflict:              &cluster.MemberIdentityConflictError{ReplicaID: 2},
	service.IdentityUnknown:               &cluster.MemberIdentityUnknownError{ReplicaID: 4},
	service.IdentityMembershipUnavailable: &cluster.MembershipReadError{ShardID: 1, Err: errors.New("no leader")},
}

// TestMapServiceError_MemberIdentity pins every member-identity refusal to
// its gRPC code (MCP renders the same codes through ServiceErrorCode).
func TestMapServiceError_MemberIdentity(t *testing.T) {
	want := map[service.MemberIdentityRefusal]codes.Code{
		service.IdentityInvalid:               codes.InvalidArgument,
		service.IdentityConflict:              codes.FailedPrecondition,
		service.IdentityUnknown:               codes.FailedPrecondition,
		service.IdentityMembershipUnavailable: codes.Unavailable,
	}
	for refusal, cause := range memberIdentityCauses {
		err := &service.MemberIdentityError{ReplicaID: 4, Err: cause}
		if err.Refusal() != refusal {
			t.Fatalf("%T classified as %d, want %d", cause, err.Refusal(), refusal)
		}
		if got := status.Code(mapServiceError(err)); got != want[refusal] {
			t.Errorf("%T: code %s, want %s", cause, got, want[refusal])
		}
		if got := ServiceErrorCode(err); got != want[refusal] {
			t.Errorf("ServiceErrorCode(%T) = %s, want %s", cause, got, want[refusal])
		}
	}
}
