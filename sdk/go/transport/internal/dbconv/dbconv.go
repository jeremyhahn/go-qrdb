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

// Package dbconv converts the protobuf database information message into the
// transport-neutral DatabaseInfo. The gRPC and Unix transports receive the
// same message, so the conversion lives here once. It is internal: only the
// transport implementations may import it.
package dbconv

import (
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Info converts a protobuf DatabaseInfoResponse. Like the message, the
// result holds no secret: each discovery secret is reported only as set.
func Info(resp *pb.DatabaseInfoResponse) *transport.DatabaseInfo {
	info := &transport.DatabaseInfo{
		TenantID:     resp.GetTenantId(),
		Name:         resp.GetName(),
		Engine:       resp.GetEngine(),
		Backend:      resp.GetBackend(),
		Status:       resp.GetStatus(),
		DataDir:      resp.GetDataDir(),
		ShardID:      resp.GetShardId(),
		Mode:         resp.GetMode(),
		StorageCodec: resp.GetStorageCodec(),
		Replicas:     int(resp.GetReplicas()),
		DedicatedWAL: resp.GetDedicatedWal(),
		Raft:         raftInfo(resp.GetRaft()),
	}
	if p := resp.GetProtocol(); p != nil {
		info.Protocol = transport.ProtocolConfig{MySQL: p.GetMysql(), PostgreSQL: p.GetPostgresql()}
		if e := p.GetEtcd(); e != nil {
			info.Protocol.Etcd = &transport.EtcdProtocolConfig{
				Enabled:             e.GetEnabled(),
				ListenAddr:          e.GetListenAddr(),
				AdvertiseClientURLs: e.GetAdvertiseClientUrls(),
			}
		}
	}
	if b := resp.GetBarrier(); b != nil {
		info.Barrier = &transport.DatabaseBarrierInfo{
			Enabled:        b.GetEnabled(),
			Mode:           b.GetMode(),
			SharesRequired: int(b.GetSharesRequired()),
			SharesTotal:    int(b.GetSharesTotal()),
		}
	}
	if c := resp.GetConnection(); c != nil {
		info.Connection = &transport.DatabaseConnectionConfig{
			Transport: c.GetTransport(),
			Address:   c.GetRemoteAddress(),
			CAFile:    c.GetCaFile(),
			CertFile:  c.GetCertFile(),
			KeyFile:   c.GetKeyFile(),
		}
	}
	if q := resp.GetSql(); q != nil {
		info.SQL = &transport.DatabaseSQLConfig{Concurrency: q.GetConcurrency()}
	}
	return info
}

// Infos converts a protobuf database list. It is never nil.
func Infos(resp []*pb.DatabaseInfoResponse) []transport.DatabaseInfo {
	out := make([]transport.DatabaseInfo, 0, len(resp))
	for _, r := range resp {
		out = append(out, *Info(r))
	}
	return out
}

func raftInfo(r *pb.DatabaseRaftInfo) *transport.DatabaseRaftInfo {
	if r == nil {
		return nil
	}
	out := &transport.DatabaseRaftInfo{
		ClusterMode:    r.GetClusterMode(),
		ReplicaID:      r.GetReplicaId(),
		RaftAddress:    r.GetRaftAddress(),
		Peers:          r.GetPeers(),
		LocalNodeCount: int(r.GetLocalNodeCount()),
		Discovery:      r.GetDiscovery(),
	}
	if s := r.GetStatic(); s != nil {
		out.Static = &transport.StaticDiscoveryInfo{Peers: s.GetPeers(), SharedSecretSet: s.GetSharedSecretSet()}
	}
	if m := r.GetMulticast(); m != nil {
		out.Multicast = &transport.MulticastDiscoveryInfo{
			SharedSecretSet:  m.GetSharedSecretSet(),
			MulticastGroup:   m.GetMulticastGroup(),
			Interface:        m.GetIface(),
			MinPeers:         int(m.GetMinPeers()),
			AnnounceInterval: m.GetAnnounceInterval(),
			DiscoverTimeout:  m.GetDiscoverTimeout(),
		}
	}
	if d := r.GetDns(); d != nil {
		out.DNS = &transport.DNSDiscoveryInfo{
			Service:       d.GetService(),
			Proto:         d.GetProto(),
			Domain:        d.GetDomain(),
			LookupTimeout: d.GetLookupTimeout(),
		}
	}
	if tk := r.GetToken(); tk != nil {
		out.Token = &transport.TokenDiscoveryInfo{
			Endpoint:    tk.GetEndpoint(),
			TokenSet:    tk.GetTokenSet(),
			ClusterSize: int(tk.GetClusterSize()),
			Timeout:     tk.GetTimeout(),
		}
	}
	return out
}
