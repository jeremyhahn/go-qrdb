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
	"github.com/jeremyhahn/go-qrdb/pkg/service"
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

// SetIndicators lists the JSON keys a response carries, true, in place of
// the secrets.
var SetIndicators = []string{"sharedSecretSet", "tokenSet"}

// RaftConfig is a distributed Raft configuration that carries every
// discovery secret.
func RaftConfig() *database.DatabaseRaftConfig {
	return &database.DatabaseRaftConfig{
		ClusterMode: database.RaftDistributed,
		ReplicaID:   1,
		RaftAddress: "db.example.com:63001",
		Peers:       []string{"db.example.com:63001"},
		Discovery:   database.DiscoveryStatic,
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

// Seed stores the fixture database through the real service: a create, then
// a Raft update carrying every discovery secret, as an operator would.
func Seed(t testing.TB, svc service.DatabaseServicer) {
	t.Helper()
	if _, err := svc.Create(database.DatabaseConfig{
		Name:    Name,
		Engine:  database.EngineKV,
		Backend: database.BackendMemory,
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

// AssertNoSecrets fails when a response holds any stored secret, and when it
// does not report each one as set. what names the transport and operation.
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
	for _, key := range SetIndicators {
		if !bytes.Contains(response, []byte(`"`+key+`":true`)) {
			t.Errorf("%s does not report %q as set:\n%s", what, key, response)
		}
	}
}
