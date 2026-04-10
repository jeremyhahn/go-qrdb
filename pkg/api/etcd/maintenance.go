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

package etcd

import (
	"context"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// maintenanceServer implements etcdserverpb.MaintenanceServer.
type maintenanceServer struct {
	pb.UnimplementedMaintenanceServer
	adminSvc  service.AdminServicer
	mvccSvc   service.MVCCKVServicer
	clusterID uint64
	memberID  uint64
}

// Status returns the status of the server including the current MVCC
// revision and Raft state.
func (s *maintenanceServer) Status(ctx context.Context, _ *pb.StatusRequest) (*pb.StatusResponse, error) {
	var rev int64
	if s.mvccSvc != nil {
		rev, _ = s.mvccSvc.CurrentRevision(ctx)
	}
	return &pb.StatusResponse{
		Header:    responseHeader(s.clusterID, s.memberID, rev, 0),
		Leader:    s.memberID,
		RaftIndex: uint64(rev),
		RaftTerm:  1,
		DbSize:    0,
	}, nil
}

// Defragment triggers a defragmentation operation. Returns immediately
// as qrdb uses PebbleDB which handles compaction automatically.
func (s *maintenanceServer) Defragment(_ context.Context, _ *pb.DefragmentRequest) (*pb.DefragmentResponse, error) {
	return &pb.DefragmentResponse{
		Header: responseHeader(s.clusterID, s.memberID, 0, 0),
	}, nil
}

// Alarm handles alarm get/activate/deactivate requests. Returns an
// empty alarm list as qrdb does not implement the etcd alarm system.
func (s *maintenanceServer) Alarm(_ context.Context, _ *pb.AlarmRequest) (*pb.AlarmResponse, error) {
	return &pb.AlarmResponse{
		Header: responseHeader(s.clusterID, s.memberID, 0, 0),
	}, nil
}

// Hash returns the hash of the backend. Not supported through the
// etcd API.
func (s *maintenanceServer) Hash(_ context.Context, _ *pb.HashRequest) (*pb.HashResponse, error) {
	return nil, status.Error(codes.Unimplemented, "hash not supported")
}

// HashKV returns the hash of the KV backend for a given revision. Not
// supported through the etcd API.
func (s *maintenanceServer) HashKV(_ context.Context, _ *pb.HashKVRequest) (*pb.HashKVResponse, error) {
	return nil, status.Error(codes.Unimplemented, "hash kv not supported")
}

// Snapshot streams the entire backend snapshot. Not supported through
// the etcd API; use the qrdb admin API instead.
func (s *maintenanceServer) Snapshot(_ *pb.SnapshotRequest, _ pb.Maintenance_SnapshotServer) error {
	return status.Error(codes.Unimplemented, "snapshot: use qrdb admin API")
}

// MoveLeader transfers leadership to another member. Not supported
// through the etcd API; use the qrdb admin API instead.
func (s *maintenanceServer) MoveLeader(_ context.Context, _ *pb.MoveLeaderRequest) (*pb.MoveLeaderResponse, error) {
	return nil, status.Error(codes.Unimplemented, "move leader: use qrdb admin API")
}

// Downgrade manages downgrades. Not supported by qrdb.
func (s *maintenanceServer) Downgrade(_ context.Context, _ *pb.DowngradeRequest) (*pb.DowngradeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "downgrade not supported")
}
