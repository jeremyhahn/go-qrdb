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

// Package dbwiretest is the shared fixture for the write-only-secrets tests:
// every transport stores the same secret-bearing database through the real
// DatabaseService and reads it back, and every one asserts the same thing.
package dbwiretest

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// Name is the database the fixture stores.
const Name = "vault"

// The secrets the fixture stores. Each is distinctive, so finding its bytes
// anywhere in a response -- under any key, in any encoding that keeps
// strings as written -- means the response disclosed it.
const (
	StaticSecret    = "static-secret-4f1c9e2a7b"
	MulticastSecret = "multicast-secret-8d3b6a0e5c"
	DiscoveryToken  = "discovery-token-2e7a9c4f1d"
)

// Secrets lists every secret the fixture stores.
var Secrets = []string{StaticSecret, MulticastSecret, DiscoveryToken}

// RaftConfig is a distributed Raft configuration that carries every
// discovery secret.
func RaftConfig() *database.DatabaseRaftConfig {
	return &database.DatabaseRaftConfig{
		ClusterMode:  database.RaftDistributed,
		StateStorage: database.BackendFile,
		ReplicaID:    1,
		RaftAddress:  "db.example.com:63001",
		Peers:        []string{"db.example.com:63001"},
		Discovery:    database.DiscoveryStatic,
		Static: &database.StaticDiscoveryConfig{
			Peers:        []string{"db.example.com:63001"},
			SharedSecret: StaticSecret,
		},
		Multicast: &database.MulticastDiscoveryConfig{
			SharedSecret:   MulticastSecret,
			MulticastGroup: "239.255.0.1:63100",
			MinPeers:       2,
		},
		DNS: &database.DNSDiscoveryConfig{Service: "qrdb", Proto: "tcp", Domain: "example.com"},
		Token: &database.TokenDiscoveryConfig{
			Endpoint:    "https://db.example.com/discovery",
			Token:       DiscoveryToken,
			ClusterSize: 3,
		},
	}
}

// ClaimShards gives mgr a shard ID claimer over an in-memory store. Seed
// creates its database with a Raft block, which the manager issues a shard
// ID, so the manager behind the service passed to Seed needs one.
func ClaimShards(t testing.TB, mgr *database.Manager) {
	t.Helper()
	c, err := shard.NewClaimer(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatalf("shard.NewClaimer: %v", err)
	}
	mgr.SetShardIDClaimer(c)
}

// Seed stores the fixture database through the real service: a create
// recording the Raft state storage, then a Raft update carrying every
// discovery secret, as an operator would. The manager behind svc needs a
// shard ID claimer (ClaimShards).
func Seed(t testing.TB, svc service.DatabaseServicer) {
	t.Helper()
	// The Raft state storage is recorded at create and immutable after it,
	// so the create records the one RaftConfig names and the update below
	// keeps it.
	if _, err := svc.Create(database.DatabaseConfig{
		Name:    Name,
		Backend: database.BackendMemory,
		Raft: &database.DatabaseRaftConfig{
			ClusterMode:  RaftConfig().ClusterMode,
			StateStorage: RaftConfig().StateStorage,
		},
	}); err != nil {
		t.Fatalf("create %s: %v", Name, err)
	}
	if _, err := svc.UpdateRaft(Name, RaftConfig()); err != nil {
		t.Fatalf("update raft on %s: %v", Name, err)
	}
	// The service holds the secrets: the transports are what must not
	// return them.
	info, err := svc.Get(Name)
	if err != nil {
		t.Fatalf("get %s: %v", Name, err)
	}
	if info.Raft == nil || info.Raft.Static == nil || info.Raft.Static.SharedSecret != StaticSecret {
		t.Fatalf("the service did not keep the stored secrets: %+v", info.Raft)
	}
}

// AssertNoSecrets fails when a response holds any stored secret, and when any
// raft block in it does not report each discovery block's secret as set --
// static, multicast and token, each by its own flag. what names the transport
// and operation.
func AssertNoSecrets(t testing.TB, what string, response []byte) {
	t.Helper()
	// protojson spaces its output unpredictably; compare compact JSON.
	var compact bytes.Buffer
	if err := json.Compact(&compact, response); err == nil {
		response = compact.Bytes()
	}
	for _, secret := range Secrets {
		if bytes.Contains(response, []byte(secret)) {
			t.Errorf("%s returned the stored secret %q:\n%s", what, secret, response)
		}
	}
	var decoded any
	if err := json.Unmarshal(response, &decoded); err != nil {
		t.Fatalf("%s: response is not JSON: %v\n%s", what, err, response)
	}
	rafts := raftBlocks(decoded, nil)
	if len(rafts) == 0 {
		t.Errorf("%s: response holds no raft block to check:\n%s", what, response)
	}
	for _, raft := range rafts {
		for block, flag := range setFlags {
			b, _ := raft[block].(map[string]any)
			if b == nil || b[flag] != true {
				t.Errorf("%s does not report %s.%s as set:\n%s", what, block, flag, response)
			}
		}
	}
}

// setFlags names, per discovery block the fixture fills, the flag that
// reports its secret set. Each block is checked on its own: one block's flag
// cannot stand in for another's.
var setFlags = map[string]string{
	"static":    "sharedSecretSet",
	"multicast": "sharedSecretSet",
	"token":     "tokenSet",
}

// raftBlocks returns every object found under a "raft" key anywhere in v --
// one per database in a list, one in a single database's response.
func raftBlocks(v any, into []map[string]any) []map[string]any {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if r, ok := child.(map[string]any); ok && k == "raft" {
				into = append(into, r)
				continue
			}
			into = raftBlocks(child, into)
		}
	case []any:
		for _, child := range x {
			into = raftBlocks(child, into)
		}
	}
	return into
}
