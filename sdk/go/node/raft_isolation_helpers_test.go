// Copyright 2026 Jeremy Hahn
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

package node

// This file carries no build constraint because its callers do not share
// one: cluster_identity_test.go has none, and tenant_isolation_test.go,
// cluster_formation_test.go, embedded_families_test.go and
// failed_boot_database_keys_test.go are !integration. A helper's constraint
// must be the union of every caller's.

import (
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
)

// raftEntitlements returns the entitlement set that permits creating
// IsolationRaft tenants with dedicated raft databases and system-managed
// auto-unseal barriers. These are used only in tests; production operators
// configure entitlements via the operator config.
func raftEntitlements() *quota.Entitlements {
	return &quota.Entitlements{
		AllowedIsolationModes:     []string{quota.IsolationModeRaft},
		AllowDedicatedRaft:        true,
		AllowSystemManagedBarrier: true,
		AllowedVolumePools:        []string{"default"},
	}
}

// newRaftIsolationConfig builds a minimal single-node clustered Config with a
// none-strategy system barrier, a single-path volume pool for tenant DataRoot
// allocation, pebble storage, and a deterministic Raft address. All protocol
// listeners are disabled so the node binds no ports. RaftAddress is set
// explicitly so BuildTenantInfra's RaftBackendFactory records a consistent
// localRaftAddr — WithoutTransport means no actual network binding occurs for
// single-node clusters.
func newRaftIsolationConfig(t *testing.T) *qrdbconfig.Config {
	t.Helper()
	dataDir := t.TempDir()
	poolDir := t.TempDir()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = dataDir
	cfg.Raft.RaftAddress = "127.0.0.1:63921"
	cfg.Raft.ReplicaID = 1
	cfg.Databases = []qrdbconfig.DatabaseConfig{
		{Name: "default", Backend: string(database.BackendPebble)},
	}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Storage.VolumePools = []qrdbconfig.VolumePoolConfig{
		{Name: "default", Path: poolDir},
	}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	// NOT disabled here, unlike sealedStandaloneConfig/newTenantIsolationConfig:
	// cluster_formation_test.go's TestClusteredNode_UnsealRetriesAfterACancelledBoot_*
	// also builds its config from this helper and is SPECIFICALLY about
	// auth-method rebinding across a retried boot (WithAuthenticators).
	// A caller that does not care about auth (e.g.
	// TestClusteredNode_EmbeddedClientServesTheRaftOnlyFamilies) disables
	// it itself, at the call site, rather than here.
	return cfg
}

// raftMembers returns the single-entry members map for a single-node clustered
// node using newRaftIsolationConfig. ReplicaID 1 maps to the configured
// RaftAddress; quicraft uses WithoutTransport for single-node clusters so
// the address is required by host config validation but never bound.
func raftMembers() map[uint64]string {
	return map[uint64]string{1: "127.0.0.1:63921"}
}
