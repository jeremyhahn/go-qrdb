//go:build qrdb_embedded

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
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
)

func TestBuildEmbeddedRaftConfigs_AppliesTransportlessTunings(t *testing.T) {
	hostCfg, shardCfg := buildEmbeddedRaftConfigs("/tmp/embedded", "127.0.0.1:63901", 7, 3)

	if hostCfg.RTTMillisecond != cluster.TransportlessRTTMillisecond {
		t.Errorf("RTTMillisecond = %d, want %d",
			hostCfg.RTTMillisecond, cluster.TransportlessRTTMillisecond)
	}
	if shardCfg.ElectionRTT != cluster.SingleNodeElectionRTT {
		t.Errorf("ElectionRTT = %d, want %d",
			shardCfg.ElectionRTT, cluster.SingleNodeElectionRTT)
	}
}

func TestBuildEmbeddedRaftConfigs_CarriesIdentityAndPaths(t *testing.T) {
	hostCfg, shardCfg := buildEmbeddedRaftConfigs("/tmp/embedded", "127.0.0.1:63901", 7, 3)

	if want := filepath.Join("/tmp/embedded", "wal"); hostCfg.WALDir != want {
		t.Errorf("WALDir = %q, want %q", hostCfg.WALDir, want)
	}
	if want := filepath.Join("/tmp/embedded", "nodehost"); hostCfg.NodeHostDir != want {
		t.Errorf("NodeHostDir = %q, want %q", hostCfg.NodeHostDir, want)
	}
	if hostCfg.RaftAddress != "127.0.0.1:63901" {
		t.Errorf("RaftAddress = %q, want %q", hostCfg.RaftAddress, "127.0.0.1:63901")
	}
	if hostCfg.ListenAddress != hostCfg.RaftAddress {
		t.Errorf("ListenAddress = %q, want it to mirror RaftAddress %q",
			hostCfg.ListenAddress, hostCfg.RaftAddress)
	}
	if !hostCfg.AllowZeroDeploymentID {
		t.Error("AllowZeroDeploymentID = false, want true for embedded mode")
	}
	if shardCfg.ShardID != 7 {
		t.Errorf("ShardID = %d, want 7", shardCfg.ShardID)
	}
	if shardCfg.ReplicaID != 3 {
		t.Errorf("ReplicaID = %d, want 3", shardCfg.ReplicaID)
	}
}

func TestBuildEmbeddedRaftConfigs_PassQuicraftValidation(t *testing.T) {
	hostCfg, shardCfg := buildEmbeddedRaftConfigs("/tmp/embedded", "127.0.0.1:63901", 7, 3)

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
