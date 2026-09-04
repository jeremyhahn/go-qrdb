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

import quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

// Raft timing defaults for a host built with quicraft.WithoutTransport().
//
// go-quicraft's own defaults describe a WAN deployment: RTTMillisecond 200
// ("typical WAN"; the same field documents "LAN: 1-5") and ElectionRTT 10
// ticks, giving a 2 s election timeout. A transport-less host has no network
// at all — every message is an in-process handoff — so those defaults size a
// budget for a link that does not exist. Correcting them here is a correction
// of the DEFAULT SELECTION for a case go-quicraft's defaults were not written
// for, not a fleet-wide retune (go-qrdb#250, owner ruling 2026-09-03).
const (
	// TransportlessRTTMillisecond is the value applied to a transport-less
	// host's RTTMillisecond. It is ABOVE go-quicraft's documented LAN range
	// ("LAN: 1-5"), deliberately, and the reason is that the field is
	// overloaded:
	//
	//	election timeout = ElectionRTT * RTTMillisecond   <- both knobs
	//	tick interval    =               RTTMillisecond   <- only this one
	//	                   (engine.go: tickInterval = RTTMillisecond * time.Millisecond)
	//
	// On a host with a network, RTTMillisecond is an estimate of something
	// real and the documented range is the guidance to follow. On a
	// transport-less host there IS no round trip to estimate — every message
	// is an in-process handoff of microseconds — so EVERY value is wrong as an
	// RTT, and the value should instead be chosen for the thing it actually
	// controls: the tick rate, whose cost is continuous.
	//
	// Chosen by measurement, not by the documented range (go-qrdb#250,
	// interleaved 4-arm A/B, 3 reps, cpu PSI some avg10 63-88 throughout, so
	// absolute figures are inflated and the comparisons are what hold):
	//
	//	ElectionRTT/RTT   election   median boot   marginal CPU per idle host
	//	  10 / 200 ms      2000 ms      2829 ms      0.26% of a core
	//	   5 / 200 ms      1000 ms      1444 ms      0.24% of a core  (free)
	//	   5 /  25 ms       125 ms       228 ms      0.66% of a core  <- this
	//	   5 /   5 ms        25 ms        98 ms      2.08% of a core
	//
	// 25 ms is the knee. It captures 95% of the achievable boot-latency
	// reduction for 32% of the CPU that 5 ms costs. Going on to 5 ms saves a
	// further 131 ms ONCE PER HOST BOOT and costs an extra 1.42% of a core
	// PER IDLE HOST CONTINUOUSLY — about 51 extra CPU-seconds per host per
	// hour, or ~1.4 extra cores across 100 idle tenant hosts, to remove a
	// latency nobody waits on. That asymmetry is the same one that keeps this
	// change off the global tick, applied one level down.
	TransportlessRTTMillisecond uint64 = 25

	// SingleNodeElectionRTT is the election timeout, in ticks, applied to a
	// shard on a transport-less host.
	//
	// This ABANDONS BOTH of go-quicraft's documented recommendations, which is
	// deliberate and is why it is scoped rather than global. Validation
	// (config.go Validate) accepts it — the floor is ElectionRTT >= 5, and
	// ElectionRTT >= 2*HeartbeatRTT holds at HeartbeatRTT 1 — but the messages
	// on those two checks recommend 10 and 10*HeartbeatRTT respectively, and 5
	// meets neither.
	//
	// What the recommendation buys, and why this case does not need it:
	// leaseTimeout is ElectionRTT-2 (raft.go), a FIXED 2-tick margin, so 10->5
	// cuts the lease from 8 ticks to 3 — a 2.67x reduction absorbing
	// "goroutine scheduling jitter ... preventing split-brain reads", a
	// correctness property. On a MULTI-NODE shard that trade is bad and this
	// constant MUST NOT be applied there. On a single-node shard leaseTimeout
	// is never consulted in a way that can fail: quorum() is
	// quorumOf(len(remotes)+len(witnesses)+1) and initRemotes excludes self,
	// so an empty membership gives q == 1 while hasValidLease starts count at
	// 1 for self — count >= q is unconditionally true. handleLeaderReadIndex
	// short-circuits on numVotingMembers == 1 before hasValidLease is even
	// called. A single-node shard's lease cannot be lost because its quorum is
	// itself. This holds regardless of the LeaseRead flag, which defaults to
	// true (cmd/qrdbd/start.go), so the exemption rests on shard topology and
	// never on the flag.
	//
	// ElectionRTT has a third consumer, quiesce (raft.go:
	// newQuiesceState(cfg.Quiesce, int(cfg.ElectionRTT))), whose entry
	// threshold and liveness-probe interval are both ElectionRTT * 10 ticks.
	// go-qrdb never sets Config.Quiesce, so it is false everywhere and
	// quiesceState.tryEnter returns on !enabled before reading either field —
	// the consumer is inert today. Were it enabled, a transport-less shard
	// would enter quiesce sooner (50 ticks rather than 100) and probe a
	// liveness set that is empty, which is the harmless direction.
	SingleNodeElectionRTT uint64 = 5
)

// ApplyTransportlessHostTuning sets the transport-less tick interval on hc when
// no explicit value has been configured. It is the caller's obligation to call
// it only for a host that is actually built with quicraft.WithoutTransport();
// a host with a live QUIC transport keeps go-quicraft's WAN default.
//
// An explicit non-zero RTTMillisecond always wins, mirroring go-quicraft's own
// zero-means-default convention, so configuring the field remains an override
// rather than something this function silently discards.
func ApplyTransportlessHostTuning(hc *quicraftconfig.HostConfig) {
	if hc == nil || hc.RTTMillisecond != 0 {
		return
	}
	hc.RTTMillisecond = TransportlessRTTMillisecond
}

// ApplyTransportlessShardTuning sets the single-node election timeout on cfg
// when no explicit value has been configured.
//
// PRECONDITION: cfg names a shard on a host built with
// quicraft.WithoutTransport(). That host has no network, so no peer can ever
// reach it and the shard's voting membership can never grow beyond the local
// replica — which is what makes SingleNodeElectionRTT safe, per the argument
// recorded on that constant. Do NOT call this for a shard on a host with a
// transport, even one whose initial member map happens to hold one entry.
func ApplyTransportlessShardTuning(cfg *quicraftconfig.Config) {
	if cfg == nil || cfg.ElectionRTT != 0 {
		return
	}
	cfg.ElectionRTT = SingleNodeElectionRTT
}
