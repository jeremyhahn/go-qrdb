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

//go:build qrdb_etcd && !integration

package etcd

import (
	"context"
	"errors"
	"testing"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mockAdminServicer implements service.AdminServicer with an embedded
// interface for methods not under test and a function field for Peers.
type mockAdminServicer struct {
	service.AdminServicer
	peersFn func(ctx context.Context) (*service.PeersInfo, error)
}

func (m *mockAdminServicer) Peers(ctx context.Context) (*service.PeersInfo, error) {
	if m.peersFn != nil {
		return m.peersFn(ctx)
	}
	return nil, errors.New("not implemented")
}

func TestClusterServer_MemberList_Success(t *testing.T) {
	mock := &mockAdminServicer{
		peersFn: func(_ context.Context) (*service.PeersInfo, error) {
			return &service.PeersInfo{
				Shards: []service.ShardPeers{
					{
						ShardID:  1,
						LeaderID: 100,
						Peers: []service.PeerInfo{
							{ReplicaID: 100, Address: "10.0.0.1:5000", IsLeader: true},
							{ReplicaID: 200, Address: "10.0.0.2:5000", IsLeader: false},
						},
					},
					{
						ShardID:  2,
						LeaderID: 100,
						Peers: []service.PeerInfo{
							{ReplicaID: 100, Address: "10.0.0.1:5000", IsLeader: true},
							{ReplicaID: 300, Address: "10.0.0.3:5000", IsLeader: false},
						},
					},
				},
			}, nil
		},
	}

	s := &clusterServer{
		adminSvc:  mock,
		clusterID: 42,
		memberID:  100,
	}

	resp, err := s.MemberList(context.Background(), &pb.MemberListRequest{})
	if err != nil {
		t.Fatalf("MemberList returned unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("MemberList returned nil response")
	}

	// ReplicaID 100 appears in both shards; should be deduplicated to 3 members.
	if got := len(resp.Members); got != 3 {
		t.Fatalf("len(Members) = %d, want 3", got)
	}

	memberIDs := make(map[uint64]bool)
	for _, m := range resp.Members {
		memberIDs[m.ID] = true
	}
	for _, wantID := range []uint64{100, 200, 300} {
		if !memberIDs[wantID] {
			t.Errorf("member ID %d not found in response", wantID)
		}
	}

	// Verify header fields.
	if resp.Header == nil {
		t.Fatal("response header is nil")
	}
	if resp.Header.ClusterId != 42 {
		t.Errorf("Header.ClusterId = %d, want 42", resp.Header.ClusterId)
	}
	if resp.Header.MemberId != 100 {
		t.Errorf("Header.MemberId = %d, want 100", resp.Header.MemberId)
	}
}

func TestClusterServer_MemberList_SingleShard(t *testing.T) {
	mock := &mockAdminServicer{
		peersFn: func(_ context.Context) (*service.PeersInfo, error) {
			return &service.PeersInfo{
				Shards: []service.ShardPeers{
					{
						ShardID:  1,
						LeaderID: 10,
						Peers: []service.PeerInfo{
							{ReplicaID: 10, Address: "10.0.0.1:5000", IsLeader: true},
						},
					},
				},
			}, nil
		},
	}

	s := &clusterServer{
		adminSvc:  mock,
		clusterID: 1,
		memberID:  10,
	}

	resp, err := s.MemberList(context.Background(), &pb.MemberListRequest{})
	if err != nil {
		t.Fatalf("MemberList returned unexpected error: %v", err)
	}
	if got := len(resp.Members); got != 1 {
		t.Fatalf("len(Members) = %d, want 1", got)
	}
	m := resp.Members[0]
	if m.ID != 10 {
		t.Errorf("Member.ID = %d, want 10", m.ID)
	}
	if m.Name != "10.0.0.1:5000" {
		t.Errorf("Member.Name = %q, want %q", m.Name, "10.0.0.1:5000")
	}
	if len(m.PeerURLs) != 1 || m.PeerURLs[0] != "10.0.0.1:5000" {
		t.Errorf("Member.PeerURLs = %v, want [10.0.0.1:5000]", m.PeerURLs)
	}
	if len(m.ClientURLs) != 1 || m.ClientURLs[0] != "10.0.0.1:5000" {
		t.Errorf("Member.ClientURLs = %v, want [10.0.0.1:5000]", m.ClientURLs)
	}
}

func TestClusterServer_MemberList_EmptyShards(t *testing.T) {
	mock := &mockAdminServicer{
		peersFn: func(_ context.Context) (*service.PeersInfo, error) {
			return &service.PeersInfo{
				Shards: []service.ShardPeers{},
			}, nil
		},
	}

	s := &clusterServer{
		adminSvc:  mock,
		clusterID: 1,
		memberID:  10,
	}

	resp, err := s.MemberList(context.Background(), &pb.MemberListRequest{})
	if err != nil {
		t.Fatalf("MemberList returned unexpected error: %v", err)
	}
	if got := len(resp.Members); got != 0 {
		t.Fatalf("len(Members) = %d, want 0", got)
	}
}

func TestClusterServer_MemberList_NilAdminService(t *testing.T) {
	s := &clusterServer{
		adminSvc:  nil,
		clusterID: 1,
		memberID:  10,
	}

	_, err := s.MemberList(context.Background(), &pb.MemberListRequest{})
	if err == nil {
		t.Fatal("MemberList with nil adminSvc: expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.Unavailable {
		t.Errorf("code = %v, want %v", st.Code(), codes.Unavailable)
	}
}

func TestClusterServer_MemberList_PeersError(t *testing.T) {
	mock := &mockAdminServicer{
		peersFn: func(_ context.Context) (*service.PeersInfo, error) {
			return nil, errors.New("raft timeout")
		},
	}

	s := &clusterServer{
		adminSvc:  mock,
		clusterID: 1,
		memberID:  10,
	}

	_, err := s.MemberList(context.Background(), &pb.MemberListRequest{})
	if err == nil {
		t.Fatal("MemberList with Peers error: expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.Internal {
		t.Errorf("code = %v, want %v", st.Code(), codes.Internal)
	}
}

func TestClusterServer_MemberList_DeduplicatesAcrossThreeShards(t *testing.T) {
	mock := &mockAdminServicer{
		peersFn: func(_ context.Context) (*service.PeersInfo, error) {
			return &service.PeersInfo{
				Shards: []service.ShardPeers{
					{
						ShardID: 1,
						Peers: []service.PeerInfo{
							{ReplicaID: 1, Address: "addr1"},
							{ReplicaID: 2, Address: "addr2"},
						},
					},
					{
						ShardID: 2,
						Peers: []service.PeerInfo{
							{ReplicaID: 2, Address: "addr2"},
							{ReplicaID: 3, Address: "addr3"},
						},
					},
					{
						ShardID: 3,
						Peers: []service.PeerInfo{
							{ReplicaID: 1, Address: "addr1"},
							{ReplicaID: 3, Address: "addr3"},
						},
					},
				},
			}, nil
		},
	}

	s := &clusterServer{adminSvc: mock, clusterID: 5, memberID: 1}
	resp, err := s.MemberList(context.Background(), &pb.MemberListRequest{})
	if err != nil {
		t.Fatalf("MemberList returned unexpected error: %v", err)
	}
	if got := len(resp.Members); got != 3 {
		t.Fatalf("len(Members) = %d, want 3 (dedup across 3 shards)", got)
	}
}

func TestClusterServer_MemberList_NilRequest(t *testing.T) {
	mock := &mockAdminServicer{
		peersFn: func(_ context.Context) (*service.PeersInfo, error) {
			return &service.PeersInfo{
				Shards: []service.ShardPeers{
					{
						ShardID: 1,
						Peers: []service.PeerInfo{
							{ReplicaID: 1, Address: "addr1"},
						},
					},
				},
			}, nil
		},
	}

	s := &clusterServer{adminSvc: mock, clusterID: 1, memberID: 1}
	resp, err := s.MemberList(context.Background(), nil)
	if err != nil {
		t.Fatalf("MemberList with nil request returned unexpected error: %v", err)
	}
	if got := len(resp.Members); got != 1 {
		t.Errorf("len(Members) = %d, want 1", got)
	}
}

func TestClusterServer_UnimplementedMethods(t *testing.T) {
	tests := []struct {
		name string
		call func(s *clusterServer) error
	}{
		{
			name: "MemberAdd",
			call: func(s *clusterServer) error {
				_, err := s.MemberAdd(context.Background(), &pb.MemberAddRequest{})
				return err
			},
		},
		{
			name: "MemberRemove",
			call: func(s *clusterServer) error {
				_, err := s.MemberRemove(context.Background(), &pb.MemberRemoveRequest{})
				return err
			},
		},
		{
			name: "MemberUpdate",
			call: func(s *clusterServer) error {
				_, err := s.MemberUpdate(context.Background(), &pb.MemberUpdateRequest{})
				return err
			},
		},
		{
			name: "MemberPromote",
			call: func(s *clusterServer) error {
				_, err := s.MemberPromote(context.Background(), &pb.MemberPromoteRequest{})
				return err
			},
		},
	}

	s := &clusterServer{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(s)
			if err == nil {
				t.Fatalf("%s: expected error, got nil", tt.name)
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("%s: expected gRPC status error, got %T: %v", tt.name, err, err)
			}
			if st.Code() != codes.Unimplemented {
				t.Errorf("%s: code = %v, want %v", tt.name, st.Code(), codes.Unimplemented)
			}
			if st.Message() == "" {
				t.Errorf("%s: expected non-empty error message", tt.name)
			}
		})
	}
}

func TestClusterServer_UnimplementedMethods_NilRequests(t *testing.T) {
	tests := []struct {
		name string
		call func(s *clusterServer) error
	}{
		{
			name: "MemberAdd_nil",
			call: func(s *clusterServer) error {
				_, err := s.MemberAdd(context.Background(), nil)
				return err
			},
		},
		{
			name: "MemberRemove_nil",
			call: func(s *clusterServer) error {
				_, err := s.MemberRemove(context.Background(), nil)
				return err
			},
		},
		{
			name: "MemberUpdate_nil",
			call: func(s *clusterServer) error {
				_, err := s.MemberUpdate(context.Background(), nil)
				return err
			},
		},
		{
			name: "MemberPromote_nil",
			call: func(s *clusterServer) error {
				_, err := s.MemberPromote(context.Background(), nil)
				return err
			},
		},
	}

	s := &clusterServer{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(s)
			if err == nil {
				t.Fatalf("%s: expected error, got nil", tt.name)
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("%s: expected gRPC status error, got %T: %v", tt.name, err, err)
			}
			if st.Code() != codes.Unimplemented {
				t.Errorf("%s: code = %v, want %v", tt.name, st.Code(), codes.Unimplemented)
			}
		})
	}
}
