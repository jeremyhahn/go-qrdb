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

package embedded

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// mockJoinAdminService layers the member-side CSR join-review surface
// (JoinRequestReviewer, ObserverMemberAdder, JoinIdentityRetirer) onto
// mockAdminService, which implements none of them -- exactly as a real
// AdminService built without member-side CSR join support does, which is
// what every "*_NotSupported" test in this file exercises by using a plain
// *mockAdminService instead.
type mockJoinAdminService struct {
	mockAdminService

	listFn    func(ctx context.Context) ([]*service.PendingJoinRequest, error)
	approveFn func(ctx context.Context, id, nodeID string) error
	rejectFn  func(ctx context.Context, id, reason string) error
	addObsFn  func(ctx context.Context, shardID, replicaID uint64, raftAddr, identity string) error
	retireFn  func(ctx context.Context, nodeID, reason string) error
}

var (
	_ service.JoinRequestReviewer = (*mockJoinAdminService)(nil)
	_ service.ObserverMemberAdder = (*mockJoinAdminService)(nil)
	_ service.JoinIdentityRetirer = (*mockJoinAdminService)(nil)
)

func (m *mockJoinAdminService) ListPendingJoinRequests(ctx context.Context) ([]*service.PendingJoinRequest, error) {
	return m.listFn(ctx)
}

func (m *mockJoinAdminService) ApproveJoinRequest(ctx context.Context, id, nodeID string) error {
	return m.approveFn(ctx, id, nodeID)
}

func (m *mockJoinAdminService) RejectJoinRequest(ctx context.Context, id, reason string) error {
	return m.rejectFn(ctx, id, reason)
}

func (m *mockJoinAdminService) AddObserverMember(ctx context.Context, shardID, replicaID uint64, raftAddr, identity string) error {
	return m.addObsFn(ctx, shardID, replicaID, raftAddr, identity)
}

func (m *mockJoinAdminService) RetireJoinIdentity(ctx context.Context, nodeID, reason string) error {
	return m.retireFn(ctx, nodeID, reason)
}

// ---------------------------------------------------------------------------
// ListPendingJoinRequests
// ---------------------------------------------------------------------------

func TestListPendingJoinRequests_NotSupported(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	_, err := tr.ListPendingJoinRequests(context.Background())
	var unsupported *service.ClusterJoinRequestReviewUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected ClusterJoinRequestReviewUnsupportedError, got %T: %v", err, err)
	}
}

func TestListPendingJoinRequests_Success(t *testing.T) {
	submitted := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expires := submitted.Add(time.Hour)
	svc := &mockJoinAdminService{listFn: func(context.Context) ([]*service.PendingJoinRequest, error) {
		return []*service.PendingJoinRequest{
			{ID: "req-1", NodeURI: "spiffe://cluster/node/1", ReplicaID: 7, Features: []string{"raft"}, SubmittedAt: submitted, ExpiresAt: expires},
		}, nil
	}}
	tr, _ := New(newMockKVService(), svc)

	out, err := tr.ListPendingJoinRequests(context.Background())
	if err != nil {
		t.Fatalf("ListPendingJoinRequests: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 pending request, got %d", len(out))
	}
	want := &transport.PendingJoinRequest{ID: "req-1", NodeURI: "spiffe://cluster/node/1", ReplicaID: 7, Features: []string{"raft"}, SubmittedAt: submitted, ExpiresAt: expires}
	got := out[0]
	if got.ID != want.ID || got.NodeURI != want.NodeURI || got.ReplicaID != want.ReplicaID ||
		len(got.Features) != 1 || got.Features[0] != want.Features[0] ||
		!got.SubmittedAt.Equal(want.SubmittedAt) || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("ListPendingJoinRequests = %+v, want %+v", got, want)
	}
}

func TestListPendingJoinRequests_ServiceError(t *testing.T) {
	boom := errors.New("catalog unavailable")
	svc := &mockJoinAdminService{listFn: func(context.Context) ([]*service.PendingJoinRequest, error) { return nil, boom }}
	tr, _ := New(newMockKVService(), svc)

	if _, err := tr.ListPendingJoinRequests(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("expected the service error to survive translation, got %v", err)
	}
}

func TestListPendingJoinRequests_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockJoinAdminService{})
	_ = tr.Close()

	_, err := tr.ListPendingJoinRequests(context.Background())
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ApproveJoinRequest
// ---------------------------------------------------------------------------

func TestApproveJoinRequest_NotSupported(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	err := tr.ApproveJoinRequest(context.Background(), "req-1", "node-1")
	var unsupported *service.ClusterJoinRequestReviewUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected ClusterJoinRequestReviewUnsupportedError, got %T: %v", err, err)
	}
}

func TestApproveJoinRequest_Success(t *testing.T) {
	var gotID, gotNodeID string
	svc := &mockJoinAdminService{approveFn: func(_ context.Context, id, nodeID string) error {
		gotID, gotNodeID = id, nodeID
		return nil
	}}
	tr, _ := New(newMockKVService(), svc)

	if err := tr.ApproveJoinRequest(context.Background(), "req-1", "node-9"); err != nil {
		t.Fatalf("ApproveJoinRequest: %v", err)
	}
	if gotID != "req-1" || gotNodeID != "node-9" {
		t.Fatalf("service received (%q, %q), want (req-1, node-9)", gotID, gotNodeID)
	}
}

func TestApproveJoinRequest_ServiceError(t *testing.T) {
	boom := errors.New("csr already bound")
	svc := &mockJoinAdminService{approveFn: func(context.Context, string, string) error { return boom }}
	tr, _ := New(newMockKVService(), svc)

	if err := tr.ApproveJoinRequest(context.Background(), "req-1", "node-1"); !errors.Is(err, boom) {
		t.Fatalf("expected the service error to survive translation, got %v", err)
	}
}

func TestApproveJoinRequest_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockJoinAdminService{})
	_ = tr.Close()

	err := tr.ApproveJoinRequest(context.Background(), "req-1", "node-1")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// RejectJoinRequest
// ---------------------------------------------------------------------------

func TestRejectJoinRequest_NotSupported(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	err := tr.RejectJoinRequest(context.Background(), "req-1", "untrusted")
	var unsupported *service.ClusterJoinRequestReviewUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected ClusterJoinRequestReviewUnsupportedError, got %T: %v", err, err)
	}
}

func TestRejectJoinRequest_Success(t *testing.T) {
	var gotID, gotReason string
	svc := &mockJoinAdminService{rejectFn: func(_ context.Context, id, reason string) error {
		gotID, gotReason = id, reason
		return nil
	}}
	tr, _ := New(newMockKVService(), svc)

	if err := tr.RejectJoinRequest(context.Background(), "req-1", "untrusted"); err != nil {
		t.Fatalf("RejectJoinRequest: %v", err)
	}
	if gotID != "req-1" || gotReason != "untrusted" {
		t.Fatalf("service received (%q, %q), want (req-1, untrusted)", gotID, gotReason)
	}
}

func TestRejectJoinRequest_ServiceError(t *testing.T) {
	boom := errors.New("request already decided")
	svc := &mockJoinAdminService{rejectFn: func(context.Context, string, string) error { return boom }}
	tr, _ := New(newMockKVService(), svc)

	if err := tr.RejectJoinRequest(context.Background(), "req-1", "untrusted"); !errors.Is(err, boom) {
		t.Fatalf("expected the service error to survive translation, got %v", err)
	}
}

func TestRejectJoinRequest_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockJoinAdminService{})
	_ = tr.Close()

	err := tr.RejectJoinRequest(context.Background(), "req-1", "untrusted")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// AddObserverMember
// ---------------------------------------------------------------------------

func TestAddObserverMember_NotSupported(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	err := tr.AddObserverMember(context.Background(), 1, 5, "10.0.0.5:9000", "spki:abcd")
	var unsupported *service.ClusterJoinRequestReviewUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected ClusterJoinRequestReviewUnsupportedError, got %T: %v", err, err)
	}
}

func TestAddObserverMember_Success(t *testing.T) {
	var gotShard, gotReplica uint64
	var gotAddr, gotIdentity string
	svc := &mockJoinAdminService{addObsFn: func(_ context.Context, shardID, replicaID uint64, raftAddr, identity string) error {
		gotShard, gotReplica, gotAddr, gotIdentity = shardID, replicaID, raftAddr, identity
		return nil
	}}
	tr, _ := New(newMockKVService(), svc)

	if err := tr.AddObserverMember(context.Background(), 1, 5, "10.0.0.5:9000", "spki:abcd"); err != nil {
		t.Fatalf("AddObserverMember: %v", err)
	}
	if gotShard != 1 || gotReplica != 5 || gotAddr != "10.0.0.5:9000" || gotIdentity != "spki:abcd" {
		t.Fatalf("service received (%d, %d, %q, %q), want (1, 5, 10.0.0.5:9000, spki:abcd)", gotShard, gotReplica, gotAddr, gotIdentity)
	}
}

func TestAddObserverMember_ServiceError(t *testing.T) {
	boom := errors.New("replica already a voter")
	svc := &mockJoinAdminService{addObsFn: func(context.Context, uint64, uint64, string, string) error { return boom }}
	tr, _ := New(newMockKVService(), svc)

	err := tr.AddObserverMember(context.Background(), 1, 5, "10.0.0.5:9000", "spki:abcd")
	if !errors.Is(err, boom) {
		t.Fatalf("expected the service error to survive translation, got %v", err)
	}
}

func TestAddObserverMember_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockJoinAdminService{})
	_ = tr.Close()

	err := tr.AddObserverMember(context.Background(), 1, 5, "10.0.0.5:9000", "")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// RetireJoinIdentity
// ---------------------------------------------------------------------------

func TestRetireJoinIdentity_NotSupported(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	err := tr.RetireJoinIdentity(context.Background(), "node-1", "compromised")
	var notConfigured *service.ClusterJoinTokenNotConfiguredError
	if !errors.As(err, &notConfigured) {
		t.Fatalf("expected ClusterJoinTokenNotConfiguredError, got %T: %v", err, err)
	}
}

func TestRetireJoinIdentity_Success(t *testing.T) {
	var gotNodeID, gotReason string
	svc := &mockJoinAdminService{retireFn: func(_ context.Context, nodeID, reason string) error {
		gotNodeID, gotReason = nodeID, reason
		return nil
	}}
	tr, _ := New(newMockKVService(), svc)

	if err := tr.RetireJoinIdentity(context.Background(), "node-1", "compromised"); err != nil {
		t.Fatalf("RetireJoinIdentity: %v", err)
	}
	if gotNodeID != "node-1" || gotReason != "compromised" {
		t.Fatalf("service received (%q, %q), want (node-1, compromised)", gotNodeID, gotReason)
	}
}

func TestRetireJoinIdentity_ServiceError(t *testing.T) {
	boom := errors.New("ledger write failed")
	svc := &mockJoinAdminService{retireFn: func(context.Context, string, string) error { return boom }}
	tr, _ := New(newMockKVService(), svc)

	if err := tr.RetireJoinIdentity(context.Background(), "node-1", "compromised"); !errors.Is(err, boom) {
		t.Fatalf("expected the service error to survive translation, got %v", err)
	}
}

func TestRetireJoinIdentity_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockJoinAdminService{})
	_ = tr.Close()

	err := tr.RetireJoinIdentity(context.Background(), "node-1", "compromised")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestRequest_RetireJoinIdentity(t *testing.T) {
	var gotNodeID, gotReason string
	svc := &mockJoinAdminService{retireFn: func(_ context.Context, nodeID, reason string) error {
		gotNodeID, gotReason = nodeID, reason
		return nil
	}}
	tr, _ := New(newMockKVService(), svc)

	err := tr.Request(context.Background(), "RetireJoinIdentity", &transport.RetireJoinIdentityRequest{NodeID: "node-1", Reason: "compromised"}, nil)
	if err != nil {
		t.Fatalf("Request RetireJoinIdentity: %v", err)
	}
	if gotNodeID != "node-1" || gotReason != "compromised" {
		t.Fatalf("service received (%q, %q), want (node-1, compromised)", gotNodeID, gotReason)
	}
}
