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

package vlog

import (
	"bytes"
	"encoding/binary"
	"math/bits"
)

// The replicated audit table is the durable half of Chain, mirroring
// pkg/lease/table.go exactly: it lives in the state machine's RAW keyspace,
// under the same 0x00 prefix every state machine already reserves for its own
// bookkeeping, so it sorts ahead of every user key and -- unlike a "__sys:"
// key, which is still an ordinary MVCC key merely filtered out of listings --
// it is not a key the KV/MVCC engines expose to Range or watch AT ALL. This is
// what makes the trail invisible to a tenant's own enumeration rather than
// merely hidden from it.
//
// Layout, under TablePrefix in the state machine's raw keyspace:
//
//	TablePrefix + "h/" + tenantID              -> head record (seq uint64 BE, lo uint64 BE, digest 32 bytes)
//	TablePrefix + "e/" + tenantID + "/" + seq (8 bytes BE) -> marshaled AuditEvent
//
// The head's lo field is the lowest surviving sequence number: Prune deletes
// exactly [lo, upper) for an upper bound it computes from the IN-MEMORY head
// alone (never from a storage read -- see log.go's Prune), then advances lo
// to upper. This is what makes pruning deterministic regardless of how many
// committed entries one Raft Update call packs together.
//
// One table, keyed by tenant, used identically whether the apply happens in
// the shared system shard (prefix/barrier tenants, many tenants multiplexed
// by the tenantID segment) or in a tenant's own dedicated shard (volume/raft
// tenants, where the segment always names that one tenant). One encoding for
// every isolation mode is deliberate: see
// docs/audit/architecture.md#record-layout-a-reserved-raw-keyspace-table-not-a-__sys-key.
//
// Unlike the last-applied index, which is local to a node, the audit table is
// replicated state: snapshots must carry it and replica hashes must cover it.
// IsTableKey is how the state machines tell the two apart (pkg/store's
// isNodeLocalKey), exactly as lease.IsTableKey already does for the lease
// table.

// TablePrefix is the raw state-machine key prefix the replicated audit table
// is stored under.
const TablePrefix = "\x00qrdb.audit/"

// The two record families under TablePrefix.
const (
	headPrefix  = TablePrefix + "h/"
	eventPrefix = TablePrefix + "e/"
)

// headRecordSize is the encoded width of a head record: an 8-byte
// big-endian sequence number, an 8-byte big-endian low-watermark, and a
// 32-byte SHA-256 digest.
const headRecordSize = 8 + 8 + 32

// MaxPrunePerCommand caps how many events a single Prune call deletes. A
// cutoff far ahead of the low-watermark deletes at most this many events and
// reports that count; the caller issues Prune again to continue, rather
// than one command staging an unbounded number of deletes into one Raft
// apply batch.
const MaxPrunePerCommand = 10000

// IsTableKey reports whether k is a record of either replicated log table:
// the audit instance's (TablePrefix) or the named logs' (LogTablePrefix).
func IsTableKey(k []byte) bool {
	return bytes.HasPrefix(k, []byte(TablePrefix)) || bytes.HasPrefix(k, []byte(LogTablePrefix))
}

// headKey returns the key of tenantID's head record.
func headKey(tenantID string) []byte {
	k := make([]byte, 0, len(headPrefix)+len(tenantID))
	k = append(k, headPrefix...)
	return append(k, tenantID...)
}

// EventKey returns the key of tenantID's event at sequence seq.
func EventKey(tenantID string, seq uint64) []byte {
	k := make([]byte, 0, len(eventPrefix)+len(tenantID)+1+8)
	k = append(k, eventPrefix...)
	k = append(k, tenantID...)
	k = append(k, '/')
	return binary.BigEndian.AppendUint64(k, seq)
}

// EventRangeStart returns the inclusive lower bound of tenantID's event
// range, at the given starting sequence (0 for the whole range).
func EventRangeStart(tenantID string, fromSeq uint64) []byte {
	return EventKey(tenantID, fromSeq)
}

// EventRangeEnd returns the exclusive upper bound of tenantID's event range.
func EventRangeEnd(tenantID string) []byte {
	prefix := make([]byte, 0, len(eventPrefix)+len(tenantID)+1)
	prefix = append(prefix, eventPrefix...)
	prefix = append(prefix, tenantID...)
	prefix = append(prefix, '/')
	return prefixEnd(prefix)
}

// EventSeq extracts the sequence number from one of tenantID's event keys.
// ok is false when k is not shaped like one.
func EventSeq(k []byte, tenantID string) (seq uint64, ok bool) {
	want := EventRangeStart(tenantID, 0)
	prefix := want[:len(want)-8]
	if !bytes.HasPrefix(k, prefix) || len(k) != len(prefix)+8 {
		return 0, false
	}
	return binary.BigEndian.Uint64(k[len(prefix):]), true
}

// encodeHead encodes a head record.
func encodeHead(seq, lo uint64, digest [32]byte) []byte {
	buf := make([]byte, 0, headRecordSize)
	buf = binary.BigEndian.AppendUint64(buf, seq)
	buf = binary.BigEndian.AppendUint64(buf, lo)
	return append(buf, digest[:]...)
}

// decodeHead decodes a head record. ok is false for any other width.
func decodeHead(b []byte) (seq, lo uint64, digest [32]byte, ok bool) {
	if len(b) != headRecordSize {
		return 0, 0, digest, false
	}
	seq = binary.BigEndian.Uint64(b[:8])
	lo = binary.BigEndian.Uint64(b[8:16])
	copy(digest[:], b[16:])
	return seq, lo, digest, true
}

// prefixEnd returns the exclusive upper bound of the keys starting with
// prefix. Every prefix in this file ends in '/', so the increment never
// carries.
func prefixEnd(prefix []byte) []byte {
	end := bytes.Clone(prefix)
	end[len(end)-1]++
	return end
}

// The named logs' table sits beside the audit instance's in the same raw
// keyspace, with the same reasons for being there, and one key prefix per
// log rather than one per record family:
//
//	LogTablePrefix + tenant + "/" + database + "/" + log + "/"   (the log's prefix)
//	  + "d"                                -> descriptor (JSON, see Descriptor)
//	  + "h"                                -> head (size, lo, compact range)
//	  + "e/" + index (8 bytes BE)          -> entry bytes as appended
//	  + "n/" + level (1 byte) + "/" + index (8 bytes BE) -> perfect-subtree hash
//	  + "c/" + size (8 bytes BE)           -> signed checkpoint for that size
//
// No name may contain '/' (LogID.Validate), so no log's prefix carves a
// segment inside another's range. A tree node is stored once, when the append
// that completes its subtree applies; a level-0 node is the entry's leaf
// hash, which is why expiring an entry's body leaves every proof valid.

// LogTablePrefix is the raw state-machine key prefix the named logs' table is
// stored under.
const LogTablePrefix = "\x00qrdb.vlog/"

// logHeadFixedSize is the encoded width of a named log's head record before
// its compact range: an 8-byte big-endian size and an 8-byte big-endian
// low-watermark (the lowest index whose body has not expired).
const logHeadFixedSize = 8 + 8

// hashSize is the width of every tree hash: SHA-256.
const hashSize = 32

// logPrefix returns id's key prefix in the named logs' table.
func logPrefix(id LogID) []byte {
	k := make([]byte, 0, len(LogTablePrefix)+len(id.TenantID)+len(id.Database)+len(id.Log)+3)
	k = append(k, LogTablePrefix...)
	k = append(k, id.TenantID...)
	k = append(k, '/')
	k = append(k, id.Database...)
	k = append(k, '/')
	k = append(k, id.Log...)
	return append(k, '/')
}

// parseLogPrefix splits the log prefix off a key in the named logs' table.
// ok is false when k does not hold three '/'-terminated name segments after
// LogTablePrefix.
func parseLogPrefix(k []byte) (id LogID, prefix []byte, ok bool) {
	if !bytes.HasPrefix(k, []byte(LogTablePrefix)) {
		return LogID{}, nil, false
	}
	rest := k[len(LogTablePrefix):]
	var segs [3]string
	off := 0
	for i := range segs {
		n := bytes.IndexByte(rest[off:], '/')
		if n < 0 {
			return LogID{}, nil, false
		}
		segs[i] = string(rest[off : off+n])
		off += n + 1
	}
	id = LogID{TenantID: segs[0], Database: segs[1], Log: segs[2]}
	if id.Validate() != nil {
		return LogID{}, nil, false
	}
	return id, k[:len(LogTablePrefix)+off], true
}

// descriptorKey returns the key of id's descriptor record.
func descriptorKey(id LogID) []byte {
	return append(logPrefix(id), 'd')
}

// logHeadKey returns the key of id's head record.
func logHeadKey(id LogID) []byte {
	return append(logPrefix(id), 'h')
}

// entryPrefix returns the prefix of id's entry records.
func entryPrefix(id LogID) []byte {
	return append(logPrefix(id), 'e', '/')
}

// entryKey returns the key of id's entry at index.
func entryKey(id LogID, index uint64) []byte {
	return binary.BigEndian.AppendUint64(entryPrefix(id), index)
}

// nodeKey returns the key of id's tree node at (level, index).
func nodeKey(id LogID, level uint, index uint64) []byte {
	k := append(logPrefix(id), 'n', '/', byte(level), '/')
	return binary.BigEndian.AppendUint64(k, index)
}

// checkpointPrefix returns the prefix of id's checkpoint records.
func checkpointPrefix(id LogID) []byte {
	return append(logPrefix(id), 'c', '/')
}

// checkpointKey returns the key of id's checkpoint for a tree of size.
func checkpointKey(id LogID, size uint64) []byte {
	return binary.BigEndian.AppendUint64(checkpointPrefix(id), size)
}

// encodeLogHead encodes a named log's head record. hashes is the compact
// range of [0, size): one hash per set bit of size, left to right.
func encodeLogHead(size, lo uint64, hashes [][]byte) []byte {
	buf := make([]byte, 0, logHeadFixedSize+len(hashes)*hashSize)
	buf = binary.BigEndian.AppendUint64(buf, size)
	buf = binary.BigEndian.AppendUint64(buf, lo)
	for _, h := range hashes {
		buf = append(buf, h...)
	}
	return buf
}

// decodeLogHead decodes a named log's head record. ok is false unless the
// record carries exactly one hash per set bit of its size and lo <= size.
func decodeLogHead(b []byte) (size, lo uint64, hashes [][]byte, ok bool) {
	if len(b) < logHeadFixedSize {
		return 0, 0, nil, false
	}
	size = binary.BigEndian.Uint64(b[:8])
	lo = binary.BigEndian.Uint64(b[8:16])
	n := bits.OnesCount64(size)
	if len(b) != logHeadFixedSize+n*hashSize || lo > size {
		return 0, 0, nil, false
	}
	hashes = make([][]byte, n)
	for i := range hashes {
		off := logHeadFixedSize + i*hashSize
		hashes[i] = bytes.Clone(b[off : off+hashSize])
	}
	return size, lo, hashes, true
}
