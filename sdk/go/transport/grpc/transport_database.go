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

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/dbconv"
)

// Compile-time interface assertion.
var _ transport.DatabaseClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// DatabaseClient implementation
// ---------------------------------------------------------------------------

// CreateDatabase creates a new database with the specified engine and storage
// backend.
func (t *Transport) CreateDatabase(ctx context.Context, req transport.CreateDatabaseRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	pbReq := &pb.DatabaseCreateRequest{
		Name:     req.Name,
		Engine:   req.Engine,
		Backend:  req.Backend,
		DataDir:  req.DataDir,
		Replicas: uint32(req.Replicas),
	}

	if req.Protocol.MySQL || req.Protocol.PostgreSQL || req.Protocol.Etcd != nil {
		pbReq.Protocol = transportProtocolConfigToProto(req.Protocol)
	}

	if req.Raft != nil {
		pbReq.Raft = transportRaftConfigToProto(req.Raft)
	}

	if req.Barrier != nil {
		pbReq.Barrier = &pb.DatabaseBarrierConfig{
			Enabled:        req.Barrier.Enabled,
			Mode:           req.Barrier.Mode,
			SharesRequired: int32(req.Barrier.SharesRequired),
			SharesTotal:    int32(req.Barrier.SharesTotal),
			Secret:         req.Barrier.Secret,
		}
	}

	if req.SQL != nil {
		pbReq.Sql = &pb.DatabaseSQLConfig{Concurrency: req.SQL.Concurrency}
	}

	resp, err := t.client.DatabaseCreate(ctx, pbReq)
	if err != nil {
		return nil, mapGRPCError("CreateDatabase", err)
	}

	return dbconv.Info(resp), nil
}

// GetDatabase returns information about a specific database.
func (t *Transport) GetDatabase(ctx context.Context, name string) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.DatabaseGet(ctx, &pb.DatabaseGetRequest{Name: name})
	if err != nil {
		return nil, mapGRPCError("GetDatabase", err)
	}

	return dbconv.Info(resp), nil
}

// ListDatabases returns information about all databases.
func (t *Transport) ListDatabases(ctx context.Context) ([]transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.DatabaseList(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("ListDatabases", err)
	}

	return dbconv.Infos(resp.GetDatabases()), nil
}

// DropDatabase removes a database by name.
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
// database.
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

	return dbconv.Info(resp), nil
}

// UpdateDatabaseEtcd updates the etcd wire protocol configuration for a
// database.
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

	return dbconv.Info(resp), nil
}

// UpdateDatabaseRaft updates the Raft configuration for a database.
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

	return dbconv.Info(resp), nil
}

// ScaleDatabase changes the replication factor of a dedicated-raft database.
func (t *Transport) ScaleDatabase(ctx context.Context, req transport.ScaleDatabaseRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	pbReq := &pb.DatabaseScaleRequest{
		Name:           req.Name,
		TargetReplicas: int32(req.TargetReplicas),
	}

	resp, err := t.client.DatabaseScale(ctx, pbReq)
	if err != nil {
		return nil, mapGRPCError("ScaleDatabase", err)
	}

	return dbconv.Info(resp), nil
}

// UpdateDatabaseBarrier updates the barrier configuration for a database.
func (t *Transport) UpdateDatabaseBarrier(ctx context.Context, req transport.UpdateDatabaseBarrierRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	pbReq := &pb.DatabaseUpdateBarrierRequest{
		Name: req.Name,
	}

	if req.Barrier != nil {
		pbReq.Barrier = &pb.DatabaseBarrierConfig{
			Enabled:        req.Barrier.Enabled,
			Mode:           req.Barrier.Mode,
			SharesRequired: int32(req.Barrier.SharesRequired),
			SharesTotal:    int32(req.Barrier.SharesTotal),
		}
	}

	resp, err := t.client.DatabaseUpdateBarrier(ctx, pbReq)
	if err != nil {
		return nil, mapGRPCError("UpdateDatabaseBarrier", err)
	}

	return dbconv.Info(resp), nil
}

// UpdateDatabaseCompaction sets a database's scheduled-compaction policy by
// calling the DatabaseUpdateCompaction gRPC method.
func (t *Transport) UpdateDatabaseCompaction(ctx context.Context, req transport.UpdateDatabaseCompactionRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	pbReq := &pb.DatabaseUpdateCompactionRequest{Name: req.Name}
	if c := req.Compaction; c != nil {
		pbReq.Compaction = &pb.DatabaseCompactionConfig{
			Mode:            c.Mode,
			RetainRevisions: c.RetainRevisions,
			RetainPeriod:    c.RetainPeriod,
		}
	}

	resp, err := t.client.DatabaseUpdateCompaction(ctx, pbReq)
	if err != nil {
		return nil, mapGRPCError("UpdateDatabaseCompaction", err)
	}

	return dbconv.Info(resp), nil
}

// UpdateDatabaseSQL updates a database's SQL settings by calling the
// DatabaseUpdateSQL gRPC method.
func (t *Transport) UpdateDatabaseSQL(ctx context.Context, req transport.UpdateDatabaseSQLRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	pbReq := &pb.DatabaseUpdateSQLRequest{Name: req.Name}
	if req.SQL != nil {
		pbReq.Sql = &pb.DatabaseSQLConfig{Concurrency: req.SQL.Concurrency}
	}

	resp, err := t.client.DatabaseUpdateSQL(ctx, pbReq)
	if err != nil {
		return nil, mapGRPCError("UpdateDatabaseSQL", err)
	}

	return dbconv.Info(resp), nil
}

// UpdateDatabaseConnection updates the connection configuration for a
// database.
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
			UseTls:        req.Connection.CAFile != "" || req.Connection.CertFile != "" || req.Connection.KeyFile != "",
			CaFile:        req.Connection.CAFile,
			CertFile:      req.Connection.CertFile,
			KeyFile:       req.Connection.KeyFile,
		}
	}

	resp, err := t.client.DatabaseUpdateConnection(ctx, pbReq)
	if err != nil {
		return nil, mapGRPCError("UpdateDatabaseConnection", err)
	}

	return dbconv.Info(resp), nil
}

// ---------------------------------------------------------------------------
// Database type conversion helpers
// ---------------------------------------------------------------------------

// transportProtocolConfigToProto converts a transport ProtocolConfig to its
// protobuf representation.
func transportProtocolConfigToProto(pc transport.ProtocolConfig) *pb.DatabaseProtocolConfig {
	proto := &pb.DatabaseProtocolConfig{
		Mysql:      pc.MySQL,
		Postgresql: pc.PostgreSQL,
	}

	if pc.Etcd != nil {
		proto.Etcd = &pb.DatabaseEtcdConfig{
			Enabled:             pc.Etcd.Enabled,
			ListenAddr:          pc.Etcd.ListenAddr,
			AdvertiseClientUrls: pc.Etcd.AdvertiseClientURLs,
		}
	}

	return proto
}

// transportRaftConfigToProto converts a transport DatabaseRaftConfig to its
// protobuf representation.
func transportRaftConfigToProto(r *transport.DatabaseRaftConfig) *pb.DatabaseRaftConfig {
	if r == nil {
		return nil
	}
	return &pb.DatabaseRaftConfig{
		ClusterMode:    r.ClusterMode,
		ReplicaId:      r.ReplicaID,
		RaftAddress:    r.RaftAddress,
		Peers:          r.Peers,
		LocalNodeCount: int32(r.LocalNodeCount),
	}
}
