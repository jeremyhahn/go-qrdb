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

package database

import (
	"errors"
	"testing"
)

// secretsRaft is a distributed Raft configuration carrying every discovery
// secret.
func secretsRaft() *DatabaseRaftConfig {
	return &DatabaseRaftConfig{
		ClusterMode: RaftDistributed,
		Discovery:   DiscoveryStatic,
		Static:      &StaticDiscoveryConfig{Peers: []string{"db.example.com:63001"}, SharedSecret: "static-secret"},
		Multicast:   &MulticastDiscoveryConfig{SharedSecret: "multicast-secret", MulticastGroup: "239.255.0.1:63100"},
		Token:       &TokenDiscoveryConfig{Endpoint: "https://db.example.com/join", Token: "bootstrap-token", ClusterSize: 3},
	}
}

// withoutSecrets is what a client reading the database back has: every
// secret is write-only, so a read-modify-write update carries none.
func withoutSecrets(r *DatabaseRaftConfig) *DatabaseRaftConfig {
	out := *r
	s, m, tk := *r.Static, *r.Multicast, *r.Token
	s.SharedSecret, m.SharedSecret, tk.Token = "", "", ""
	out.Static, out.Multicast, out.Token = &s, &m, &tk
	return &out
}

func storedRaft(t *testing.T, m *Manager, name string) *DatabaseRaftConfig {
	t.Helper()
	db, err := m.Get(name)
	if err != nil {
		t.Fatalf("Get(%s): %v", name, err)
	}
	return db.Config().Raft
}

// A Raft update that leaves a secret out keeps the stored secret: secrets
// are write-only, so a client that reads the database and writes its Raft
// settings back -- changing only the peers -- cannot send them, and must not
// wipe them or fail validation for want of them.
func TestUpdateRaft_OmittedSecretsAreKept(t *testing.T) {
	m := newTestManager(t)
	t.Cleanup(func() { _ = m.Close() })
	if _, err := m.Create(DatabaseConfig{Name: "kept", Engine: EngineKV, Backend: BackendMemory}); err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateRaft("kept", secretsRaft()); err != nil {
		t.Fatalf("initial UpdateRaft: %v", err)
	}

	update := withoutSecrets(secretsRaft())
	update.RaftAddress = "db.example.com:63002"
	update.Discovery = DiscoveryMulticast // validation requires the multicast secret
	if err := m.UpdateRaft("kept", update); err != nil {
		t.Fatalf("read-modify-write UpdateRaft without secrets: %v", err)
	}
	got := storedRaft(t, m, "kept")
	if got.Static.SharedSecret != "static-secret" || got.Multicast.SharedSecret != "multicast-secret" || got.Token.Token != "bootstrap-token" {
		t.Fatalf("stored secrets after an update that omitted them = %q/%q/%q, want them kept",
			got.Static.SharedSecret, got.Multicast.SharedSecret, got.Token.Token)
	}
	if got.RaftAddress != "db.example.com:63002" || got.Discovery != DiscoveryMulticast {
		t.Fatalf("the rest of the update was not applied: %+v", got)
	}
}

// A secret sent in an update replaces the stored one; leaving a discovery
// block out removes it, secret and all.
func TestUpdateRaft_SecretsRotateAndBlocksRemove(t *testing.T) {
	m := newTestManager(t)
	t.Cleanup(func() { _ = m.Close() })
	if _, err := m.Create(DatabaseConfig{Name: "rotated", Engine: EngineKV, Backend: BackendMemory}); err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateRaft("rotated", secretsRaft()); err != nil {
		t.Fatal(err)
	}

	update := withoutSecrets(secretsRaft())
	update.Static.SharedSecret = "static-secret-2"
	update.Token = nil // token discovery disabled
	if err := m.UpdateRaft("rotated", update); err != nil {
		t.Fatalf("UpdateRaft: %v", err)
	}
	got := storedRaft(t, m, "rotated")
	if got.Static.SharedSecret != "static-secret-2" {
		t.Fatalf("static secret = %q, want the rotated one", got.Static.SharedSecret)
	}
	if got.Multicast.SharedSecret != "multicast-secret" {
		t.Fatalf("multicast secret = %q, want it kept", got.Multicast.SharedSecret)
	}
	if got.Token != nil {
		t.Fatalf("token discovery = %+v, want it removed with its token", got.Token)
	}
}

// A raft-backend database's Raft configuration holds its issued shard ID: an
// update cannot clear it.
func TestUpdateRaft_NilRefusedOnARaftBackend(t *testing.T) {
	m := newTestManager(t)
	t.Cleanup(func() { _ = m.Close() })
	if _, err := m.Create(DatabaseConfig{Name: "raftdb", Engine: EngineKV, Backend: BackendRaft}); err != nil {
		t.Fatal(err)
	}
	err := m.UpdateRaft("raftdb", nil)
	var required *RaftConfigRequiredError
	if !errors.As(err, &required) || required.Name != "raftdb" {
		t.Fatalf("UpdateRaft(nil) on a raft backend = %v, want *RaftConfigRequiredError", err)
	}
	if required.Error() == "" {
		t.Fatal("empty error text")
	}

	// A database on another backend may drop its Raft configuration.
	if _, err := m.Create(DatabaseConfig{Name: "memdb", Engine: EngineKV, Backend: BackendMemory}); err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateRaft("memdb", nil); err != nil {
		t.Fatalf("UpdateRaft(nil) on a memory backend: %v", err)
	}
}
