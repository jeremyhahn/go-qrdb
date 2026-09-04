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

package cluster

import (
	"testing"

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
)

// tunableHostConfig returns a HostConfig that is valid apart from the field
// under test, so a Validate failure can only be attributed to the tuning.
func tunableHostConfig() quicraftconfig.HostConfig {
	return quicraftconfig.HostConfig{
		WALDir:        "/tmp/qrdb-raft-tuning-test/wal",
		NodeHostDir:   "/tmp/qrdb-raft-tuning-test/nodehost",
		RaftAddress:   "127.0.0.1:63901",
		ListenAddress: "127.0.0.1:63901",
		DeploymentID:  1,
		// Model a transport-less host: quicraft's HostConfig.Validate
		// otherwise demands mTLS material for the QUIC transport, which a
		// host built WithoutTransport never has.
		TransportConfig: quicraftconfig.TransportConfig{TransportDisabled: true},
	}
}

// tunableShardConfig returns a shard Config that is valid apart from the
// field under test.
func tunableShardConfig() quicraftconfig.Config {
	return quicraftconfig.Config{ShardID: 1, ReplicaID: 1}
}

func TestApplyTransportlessHostTuning_SetsTransportlessTickWhenUnset(t *testing.T) {
	hc := tunableHostConfig()
	if hc.RTTMillisecond != 0 {
		t.Fatalf("precondition: RTTMillisecond = %d, want 0", hc.RTTMillisecond)
	}

	ApplyTransportlessHostTuning(&hc)

	if hc.RTTMillisecond != TransportlessRTTMillisecond {
		t.Errorf("RTTMillisecond = %d, want %d",
			hc.RTTMillisecond, TransportlessRTTMillisecond)
	}
	// The whole point is to NOT inherit go-quicraft's WAN estimate. Asserting
	// the inequality separately means a future change that happens to set
	// RTTMillisecond to the upstream default still fails here.
	if hc.RTTMillisecond == quicraftconfig.DefaultRTTMillisecond {
		t.Errorf("RTTMillisecond = %d, which is go-quicraft's WAN default; a "+
			"transport-less host must not inherit it", hc.RTTMillisecond)
	}
}

// TestTransportlessRTT_IsAboveTheDocumentedLANRange pins the deliberate
// departure so it cannot be "corrected" back into go-quicraft's documented
// range by someone reading only that doc comment. RTTMillisecond is both the
// RTT estimate and the tick interval; a transport-less host has no round trip
// to estimate, so the value is chosen for the tick cost it controls. The
// measured knee is 25 ms — 95% of the achievable boot-latency reduction at 32%
// of the CPU that 5 ms costs (go-qrdb#250).
func TestTransportlessRTT_IsAboveTheDocumentedLANRange(t *testing.T) {
	const documentedLANMax uint64 = 5 // go-quicraft hostconfig.go: "LAN: 1-5"

	if TransportlessRTTMillisecond <= documentedLANMax {
		t.Errorf("TransportlessRTTMillisecond = %d; the measured choice is "+
			"deliberately above the documented LAN maximum of %d, because the "+
			"field sets the tick interval and a transport-less host has no "+
			"round trip to estimate",
			TransportlessRTTMillisecond, documentedLANMax)
	}
}

func TestApplyTransportlessHostTuning_PreservesExplicitValue(t *testing.T) {
	const explicit uint64 = 37

	hc := tunableHostConfig()
	hc.RTTMillisecond = explicit

	ApplyTransportlessHostTuning(&hc)

	if hc.RTTMillisecond != explicit {
		t.Errorf("RTTMillisecond = %d, want the configured %d preserved",
			hc.RTTMillisecond, explicit)
	}
}

func TestApplyTransportlessHostTuning_NilIsNoOp(t *testing.T) {
	ApplyTransportlessHostTuning(nil) // must not panic
}

func TestApplyTransportlessShardTuning_SetsSingleNodeElectionRTTWhenUnset(t *testing.T) {
	cfg := tunableShardConfig()
	if cfg.ElectionRTT != 0 {
		t.Fatalf("precondition: ElectionRTT = %d, want 0", cfg.ElectionRTT)
	}

	ApplyTransportlessShardTuning(&cfg)

	if cfg.ElectionRTT != SingleNodeElectionRTT {
		t.Errorf("ElectionRTT = %d, want %d", cfg.ElectionRTT, SingleNodeElectionRTT)
	}
	if cfg.ElectionRTT == quicraftconfig.DefaultElectionRTT {
		t.Errorf("ElectionRTT = %d, which is go-quicraft's default; the "+
			"single-node exemption must actually shorten the election",
			cfg.ElectionRTT)
	}
}

func TestApplyTransportlessShardTuning_PreservesExplicitValue(t *testing.T) {
	const explicit uint64 = 20

	cfg := tunableShardConfig()
	cfg.ElectionRTT = explicit

	ApplyTransportlessShardTuning(&cfg)

	if cfg.ElectionRTT != explicit {
		t.Errorf("ElectionRTT = %d, want the configured %d preserved",
			cfg.ElectionRTT, explicit)
	}
}

func TestApplyTransportlessShardTuning_NilIsNoOp(t *testing.T) {
	ApplyTransportlessShardTuning(nil) // must not panic
}

// TestTransportlessTunings_PassQuicraftValidation pins the tuned values
// against go-quicraft's own validators rather than against a restated copy of
// their rules. SingleNodeElectionRTT sits exactly on the ElectionRTT floor and
// abandons both documented recommendations, so a dependency bump that raises
// the floor must fail here rather than at StartShard on a live node.
func TestTransportlessTunings_PassQuicraftValidation(t *testing.T) {
	hc := tunableHostConfig()
	ApplyTransportlessHostTuning(&hc)
	hc.SetDefaults()
	if err := hc.Validate(); err != nil {
		t.Errorf("HostConfig.Validate() with RTTMillisecond=%d: %v",
			TransportlessRTTMillisecond, err)
	}

	// LeaseRead is exercised explicitly: --lease-read defaults to true, and
	// go-quicraft applies an extra ElectionRTT floor when it is on.
	for _, leaseRead := range []bool{false, true} {
		cfg := tunableShardConfig()
		cfg.LeaseRead = leaseRead
		ApplyTransportlessShardTuning(&cfg)
		cfg.SetDefaults()
		if err := cfg.Validate(); err != nil {
			t.Errorf("Config.Validate() with ElectionRTT=%d LeaseRead=%v: %v",
				SingleNodeElectionRTT, leaseRead, err)
		}
	}
}

// TestSingleNodeElectionRTT_LeavesAPositiveLeaseTimeout guards the arithmetic
// the exemption argument depends on. raft.go derives leaseTimeout as
// ElectionRTT-2 with a FIXED 2-tick margin; the single-node case never
// consults it, but the value must still be a sane positive number so that a
// future reduction of SingleNodeElectionRTT cannot silently produce a
// zero or negative lease.
func TestSingleNodeElectionRTT_LeavesAPositiveLeaseTimeout(t *testing.T) {
	const fixedLeaseMargin = 2

	if SingleNodeElectionRTT <= fixedLeaseMargin {
		t.Fatalf("SingleNodeElectionRTT = %d, must exceed the %d-tick lease margin",
			SingleNodeElectionRTT, fixedLeaseMargin)
	}
	if got := SingleNodeElectionRTT - fixedLeaseMargin; got != 3 {
		t.Errorf("derived leaseTimeout = %d ticks, want 3", got)
	}
}

// TestTransportlessTunings_ShortenTheElectionBudget states the outcome the
// change exists to produce, in the units the issue argued in: the election
// timeout is ElectionRTT ticks of RTTMillisecond each.
func TestTransportlessTunings_ShortenTheElectionBudget(t *testing.T) {
	upstreamMs := quicraftconfig.DefaultElectionRTT * quicraftconfig.DefaultRTTMillisecond
	tunedMs := SingleNodeElectionRTT * TransportlessRTTMillisecond

	if upstreamMs != 2000 {
		t.Fatalf("go-quicraft default election budget = %d ms, want 2000; the "+
			"issue's premise has moved and this change needs re-argued", upstreamMs)
	}
	if tunedMs >= upstreamMs {
		t.Errorf("tuned election budget = %d ms, want less than the upstream %d ms",
			tunedMs, upstreamMs)
	}
}
