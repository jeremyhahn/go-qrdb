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

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Database management handlers
// ---------------------------------------------------------------------------

// DatabaseCreate creates a new database with the specified configuration.
// When the request carries a barrier config with a non-empty secret, the
// database is created via the credentialed path (routing to
// CreateForTenantWithCredentials when the resolved DatabaseServicer
// supports it), mirroring the REST POST /v1/databases handler.
func (s *qrdbServiceServer) DatabaseCreate(ctx context.Context, req *pb.DatabaseCreateRequest) (*pb.DatabaseInfoResponse, error) {
	if s.databaseSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "database"})
	}

	cfg := protoDatabaseCreateToConfig(req)

	var creds seal.Credentials
	if b := req.GetBarrier(); b != nil {
		var err error
		if creds, err = seal.CredentialsFor([]byte(b.GetSecret())); err != nil {
			return nil, mapServiceError(err)
		}
		defer creds.Secret.Destroy()
	}

	info, err := service.CreateDatabaseForTenantWithCredentials(ctx, s.databaseSvc, cfg, creds)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return dbwire.InfoProto(info), nil
}

// DatabaseGet returns information about a specific database.
func (s *qrdbServiceServer) DatabaseGet(ctx context.Context, req *pb.DatabaseGetRequest) (*pb.DatabaseInfoResponse, error) {
	if s.databaseSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "database"})
	}

	info, err := s.databaseSvc.GetForTenant(ctx, req.GetName())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return dbwire.InfoProto(info), nil
}

// DatabaseList returns information about all open databases.
func (s *qrdbServiceServer) DatabaseList(ctx context.Context, _ *emptypb.Empty) (*pb.DatabaseListResponse, error) {
	if s.databaseSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "database"})
	}

	databases, err := s.databaseSvc.ListForTenant(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.DatabaseListResponse{Databases: dbwire.InfosProto(databases)}, nil
}

// DatabaseDrop closes and removes a database.
func (s *qrdbServiceServer) DatabaseDrop(ctx context.Context, req *pb.DatabaseDropRequest) (*emptypb.Empty, error) {
	if s.databaseSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "database"})
	}

	if err := s.databaseSvc.DropForTenant(ctx, req.GetName()); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// DatabaseUpdateProtocol updates the wire protocol configuration.
func (s *qrdbServiceServer) DatabaseUpdateProtocol(ctx context.Context, req *pb.DatabaseUpdateProtocolRequest) (*pb.DatabaseInfoResponse, error) {
	if s.databaseSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "database"})
	}

	protocol := database.ProtocolConfig{
		MySQL:      req.GetMysql(),
		PostgreSQL: req.GetPostgresql(),
	}

	info, err := s.databaseSvc.UpdateProtocolForTenant(ctx, req.GetName(), protocol)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return dbwire.InfoProto(info), nil
}

// DatabaseUpdateEtcd updates the etcd wire protocol configuration.
func (s *qrdbServiceServer) DatabaseUpdateEtcd(ctx context.Context, req *pb.DatabaseUpdateEtcdRequest) (*pb.DatabaseInfoResponse, error) {
	if s.databaseSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "database"})
	}

	etcd := &database.EtcdConfig{
		Enabled:             req.GetEnabled(),
		ListenAddr:          req.GetListenAddr(),
		AdvertiseClientURLs: req.GetAdvertiseClientUrls(),
	}

	info, err := s.databaseSvc.UpdateEtcdForTenant(ctx, req.GetName(), etcd)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return dbwire.InfoProto(info), nil
}

// DatabaseUpdateRaft updates the Raft consensus configuration.
func (s *qrdbServiceServer) DatabaseUpdateRaft(ctx context.Context, req *pb.DatabaseUpdateRaftRequest) (*pb.DatabaseInfoResponse, error) {
	if s.databaseSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "database"})
	}

	raft := protoRaftConfigToDomain(req.GetRaft())

	info, err := s.databaseSvc.UpdateRaftForTenant(ctx, req.GetName(), raft)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return dbwire.InfoProto(info), nil
}

// DatabaseScale changes the replication factor of an existing dedicated-raft
// database.
func (s *qrdbServiceServer) DatabaseScale(ctx context.Context, req *pb.DatabaseScaleRequest) (*pb.DatabaseInfoResponse, error) {
	if s.databaseSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "database"})
	}

	info, err := s.databaseSvc.ScaleForTenant(ctx, req.GetName(), int(req.GetTargetReplicas()))
	if err != nil {
		return nil, mapServiceError(err)
	}

	return dbwire.InfoProto(info), nil
}

// DatabaseUpdateBarrier updates the encryption barrier configuration.
func (s *qrdbServiceServer) DatabaseUpdateBarrier(ctx context.Context, req *pb.DatabaseUpdateBarrierRequest) (*pb.DatabaseInfoResponse, error) {
	if s.databaseSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "database"})
	}

	barrier := protoBarrierConfigToDomain(req.GetBarrier())

	info, err := s.databaseSvc.UpdateBarrierForTenant(ctx, req.GetName(), barrier)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return dbwire.InfoProto(info), nil
}

// DatabaseUpdateConnection updates the remote connection configuration.
func (s *qrdbServiceServer) DatabaseUpdateConnection(ctx context.Context, req *pb.DatabaseUpdateConnectionRequest) (*pb.DatabaseInfoResponse, error) {
	if s.databaseSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "database"})
	}

	conn := protoConnectionConfigToDomain(req.GetConnection())

	info, err := s.databaseSvc.UpdateConnectionForTenant(ctx, req.GetName(), conn)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return dbwire.InfoProto(info), nil
}

// DatabaseUpdateSQL updates a database's SQL settings -- its
// sql.concurrency. An unset sql restores the defaults.
func (s *qrdbServiceServer) DatabaseUpdateSQL(ctx context.Context, req *pb.DatabaseUpdateSQLRequest) (*pb.DatabaseInfoResponse, error) {
	if s.databaseSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "database"})
	}

	info, err := s.databaseSvc.UpdateSQLForTenant(ctx, req.GetName(), protoSQLConfigToDomain(req.GetSql()))
	if err != nil {
		return nil, mapServiceError(err)
	}

	return dbwire.InfoProto(info), nil
}

// DatabaseUpdateCompaction sets a database's scheduled-compaction policy. An
// unset compaction turns it off. The RPC requires mvcc:compact, and the
// service confines it to the caller's own databases.
func (s *qrdbServiceServer) DatabaseUpdateCompaction(ctx context.Context, req *pb.DatabaseUpdateCompactionRequest) (*pb.DatabaseInfoResponse, error) {
	if s.databaseSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "database"})
	}

	info, err := s.databaseSvc.UpdateCompactionForTenant(ctx, req.GetName(), protoCompactionConfigToDomain(req.GetCompaction()))
	if err != nil {
		return nil, mapServiceError(err)
	}

	return dbwire.InfoProto(info), nil
}

// DatabaseSeal seals the encryption barrier for a database.
func (s *qrdbServiceServer) DatabaseSeal(ctx context.Context, req *pb.DatabaseSealRequest) (*emptypb.Empty, error) {
	if s.databaseSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "database"})
	}
	if err := s.databaseSvc.SealDatabase(ctx, req.GetName()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// DatabaseUnseal unseals the encryption barrier for a database.
func (s *qrdbServiceServer) DatabaseUnseal(ctx context.Context, req *pb.DatabaseUnsealRequest) (*emptypb.Empty, error) {
	if s.databaseSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "database"})
	}
	creds, err := seal.CredentialsFor([]byte(req.GetSecret()))
	if err != nil {
		return nil, mapServiceError(err)
	}
	defer creds.Secret.Destroy()
	if err := s.databaseSvc.UnsealDatabase(ctx, req.GetName(), creds); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// DatabaseBarrierStatus returns the encryption-barrier status for a database.
func (s *qrdbServiceServer) DatabaseBarrierStatus(ctx context.Context, req *pb.DatabaseBarrierStatusRequest) (*pb.DatabaseBarrierStatusResponse, error) {
	if s.databaseSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "database"})
	}
	st, err := s.databaseSvc.DatabaseBarrierStatus(ctx, req.GetName())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return databaseBarrierStatusToProto(st), nil
}

// databaseBarrierStatusToProto converts a seal.BarrierStatus to its protobuf form.
func databaseBarrierStatusToProto(st *seal.BarrierStatus) *pb.DatabaseBarrierStatusResponse {
	if st == nil {
		return &pb.DatabaseBarrierStatusResponse{}
	}
	return &pb.DatabaseBarrierStatusResponse{
		Sealed:            st.Sealed,
		Strategy:          string(st.Strategy),
		HardwareBacked:    st.HardwareBacked,
		InitializedAtUnix: st.InitializedAt.Unix(),
	}
}

// ---------------------------------------------------------------------------
// Database type conversion helpers
// ---------------------------------------------------------------------------

// protoDatabaseCreateToConfig converts a DatabaseCreateRequest proto to a
// domain DatabaseConfig.
func protoDatabaseCreateToConfig(req *pb.DatabaseCreateRequest) database.DatabaseConfig {
	cfg := database.DatabaseConfig{
		Name:     req.GetName(),
		Engine:   database.EngineType(req.GetEngine()),
		Backend:  database.BackendType(req.GetBackend()),
		DataDir:  req.GetDataDir(),
		Replicas: int(req.GetReplicas()),
	}

	if p := req.GetProtocol(); p != nil {
		cfg.Protocol = protoProtocolConfigToDomain(p)
	}

	if r := req.GetRaft(); r != nil {
		cfg.Raft = protoRaftConfigToDomain(r)
	}

	if b := req.GetBarrier(); b != nil {
		cfg.Barrier = protoBarrierConfigToDomain(b)
	}

	cfg.SQL = protoSQLConfigToDomain(req.GetSql())

	return cfg
}

// protoSQLConfigToDomain converts a DatabaseSQLConfig proto to the domain
// SQLConfig; nil stays nil (the defaults).
func protoSQLConfigToDomain(c *pb.DatabaseSQLConfig) *database.SQLConfig {
	if c == nil {
		return nil
	}
	return &database.SQLConfig{Concurrency: database.Concurrency(c.GetConcurrency())}
}

// protoProtocolConfigToDomain converts a DatabaseProtocolConfig proto to a
// domain ProtocolConfig.
func protoProtocolConfigToDomain(p *pb.DatabaseProtocolConfig) database.ProtocolConfig {
	pc := database.ProtocolConfig{
		MySQL:      p.GetMysql(),
		PostgreSQL: p.GetPostgresql(),
	}

	if e := p.GetEtcd(); e != nil {
		pc.Etcd = &database.EtcdConfig{
			Enabled:             e.GetEnabled(),
			ListenAddr:          e.GetListenAddr(),
			AdvertiseClientURLs: e.GetAdvertiseClientUrls(),
		}
	}

	return pc
}

// protoRaftConfigToDomain converts a DatabaseRaftConfig proto to a domain
// DatabaseRaftConfig.
func protoRaftConfigToDomain(r *pb.DatabaseRaftConfig) *database.DatabaseRaftConfig {
	if r == nil {
		return nil
	}

	raft := &database.DatabaseRaftConfig{
		ClusterMode:    database.RaftClusterMode(r.GetClusterMode()),
		ReplicaID:      r.GetReplicaId(),
		RaftAddress:    r.GetRaftAddress(),
		Peers:          r.GetPeers(),
		LocalNodeCount: int(r.GetLocalNodeCount()),
		Discovery:      database.DiscoveryMethod(r.GetDiscovery()),
	}

	if s := r.GetStatic(); s != nil {
		raft.Static = &database.StaticDiscoveryConfig{
			Peers:        s.GetPeers(),
			SharedSecret: s.GetSharedSecret(),
		}
	}

	if m := r.GetMulticast(); m != nil {
		raft.Multicast = &database.MulticastDiscoveryConfig{
			SharedSecret:     m.GetSharedSecret(),
			MulticastGroup:   m.GetMulticastGroup(),
			Interface:        m.GetIface(),
			MinPeers:         int(m.GetMinPeers()),
			AnnounceInterval: m.GetAnnounceInterval(),
			DiscoverTimeout:  m.GetDiscoverTimeout(),
		}
	}

	if d := r.GetDns(); d != nil {
		raft.DNS = &database.DNSDiscoveryConfig{
			Service:       d.GetService(),
			Proto:         d.GetProto(),
			Domain:        d.GetDomain(),
			LookupTimeout: d.GetLookupTimeout(),
		}
	}

	if t := r.GetToken(); t != nil {
		raft.Token = &database.TokenDiscoveryConfig{
			Endpoint:    t.GetEndpoint(),
			Token:       t.GetToken(),
			ClusterSize: int(t.GetClusterSize()),
			Timeout:     t.GetTimeout(),
		}
	}

	return raft
}

// protoBarrierConfigToDomain converts a DatabaseBarrierConfig proto to a
// domain DatabaseBarrierConfig.
func protoBarrierConfigToDomain(b *pb.DatabaseBarrierConfig) *database.DatabaseBarrierConfig {
	if b == nil {
		return nil
	}
	return &database.DatabaseBarrierConfig{
		Enabled:        b.GetEnabled(),
		Mode:           b.GetMode(),
		SharesRequired: int(b.GetSharesRequired()),
		SharesTotal:    int(b.GetSharesTotal()),
	}
}

// protoConnectionConfigToDomain converts a DatabaseConnectionConfig proto
// to a domain DatabaseConnectionConfig.
func protoConnectionConfigToDomain(c *pb.DatabaseConnectionConfig) *database.DatabaseConnectionConfig {
	if c == nil {
		return nil
	}
	return &database.DatabaseConnectionConfig{
		Transport: c.GetTransport(),
		Address:   c.GetRemoteAddress(),
		CAFile:    c.GetCaFile(),
		CertFile:  c.GetCertFile(),
		KeyFile:   c.GetKeyFile(),
	}
}

// protoCompactionConfigToDomain converts a DatabaseCompactionConfig proto to
// the domain policy; nil stays nil (off).
func protoCompactionConfigToDomain(c *pb.DatabaseCompactionConfig) *compaction.Policy {
	if c == nil {
		return nil
	}
	return &compaction.Policy{
		Mode:            compaction.Mode(c.GetMode()),
		RetainRevisions: c.GetRetainRevisions(),
		RetainPeriod:    c.GetRetainPeriod(),
	}
}
