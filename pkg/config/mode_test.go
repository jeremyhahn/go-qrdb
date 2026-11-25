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

package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
)

func boolPtr(b bool) *bool { return &b }

// standaloneConfig returns a minimal valid mode:standalone configuration.
// Standalone needs no raft identity — only a data dir.
func standaloneConfig() *Config {
	cfg := validConfig()
	cfg.Mode = capabilities.ModeStandalone
	cfg.Raft = RaftConfig{DataDir: "/tmp/data"}
	return cfg
}

func TestValidate_ModeEnum(t *testing.T) {
	for _, mode := range []string{"", "raft", "standalone"} {
		cfg := validConfig()
		cfg.Mode = mode
		if mode == "standalone" {
			cfg = standaloneConfig()
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("mode %q should be valid, got %v", mode, err)
		}
	}

	cfg := validConfig()
	cfg.Mode = "clustered"
	err := cfg.Validate()
	var verr *ValidationError
	if !errors.As(err, &verr) || verr.Field != "mode" {
		t.Fatalf("invalid mode: got %v, want ValidationError on field mode", err)
	}
}

// TestValidate_StandaloneRefusesClusterSettings pins the config gate: a
// cluster setting under mode standalone is a validation error naming the
// remedy, never a silent no-op.
func TestValidate_StandaloneRefusesClusterSettings(t *testing.T) {
	cases := []struct {
		name  string
		field string
		mut   func(*Config)
	}{
		{"join", "raft.join", func(c *Config) { c.Raft.Join = true }},
		{"peers", "raft.peers", func(c *Config) {
			c.Raft.Peers = "1=127.0.0.1:63001,2=127.0.0.1:63002"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := standaloneConfig()
			tc.mut(cfg)
			err := cfg.Validate()
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("got %v, want ValidationError", err)
			}
			if verr.Field != tc.field {
				t.Errorf("Field: got %q, want %q", verr.Field, tc.field)
			}
			if !strings.Contains(verr.Reason, "requires mode: raft") {
				t.Errorf("Reason %q must name the remedy \"requires mode: raft\"", verr.Reason)
			}
		})
	}
}

// TestValidate_StandaloneCoordinationTogglesAreValid: MVCC, watch and lease
// are served in both modes (go-qrdb#582), so enabling or disabling them under
// standalone validates. They used to be refused when enabled.
func TestValidate_StandaloneCoordinationTogglesAreValid(t *testing.T) {
	for _, on := range []bool{true, false} {
		cfg := standaloneConfig()
		cfg.MVCC.Enabled = boolPtr(on)
		cfg.Watch.Enabled = boolPtr(on)
		cfg.Lease.Enabled = boolPtr(on)
		if err := cfg.Validate(); err != nil {
			t.Fatalf("enabled=%v under standalone should validate, got %v", on, err)
		}
	}
}

// TestValidate_StandaloneSkipsRaftIdentity pins that standalone does not
// require shard/replica/raft-address — only the data dir.
func TestValidate_StandaloneSkipsRaftIdentity(t *testing.T) {
	cfg := standaloneConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("standalone without raft identity should validate, got %v", err)
	}

	cfg.Raft.DataDir = ""
	err := cfg.Validate()
	var verr *ValidationError
	if !errors.As(err, &verr) || verr.Field != "raft.data_dir" {
		t.Fatalf("missing data_dir: got %v, want ValidationError on raft.data_dir", err)
	}
}

// TestValidate_RaftStillRequiresIdentity guards against the standalone
// carve-out loosening raft mode: shard/replica/address stay mandatory.
func TestValidate_RaftStillRequiresIdentity(t *testing.T) {
	cfg := validConfig()
	cfg.Mode = capabilities.ModeRaft
	cfg.Raft.ShardID = 0
	err := cfg.Validate()
	var verr *ValidationError
	if !errors.As(err, &verr) || verr.Field != "raft.shard_id" {
		t.Fatalf("raft without shard_id: got %v, want ValidationError on raft.shard_id", err)
	}
}

// TestValidateCapabilities_TagAbsentProtocol verifies the boot-time build
// gate: a wire protocol enabled in config but not compiled into the
// binary is refused with an error naming the build tag. The expectation
// tracks whether this test binary was built with the tag.
func TestValidateCapabilities_TagAbsentProtocol(t *testing.T) {
	cases := []struct {
		name string
		tag  string
		feat string
		mut  func(*Config)
	}{
		{"mysql", "qrdb_mysql", "mysql", func(c *Config) { c.Protocols.MySQL = true }},
		{"postgresql", "qrdb_pgsql", "pgsql", func(c *Config) { c.Protocols.PostgreSQL = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mut(cfg)
			err := cfg.ValidateCapabilities()
			if capabilities.Has(tc.feat) {
				if err != nil {
					t.Fatalf("tag %s compiled in: want nil, got %v", tc.tag, err)
				}
				return
			}
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("tag %s absent: got %v, want ValidationError", tc.tag, err)
			}
			if !strings.Contains(verr.Reason, tc.tag) {
				t.Errorf("Reason %q must name the missing build tag %s", verr.Reason, tc.tag)
			}
		})
	}
}

// TestEffectiveCapabilities_StandaloneProfile is the guard test required
// by the run-modes plan: a sealed standalone profile reports every
// raft-only feature false and the core true. The coordination families --
// MVCC, watch, lease, lock -- are served in both modes since go-qrdb#582.
func TestEffectiveCapabilities_StandaloneProfile(t *testing.T) {
	caps := standaloneConfig().EffectiveCapabilities()

	if caps.Mode != capabilities.ModeStandalone {
		t.Fatalf("Mode = %q, want %q", caps.Mode, capabilities.ModeStandalone)
	}
	if !caps.KV || !caps.Batch || !caps.Index {
		t.Error("core kv/batch/index must be true in every mode")
	}
	for name, v := range map[string]bool{
		"cluster": caps.Cluster, "backup": caps.Backup,
		"rebalance": caps.Rebalance, "etcd": caps.Etcd,
	} {
		if v {
			t.Errorf("standalone profile: %s = true, want false (raft-only)", name)
		}
	}
	for name, v := range map[string]bool{
		"mvcc": caps.MVCC, "watch": caps.Watch, "lease": caps.Lease, "lock": caps.Lock,
	} {
		if !v {
			t.Errorf("standalone profile: %s = false, want true (served in both modes)", name)
		}
	}
	// Transactions moved OUT of the raft-only set (owner ruling 2026-08-23,
	// go-qrdb#62). Consensus is what makes a compare-and-swap linearizable
	// across REPLICAS, and a standalone node is engine-direct and
	// single-process with none, so it serves the guarantee outright.
	//
	// Asserted here rather than merely dropped from the loop above: both the
	// SDK's in-process transport and the daemon's gated registry now serve
	// transactions in standalone, so a capability report saying otherwise
	// would be the lie rather than the gate.
	if !caps.Transaction {
		t.Error("standalone profile: transaction = false; standalone serves " +
			"compare-and-swap, so the capability report must say so")
	}
}

// TestEffectiveCapabilities_RaftProfileAndConfigNarrowing verifies raft
// mode enables the raft-only set and that config toggles narrow it.
func TestEffectiveCapabilities_RaftProfileAndConfigNarrowing(t *testing.T) {
	cfg := validConfig()
	caps := cfg.EffectiveCapabilities()
	if caps.Mode != capabilities.ModeRaft {
		t.Fatalf("Mode = %q, want %q", caps.Mode, capabilities.ModeRaft)
	}
	if !caps.MVCC || !caps.Watch || !caps.Lease || !caps.Lock || !caps.Cluster || !caps.Backup || !caps.Rebalance || !caps.Transaction {
		t.Fatal("raft profile must enable the raft-only feature set by default")
	}

	cfg.Lease.Enabled = boolPtr(false)
	cfg.Watch.Enabled = boolPtr(false)
	caps = cfg.EffectiveCapabilities()
	if caps.Lease || caps.Watch {
		t.Fatal("config toggles must narrow the raft profile")
	}
	if !caps.MVCC {
		t.Fatal("narrowing lease/watch must not affect mvcc")
	}
}

// TestEffectiveCapabilities_TagFeaturesTrackRegistration verifies the
// build-tag gate flows through the profile.
func TestEffectiveCapabilities_TagFeaturesTrackRegistration(t *testing.T) {
	caps := validConfig().EffectiveCapabilities()
	for feat, v := range map[string]bool{
		"timeSeries": caps.TimeSeries, "vector": caps.Vector,
		"sql": caps.SQL, "mysql": caps.MySQL, "pgsql": caps.PostgreSQL,
		"fts": caps.FTS,
	} {
		if v != capabilities.Has(feat) {
			t.Errorf("%s = %v, want registration state %v", feat, v, capabilities.Has(feat))
		}
	}
}

// TestEffectiveCapabilities_UnencryptedAtRest pins the SEC-4/SEC-5 report:
// with seal enabled, a standalone node whose local backend keeps data on disk
// outside the barrier's encryption must say so in its capability profile,
// and a node that encrypts must not.
func TestEffectiveCapabilities_UnencryptedAtRest(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		seal    bool
		smType  string
		backend string
		want    string // substring of the single reported gap; "" = none
	}{
		{"raft mvcc", "raft", true, "mvcc", "", ""},
		{"raft pebble", "raft", true, "pebble", "", ""},
		{"raft default sm", "raft", true, "", "", ""},
		// A raft node starts no state machine that persists plaintext under
		// the barrier (disk/fast over pebble are encrypted, over file refused
		// at start; the rest persist nothing), so it reports no gap.
		{"raft disk", "raft", true, "disk", "", ""},
		{"raft fast", "raft", true, "fast", "", ""},
		{"raft memory", "raft", true, "memory", "", ""},
		{"raft disk, seal off", "raft", false, "disk", "", ""},
		{"standalone pebble", "standalone", true, "", "pebble", ""},
		{"standalone default", "standalone", true, "", "", ""},
		{"standalone memory", "standalone", true, "", "memory", ""},
		{"standalone file", "standalone", true, "", "file", ""},
		{"standalone objstore", "standalone", true, "", "objstore", `"objstore"`},
		{"standalone file, seal off", "standalone", false, "", "file", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &Config{Mode: c.mode, Seal: SealConfig{Enabled: c.seal}, Raft: RaftConfig{SMType: c.smType}}
			if c.backend != "" {
				cfg.Databases = []DatabaseConfig{{Name: "system", Backend: c.backend}}
			}
			gaps := cfg.EffectiveCapabilities().UnencryptedAtRest
			if c.want == "" {
				if len(gaps) != 0 {
					t.Fatalf("UnencryptedAtRest = %q, want none", gaps)
				}
				return
			}
			if len(gaps) != 1 || !strings.Contains(gaps[0], c.want) {
				t.Fatalf("UnencryptedAtRest = %q, want one gap naming %s", gaps, c.want)
			}
		})
	}
}

// TestValidate_SMStorage covers the raft.sm_storage contract: valid
// backends, the objstore snapshot-tier refusal, and the pebble-native
// mismatch rule.
func TestValidate_SMStorage(t *testing.T) {
	cases := []struct {
		name      string
		storage   string
		smType    string
		smBackend string
		wantErr   bool
		wantSub   string
	}{
		{name: "empty-default", storage: "", smType: "pebble"},
		{name: "pebble-any-type", storage: "pebble", smType: "mvcc"},
		{name: "file-disk", storage: "file", smType: "disk"},
		{name: "memory-fast", storage: "memory", smType: "fast"},
		{name: "file-composite-backend", storage: "file", smType: "standard", smBackend: "disk"},
		{name: "objstore-refused", storage: "objstore", smType: "disk", wantErr: true, wantSub: "snapshot/backup tier"},
		{name: "unknown", storage: "s3", smType: "disk", wantErr: true, wantSub: "must be one of"},
		{name: "file-pebble-native", storage: "file", smType: "mvcc", wantErr: true, wantSub: "storage-generic"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Raft.SMType = tc.smType
			cfg.Raft.SMBackend = tc.smBackend
			cfg.Raft.SMStorage = tc.storage
			err := cfg.Validate()
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			var verr *ValidationError
			if !errors.As(err, &verr) || verr.Field != "raft.sm_storage" {
				t.Fatalf("got %v, want ValidationError on raft.sm_storage", err)
			}
			if !strings.Contains(verr.Reason, tc.wantSub) {
				t.Errorf("Reason %q missing %q", verr.Reason, tc.wantSub)
			}
		})
	}
}
