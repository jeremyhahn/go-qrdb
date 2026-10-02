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

// Package dbwire converts database information to its transport wire
// shapes. It is the one conversion every transport shares -- the JSON shape
// for REST, QUIC, MCP, the embedded transport and the desktop bindings, the
// protobuf shape for gRPC and the Unix socket -- so they cannot drift apart.
//
// A secret set on a database is write-only. The wire shapes are read views
// with no field that can hold one: each discovery secret is reported only as
// set, and the barrier passphrase, which is never stored, not at all.
package dbwire

import (
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Info converts database information to its JSON wire form.
func Info(info *database.DatabaseInfo) *transport.DatabaseInfo {
	out := &transport.DatabaseInfo{
		TenantID: info.TenantID,
		Name:     info.Name,
		Engine:   string(info.Engine),
		Backend:  string(info.Backend),
		Status:   string(info.Status),
		Protocol: transport.ProtocolConfig{
			MySQL:      info.Protocol.MySQL,
			PostgreSQL: info.Protocol.PostgreSQL,
		},
		DataDir:      info.DataDir,
		ShardID:      info.ShardID,
		Mode:         string(info.Mode),
		StorageCodec: info.StorageCodec,
		Replicas:     info.Replicas,
		DedicatedWAL: info.DedicatedWAL,
		Raft:         raftInfo(info.Raft),
	}
	if e := info.Protocol.Etcd; e != nil {
		out.Protocol.Etcd = &transport.EtcdProtocolConfig{
			Enabled:             e.Enabled,
			ListenAddr:          e.ListenAddr,
			AdvertiseClientURLs: e.AdvertiseClientURLs,
		}
	}
	if b := info.Barrier; b != nil {
		out.Barrier = &transport.DatabaseBarrierInfo{
			Enabled:        b.Enabled,
			Mode:           b.Mode,
			SharesRequired: b.SharesRequired,
			SharesTotal:    b.SharesTotal,
		}
	}
	if c := info.Connection; c != nil {
		out.Connection = &transport.DatabaseConnectionConfig{
			Transport: c.Transport,
			Address:   c.Address,
			CAFile:    c.CAFile,
			CertFile:  c.CertFile,
			KeyFile:   c.KeyFile,
		}
	}
	if s := info.SQL; s != nil {
		out.SQL = &transport.DatabaseSQLConfig{Concurrency: string(s.Concurrency)}
	}
	return out
}

// Infos converts a list of database information to its JSON wire form. It is
// never nil, so an empty list encodes as [] rather than null.
func Infos(infos []database.DatabaseInfo) []transport.DatabaseInfo {
	out := make([]transport.DatabaseInfo, 0, len(infos))
	for i := range infos {
		out = append(out, *Info(&infos[i]))
	}
	return out
}

func raftInfo(r *database.DatabaseRaftConfig) *transport.DatabaseRaftInfo {
	if r == nil {
		return nil
	}
	out := &transport.DatabaseRaftInfo{
		ClusterMode:    string(r.ClusterMode),
		ReplicaID:      r.ReplicaID,
		RaftAddress:    r.RaftAddress,
		Peers:          r.Peers,
		LocalNodeCount: r.LocalNodeCount,
		Discovery:      string(r.Discovery),
	}
	if s := r.Static; s != nil {
		out.Static = &transport.StaticDiscoveryInfo{Peers: s.Peers, SharedSecretSet: s.SharedSecret != ""}
	}
	if m := r.Multicast; m != nil {
		out.Multicast = &transport.MulticastDiscoveryInfo{
			SharedSecretSet:  m.SharedSecret != "",
			MulticastGroup:   m.MulticastGroup,
			Interface:        m.Interface,
			MinPeers:         m.MinPeers,
			AnnounceInterval: m.AnnounceInterval,
			DiscoverTimeout:  m.DiscoverTimeout,
		}
	}
	if d := r.DNS; d != nil {
		out.DNS = &transport.DNSDiscoveryInfo{
			Service:       d.Service,
			Proto:         d.Proto,
			Domain:        d.Domain,
			LookupTimeout: d.LookupTimeout,
		}
	}
	if tk := r.Token; tk != nil {
		out.Token = &transport.TokenDiscoveryInfo{
			Endpoint:    tk.Endpoint,
			TokenSet:    tk.Token != "",
			ClusterSize: tk.ClusterSize,
			Timeout:     tk.Timeout,
		}
	}
	return out
}

// InfoProto converts database information to its protobuf form, used by gRPC
// and the Unix socket.
func InfoProto(info *database.DatabaseInfo) *pb.DatabaseInfoResponse {
	out := &pb.DatabaseInfoResponse{
		TenantId: info.TenantID,
		Name:     info.Name,
		Engine:   string(info.Engine),
		Backend:  string(info.Backend),
		Status:   string(info.Status),
		Protocol: &pb.DatabaseProtocolConfig{
			Mysql:      info.Protocol.MySQL,
			Postgresql: info.Protocol.PostgreSQL,
		},
		DataDir:      info.DataDir,
		ShardId:      info.ShardID,
		Mode:         string(info.Mode),
		StorageCodec: info.StorageCodec,
		Replicas:     uint32(info.Replicas),
		DedicatedWal: info.DedicatedWAL,
		Raft:         raftInfoProto(info.Raft),
	}
	if e := info.Protocol.Etcd; e != nil {
		out.Protocol.Etcd = &pb.DatabaseEtcdConfig{
			Enabled:             e.Enabled,
			ListenAddr:          e.ListenAddr,
			AdvertiseClientUrls: e.AdvertiseClientURLs,
		}
	}
	if b := info.Barrier; b != nil {
		out.Barrier = &pb.DatabaseBarrierInfo{
			Enabled:        b.Enabled,
			Mode:           b.Mode,
			SharesRequired: int32(b.SharesRequired),
			SharesTotal:    int32(b.SharesTotal),
		}
	}
	if c := info.Connection; c != nil {
		out.Connection = &pb.DatabaseConnectionConfig{
			Transport:     c.Transport,
			RemoteAddress: c.Address,
			CaFile:        c.CAFile,
			CertFile:      c.CertFile,
			KeyFile:       c.KeyFile,
		}
	}
	if s := info.SQL; s != nil {
		out.Sql = &pb.DatabaseSQLConfig{Concurrency: string(s.Concurrency)}
	}
	return out
}

// InfosProto converts a list of database information to its protobuf form.
func InfosProto(infos []database.DatabaseInfo) []*pb.DatabaseInfoResponse {
	out := make([]*pb.DatabaseInfoResponse, 0, len(infos))
	for i := range infos {
		out = append(out, InfoProto(&infos[i]))
	}
	return out
}

func raftInfoProto(r *database.DatabaseRaftConfig) *pb.DatabaseRaftInfo {
	if r == nil {
		return nil
	}
	out := &pb.DatabaseRaftInfo{
		ClusterMode:    string(r.ClusterMode),
		ReplicaId:      r.ReplicaID,
		RaftAddress:    r.RaftAddress,
		Peers:          r.Peers,
		LocalNodeCount: int32(r.LocalNodeCount),
		Discovery:      string(r.Discovery),
	}
	if s := r.Static; s != nil {
		out.Static = &pb.DatabaseStaticDiscoveryInfo{Peers: s.Peers, SharedSecretSet: s.SharedSecret != ""}
	}
	if m := r.Multicast; m != nil {
		out.Multicast = &pb.DatabaseMulticastDiscoveryInfo{
			SharedSecretSet:  m.SharedSecret != "",
			MulticastGroup:   m.MulticastGroup,
			Iface:            m.Interface,
			MinPeers:         int32(m.MinPeers),
			AnnounceInterval: m.AnnounceInterval,
			DiscoverTimeout:  m.DiscoverTimeout,
		}
	}
	if d := r.DNS; d != nil {
		out.Dns = &pb.DatabaseDNSDiscovery{
			Service:       d.Service,
			Proto:         d.Proto,
			Domain:        d.Domain,
			LookupTimeout: d.LookupTimeout,
		}
	}
	if tk := r.Token; tk != nil {
		out.Token = &pb.DatabaseTokenDiscoveryInfo{
			Endpoint:    tk.Endpoint,
			TokenSet:    tk.Token != "",
			ClusterSize: int32(tk.ClusterSize),
			Timeout:     tk.Timeout,
		}
	}
	return out
}
