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

//go:build !integration

package etcd

import (
	"context"
	"errors"
	"testing"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMaintenanceServer_Status_WithRevision(t *testing.T) {
	mock := &mockMVCCKVServicer{
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 42, nil
		},
	}

	s := &maintenanceServer{
		mvccSvc:   mock,
		clusterID: 10,
		memberID:  20,
	}

	resp, err := s.Status(context.Background(), &pb.StatusRequest{})
	if err != nil {
		t.Fatalf("Status returned unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("Status returned nil response")
	}
	if resp.Leader != 20 {
		t.Errorf("Leader = %d, want 20", resp.Leader)
	}
	if resp.RaftIndex != 42 {
		t.Errorf("RaftIndex = %d, want 42", resp.RaftIndex)
	}
	if resp.RaftTerm != 1 {
		t.Errorf("RaftTerm = %d, want 1", resp.RaftTerm)
	}
	if resp.Header == nil {
		t.Fatal("response header is nil")
	}
	if resp.Header.ClusterId != 10 {
		t.Errorf("Header.ClusterId = %d, want 10", resp.Header.ClusterId)
	}
	if resp.Header.MemberId != 20 {
		t.Errorf("Header.MemberId = %d, want 20", resp.Header.MemberId)
	}
	if resp.Header.Revision != 42 {
		t.Errorf("Header.Revision = %d, want 42", resp.Header.Revision)
	}
}

func TestMaintenanceServer_Status_NilMVCCSvc(t *testing.T) {
	s := &maintenanceServer{
		mvccSvc:   nil,
		clusterID: 10,
		memberID:  20,
	}

	resp, err := s.Status(context.Background(), &pb.StatusRequest{})
	if err != nil {
		t.Fatalf("Status with nil mvccSvc returned unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("Status returned nil response")
	}
	if resp.RaftIndex != 0 {
		t.Errorf("RaftIndex = %d, want 0 when mvccSvc is nil", resp.RaftIndex)
	}
	if resp.Leader != 20 {
		t.Errorf("Leader = %d, want 20", resp.Leader)
	}
}

func TestMaintenanceServer_Status_RevisionError(t *testing.T) {
	mock := &mockMVCCKVServicer{
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 0, errors.New("storage error")
		},
	}

	s := &maintenanceServer{
		mvccSvc:   mock,
		clusterID: 10,
		memberID:  20,
	}

	// Status ignores the error from CurrentRevision and uses rev=0.
	resp, err := s.Status(context.Background(), &pb.StatusRequest{})
	if err != nil {
		t.Fatalf("Status returned unexpected error: %v", err)
	}
	if resp.RaftIndex != 0 {
		t.Errorf("RaftIndex = %d, want 0 when CurrentRevision errors", resp.RaftIndex)
	}
}

func TestMaintenanceServer_Status_ZeroRevision(t *testing.T) {
	mock := &mockMVCCKVServicer{
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 0, nil
		},
	}

	s := &maintenanceServer{
		mvccSvc:   mock,
		clusterID: 5,
		memberID:  15,
	}

	resp, err := s.Status(context.Background(), &pb.StatusRequest{})
	if err != nil {
		t.Fatalf("Status returned unexpected error: %v", err)
	}
	if resp.RaftIndex != 0 {
		t.Errorf("RaftIndex = %d, want 0", resp.RaftIndex)
	}
}

func TestMaintenanceServer_Status_LargeRevision(t *testing.T) {
	mock := &mockMVCCKVServicer{
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 999999, nil
		},
	}

	s := &maintenanceServer{
		mvccSvc:   mock,
		clusterID: 1,
		memberID:  2,
	}

	resp, err := s.Status(context.Background(), &pb.StatusRequest{})
	if err != nil {
		t.Fatalf("Status returned unexpected error: %v", err)
	}
	if resp.RaftIndex != 999999 {
		t.Errorf("RaftIndex = %d, want 999999", resp.RaftIndex)
	}
}

func TestMaintenanceServer_Defragment_Success(t *testing.T) {
	s := &maintenanceServer{
		clusterID: 10,
		memberID:  20,
	}

	resp, err := s.Defragment(context.Background(), &pb.DefragmentRequest{})
	if err != nil {
		t.Fatalf("Defragment returned unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("Defragment returned nil response")
	}
	if resp.Header == nil {
		t.Fatal("Defragment response header is nil")
	}
	if resp.Header.ClusterId != 10 {
		t.Errorf("Header.ClusterId = %d, want 10", resp.Header.ClusterId)
	}
	if resp.Header.MemberId != 20 {
		t.Errorf("Header.MemberId = %d, want 20", resp.Header.MemberId)
	}
}

func TestMaintenanceServer_Defragment_NilRequest(t *testing.T) {
	s := &maintenanceServer{
		clusterID: 10,
		memberID:  20,
	}

	resp, err := s.Defragment(context.Background(), nil)
	if err != nil {
		t.Fatalf("Defragment with nil request returned unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("Defragment returned nil response")
	}
}

func TestMaintenanceServer_Alarm_ReturnsEmpty(t *testing.T) {
	s := &maintenanceServer{
		clusterID: 10,
		memberID:  20,
	}

	resp, err := s.Alarm(context.Background(), &pb.AlarmRequest{})
	if err != nil {
		t.Fatalf("Alarm returned unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("Alarm returned nil response")
	}
	if len(resp.Alarms) != 0 {
		t.Errorf("len(Alarms) = %d, want 0", len(resp.Alarms))
	}
	if resp.Header == nil {
		t.Fatal("Alarm response header is nil")
	}
	if resp.Header.ClusterId != 10 {
		t.Errorf("Header.ClusterId = %d, want 10", resp.Header.ClusterId)
	}
}

func TestMaintenanceServer_Alarm_NilRequest(t *testing.T) {
	s := &maintenanceServer{
		clusterID: 10,
		memberID:  20,
	}

	resp, err := s.Alarm(context.Background(), nil)
	if err != nil {
		t.Fatalf("Alarm with nil request returned unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("Alarm returned nil response")
	}
}

func TestMaintenanceServer_UnimplementedMethods(t *testing.T) {
	tests := []struct {
		name string
		call func(s *maintenanceServer) error
	}{
		{
			name: "Hash",
			call: func(s *maintenanceServer) error {
				_, err := s.Hash(context.Background(), &pb.HashRequest{})
				return err
			},
		},
		{
			name: "HashKV",
			call: func(s *maintenanceServer) error {
				_, err := s.HashKV(context.Background(), &pb.HashKVRequest{})
				return err
			},
		},
		{
			name: "Snapshot",
			call: func(s *maintenanceServer) error {
				return s.Snapshot(&pb.SnapshotRequest{}, nil)
			},
		},
		{
			name: "MoveLeader",
			call: func(s *maintenanceServer) error {
				_, err := s.MoveLeader(context.Background(), &pb.MoveLeaderRequest{})
				return err
			},
		},
		{
			name: "Downgrade",
			call: func(s *maintenanceServer) error {
				_, err := s.Downgrade(context.Background(), &pb.DowngradeRequest{})
				return err
			},
		},
	}

	s := &maintenanceServer{}
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

func TestMaintenanceServer_UnimplementedMethods_NilRequests(t *testing.T) {
	tests := []struct {
		name string
		call func(s *maintenanceServer) error
	}{
		{
			name: "Hash_nil",
			call: func(s *maintenanceServer) error {
				_, err := s.Hash(context.Background(), nil)
				return err
			},
		},
		{
			name: "HashKV_nil",
			call: func(s *maintenanceServer) error {
				_, err := s.HashKV(context.Background(), nil)
				return err
			},
		},
		{
			name: "Snapshot_nil",
			call: func(s *maintenanceServer) error {
				return s.Snapshot(nil, nil)
			},
		},
		{
			name: "MoveLeader_nil",
			call: func(s *maintenanceServer) error {
				_, err := s.MoveLeader(context.Background(), nil)
				return err
			},
		},
		{
			name: "Downgrade_nil",
			call: func(s *maintenanceServer) error {
				_, err := s.Downgrade(context.Background(), nil)
				return err
			},
		},
	}

	s := &maintenanceServer{}
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
