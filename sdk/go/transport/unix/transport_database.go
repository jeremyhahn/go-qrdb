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

package unix

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.DatabaseClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// DatabaseClient implementation
// ---------------------------------------------------------------------------

// CreateDatabase creates a new database by calling the DatabaseCreate gRPC
// method over the Unix domain socket.
func (t *Transport) CreateDatabase(ctx context.Context, req transport.CreateDatabaseRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	pbReq := &pb.DatabaseCreateRequest{
		Name:    req.Name,
		Engine:  req.Engine,
		Backend: req.Backend,
		DataDir: req.DataDir,
		Protocol: &pb.DatabaseProtocolConfig{
			Mysql:      req.Protocol.MySQL,
			Postgresql: req.Protocol.PostgreSQL,
		},
	}

	if req.Protocol.Etcd != nil {
		pbReq.Protocol.Etcd = &pb.DatabaseEtcdConfig{
			Enabled:             req.Protocol.Etcd.Enabled,
			ListenAddr:          req.Protocol.Etcd.ListenAddr,
			AdvertiseClientUrls: req.Protocol.Etcd.AdvertiseClientURLs,
		}
	}

	if req.Raft != nil {
		pbReq.Raft = transportRaftConfigToProto(req.Raft)
	}

	resp, err := t.client.DatabaseCreate(ctx, pbReq)
	if err != nil {
		return nil, mapGRPCError("CreateDatabase", err)
	}

	return protoDatabaseInfoToTransport(resp), nil
}

// GetDatabase returns information about a specific database by calling the
// DatabaseGet gRPC method over the Unix domain socket.
func (t *Transport) GetDatabase(ctx context.Context, name string) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.DatabaseGet(ctx, &pb.DatabaseGetRequest{Name: name})
	if err != nil {
		return nil, mapGRPCError("GetDatabase", err)
	}

	return protoDatabaseInfoToTransport(resp), nil
}

// ListDatabases returns information about all databases by calling the
// DatabaseList gRPC method over the Unix domain socket.
func (t *Transport) ListDatabases(ctx context.Context) ([]transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.DatabaseList(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("ListDatabases", err)
	}

	databases := make([]transport.DatabaseInfo, 0, len(resp.GetDatabases()))
	for _, db := range resp.GetDatabases() {
		databases = append(databases, *protoDatabaseInfoToTransport(db))
	}

	return databases, nil
}

// DropDatabase removes a database by name by calling the DatabaseDrop gRPC
// method over the Unix domain socket.
func (t *Transport) DropDatabase(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.DatabaseDrop(ctx, &pb.DatabaseDropRequest{Name: name})
	if err != nil {
		return mapGRPCError("DropDatabase", err)
	}

	return nil
}

// UpdateDatabaseProtocol updates the wire protocol configuration for a
// database by calling the DatabaseUpdateProtocol gRPC method over the Unix
// domain socket.
func (t *Transport) UpdateDatabaseProtocol(ctx context.Context, req transport.UpdateDatabaseProtocolRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.DatabaseUpdateProtocol(ctx, &pb.DatabaseUpdateProtocolRequest{
		Name:       req.Name,
		Mysql:      req.MySQL,
		Postgresql: req.PostgreSQL,
	})
	if err != nil {
		return nil, mapGRPCError("UpdateDatabaseProtocol", err)
	}

	return protoDatabaseInfoToTransport(resp), nil
}

// UpdateDatabaseEtcd updates the etcd wire protocol configuration for a
// database by calling the DatabaseUpdateEtcd gRPC method over the Unix
// domain socket.
func (t *Transport) UpdateDatabaseEtcd(ctx context.Context, req transport.UpdateDatabaseEtcdRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.DatabaseUpdateEtcd(ctx, &pb.DatabaseUpdateEtcdRequest{
		Name:                req.Name,
		Enabled:             req.Enabled,
		ListenAddr:          req.ListenAddr,
		AdvertiseClientUrls: req.AdvertiseClientURLs,
	})
	if err != nil {
		return nil, mapGRPCError("UpdateDatabaseEtcd", err)
	}

	return protoDatabaseInfoToTransport(resp), nil
}

// UpdateDatabaseRaft updates the Raft consensus configuration for a database
// by calling the DatabaseUpdateRaft gRPC method over the Unix domain socket.
func (t *Transport) UpdateDatabaseRaft(ctx context.Context, req transport.UpdateDatabaseRaftRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	pbReq := &pb.DatabaseUpdateRaftRequest{
		Name: req.Name,
	}

	if req.Raft != nil {
		pbReq.Raft = transportRaftConfigToProto(req.Raft)
	}

	resp, err := t.client.DatabaseUpdateRaft(ctx, pbReq)
	if err != nil {
		return nil, mapGRPCError("UpdateDatabaseRaft", err)
	}

	return protoDatabaseInfoToTransport(resp), nil
}

// UpdateDatabaseBarrier updates the encryption barrier configuration for a
// database by calling the DatabaseUpdateBarrier gRPC method over the Unix
// domain socket.
func (t *Transport) UpdateDatabaseBarrier(ctx context.Context, req transport.UpdateDatabaseBarrierRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	pbReq := &pb.DatabaseUpdateBarrierRequest{
		Name: req.Name,
	}

	if req.Barrier != nil {
		pbReq.Barrier = &pb.DatabaseBarrierConfig{
			Enabled: req.Barrier.Enabled,
		}
	}

	resp, err := t.client.DatabaseUpdateBarrier(ctx, pbReq)
	if err != nil {
		return nil, mapGRPCError("UpdateDatabaseBarrier", err)
	}

	return protoDatabaseInfoToTransport(resp), nil
}

// UpdateDatabaseConnection updates the remote connection configuration for a
// database by calling the DatabaseUpdateConnection gRPC method over the Unix
// domain socket.
func (t *Transport) UpdateDatabaseConnection(ctx context.Context, req transport.UpdateDatabaseConnectionRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	pbReq := &pb.DatabaseUpdateConnectionRequest{
		Name: req.Name,
	}

	if req.Connection != nil {
		pbReq.Connection = &pb.DatabaseConnectionConfig{
			Transport:     req.Connection.Transport,
			RemoteAddress: req.Connection.Address,
			CaFile:        req.Connection.CAFile,
			CertFile:      req.Connection.CertFile,
			KeyFile:       req.Connection.KeyFile,
		}
	}

	resp, err := t.client.DatabaseUpdateConnection(ctx, pbReq)
	if err != nil {
		return nil, mapGRPCError("UpdateDatabaseConnection", err)
	}

	return protoDatabaseInfoToTransport(resp), nil
}

// ---------------------------------------------------------------------------
// Proto conversion helpers
// ---------------------------------------------------------------------------

// protoDatabaseInfoToTransport converts a protobuf DatabaseInfoResponse to the
// SDK transport DatabaseInfo type.
func protoDatabaseInfoToTransport(resp *pb.DatabaseInfoResponse) *transport.DatabaseInfo {
	info := &transport.DatabaseInfo{
		TenantID: resp.GetTenantId(),
		Name:     resp.GetName(),
		Engine:   resp.GetEngine(),
		Backend:  resp.GetBackend(),
		Status:   resp.GetStatus(),
		DataDir:  resp.GetDataDir(),
		ShardID:  resp.GetShardId(),
		Mode:     resp.GetMode(),
	}

	if p := resp.GetProtocol(); p != nil {
		info.Protocol = transport.ProtocolConfig{
			MySQL:      p.GetMysql(),
			PostgreSQL: p.GetPostgresql(),
		}
		if e := p.GetEtcd(); e != nil {
			info.Protocol.Etcd = &transport.EtcdProtocolConfig{
				Enabled:             e.GetEnabled(),
				ListenAddr:          e.GetListenAddr(),
				AdvertiseClientURLs: e.GetAdvertiseClientUrls(),
			}
		}
	}

	if r := resp.GetRaft(); r != nil {
		info.Raft = protoRaftConfigToTransport(r)
	}

	if b := resp.GetBarrier(); b != nil {
		info.Barrier = &transport.DatabaseBarrierConfig{
			Enabled: b.GetEnabled(),
		}
	}

	if c := resp.GetConnection(); c != nil {
		info.Connection = &transport.DatabaseConnectionConfig{
			Address: c.GetRemoteAddress(),
		}
	}

	return info
}

// protoRaftConfigToTransport converts a protobuf DatabaseRaftConfig to the
// SDK transport DatabaseRaftConfig type.
func protoRaftConfigToTransport(r *pb.DatabaseRaftConfig) *transport.DatabaseRaftConfig {
	return &transport.DatabaseRaftConfig{
		ClusterMode:    r.GetClusterMode(),
		ShardID:        r.GetShardId(),
		ReplicaID:      r.GetReplicaId(),
		RaftAddress:    r.GetRaftAddress(),
		Peers:          r.GetPeers(),
		LocalNodeCount: int(r.GetLocalNodeCount()),
	}
}

// transportRaftConfigToProto converts an SDK transport DatabaseRaftConfig to
// its protobuf representation.
func transportRaftConfigToProto(r *transport.DatabaseRaftConfig) *pb.DatabaseRaftConfig {
	return &pb.DatabaseRaftConfig{
		ClusterMode:    r.ClusterMode,
		ShardId:        r.ShardID,
		ReplicaId:      r.ReplicaID,
		RaftAddress:    r.RaftAddress,
		Peers:          r.Peers,
		LocalNodeCount: int32(r.LocalNodeCount),
	}
}
