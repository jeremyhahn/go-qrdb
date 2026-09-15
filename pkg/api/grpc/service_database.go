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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

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
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "database"}).Error())
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

	return databaseInfoToProto(info), nil
}

// DatabaseGet returns information about a specific database.
func (s *qrdbServiceServer) DatabaseGet(ctx context.Context, req *pb.DatabaseGetRequest) (*pb.DatabaseInfoResponse, error) {
	if s.databaseSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "database"}).Error())
	}

	info, err := s.databaseSvc.GetForTenant(ctx, req.GetName())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return databaseInfoToProto(info), nil
}

// DatabaseList returns information about all open databases.
func (s *qrdbServiceServer) DatabaseList(ctx context.Context, _ *emptypb.Empty) (*pb.DatabaseListResponse, error) {
	if s.databaseSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "database"}).Error())
	}

	databases, err := s.databaseSvc.ListForTenant(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}

	pbDatabases := make([]*pb.DatabaseInfoResponse, len(databases))
	for i, db := range databases {
		d := db
		pbDatabases[i] = databaseInfoToProto(&d)
	}

	return &pb.DatabaseListResponse{Databases: pbDatabases}, nil
}

// DatabaseDrop closes and removes a database.
func (s *qrdbServiceServer) DatabaseDrop(ctx context.Context, req *pb.DatabaseDropRequest) (*emptypb.Empty, error) {
	if s.databaseSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "database"}).Error())
	}

	if err := s.databaseSvc.DropForTenant(ctx, req.GetName()); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// DatabaseUpdateProtocol updates the wire protocol configuration.
func (s *qrdbServiceServer) DatabaseUpdateProtocol(ctx context.Context, req *pb.DatabaseUpdateProtocolRequest) (*pb.DatabaseInfoResponse, error) {
	if s.databaseSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "database"}).Error())
	}

	protocol := database.ProtocolConfig{
		MySQL:      req.GetMysql(),
		PostgreSQL: req.GetPostgresql(),
	}

	info, err := s.databaseSvc.UpdateProtocolForTenant(ctx, req.GetName(), protocol)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return databaseInfoToProto(info), nil
}

// DatabaseUpdateEtcd updates the etcd wire protocol configuration.
func (s *qrdbServiceServer) DatabaseUpdateEtcd(ctx context.Context, req *pb.DatabaseUpdateEtcdRequest) (*pb.DatabaseInfoResponse, error) {
	if s.databaseSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "database"}).Error())
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

	return databaseInfoToProto(info), nil
}

// DatabaseUpdateRaft updates the Raft consensus configuration.
func (s *qrdbServiceServer) DatabaseUpdateRaft(ctx context.Context, req *pb.DatabaseUpdateRaftRequest) (*pb.DatabaseInfoResponse, error) {
	if s.databaseSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "database"}).Error())
	}

	raft := protoRaftConfigToDomain(req.GetRaft())

	info, err := s.databaseSvc.UpdateRaftForTenant(ctx, req.GetName(), raft)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return databaseInfoToProto(info), nil
}

// DatabaseScale changes the replication factor of an existing dedicated-raft
// database.
func (s *qrdbServiceServer) DatabaseScale(ctx context.Context, req *pb.DatabaseScaleRequest) (*pb.DatabaseInfoResponse, error) {
	if s.databaseSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "database"}).Error())
	}

	info, err := s.databaseSvc.ScaleForTenant(ctx, req.GetName(), int(req.GetTargetReplicas()))
	if err != nil {
		return nil, mapServiceError(err)
	}

	return databaseInfoToProto(info), nil
}

// DatabaseUpdateBarrier updates the encryption barrier configuration.
func (s *qrdbServiceServer) DatabaseUpdateBarrier(ctx context.Context, req *pb.DatabaseUpdateBarrierRequest) (*pb.DatabaseInfoResponse, error) {
	if s.databaseSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "database"}).Error())
	}

	barrier := protoBarrierConfigToDomain(req.GetBarrier())

	info, err := s.databaseSvc.UpdateBarrierForTenant(ctx, req.GetName(), barrier)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return databaseInfoToProto(info), nil
}

// DatabaseUpdateConnection updates the remote connection configuration.
func (s *qrdbServiceServer) DatabaseUpdateConnection(ctx context.Context, req *pb.DatabaseUpdateConnectionRequest) (*pb.DatabaseInfoResponse, error) {
	if s.databaseSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "database"}).Error())
	}

	conn := protoConnectionConfigToDomain(req.GetConnection())

	info, err := s.databaseSvc.UpdateConnectionForTenant(ctx, req.GetName(), conn)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return databaseInfoToProto(info), nil
}

// DatabaseSeal seals the encryption barrier for a database.
func (s *qrdbServiceServer) DatabaseSeal(ctx context.Context, req *pb.DatabaseSealRequest) (*emptypb.Empty, error) {
	if s.databaseSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "database"}).Error())
	}
	if err := s.databaseSvc.SealDatabase(ctx, req.GetName()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// DatabaseUnseal unseals the encryption barrier for a database.
func (s *qrdbServiceServer) DatabaseUnseal(ctx context.Context, req *pb.DatabaseUnsealRequest) (*emptypb.Empty, error) {
	if s.databaseSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "database"}).Error())
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
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "database"}).Error())
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

	return cfg
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
		ShardID:        r.GetShardId(),
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

// databaseInfoToProto converts a domain DatabaseInfo to its protobuf
// representation.
func databaseInfoToProto(info *database.DatabaseInfo) *pb.DatabaseInfoResponse {
	resp := &pb.DatabaseInfoResponse{
		Name:     info.Name,
		Engine:   string(info.Engine),
		Backend:  string(info.Backend),
		Status:   string(info.Status),
		DataDir:  info.DataDir,
		ShardId:  info.ShardID,
		Mode:     string(info.Mode),
		TenantId: info.TenantID,
		Replicas: uint32(info.Replicas),
	}

	resp.Protocol = domainProtocolConfigToProto(info.Protocol)

	if info.Raft != nil {
		resp.Raft = domainRaftConfigToProto(info.Raft)
	}

	if info.Barrier != nil {
		resp.Barrier = &pb.DatabaseBarrierConfig{
			Enabled:        info.Barrier.Enabled,
			Mode:           info.Barrier.Mode,
			SharesRequired: int32(info.Barrier.SharesRequired),
			SharesTotal:    int32(info.Barrier.SharesTotal),
		}
	}

	if info.Connection != nil {
		resp.Connection = &pb.DatabaseConnectionConfig{
			Transport:     info.Connection.Transport,
			RemoteAddress: info.Connection.Address,
			CaFile:        info.Connection.CAFile,
			CertFile:      info.Connection.CertFile,
			KeyFile:       info.Connection.KeyFile,
		}
	}

	return resp
}

// domainProtocolConfigToProto converts a domain ProtocolConfig to its
// protobuf representation.
func domainProtocolConfigToProto(pc database.ProtocolConfig) *pb.DatabaseProtocolConfig {
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

// domainRaftConfigToProto converts a domain DatabaseRaftConfig to its
// protobuf representation.
func domainRaftConfigToProto(r *database.DatabaseRaftConfig) *pb.DatabaseRaftConfig {
	raft := &pb.DatabaseRaftConfig{
		ClusterMode:    string(r.ClusterMode),
		ShardId:        r.ShardID,
		ReplicaId:      r.ReplicaID,
		RaftAddress:    r.RaftAddress,
		Peers:          r.Peers,
		LocalNodeCount: int32(r.LocalNodeCount),
		Discovery:      string(r.Discovery),
	}

	if r.Static != nil {
		raft.Static = &pb.DatabaseStaticDiscovery{
			Peers:        r.Static.Peers,
			SharedSecret: r.Static.SharedSecret,
		}
	}

	if r.Multicast != nil {
		raft.Multicast = &pb.DatabaseMulticastDiscovery{
			SharedSecret:     r.Multicast.SharedSecret,
			MulticastGroup:   r.Multicast.MulticastGroup,
			Iface:            r.Multicast.Interface,
			MinPeers:         int32(r.Multicast.MinPeers),
			AnnounceInterval: r.Multicast.AnnounceInterval,
			DiscoverTimeout:  r.Multicast.DiscoverTimeout,
		}
	}

	if r.DNS != nil {
		raft.Dns = &pb.DatabaseDNSDiscovery{
			Service:       r.DNS.Service,
			Proto:         r.DNS.Proto,
			Domain:        r.DNS.Domain,
			LookupTimeout: r.DNS.LookupTimeout,
		}
	}

	if r.Token != nil {
		raft.Token = &pb.DatabaseTokenDiscovery{
			Endpoint:    r.Token.Endpoint,
			Token:       r.Token.Token,
			ClusterSize: int32(r.Token.ClusterSize),
			Timeout:     r.Token.Timeout,
		}
	}

	return raft
}
