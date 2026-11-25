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
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// handleHealth
// ---------------------------------------------------------------------------

func TestHandleHealth_Success(t *testing.T) {
	adminSvc := &mockAdminService{
		healthFn: func(_ context.Context) (*service.HealthStatus, error) {
			return &service.HealthStatus{Status: "ok", Leader: true}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, adminSvc)
	ctx := context.Background()

	resp, err := srv.handleHealth(ctx, nil)
	if err != nil {
		t.Fatalf("handleHealth returned unexpected error: %v", err)
	}

	var healthResp pb.HealthResponse
	if err := proto.Unmarshal(resp, &healthResp); err != nil {
		t.Fatalf("failed to unmarshal HealthResponse: %v", err)
	}
	if healthResp.GetStatus() != "ok" {
		t.Errorf("expected status %q, got %q", "ok", healthResp.GetStatus())
	}
	if !healthResp.GetLeader() {
		t.Error("expected leader to be true")
	}
}

func TestHandleHealth_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "health check failed"}
	adminSvc := &mockAdminService{
		healthFn: func(_ context.Context) (*service.HealthStatus, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, adminSvc)
	ctx := context.Background()

	_, err := srv.handleHealth(ctx, nil)
	if err == nil {
		t.Fatal("expected error from handleHealth, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleStatus
// ---------------------------------------------------------------------------

func TestHandleStatus_Success(t *testing.T) {
	adminSvc := &mockAdminService{
		statusFn: func(_ context.Context) (*service.StatusInfo, error) {
			return &service.StatusInfo{
				NodeID:      "node-1",
				RaftAddress: "127.0.0.1:5000",
				IsLeader:    true,
				LeaderID:    1,
				ShardCount:  3,
				IsDraining:  false,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, adminSvc)
	ctx := context.Background()

	resp, err := srv.handleStatus(ctx, nil)
	if err != nil {
		t.Fatalf("handleStatus returned unexpected error: %v", err)
	}

	var statusResp pb.StatusResponse
	if err := proto.Unmarshal(resp, &statusResp); err != nil {
		t.Fatalf("failed to unmarshal StatusResponse: %v", err)
	}
	if statusResp.GetNodeId() != "node-1" {
		t.Errorf("expected node_id %q, got %q", "node-1", statusResp.GetNodeId())
	}
	if statusResp.GetRaftAddress() != "127.0.0.1:5000" {
		t.Errorf("expected raft_address %q, got %q", "127.0.0.1:5000", statusResp.GetRaftAddress())
	}
	if !statusResp.GetIsLeader() {
		t.Error("expected is_leader to be true")
	}
	if statusResp.GetLeaderId() != 1 {
		t.Errorf("expected leader_id 1, got %d", statusResp.GetLeaderId())
	}
	if statusResp.GetShardCount() != 3 {
		t.Errorf("expected shard_count 3, got %d", statusResp.GetShardCount())
	}
	if statusResp.GetIsDraining() {
		t.Error("expected is_draining to be false")
	}
}

func TestHandleStatus_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "status failed"}
	adminSvc := &mockAdminService{
		statusFn: func(_ context.Context) (*service.StatusInfo, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, adminSvc)
	ctx := context.Background()

	_, err := srv.handleStatus(ctx, nil)
	if err == nil {
		t.Fatal("expected error from handleStatus, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handlePeers
// ---------------------------------------------------------------------------

func TestHandlePeers_Success(t *testing.T) {
	adminSvc := &mockAdminService{
		peersFn: func(_ context.Context) (*service.PeersInfo, error) {
			return &service.PeersInfo{
				Shards: []service.ShardPeers{
					{
						ShardID:  1,
						LeaderID: 10,
						Peers: []service.PeerInfo{
							{ReplicaID: 10, Address: "127.0.0.1:5001", IsLeader: true},
							{ReplicaID: 20, Address: "127.0.0.1:5002", IsLeader: false},
						},
					},
				},
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, adminSvc)
	ctx := context.Background()

	resp, err := srv.handlePeers(ctx, nil)
	if err != nil {
		t.Fatalf("handlePeers returned unexpected error: %v", err)
	}

	var peersResp pb.PeersResponse
	if err := proto.Unmarshal(resp, &peersResp); err != nil {
		t.Fatalf("failed to unmarshal PeersResponse: %v", err)
	}
	if len(peersResp.GetShards()) != 1 {
		t.Fatalf("expected 1 shard, got %d", len(peersResp.GetShards()))
	}
	shard := peersResp.GetShards()[0]
	if shard.GetShardId() != 1 {
		t.Errorf("expected shard_id 1, got %d", shard.GetShardId())
	}
	if shard.GetLeaderId() != 10 {
		t.Errorf("expected leader_id 10, got %d", shard.GetLeaderId())
	}
	if len(shard.GetPeers()) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(shard.GetPeers()))
	}
}

func TestHandlePeers_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "peers failed"}
	adminSvc := &mockAdminService{
		peersFn: func(_ context.Context) (*service.PeersInfo, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, adminSvc)
	ctx := context.Background()

	_, err := srv.handlePeers(ctx, nil)
	if err == nil {
		t.Fatal("expected error from handlePeers, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleAddMember
// ---------------------------------------------------------------------------

func TestHandleAddMember_Success(t *testing.T) {
	var capturedShardID, capturedReplicaID uint64
	var capturedAddr string

	adminSvc := &mockAdminService{
		addMemberFn: func(_ context.Context, shardID, replicaID uint64, raftAddr string) error {
			capturedShardID = shardID
			capturedReplicaID = replicaID
			capturedAddr = raftAddr
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, adminSvc)
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.AddMemberRequest{
		ShardId:     1,
		ReplicaId:   100,
		RaftAddress: "10.0.0.1:5000",
	})
	if err != nil {
		t.Fatalf("failed to marshal AddMemberRequest: %v", err)
	}

	resp, err := srv.handleAddMember(ctx, payload)
	if err != nil {
		t.Fatalf("handleAddMember returned unexpected error: %v", err)
	}

	if capturedShardID != 1 {
		t.Errorf("expected shardID 1, got %d", capturedShardID)
	}
	if capturedReplicaID != 100 {
		t.Errorf("expected replicaID 100, got %d", capturedReplicaID)
	}
	if capturedAddr != "10.0.0.1:5000" {
		t.Errorf("expected addr %q, got %q", "10.0.0.1:5000", capturedAddr)
	}

	var empty emptypb.Empty
	if err := proto.Unmarshal(resp, &empty); err != nil {
		t.Fatalf("failed to unmarshal Empty: %v", err)
	}
}

func TestHandleAddMember_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "add member failed"}
	adminSvc := &mockAdminService{
		addMemberFn: func(_ context.Context, _, _ uint64, _ string) error {
			return svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, adminSvc)
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.AddMemberRequest{
		ShardId:     1,
		ReplicaId:   100,
		RaftAddress: "addr",
	})
	if err != nil {
		t.Fatalf("failed to marshal AddMemberRequest: %v", err)
	}

	_, err = srv.handleAddMember(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleAddMember, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleAddMember_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleAddMember(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleRemoveMember
// ---------------------------------------------------------------------------

func TestHandleRemoveMember_Success(t *testing.T) {
	var capturedShardID, capturedReplicaID uint64

	adminSvc := &mockAdminService{
		removeMemberFn: func(_ context.Context, shardID, replicaID uint64) error {
			capturedShardID = shardID
			capturedReplicaID = replicaID
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, adminSvc)
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.RemoveMemberRequest{
		ShardId:   2,
		ReplicaId: 200,
	})
	if err != nil {
		t.Fatalf("failed to marshal RemoveMemberRequest: %v", err)
	}

	resp, err := srv.handleRemoveMember(ctx, payload)
	if err != nil {
		t.Fatalf("handleRemoveMember returned unexpected error: %v", err)
	}

	if capturedShardID != 2 {
		t.Errorf("expected shardID 2, got %d", capturedShardID)
	}
	if capturedReplicaID != 200 {
		t.Errorf("expected replicaID 200, got %d", capturedReplicaID)
	}

	var empty emptypb.Empty
	if err := proto.Unmarshal(resp, &empty); err != nil {
		t.Fatalf("failed to unmarshal Empty: %v", err)
	}
}

func TestHandleRemoveMember_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "remove member failed"}
	adminSvc := &mockAdminService{
		removeMemberFn: func(_ context.Context, _, _ uint64) error {
			return svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, adminSvc)
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.RemoveMemberRequest{
		ShardId:   1,
		ReplicaId: 1,
	})
	if err != nil {
		t.Fatalf("failed to marshal RemoveMemberRequest: %v", err)
	}

	_, err = srv.handleRemoveMember(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleRemoveMember, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleRemoveMember_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleRemoveMember(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handlePromoteMember
// ---------------------------------------------------------------------------

func TestHandlePromoteMember_Success(t *testing.T) {
	var capturedShardID, capturedReplicaID uint64

	adminSvc := &mockAdminService{
		promoteMemberFn: func(_ context.Context, shardID, replicaID uint64) error {
			capturedShardID = shardID
			capturedReplicaID = replicaID
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, adminSvc)
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.PromoteMemberRequest{
		ShardId:   2,
		ReplicaId: 200,
	})
	if err != nil {
		t.Fatalf("failed to marshal PromoteMemberRequest: %v", err)
	}

	resp, err := srv.handlePromoteMember(ctx, payload)
	if err != nil {
		t.Fatalf("handlePromoteMember returned unexpected error: %v", err)
	}

	if capturedShardID != 2 {
		t.Errorf("expected shardID 2, got %d", capturedShardID)
	}
	if capturedReplicaID != 200 {
		t.Errorf("expected replicaID 200, got %d", capturedReplicaID)
	}

	var empty emptypb.Empty
	if err := proto.Unmarshal(resp, &empty); err != nil {
		t.Fatalf("failed to unmarshal Empty: %v", err)
	}
}

func TestHandlePromoteMember_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "promote member failed"}
	adminSvc := &mockAdminService{
		promoteMemberFn: func(_ context.Context, _, _ uint64) error {
			return svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, adminSvc)
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.PromoteMemberRequest{
		ShardId:   1,
		ReplicaId: 1,
	})
	if err != nil {
		t.Fatalf("failed to marshal PromoteMemberRequest: %v", err)
	}

	_, err = srv.handlePromoteMember(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handlePromoteMember, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandlePromoteMember_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handlePromoteMember(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleRequestSnapshot
// ---------------------------------------------------------------------------

func TestHandleRequestSnapshot_Success(t *testing.T) {
	var capturedShardID uint64

	adminSvc := &mockAdminService{
		requestSnapshotFn: func(_ context.Context, shardID uint64) error {
			capturedShardID = shardID
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, adminSvc)
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.RequestSnapshotMessage{ShardId: 42})
	if err != nil {
		t.Fatalf("failed to marshal RequestSnapshotMessage: %v", err)
	}

	resp, err := srv.handleRequestSnapshot(ctx, payload)
	if err != nil {
		t.Fatalf("handleRequestSnapshot returned unexpected error: %v", err)
	}

	if capturedShardID != 42 {
		t.Errorf("expected shardID 42, got %d", capturedShardID)
	}

	var empty emptypb.Empty
	if err := proto.Unmarshal(resp, &empty); err != nil {
		t.Fatalf("failed to unmarshal Empty: %v", err)
	}
}

func TestHandleRequestSnapshot_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "snapshot failed"}
	adminSvc := &mockAdminService{
		requestSnapshotFn: func(_ context.Context, _ uint64) error {
			return svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, adminSvc)
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.RequestSnapshotMessage{ShardId: 1})
	if err != nil {
		t.Fatalf("failed to marshal RequestSnapshotMessage: %v", err)
	}

	_, err = srv.handleRequestSnapshot(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleRequestSnapshot, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleRequestSnapshot_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleRequestSnapshot(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleDrain
// ---------------------------------------------------------------------------

func TestHandleDrain_Success(t *testing.T) {
	drainCalled := false
	adminSvc := &mockAdminService{
		drainFn: func(_ context.Context) error {
			drainCalled = true
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, adminSvc)
	ctx := context.Background()

	resp, err := srv.handleDrain(ctx, nil)
	if err != nil {
		t.Fatalf("handleDrain returned unexpected error: %v", err)
	}

	if !drainCalled {
		t.Error("expected drain to be called")
	}

	var empty emptypb.Empty
	if err := proto.Unmarshal(resp, &empty); err != nil {
		t.Fatalf("failed to unmarshal Empty: %v", err)
	}
}

func TestHandleDrain_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "drain failed"}
	adminSvc := &mockAdminService{
		drainFn: func(_ context.Context) error {
			return svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, adminSvc)
	ctx := context.Background()

	_, err := srv.handleDrain(ctx, nil)
	if err == nil {
		t.Fatal("expected error from handleDrain, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleCapabilities
// ---------------------------------------------------------------------------

func TestHandleCapabilities_Success(t *testing.T) {
	adminSvc := &mockAdminService{
		capabilitiesFn: func(_ context.Context) (*capabilities.Capabilities, error) {
			return &capabilities.Capabilities{
				KV:          true,
				MVCC:        true,
				Transaction: true,
				Batch:       true,
				Watch:       true,
				Lease:       true,
				Index:       false,
				TimeSeries:  false,
				Vector:      false,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, adminSvc)
	ctx := context.Background()

	resp, err := srv.handleCapabilities(ctx, nil)
	if err != nil {
		t.Fatalf("handleCapabilities returned unexpected error: %v", err)
	}

	var capsResp pb.CapabilitiesResponse
	if err := proto.Unmarshal(resp, &capsResp); err != nil {
		t.Fatalf("failed to unmarshal CapabilitiesResponse: %v", err)
	}
	if !capsResp.GetKv() {
		t.Error("expected kv to be true")
	}
	if !capsResp.GetMvcc() {
		t.Error("expected mvcc to be true")
	}
	if !capsResp.GetTransaction() {
		t.Error("expected transaction to be true")
	}
	if !capsResp.GetBatch() {
		t.Error("expected batch to be true")
	}
	if !capsResp.GetWatch() {
		t.Error("expected watch to be true")
	}
	if !capsResp.GetLease() {
		t.Error("expected lease to be true")
	}
	if capsResp.GetIndex() {
		t.Error("expected index to be false")
	}
	if capsResp.GetTimeSeries() {
		t.Error("expected timeSeries to be false")
	}
	if capsResp.GetVector() {
		t.Error("expected vector to be false")
	}
}

func TestHandleCapabilities_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "capabilities failed"}
	adminSvc := &mockAdminService{
		capabilitiesFn: func(_ context.Context) (*capabilities.Capabilities, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, adminSvc)
	ctx := context.Background()

	_, err := srv.handleCapabilities(ctx, nil)
	if err == nil {
		t.Fatal("expected error from handleCapabilities, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

// joinReviewAdmin extends mockAdminService with the optional join-review
// interfaces the handlers type-assert for.
type joinReviewAdmin struct {
	mockAdminService
	err      error
	pending  []*service.PendingJoinRequest
	gotID    string
	gotArg   string
	observer [3]any
}

var (
	_ service.JoinRequestReviewer = (*joinReviewAdmin)(nil)
	_ service.ObserverMemberAdder = (*joinReviewAdmin)(nil)
	_ service.JoinIdentityRetirer = (*joinReviewAdmin)(nil)
)

func (a *joinReviewAdmin) ListPendingJoinRequests(context.Context) ([]*service.PendingJoinRequest, error) {
	return a.pending, a.err
}

func (a *joinReviewAdmin) ApproveJoinRequest(_ context.Context, id, nodeID string) error {
	a.gotID, a.gotArg = id, nodeID
	return a.err
}

func (a *joinReviewAdmin) RejectJoinRequest(_ context.Context, id, reason string) error {
	a.gotID, a.gotArg = id, reason
	return a.err
}

func (a *joinReviewAdmin) AddObserverMember(_ context.Context, shardID, replicaID uint64, raftAddr, identity string) error {
	a.observer = [3]any{shardID, replicaID, raftAddr}
	a.gotArg = identity
	return a.err
}

func (a *joinReviewAdmin) RetireJoinIdentity(_ context.Context, nodeID, reason string) error {
	a.gotID, a.gotArg = nodeID, reason
	return a.err
}

func TestJoinReviewHandlers_Unsupported(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	review := map[string]func(context.Context, []byte) ([]byte, error){
		"list":     srv.handleListPendingJoinRequests,
		"approve":  srv.handleApproveJoinRequest,
		"reject":   srv.handleRejectJoinRequest,
		"observer": srv.handleAddObserverMember,
	}
	for name, h := range review {
		t.Run(name, func(t *testing.T) {
			var ue *service.ClusterJoinRequestReviewUnsupportedError
			if _, err := h(context.Background(), nil); !errors.As(err, &ue) {
				t.Fatalf("err = %v, want *ClusterJoinRequestReviewUnsupportedError", err)
			}
		})
	}
	t.Run("retire", func(t *testing.T) {
		var nc *service.ClusterJoinTokenNotConfiguredError
		if _, err := srv.handleRetireJoinIdentity(context.Background(), nil); !errors.As(err, &nc) {
			t.Fatalf("err = %v, want *ClusterJoinTokenNotConfiguredError", err)
		}
	})
}

func TestJoinReviewHandlers_UnmarshalAndServiceErrors(t *testing.T) {
	svcErr := &testError{msg: "review refused"}
	admin := &joinReviewAdmin{err: svcErr}
	srv := newTestServer(&mockKVService{}, admin)
	handlers := map[string]func(context.Context, []byte) ([]byte, error){
		"approve":  srv.handleApproveJoinRequest,
		"reject":   srv.handleRejectJoinRequest,
		"observer": srv.handleAddObserverMember,
		"retire":   srv.handleRetireJoinIdentity,
	}
	for name, h := range handlers {
		t.Run(name+"/unmarshal", func(t *testing.T) {
			var ue *UnmarshalError
			if _, err := h(context.Background(), badProto); !errors.As(err, &ue) {
				t.Fatalf("err = %v, want *UnmarshalError", err)
			}
		})
		t.Run(name+"/service", func(t *testing.T) {
			if _, err := h(context.Background(), nil); !errors.Is(err, svcErr) {
				t.Fatalf("err = %v, want the service error", err)
			}
		})
	}
	t.Run("list/service", func(t *testing.T) {
		if _, err := srv.handleListPendingJoinRequests(context.Background(), nil); !errors.Is(err, svcErr) {
			t.Fatalf("err = %v, want the service error", err)
		}
	})
}

func TestHandleListPendingJoinRequests_Success(t *testing.T) {
	submitted := time.Date(2026, 10, 1, 12, 0, 0, 0, time.FixedZone("x", 3600))
	admin := &joinReviewAdmin{pending: []*service.PendingJoinRequest{{
		ID: "abc", NodeURI: "spiffe://db.example.com/node/n2", ReplicaID: 2,
		Features: []string{"f1"}, SubmittedAt: submitted, ExpiresAt: submitted.Add(time.Hour),
	}}}
	srv := newTestServer(&mockKVService{}, admin)
	out, err := srv.handleListPendingJoinRequests(context.Background(), nil)
	if err != nil {
		t.Fatalf("handleListPendingJoinRequests: %v", err)
	}
	var resp pb.ListPendingJoinRequestsResponse
	if err := proto.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.GetRequests()) != 1 {
		t.Fatalf("requests = %+v", resp.GetRequests())
	}
	r := resp.GetRequests()[0]
	if r.GetId() != "abc" || r.GetReplicaId() != 2 || r.GetSubmittedAt() != "2026-10-01T11:00:00Z" || r.GetExpiresAt() != "2026-10-01T12:00:00Z" {
		t.Fatalf("request = %+v", r)
	}
}

func TestJoinReviewHandlers_Success(t *testing.T) {
	tests := map[string]struct {
		call    func(*Server) ([]byte, error)
		wantID  string
		wantArg string
	}{
		"approve": {
			call: func(s *Server) ([]byte, error) {
				return s.handleApproveJoinRequest(context.Background(), mustMarshal(t, &pb.ApproveJoinRequestRequest{Id: "abc", NodeId: "n2"}))
			},
			wantID: "abc", wantArg: "n2",
		},
		"reject": {
			call: func(s *Server) ([]byte, error) {
				return s.handleRejectJoinRequest(context.Background(), mustMarshal(t, &pb.RejectJoinRequestRequest{Id: "abc", Reason: "unknown"}))
			},
			wantID: "abc", wantArg: "unknown",
		},
		"retire": {
			call: func(s *Server) ([]byte, error) {
				return s.handleRetireJoinIdentity(context.Background(), mustMarshal(t, &pb.RetireJoinIdentityRequest{NodeId: "n3", Reason: "decommissioned"}))
			},
			wantID: "n3", wantArg: "decommissioned",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			admin := &joinReviewAdmin{}
			if _, err := tc.call(newTestServer(&mockKVService{}, admin)); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if admin.gotID != tc.wantID || admin.gotArg != tc.wantArg {
				t.Fatalf("service got (%q, %q), want (%q, %q)", admin.gotID, admin.gotArg, tc.wantID, tc.wantArg)
			}
		})
	}
}

func TestHandleAddObserverMember_Success(t *testing.T) {
	admin := &joinReviewAdmin{}
	srv := newTestServer(&mockKVService{}, admin)
	_, err := srv.handleAddObserverMember(context.Background(), mustMarshal(t, &pb.AddObserverMemberRequest{
		ShardId: 1, ReplicaId: 4, RaftAddress: "n4.db.example.com:7000", MemberIdentity: "spiffe://db.example.com/node/n4",
	}))
	if err != nil {
		t.Fatalf("handleAddObserverMember: %v", err)
	}
	if admin.observer != [3]any{uint64(1), uint64(4), "n4.db.example.com:7000"} || admin.gotArg != "spiffe://db.example.com/node/n4" {
		t.Fatalf("service got %v identity=%q", admin.observer, admin.gotArg)
	}
}
