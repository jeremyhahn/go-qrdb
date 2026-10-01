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
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestMemberAddStatus pins every member-identity refusal to its HTTP status,
// and anything else a failed add returns to 500.
func TestMemberAddStatus(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{&service.MemberIdentityError{Err: &cluster.MemberIdentityFormatError{Identity: "x"}}, http.StatusBadRequest},
		{&service.MemberIdentityError{Err: &cluster.MemberIdentityConflictError{ReplicaID: 2}}, http.StatusConflict},
		{&service.MemberIdentityError{Err: &cluster.MemberIdentityUnknownError{ReplicaID: 4}}, http.StatusPreconditionFailed},
		{&service.MemberIdentityError{Err: &cluster.MembershipReadError{Err: errors.New("no leader")}}, http.StatusServiceUnavailable},
		{&service.MembershipError{Op: "add", Err: errors.New("raft")}, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		if got := membershipChangeStatus(tc.err); got != tc.want {
			t.Errorf("membershipChangeStatus(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

// TestHandler_MembershipAdd_CarriesIdentity pins that POST /membership/add
// hands the body's identity to the admin service.
func TestHandler_MembershipAdd_CarriesIdentity(t *testing.T) {
	adminSvc := &mockAdminService{}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	identity := "spki:" + strings.Repeat("ab", 32)
	body := `{"shard_id":1,"replica_id":4,"address":"10.0.0.4:63001","identity":"` + identity + `"}`
	rec := httptest.NewRecorder()
	srv.dispatch(rec, httptest.NewRequest(http.MethodPost, "/membership/add", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if adminSvc.addIdentity != identity {
		t.Fatalf("admin service received identity %q, want %q", adminSvc.addIdentity, identity)
	}
}
