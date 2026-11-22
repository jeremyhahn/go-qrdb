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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/release"
)

// A member add refused because the node does not advertise a feature the
// group uses is a conflict the operator resolves by upgrading the node --
// 409 naming the node and the feature, not a 500.
func TestHandler_MembershipAdd_RefusedReplicaIsConflict(t *testing.T) {
	refusal := &featuregate.UnsupportedReplicaError{ShardID: 1, Address: "10.0.0.3:63001", Feature: featuregate.GuardedWrites}
	adminSvc := &mockAdminService{
		addMemberFn: func(context.Context, uint64, uint64, string) error { return refusal },
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	body := `{"shard_id":1,"replica_id":3,"address":"10.0.0.3:63001"}`
	req := httptest.NewRequest(http.MethodPost, "/membership/add", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if !strings.Contains(rec.Body.String(), "guarded-writes") {
		t.Fatalf("body %q does not name the missing feature", rec.Body.String())
	}
	if got := mapHTTPError(refusal); got != http.StatusConflict {
		t.Fatalf("mapHTTPError = %d, want %d", got, http.StatusConflict)
	}
}

// TestHandler_MembershipAdd_UnannouncedCandidateIsConflict: a system-shard
// add for a node with no live advertisement is a well-formed request the
// operator resolves by enrolling the node, so REST answers the same 409 it
// answers an unsupported replica, naming the address, not a 500.
func TestHandler_MembershipAdd_UnannouncedCandidateIsConflict(t *testing.T) {
	refusal := &featuregate.UnannouncedCandidateError{ShardID: 1, Address: "n3:1"}
	adminSvc := &mockAdminService{
		addMemberFn: func(context.Context, uint64, uint64, string) error { return refusal },
	}
	srv := NewServer(&mockKVService{}, adminSvc, ":0")

	body := `{"shard_id":1,"replica_id":3,"address":"n3:1"}`
	req := httptest.NewRequest(http.MethodPost, "/membership/add", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if !strings.Contains(rec.Body.String(), "n3:1") {
		t.Fatalf("body %q does not name the unannounced address", rec.Body.String())
	}
	if got := membershipChangeStatus(refusal); got != http.StatusConflict {
		t.Fatalf("membershipChangeStatus = %d, want %d", got, http.StatusConflict)
	}
}

// observerAdminService is a mockAdminService that also adds observers
// (service.ObserverMemberAdder), which handleMembershipAddObserver requires.
type observerAdminService struct {
	*mockAdminService
	addObserverMemberFn func(ctx context.Context, shardID, replicaID uint64, raftAddr, identity string) error
}

func (m *observerAdminService) AddObserverMember(ctx context.Context, shardID, replicaID uint64, raftAddr, identity string) error {
	return m.addObserverMemberFn(ctx, shardID, replicaID, raftAddr, identity)
}

// releaseGapRefusal is the refusal release admission returns for a candidate
// two minor releases from a member.
func releaseGapRefusal() *release.GapError {
	return &release.GapError{Channel: "raft-membership", Local: "0.59.0-alpha", Peer: "0.61.0-alpha"}
}

// assertReleaseGapConflict dispatches body to path and requires 409 with a
// body naming both releases.
func assertReleaseGapConflict(t *testing.T, srv *Server, path string, gap *release.GapError) {
	t.Helper()
	body := `{"shard_id":1,"replica_id":3,"address":"n3:1"}`
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("%s status = %d, want %d (body %q)", path, rec.Code, http.StatusConflict, rec.Body.String())
	}
	for _, want := range []string{gap.Local, gap.Peer} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("%s body %q does not name release %q", path, rec.Body.String(), want)
		}
	}
	if got := membershipChangeStatus(gap); got != http.StatusConflict {
		t.Fatalf("membershipChangeStatus = %d, want %d", got, http.StatusConflict)
	}
}

// TestHandler_MembershipAdd_ReleaseGapIsConflict: a release-gap refusal of
// POST /membership/add is the client's precondition failure, 409, not 500.
func TestHandler_MembershipAdd_ReleaseGapIsConflict(t *testing.T) {
	gap := releaseGapRefusal()
	adminSvc := &mockAdminService{
		addMemberFn: func(context.Context, uint64, uint64, string) error { return gap },
	}
	assertReleaseGapConflict(t, NewServer(&mockKVService{}, adminSvc, ":0"), "/membership/add", gap)
}

// TestHandler_MembershipAddObserver_ReleaseGapIsConflict: the same refusal of
// POST /membership/add-observer is 409 too.
func TestHandler_MembershipAddObserver_ReleaseGapIsConflict(t *testing.T) {
	gap := releaseGapRefusal()
	adminSvc := &observerAdminService{
		mockAdminService: &mockAdminService{},
		addObserverMemberFn: func(context.Context, uint64, uint64, string, string) error {
			return gap
		},
	}
	assertReleaseGapConflict(t, NewServer(&mockKVService{}, adminSvc, ":0"), "/membership/add-observer", gap)
}
