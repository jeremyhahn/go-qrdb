// Copyright (c) 2026 Jeremy Hahn
// Copyright (c) 2026 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package dao

import (
	"sync/atomic"
	"time"
)

// Snowflake bit layout (63 usable bits — the high bit stays zero so every ID
// is a positive int64/uint64):
//
//	[ 41 bits milliseconds-since-epoch ][ 10 bits node ][ 12 bits sequence ]
//
// This yields ~69 years of lifetime from the custom epoch, up to 1024 nodes,
// and 4096 IDs per node per millisecond. IDs are generated locally on each
// node with NO coordination, so the same generator works identically in
// standalone (single node) and Raft-cluster mode — the node component is the
// qrdb Raft ReplicaID, which is unique per cluster member and defaults to 1
// when standalone.
const (
	snowflakeNodeBits = 10
	snowflakeSeqBits  = 12

	snowflakeMaxNode = (1 << snowflakeNodeBits) - 1 // 1023
	snowflakeMaxSeq  = (1 << snowflakeSeqBits) - 1  // 4095

	// snowflakeEpochMillis is the custom epoch (2026-01-01T00:00:00Z) in Unix
	// milliseconds. Shifting off the Unix epoch maximises the usable lifetime
	// of the 41-bit timestamp field.
	snowflakeEpochMillis = 1767225600000
)

// SnowflakeNodeRangeError is returned when a node identifier exceeds the
// 10-bit Snowflake node field (0..1023).
type SnowflakeNodeRangeError struct {
	NodeID uint64
}

// Error implements the error interface.
func (e *SnowflakeNodeRangeError) Error() string {
	return "dao: snowflake node id out of range (must be 0..1023)"
}

// SnowflakeGenerator is a coordination-free, lock-free uint64 ID generator.
// It is safe for concurrent use: the timestamp+sequence state is advanced via
// a single atomic compare-and-swap loop, so no mutex is held. It implements
// IDGenerator, so it can be dropped into any entity-store wiring that accepts
// one, and also exposes Next for direct uint64 generation.
type SnowflakeGenerator struct {
	// node is the pre-shifted node component (ReplicaID << seqBits).
	node uint64

	// state packs the last issued (timestamp << seqBits) | sequence, where
	// timestamp is milliseconds since snowflakeEpochMillis. A single atomic
	// word keeps generation lock-free.
	state atomic.Uint64

	// nowMillis returns the current time in Unix milliseconds. Injectable for
	// deterministic testing of sequence rollover and clock regression.
	nowMillis func() int64
}

// NewSnowflakeGenerator creates a Snowflake generator for the given node ID
// (the qrdb Raft ReplicaID). It returns a SnowflakeNodeRangeError when nodeID
// does not fit the 10-bit node field.
func NewSnowflakeGenerator(nodeID uint64) (*SnowflakeGenerator, error) {
	if nodeID > snowflakeMaxNode {
		return nil, &SnowflakeNodeRangeError{NodeID: nodeID}
	}
	return &SnowflakeGenerator{
		node:      nodeID << snowflakeSeqBits,
		nowMillis: func() int64 { return time.Now().UnixMilli() },
	}, nil
}

// currentMillis returns the current time relative to the Snowflake epoch,
// clamped to be non-negative.
func (g *SnowflakeGenerator) currentMillis() uint64 {
	now := g.nowMillis() - snowflakeEpochMillis
	if now < 0 {
		return 0
	}
	return uint64(now)
}

// Next returns the next unique uint64 ID. It never blocks on a lock and is
// monotonically increasing per node, even across clock stalls or small
// backwards clock movements (it advances logically rather than regressing).
func (g *SnowflakeGenerator) Next() uint64 {
	for {
		old := g.state.Load()
		oldTS := old >> snowflakeSeqBits
		oldSeq := old & snowflakeMaxSeq

		now := g.currentMillis()

		var newTS, newSeq uint64
		if now > oldTS {
			// Clock advanced to a fresh millisecond: restart the sequence.
			newTS, newSeq = now, 0
		} else {
			// Same millisecond (or clock stalled/regressed): keep advancing
			// the sequence within the last-seen timestamp so IDs stay
			// monotonic and never collide.
			newSeq = oldSeq + 1
			newTS = oldTS
			if newSeq > snowflakeMaxSeq {
				// Sequence exhausted for this millisecond: roll to the next
				// logical millisecond.
				newTS = oldTS + 1
				newSeq = 0
			}
		}

		newState := (newTS << snowflakeSeqBits) | newSeq
		if g.state.CompareAndSwap(old, newState) {
			return (newTS << (snowflakeNodeBits + snowflakeSeqBits)) | g.node | newSeq
		}
		// Lost the race; retry with the updated state.
	}
}

// NextID implements IDGenerator. The entity is unused — Snowflake IDs are
// independent of entity content.
func (g *SnowflakeGenerator) NextID(_ Entity) uint64 {
	return g.Next()
}

// Compile-time check that SnowflakeGenerator satisfies IDGenerator.
var _ IDGenerator = (*SnowflakeGenerator)(nil)
