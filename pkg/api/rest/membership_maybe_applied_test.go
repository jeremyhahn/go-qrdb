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

package rest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// maybeAppliedMembershipError is what the service layer returns for a member
// change already in the log when its leader stepped down (main run 70834).
func maybeAppliedMembershipError(op string) error {
	return &service.MembershipError{Op: op, Err: &quicraft.MaybeAppliedError{
		Err: &quicraft.NotLeaderError{ShardID: 1},
	}}
}

// TestMembershipChangeStatus_MaybeAppliedIsServiceUnavailable pins a member
// change whose outcome is unknown -- its leader stepped down, or Raft timed
// out, after it was submitted -- to 503, the status gRPC and QUIC give the
// same error through errclass, for every membership op.
func TestMembershipChangeStatus_MaybeAppliedIsServiceUnavailable(t *testing.T) {
	for name, err := range map[string]error{
		"remove, leader stepped down":  maybeAppliedMembershipError("remove"),
		"add, leader stepped down":     maybeAppliedMembershipError("add"),
		"promote, leader stepped down": maybeAppliedMembershipError("promote"),
		"remove, raft timeout": &service.MembershipError{Op: "remove", Err: &quicraft.MaybeAppliedError{
			Err: quicraft.ErrTimeout,
		}},
	} {
		if got := membershipChangeStatus(err); got != http.StatusServiceUnavailable {
			t.Errorf("%s: membershipChangeStatus = %d, want %d", name, got, http.StatusServiceUnavailable)
		}
	}
}

// TestMembershipChangeStatus_RaftFaultStaysInternal pins that widening the
// mapping to transient conditions leaves a definite Raft failure a 500.
func TestMembershipChangeStatus_RaftFaultStaysInternal(t *testing.T) {
	for name, err := range map[string]error{
		"rejected": &service.MembershipError{Op: "remove", Err: quicraft.ErrRejected},
		"opaque":   &service.MembershipError{Op: "remove", Err: errors.New("raft")},
	} {
		if got := membershipChangeStatus(err); got != http.StatusInternalServerError {
			t.Errorf("%s: membershipChangeStatus = %d, want %d", name, got, http.StatusInternalServerError)
		}
	}
}

// TestHandler_MembershipRemove_StatusFollowsErrclass drives POST
// /membership/remove: a maybe-applied not-leader answers 503, as it does on
// add, where it used to answer a hard-coded 500; a definite failure stays 500.
func TestHandler_MembershipRemove_StatusFollowsErrclass(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"maybe applied": {maybeAppliedMembershipError("remove"), http.StatusServiceUnavailable},
		"raft fault":    {&service.MembershipError{Op: "remove", Err: quicraft.ErrRejected}, http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			adminSvc := &mockAdminService{
				removeMemberFn: func(context.Context, uint64, uint64) error { return tc.err },
			}
			srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

			rec := httptest.NewRecorder()
			srv.dispatch(rec, httptest.NewRequest(http.MethodPost, "/membership/remove",
				strings.NewReader(`{"shard_id":1,"replica_id":4}`)))

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}
