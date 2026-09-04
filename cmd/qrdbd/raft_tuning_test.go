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

package main

import (
	"testing"

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
)

// singleNodeFlags returns start flags describing a single-node daemon: static
// discovery naming only this node. initializeNode builds such a host with
// quicraft.WithoutTransport(), which is the precondition for both tunings.
func singleNodeFlags() *startFlags {
	return &startFlags{
		dataDir:         "/tmp/data",
		raftAddress:     "127.0.0.1:63001",
		listen:          "127.0.0.1:63001",
		deploymentID:    1,
		shardID:         1,
		replicaID:       1,
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001"},
		checkQuorum:     true,
	}
}

// multiNodeFlags returns start flags describing a three-node cluster, which
// keeps a live QUIC transport and must retain go-quicraft's defaults.
func multiNodeFlags() *startFlags {
	f := singleNodeFlags()
	f.peers = []string{
		"1=127.0.0.1:63001",
		"2=127.0.0.1:63002",
		"3=127.0.0.1:63003",
	}
	return f
}

func TestBuildHostConfig_SingleNodeGetsTransportlessRTT(t *testing.T) {
	f := singleNodeFlags()
	if !isSingleNode(f) {
		t.Fatalf("precondition: isSingleNode = false, want true")
	}

	cfg := buildHostConfig(f, nil)

	if cfg.RTTMillisecond != cluster.TransportlessRTTMillisecond {
		t.Errorf("RTTMillisecond = %d, want %d",
			cfg.RTTMillisecond, cluster.TransportlessRTTMillisecond)
	}
}

func TestBuildHostConfig_MultiNodeKeepsQuicraftRTTDefault(t *testing.T) {
	f := multiNodeFlags()
	if isSingleNode(f) {
		t.Fatalf("precondition: isSingleNode = true, want false")
	}

	cfg := buildHostConfig(f, nil)

	// Zero means "let go-quicraft apply its own default", which is the WAN
	// estimate. A clustered host has a real network and must keep it.
	if cfg.RTTMillisecond != 0 {
		t.Errorf("RTTMillisecond = %d, want 0 (go-quicraft's default preserved)",
			cfg.RTTMillisecond)
	}
}

func TestBuildShardConfig_SingleNodeGetsShortElectionRTT(t *testing.T) {
	f := singleNodeFlags()

	cfg := buildShardConfig(f)

	if cfg.ElectionRTT != cluster.SingleNodeElectionRTT {
		t.Errorf("ElectionRTT = %d, want %d",
			cfg.ElectionRTT, cluster.SingleNodeElectionRTT)
	}
}

func TestBuildShardConfig_MultiNodeKeepsQuicraftElectionDefault(t *testing.T) {
	f := multiNodeFlags()

	cfg := buildShardConfig(f)

	// The 2-tick lease margin is a fixed constant, so shortening ElectionRTT
	// on a multi-node shard trades 2.67x of lease validity for 2x of election
	// speed. That must never happen here.
	if cfg.ElectionRTT != 0 {
		t.Errorf("ElectionRTT = %d, want 0 (go-quicraft's default preserved)",
			cfg.ElectionRTT)
	}
}

// TestBuildShardConfig_JoinKeepsQuicraftElectionDefault covers the flag that
// declares an intent to be part of a multi-node cluster. --join cannot in fact
// succeed on a transport-less host, but the exemption rests on a shard being
// single-node by intent, and a misconfiguration must not be what grants it.
func TestBuildShardConfig_JoinKeepsQuicraftElectionDefault(t *testing.T) {
	f := singleNodeFlags()
	f.join = true

	cfg := buildShardConfig(f)

	if cfg.ElectionRTT != 0 {
		t.Errorf("ElectionRTT = %d, want 0 (go-quicraft's default preserved for --join)",
			cfg.ElectionRTT)
	}
}

// TestBuildHostConfig_JoinStillGetsTransportlessRTT records the deliberate
// asymmetry between the two tunings: the RTT estimate describes the host's
// link and is right for a transport-less host regardless of --join, while only
// the election timeout carries a topology precondition.
func TestBuildHostConfig_JoinStillGetsTransportlessRTT(t *testing.T) {
	f := singleNodeFlags()
	f.join = true

	cfg := buildHostConfig(f, nil)

	if cfg.RTTMillisecond != cluster.TransportlessRTTMillisecond {
		t.Errorf("RTTMillisecond = %d, want %d",
			cfg.RTTMillisecond, cluster.TransportlessRTTMillisecond)
	}
}

// TestSingleNodeDaemonConfigsValidate runs the pair the daemon would actually
// hand to quicraft through quicraft's own validators, with LeaseRead on as it
// is by default.
func TestSingleNodeDaemonConfigsValidate(t *testing.T) {
	f := singleNodeFlags()
	f.leaseRead = true

	hostCfg := buildHostConfig(f, nil)
	hostCfg.TransportConfig.TransportDisabled = true // models WithoutTransport
	hostCfg.SetDefaults()
	if err := hostCfg.Validate(); err != nil {
		t.Errorf("HostConfig.Validate(): %v", err)
	}

	shardCfg := buildShardConfig(f)
	shardCfg.SetDefaults()
	if err := shardCfg.Validate(); err != nil {
		t.Errorf("Config.Validate(): %v", err)
	}
	if !shardCfg.LeaseRead {
		t.Fatal("LeaseRead = false; this test is vacuous unless lease reads are on")
	}
	var _ quicraftconfig.Config = shardCfg
}
