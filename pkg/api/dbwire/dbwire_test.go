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

package dbwire_test

import (
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire"
	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire/dbwiretest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// full sets every field the wire shapes carry, every secret included.
func full() *database.DatabaseInfo {
	return &database.DatabaseInfo{
		TenantID: "acme",
		Name:     "orders",
		Engine:   database.EngineKV,
		Backend:  database.BackendRaft,
		Status:   database.StatusOpen,
		Protocol: database.ProtocolConfig{
			MySQL:      true,
			PostgreSQL: true,
			Etcd:       &database.EtcdConfig{Enabled: true, ListenAddr: ":2379", AdvertiseClientURLs: []string{"https://db.example.com:2379"}},
		},
		DataDir:      "/var/lib/qrdb/orders",
		ShardID:      1007,
		Mode:         database.ModeEmbedded,
		Replicas:     3,
		DedicatedWAL: true,
		Raft:         dbwiretest.RaftConfig(),
		Barrier:      &database.DatabaseBarrierConfig{Enabled: true, Mode: "shamir", SharesRequired: 2, SharesTotal: 3},
		Connection:   &database.DatabaseConnectionConfig{Transport: "grpc", Address: "db.example.com:63000", CAFile: "/etc/qrdb/ca.pem"},
		SQL:          &database.SQLConfig{Concurrency: database.ConcurrencyPessimistic},
	}
}

func TestInfo_CarriesEveryFieldAndNoSecret(t *testing.T) {
	in := full()
	out := dbwire.Info(in)
	if out.TenantID != "acme" || out.Name != "orders" || out.Engine != "kv" || out.Backend != "raft" || out.Status != "open" {
		t.Errorf("identity = %+v", out)
	}
	if out.DataDir != in.DataDir || out.ShardID != 1007 || out.Mode != "embedded" || out.Replicas != 3 || !out.DedicatedWAL {
		t.Errorf("placement = %+v", out)
	}
	if !out.Protocol.MySQL || !out.Protocol.PostgreSQL || out.Protocol.Etcd == nil || out.Protocol.Etcd.ListenAddr != ":2379" {
		t.Errorf("protocol = %+v", out.Protocol)
	}
	r := out.Raft
	if r == nil || r.ClusterMode != "distributed" || r.ReplicaID != 1 || r.RaftAddress != in.Raft.RaftAddress || r.Discovery != "static" {
		t.Fatalf("raft = %+v", r)
	}
	if r.Static == nil || !r.Static.SharedSecretSet || len(r.Static.Peers) != 1 {
		t.Errorf("static = %+v", r.Static)
	}
	if r.Multicast == nil || !r.Multicast.SharedSecretSet || r.Multicast.MulticastGroup != in.Raft.Multicast.MulticastGroup || r.Multicast.MinPeers != 2 {
		t.Errorf("multicast = %+v", r.Multicast)
	}
	if r.DNS == nil || r.DNS.Domain != "example.com" {
		t.Errorf("dns = %+v", r.DNS)
	}
	if r.Token == nil || !r.Token.TokenSet || r.Token.ClusterSize != 3 {
		t.Errorf("token = %+v", r.Token)
	}
	if b := out.Barrier; b == nil || !b.Enabled || b.Mode != "shamir" || b.SharesRequired != 2 || b.SharesTotal != 3 {
		t.Errorf("barrier = %+v", out.Barrier)
	}
	if c := out.Connection; c == nil || c.Address != "db.example.com:63000" || c.CAFile != "/etc/qrdb/ca.pem" {
		t.Errorf("connection = %+v", out.Connection)
	}
	if out.SQL == nil || out.SQL.Concurrency != "pessimistic" {
		t.Errorf("sql = %+v", out.SQL)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	dbwiretest.AssertNoSecrets(t, "dbwire.Info", encoded)
}

func TestInfoProto_CarriesEveryFieldAndNoSecret(t *testing.T) {
	in := full()
	out := dbwire.InfoProto(in)
	if out.GetTenantId() != "acme" || out.GetShardId() != 1007 || out.GetReplicas() != 3 || !out.GetDedicatedWal() {
		t.Errorf("identity = %v", out)
	}
	if out.GetProtocol().GetEtcd().GetListenAddr() != ":2379" || !out.GetProtocol().GetMysql() {
		t.Errorf("protocol = %v", out.GetProtocol())
	}
	r := out.GetRaft()
	if !r.GetStatic().GetSharedSecretSet() || !r.GetMulticast().GetSharedSecretSet() || !r.GetToken().GetTokenSet() {
		t.Errorf("raft set flags = %v", r)
	}
	if r.GetMulticast().GetMulticastGroup() != in.Raft.Multicast.MulticastGroup || r.GetDns().GetDomain() != "example.com" || r.GetToken().GetClusterSize() != 3 {
		t.Errorf("raft discovery = %v", r)
	}
	if out.GetBarrier().GetSharesTotal() != 3 || out.GetConnection().GetRemoteAddress() != "db.example.com:63000" {
		t.Errorf("barrier/connection = %v / %v", out.GetBarrier(), out.GetConnection())
	}
	if out.GetSql().GetConcurrency() != "pessimistic" {
		t.Errorf("sql = %v", out.GetSql())
	}
	encoded, err := protojson.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	dbwiretest.AssertNoSecrets(t, "dbwire.InfoProto", encoded)
}

// An unset secret is not reported as set.
func TestInfo_UnsetSecretIsNotReportedSet(t *testing.T) {
	in := &database.DatabaseInfo{Raft: &database.DatabaseRaftConfig{
		Static:    &database.StaticDiscoveryConfig{Peers: []string{"db.example.com:63001"}},
		Multicast: &database.MulticastDiscoveryConfig{MulticastGroup: "239.255.0.1:63100"},
		Token:     &database.TokenDiscoveryConfig{Endpoint: "https://db.example.com/join"},
	}}
	j := dbwire.Info(in).Raft
	if j.Static.SharedSecretSet || j.Multicast.SharedSecretSet || j.Token.TokenSet {
		t.Errorf("JSON reports an unset secret as set: %+v", j)
	}
	p := dbwire.InfoProto(in).GetRaft()
	if p.GetStatic().GetSharedSecretSet() || p.GetMulticast().GetSharedSecretSet() || p.GetToken().GetTokenSet() {
		t.Errorf("protobuf reports an unset secret as set: %v", p)
	}
}

func TestInfo_OptionalBlocksAbsent(t *testing.T) {
	in := &database.DatabaseInfo{Name: "plain", Engine: database.EngineKV, Backend: database.BackendMemory}
	j := dbwire.Info(in)
	if j.Raft != nil || j.Barrier != nil || j.Connection != nil || j.SQL != nil || j.Protocol.Etcd != nil {
		t.Errorf("JSON invented blocks: %+v", j)
	}
	p := dbwire.InfoProto(in)
	if p.GetRaft() != nil || p.GetBarrier() != nil || p.GetConnection() != nil || p.GetSql() != nil || p.GetProtocol().GetEtcd() != nil {
		t.Errorf("protobuf invented blocks: %v", p)
	}
	r := dbwire.Info(&database.DatabaseInfo{Raft: &database.DatabaseRaftConfig{ClusterMode: database.RaftLocal}}).Raft
	if r.Static != nil || r.Multicast != nil || r.DNS != nil || r.Token != nil {
		t.Errorf("JSON invented discovery blocks: %+v", r)
	}
	pr := dbwire.InfoProto(&database.DatabaseInfo{Raft: &database.DatabaseRaftConfig{ClusterMode: database.RaftLocal}}).GetRaft()
	if pr.GetStatic() != nil || pr.GetMulticast() != nil || pr.GetDns() != nil || pr.GetToken() != nil {
		t.Errorf("protobuf invented discovery blocks: %v", pr)
	}
}

func TestInfos(t *testing.T) {
	if got := dbwire.Infos(nil); got == nil || len(got) != 0 {
		t.Errorf("Infos(nil) = %#v, want an empty, non-nil list", got)
	}
	if got := dbwire.InfosProto(nil); got == nil || len(got) != 0 {
		t.Errorf("InfosProto(nil) = %#v, want an empty, non-nil list", got)
	}
	in := []database.DatabaseInfo{*full(), {Name: "b"}}
	if got := dbwire.Infos(in); len(got) != 2 || got[0].Name != "orders" || got[1].Name != "b" {
		t.Errorf("Infos = %+v", got)
	}
	if got := dbwire.InfosProto(in); len(got) != 2 || got[0].GetName() != "orders" || got[1].GetName() != "b" {
		t.Errorf("InfosProto = %v", got)
	}
}
