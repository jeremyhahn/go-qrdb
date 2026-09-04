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

package server

import (
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
)

func TestBuildTenantRaftConfigs_AppliesTransportlessTunings(t *testing.T) {
	hostCfg, shardCfg := buildTenantRaftConfigs(t.TempDir())

	if hostCfg.RTTMillisecond != cluster.TransportlessRTTMillisecond {
		t.Errorf("RTTMillisecond = %d, want %d",
			hostCfg.RTTMillisecond, cluster.TransportlessRTTMillisecond)
	}
	if shardCfg.ElectionRTT != cluster.SingleNodeElectionRTT {
		t.Errorf("ElectionRTT = %d, want %d",
			shardCfg.ElectionRTT, cluster.SingleNodeElectionRTT)
	}
}

func TestBuildTenantRaftConfigs_RootsStateUnderDatabases(t *testing.T) {
	root := t.TempDir()

	hostCfg, shardCfg := buildTenantRaftConfigs(root)

	hostRoot := filepath.Join(root, "databases")
	if want := filepath.Join(hostRoot, "wal"); hostCfg.WALDir != want {
		t.Errorf("WALDir = %q, want %q", hostCfg.WALDir, want)
	}
	if want := filepath.Join(hostRoot, "nodehost"); hostCfg.NodeHostDir != want {
		t.Errorf("NodeHostDir = %q, want %q", hostCfg.NodeHostDir, want)
	}
	if shardCfg.ShardID != tenantHostShardID {
		t.Errorf("ShardID = %d, want %d", shardCfg.ShardID, tenantHostShardID)
	}
	if shardCfg.ReplicaID != 1 {
		t.Errorf("ReplicaID = %d, want 1", shardCfg.ReplicaID)
	}
}

func TestBuildTenantRaftConfigs_PassQuicraftValidation(t *testing.T) {
	hostCfg, shardCfg := buildTenantRaftConfigs(t.TempDir())

	hostCfg.TransportConfig.TransportDisabled = true // models WithoutTransport
	hostCfg.SetDefaults()
	if err := hostCfg.Validate(); err != nil {
		t.Errorf("HostConfig.Validate(): %v", err)
	}

	shardCfg.SetDefaults()
	if err := shardCfg.Validate(); err != nil {
		t.Errorf("Config.Validate(): %v", err)
	}
}
