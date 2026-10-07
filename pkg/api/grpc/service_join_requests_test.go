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
	"errors"
	"testing"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// errJoinAdmin is the failure the join-admin double returns when told to
// fail, so a test can prove the handler maps and returns it.
var errJoinAdmin = errors.New("join admin: backend refused")

// joinAdmin is an admin double implementing service.JoinRequestReviewer,
// service.ObserverMemberAdder and service.JoinIdentityRetirer, recording the
// arguments each handler passed through.
type joinAdmin struct {
	*mockAdminService
	pending []*service.PendingJoinRequest
	err     error

	approvedID, approvedNode string
	rejectedID, rejectedWhy  string
	retiredNode, retiredWhy  string
	observerShard            uint64
	observerReplica          uint64
	observerAddr             string
	observerIdentity         string
}

func (a *joinAdmin) ListPendingJoinRequests(context.Context) ([]*service.PendingJoinRequest, error) {
	return a.pending, a.err
}

func (a *joinAdmin) ApproveJoinRequest(_ context.Context, id, nodeID string) error {
	a.approvedID, a.approvedNode = id, nodeID
	return a.err
}

func (a *joinAdmin) RejectJoinRequest(_ context.Context, id, reason string) error {
	a.rejectedID, a.rejectedWhy = id, reason
	return a.err
}

func (a *joinAdmin) RetireJoinIdentity(_ context.Context, nodeID, reason string) error {
	a.retiredNode, a.retiredWhy = nodeID, reason
	return a.err
}

func (a *joinAdmin) AddObserverMember(_ context.Context, shardID, replicaID uint64, raftAddr, identity string) error {
	a.observerShard, a.observerReplica, a.observerAddr, a.observerIdentity = shardID, replicaID, raftAddr, identity
	return a.err
}

// newJoinAdminServer returns a server over a joinAdmin and the double itself.
func newJoinAdminServer() (*qrdbServiceServer, *joinAdmin) {
	admin := &joinAdmin{mockAdminService: newMockAdminService()}
	return &qrdbServiceServer{adminSvc: admin}, admin
}

// joinAdminCalls lists every join-admin handler as a call that takes only a
// server, so the refusal tests below cover each one the same way.
var joinAdminCalls = map[string]func(*qrdbServiceServer) error{
	"AddObserverMember": func(s *qrdbServiceServer) error {
		_, err := s.AddObserverMember(context.Background(), &pb.AddObserverMemberRequest{ShardId: 1, ReplicaId: 4})
		return err
	},
	"RetireJoinIdentity": func(s *qrdbServiceServer) error {
		_, err := s.RetireJoinIdentity(context.Background(), &pb.RetireJoinIdentityRequest{NodeId: "node-9"})
		return err
	},
	"ListPendingJoinRequests": func(s *qrdbServiceServer) error {
		_, err := s.ListPendingJoinRequests(context.Background(), nil)
		return err
	},
	"ApproveJoinRequest": func(s *qrdbServiceServer) error {
		_, err := s.ApproveJoinRequest(context.Background(), &pb.ApproveJoinRequestRequest{Id: "req-1", NodeId: "node-9"})
		return err
	},
	"RejectJoinRequest": func(s *qrdbServiceServer) error {
		_, err := s.RejectJoinRequest(context.Background(), &pb.RejectJoinRequestRequest{Id: "req-1"})
		return err
	},
}

// TestGRPC_JoinAdmin_NoAdminService proves every join-admin handler refuses,
// rather than dereferencing nil, when the server has no admin service.
func TestGRPC_JoinAdmin_NoAdminService(t *testing.T) {
	for name, call := range joinAdminCalls {
		t.Run(name, func(t *testing.T) {
			if err := call(&qrdbServiceServer{}); err == nil {
				t.Fatalf("%s with no admin service succeeded, want a refusal", name)
			}
		})
	}
}

// TestGRPC_JoinAdmin_UnsupportedAdminService proves every join-admin handler
// refuses an admin service that does not implement its capability.
func TestGRPC_JoinAdmin_UnsupportedAdminService(t *testing.T) {
	for name, call := range joinAdminCalls {
		t.Run(name, func(t *testing.T) {
			s := &qrdbServiceServer{adminSvc: newMockAdminService()}
			if err := call(s); err == nil {
				t.Fatalf("%s on an admin service without the capability succeeded, want a refusal", name)
			}
		})
	}
}

// TestGRPC_JoinAdmin_BackendErrorReturned proves a failure from the admin
// service reaches the caller instead of being swallowed.
func TestGRPC_JoinAdmin_BackendErrorReturned(t *testing.T) {
	for name, call := range joinAdminCalls {
		t.Run(name, func(t *testing.T) {
			s, admin := newJoinAdminServer()
			admin.err = errJoinAdmin
			if err := call(s); err == nil {
				t.Fatalf("%s hid the backend failure, want an error", name)
			}
		})
	}
}

// TestGRPC_AddObserverMember_PassesArguments proves the handler hands the
// request's shard, replica, address and identity to the admin service.
func TestGRPC_AddObserverMember_PassesArguments(t *testing.T) {
	s, admin := newJoinAdminServer()

	_, err := s.AddObserverMember(context.Background(), &pb.AddObserverMemberRequest{
		ShardId: 7, ReplicaId: 4, RaftAddress: "10.0.0.4:63001", MemberIdentity: "spiffe://example.test/node/n4",
	})
	if err != nil {
		t.Fatalf("AddObserverMember: %v", err)
	}
	if admin.observerShard != 7 || admin.observerReplica != 4 ||
		admin.observerAddr != "10.0.0.4:63001" || admin.observerIdentity != "spiffe://example.test/node/n4" {
		t.Fatalf("admin saw shard=%d replica=%d addr=%q identity=%q",
			admin.observerShard, admin.observerReplica, admin.observerAddr, admin.observerIdentity)
	}
}

// TestGRPC_RetireJoinIdentity_PassesArguments proves the handler hands the
// node identity and the operator's reason to the admin service.
func TestGRPC_RetireJoinIdentity_PassesArguments(t *testing.T) {
	s, admin := newJoinAdminServer()

	if _, err := s.RetireJoinIdentity(context.Background(), &pb.RetireJoinIdentityRequest{NodeId: "node-9", Reason: "decommissioned"}); err != nil {
		t.Fatalf("RetireJoinIdentity: %v", err)
	}
	if admin.retiredNode != "node-9" || admin.retiredWhy != "decommissioned" {
		t.Fatalf("admin saw node=%q reason=%q", admin.retiredNode, admin.retiredWhy)
	}
}

// TestGRPC_ApproveAndRejectJoinRequest_PassArguments proves the review
// handlers hand the request id, the operator-chosen node identity and the
// rejection reason to the admin service.
func TestGRPC_ApproveAndRejectJoinRequest_PassArguments(t *testing.T) {
	s, admin := newJoinAdminServer()

	if _, err := s.ApproveJoinRequest(context.Background(), &pb.ApproveJoinRequestRequest{Id: "req-1", NodeId: "node-9"}); err != nil {
		t.Fatalf("ApproveJoinRequest: %v", err)
	}
	if admin.approvedID != "req-1" || admin.approvedNode != "node-9" {
		t.Fatalf("admin approved id=%q node=%q", admin.approvedID, admin.approvedNode)
	}

	if _, err := s.RejectJoinRequest(context.Background(), &pb.RejectJoinRequestRequest{Id: "req-2", Reason: "unknown host"}); err != nil {
		t.Fatalf("RejectJoinRequest: %v", err)
	}
	if admin.rejectedID != "req-2" || admin.rejectedWhy != "unknown host" {
		t.Fatalf("admin rejected id=%q reason=%q", admin.rejectedID, admin.rejectedWhy)
	}
}

// TestGRPC_ListPendingJoinRequests_ConvertsToWire proves the handler returns
// every queued request with its fields and RFC 3339 UTC timestamps, and an
// empty (non-nil) list when nothing is queued.
func TestGRPC_ListPendingJoinRequests_ConvertsToWire(t *testing.T) {
	s, admin := newJoinAdminServer()

	empty, err := s.ListPendingJoinRequests(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListPendingJoinRequests (empty): %v", err)
	}
	if empty.GetRequests() == nil || len(empty.GetRequests()) != 0 {
		t.Fatalf("empty queue = %v, want an empty non-nil list", empty.GetRequests())
	}

	submitted := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	expires := submitted.Add(time.Hour)
	admin.pending = []*service.PendingJoinRequest{{
		ID: "req-1", NodeURI: "spiffe://example.test/node/n4", ReplicaID: 4,
		SubmittedAt: submitted, ExpiresAt: expires,
	}}

	resp, err := s.ListPendingJoinRequests(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListPendingJoinRequests: %v", err)
	}
	if len(resp.GetRequests()) != 1 {
		t.Fatalf("requests = %d, want 1", len(resp.GetRequests()))
	}
	got := resp.GetRequests()[0]
	if got.GetId() != "req-1" || got.GetNodeUri() != "spiffe://example.test/node/n4" || got.GetReplicaId() != 4 {
		t.Fatalf("request = %v", got)
	}
	if got.GetSubmittedAt() != "2026-10-01T12:00:00Z" || got.GetExpiresAt() != "2026-10-01T13:00:00Z" {
		t.Fatalf("timestamps = %q / %q", got.GetSubmittedAt(), got.GetExpiresAt())
	}
}
