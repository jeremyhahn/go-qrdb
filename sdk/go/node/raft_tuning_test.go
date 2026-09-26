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

package node

import (
	"testing"

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
)

func tuningTestConfig(t *testing.T) *qrdbconfig.Config {
	t.Helper()
	cfg := &qrdbconfig.Config{}
	cfg.Raft.DataDir = t.TempDir()
	cfg.Raft.ShardID = 1
	cfg.Raft.ReplicaID = 1
	return cfg
}

func singleMember() map[uint64]string {
	return map[uint64]string{1: "127.0.0.1:63901"}
}

func threeMembers() map[uint64]string {
	return map[uint64]string{
		1: "127.0.0.1:63901",
		2: "127.0.0.1:63902",
		3: "127.0.0.1:63903",
	}
}

func TestBuildClusteredRaftConfigs_SingleMemberAppliesTransportlessTunings(t *testing.T) {
	hostCfg, shardCfg, _, err := buildClusteredRaftConfigs(tuningTestConfig(t), singleMember(), nil)
	if err != nil {
		t.Fatalf("buildClusteredRaftConfigs: %v", err)
	}

	if hostCfg.RTTMillisecond != cluster.TransportlessRTTMillisecond {
		t.Errorf("RTTMillisecond = %d, want %d",
			hostCfg.RTTMillisecond, cluster.TransportlessRTTMillisecond)
	}
	if shardCfg.ElectionRTT != cluster.SingleNodeElectionRTT {
		t.Errorf("ElectionRTT = %d, want %d",
			shardCfg.ElectionRTT, cluster.SingleNodeElectionRTT)
	}
}

func TestBuildClusteredRaftConfigs_MultiMemberKeepsQuicraftDefaults(t *testing.T) {
	hostCfg, shardCfg, opts, err := buildClusteredRaftConfigs(tuningTestConfig(t), threeMembers(), nil)
	if err != nil {
		t.Fatalf("buildClusteredRaftConfigs: %v", err)
	}

	if len(opts) != 1 {
		t.Fatalf("host options = %d, want 1 (WithoutEncryption only: a clustered host keeps its transport)", len(opts))
	}
	// Zero means "let go-quicraft apply its own default". A clustered host has
	// a real network, and the multi-node lease margin must not be shortened.
	if hostCfg.RTTMillisecond != 0 {
		t.Errorf("RTTMillisecond = %d, want 0 (go-quicraft's default preserved)",
			hostCfg.RTTMillisecond)
	}
	if shardCfg.ElectionRTT != 0 {
		t.Errorf("ElectionRTT = %d, want 0 (go-quicraft's default preserved)",
			shardCfg.ElectionRTT)
	}
}

// TestBuildClusteredRaftConfigs_JoinKeepsQuicraftElectionDefault covers the
// config flag declaring an intent to join an existing cluster. The shorter
// election timeout is sound only for a shard that is single-node by intent.
func TestBuildClusteredRaftConfigs_JoinKeepsQuicraftElectionDefault(t *testing.T) {
	cfg := tuningTestConfig(t)
	cfg.Raft.Join = true

	hostCfg, shardCfg, _, err := buildClusteredRaftConfigs(cfg, singleMember(), nil)
	if err != nil {
		t.Fatalf("buildClusteredRaftConfigs: %v", err)
	}

	if shardCfg.ElectionRTT != 0 {
		t.Errorf("ElectionRTT = %d, want 0 (go-quicraft's default preserved for join)",
			shardCfg.ElectionRTT)
	}
	// The RTT estimate describes the host's link, which is absent either way.
	if hostCfg.RTTMillisecond != cluster.TransportlessRTTMillisecond {
		t.Errorf("RTTMillisecond = %d, want %d",
			hostCfg.RTTMillisecond, cluster.TransportlessRTTMillisecond)
	}
}

func TestBuildClusteredRaftConfigs_SingleMemberPassesQuicraftValidation(t *testing.T) {
	hostCfg, shardCfg, opts, err := buildClusteredRaftConfigs(tuningTestConfig(t), singleMember(), nil)
	if err != nil {
		t.Fatalf("buildClusteredRaftConfigs: %v", err)
	}

	if len(opts) != 2 {
		t.Fatalf("host options = %d, want 2 (WithoutEncryption + WithoutTransport) for a single-member cluster", len(opts))
	}

	hostCfg.TransportConfig.TransportDisabled = true // models WithoutTransport
	hostCfg.SetDefaults()
	if err := hostCfg.Validate(); err != nil {
		t.Errorf("HostConfig.Validate(): %v", err)
	}

	shardCfg.LeaseRead = true // --lease-read defaults to true
	shardCfg.SetDefaults()
	if err := shardCfg.Validate(); err != nil {
		t.Errorf("Config.Validate(): %v", err)
	}
	var _ quicraftconfig.Config = shardCfg
}
