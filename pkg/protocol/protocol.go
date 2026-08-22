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

// Package protocol defines the extended wire protocol for MVCC, transaction,
// lease, watch, batch, index, and time-series operations in QRDB.
//
// The base KV protocol (OpPut=1, OpGet=2, OpDelete=3) is defined in
// go-quicraft's pkg/sm/kv package. This package extends it with
// higher-numbered op codes for MVCC-aware operations that are specific
// to the distributed transactional KV layer.
//
// Wire format:
//
//	[op:1][payload...]
//
// Each op code defines its own payload layout. All multi-byte integers
// are encoded in little-endian byte order for consistency with the base
// protocol.
package protocol

import (
	"encoding/binary"
	"math"
)

// ---------------------------------------------------------------------------
// Op codes (base: 1-3 in go-quicraft/pkg/sm/kv)
// ---------------------------------------------------------------------------

// FSM base KV operations (go-quicraft sm/kv).
const (
	// OpFSMPut is the base KV put operation (go-quicraft sm/kv).
	OpFSMPut byte = 0x01

	// OpFSMDelete is the base KV delete operation (go-quicraft sm/kv).
	OpFSMDelete byte = 0x02

	// OpFSMBatch is the base KV batch operation (go-quicraft sm/kv).
	OpFSMBatch byte = 0x03
)

const (
	// MVCC operations (10-19).

	// OpMVCCPut stores a key-value pair with MVCC revision tracking.
	// Payload: [keyLen:2 LE][key][leaseID:8 LE][value].
	OpMVCCPut byte = 10

	// OpMVCCPutIfAbsent stores a key-value pair with MVCC revision tracking
	// only when the key is absent.
	//
	// It carries a distinct opcode rather than a flag on OpMVCCPut so an older
	// replica rejects it as unknown instead of applying it as an unconditional
	// put — which would silently discard the condition and let a single-use
	// credential be issued twice.
	//
	// The condition is evaluated inside the state machine, against replicated
	// state at this log position, so every replica decides identically.
	OpMVCCPutIfAbsent byte = 65

	// OpMVCCDelete removes a key with MVCC revision tracking.
	// Payload: [keyLen:2 LE][key].
	OpMVCCDelete byte = 11

	// OpMVCCRange queries a range of keys in the MVCC store.
	// Payload: [startLen:2 LE][start][endLen:2 LE][end][revision:8 LE][limit:8 LE][flags:1].
	OpMVCCRange byte = 12

	// OpMVCCCompact compacts revisions up to the specified revision.
	// Payload: [revision:8 LE].
	OpMVCCCompact byte = 13

	// Transaction operations (20-29).

	// OpTransaction executes an atomic compare-and-swap transaction.
	// Payload: [compareCount:2 LE][compares...][successCount:2 LE][successOps...][failureCount:2 LE][failureOps...].
	OpTransaction byte = 20

	// Batch operations (30-39).

	// OpBatch executes multiple operations atomically.
	// Payload: [opCount:2 LE][ops...].
	OpBatch byte = 30

	// Lease operations (40-49).

	// OpLeaseGrant creates a new lease.
	// Payload: [ttl:8 LE].
	OpLeaseGrant byte = 40

	// OpLeaseRevoke expires a lease and deletes attached keys.
	// Payload: [leaseID:8 LE].
	OpLeaseRevoke byte = 41

	// OpLeaseKeepAlive resets a lease's TTL.
	// Payload: [leaseID:8 LE].
	OpLeaseKeepAlive byte = 42

	// Index operations (50-59).

	// OpIndexCreate creates a new secondary index.
	// Payload: [nameLen:2 LE][name][fieldLen:2 LE][field][flags:1].
	OpIndexCreate byte = 50

	// OpIndexDrop removes a secondary index.
	// Payload: [nameLen:2 LE][name].
	OpIndexDrop byte = 51

	// Time-series operations (60-69).

	// OpTSWrite stores a single time-series data point.
	// Payload: [metricLen:2 LE][metric][timestamp:8 LE][value:8 LE float64][tagCount:2 LE][tags...].
	OpTSWrite byte = 60

	// OpTSWriteBatch stores multiple time-series data points.
	// Payload: [pointCount:2 LE][points...].
	OpTSWriteBatch byte = 61

	// OpTSWritePoint stores a single validated time-series data point.
	// Payload: [metricLen:2 LE][metric][timestamp:8 LE][value:8 LE float64][tagCount:2 LE][tags...].
	OpTSWritePoint byte = 62

	// OpTSDeleteSeries deletes time-series data matching the criteria.
	// Payload: [metricLen:2 LE][metric][startNs:8 LE][endNs:8 LE][tagCount:2 LE][tags...].
	OpTSDeleteSeries byte = 63

	// OpTSSetRetention sets the retention policy for a metric.
	// Payload: [metricLen:2 LE][metric][durationNs:8 LE][resolutionNs:8 LE][aggregation:1].
	OpTSSetRetention byte = 64

	// OpCompareAndDelete removes a key only when its stored value still
	// equals an expected value, evaluated inside the state machine.
	//
	// It is the release half of OpPutIfAbsent / kv.OpPutIfAbsent. A guarded
	// create paired with an unguarded delete is half a primitive: the delete
	// can un-decide what the create decided. Concretely, two releases of one
	// key both read holder A, the first deletes, a third party legitimately
	// takes the now-absent key, and the second delete removes THEIR value
	// (go-qrdb#41). Only an in-state-machine comparison closes that, for the
	// same reason the conditional put's guard must run there: every replica
	// must decide identically from the same log position.
	OpCompareAndDelete byte = 66

	// OpCompareAndSwap replaces a key's value only when its stored value still
	// equals an expected value, evaluated inside the state machine.
	//
	// It completes the guarded set that OpMVCCPutIfAbsent and
	// OpCompareAndDelete began. A guarded create and a guarded delete with no
	// guarded EDIT between them is two thirds of a primitive: the create and
	// the release are decided against replicated state, and the transition in
	// the middle is decided nowhere.
	//
	// Composing one from the existing pair does not work. CompareAndDelete
	// followed by PutIfAbsent leaves the key briefly ABSENT — readers see "not
	// found" for a live record — and it reopens exactly the delete-then-recreate
	// window that lets a stalled contender take the key as a fresh create
	// (go-qrdb#56).
	OpCompareAndSwap byte = 67
)

// Range flags (bit mask for OpMVCCRange).
const (
	// RangeFlagCountOnly returns only the count without values.
	RangeFlagCountOnly byte = 1 << 0

	// RangeFlagKeysOnly returns keys without their values.
	RangeFlagKeysOnly byte = 1 << 1
)

// Index flags (bit mask for OpIndexCreate).
const (
	// IndexFlagUnique enforces unique index values.
	IndexFlagUnique byte = 1 << 0

	// IndexFlagSparse skips indexing absent fields.
	IndexFlagSparse byte = 1 << 1

	// IndexFlagNumeric encodes field values as fixed-width 8-byte big-endian
	// integers so that byte ordering matches numeric ordering. Range scans
	// like [1, 222) will not incorrectly include 1234.
	IndexFlagNumeric byte = 1 << 2
)

// headerSize is 1 byte for the op code.
const headerSize = 1

// FrameSize is the width, in bytes, of every length and count prefix in the
// transaction and batch encodings (EncodeOp, EncodeCompare, EncodeTransaction,
// EncodeBatch and their decoders).
//
// It is 4 rather than 2 because these are the encodings the SQL layer writes
// rows through: a 2-byte prefix caps a single value at 65535 bytes and a
// single transaction at 65535 operations, which makes an ordinary BLOB column
// or a moderately sized batch insert unrepresentable. A 4-byte prefix removes
// both ceilings, leaving go-quicraft's proto.MaxEntrySize (8 MiB per Raft log
// entry) as the only real limit — an operational bound the service layer
// already accounts for.
//
// It is exported because callers that size or chunk proposals must account for
// this framing exactly; deriving it from here keeps their arithmetic correct if
// the width ever changes again, rather than silently under-counting.
//
// Note that these encodings appear in the Raft log, so this width is not a
// free parameter: changing it makes previously written log entries
// undecodable. That is acceptable while qrdb is pre-release and no
// compatibility is promised.
const FrameSize = 4

// frameSize is the unexported spelling used throughout this package.
const frameSize = FrameSize

// ---------------------------------------------------------------------------
// MVCC encoders
// ---------------------------------------------------------------------------

// EncodeMVCCPut creates a binary MVCC Put command.
// Format: [op:1][keyLen:2 LE][key][leaseID:8 LE][value].
func EncodeMVCCPut(key, value []byte, leaseID int64) ([]byte, error) {
	if err := checkUint16Len(len(key), "key"); err != nil {
		return nil, err
	}
	// The VALUE is the unframed remainder of the payload, so nothing bounds it
	// and the sum can wrap where int is 32 bits. Checked accumulation before
	// make (go-qrdb#52).
	size, err := addSizes(headerSize, 2, len(key), 8, len(value))
	if err != nil {
		return nil, err
	}
	buf := make([]byte, size)
	buf[0] = OpMVCCPut
	if err := putUint16Len(buf[1:3], len(key), "key"); err != nil {
		return nil, err
	}
	copy(buf[3:], key)
	binary.LittleEndian.PutUint64(buf[3+len(key):], uint64(leaseID))
	copy(buf[3+len(key)+8:], value)
	return buf, nil
}

// EncodeMVCCPutIfAbsent creates a binary MVCC conditional-put command.
// Format: [op:1][keyLen:2 LE][key][leaseID:8 LE][value] — identical to
// EncodeMVCCPut apart from the opcode, so the decoder needs no new path.
func EncodeMVCCPutIfAbsent(key, value []byte, leaseID int64) ([]byte, error) {
	if err := checkUint16Len(len(key), "key"); err != nil {
		return nil, err
	}
	// Unframed value, as EncodeMVCCPut.
	size, err := addSizes(headerSize, 2, len(key), 8, len(value))
	if err != nil {
		return nil, err
	}
	buf := make([]byte, size)
	buf[0] = OpMVCCPutIfAbsent
	if err := putUint16Len(buf[1:3], len(key), "key"); err != nil {
		return nil, err
	}
	copy(buf[3:], key)
	binary.LittleEndian.PutUint64(buf[3+len(key):], uint64(leaseID))
	copy(buf[3+len(key)+8:], value)
	return buf, nil
}

// Result values reported by OpCompareAndDelete, mirroring kv.ResultStored /
// kv.ResultNotStored so the guarded create and the guarded release read the
// same way at a call site.
const (
	// ResultDeleted means this call removed the key.
	ResultDeleted uint64 = 1

	// ResultNotDeleted means the key was absent or held a different value.
	// The store is unchanged and this is a normal outcome, not an error.
	ResultNotDeleted uint64 = 0
)

// EncodeCompareAndDelete creates a binary conditional-delete command.
// Format: [op:1][keyLen:2 LE][key][expected] — the remainder after the key is
// the expected value, so an empty expected value is representable and is
// distinct from "no expectation" (which this opcode does not offer: an
// unconditional delete is OpFSMDelete).
//
// The key length is VALIDATED rather than cast. A narrow cast is not a
// truncation the caller notices: a 65,537-byte key writes a length of 1, the
// decoder reads a one-byte key, and the remaining bytes fold into the
// expected value — so the state machine compares and deletes a DIFFERENT,
// shorter key than the caller named. The expected value needs no equivalent
// check: it is the payload remainder and has no length field of its own.
func EncodeCompareAndDelete(key, expected []byte) ([]byte, error) {
	if err := checkUint16Len(len(key), "key"); err != nil {
		return nil, err
	}
	// `expected` is the unframed remainder, as the value is elsewhere.
	size, err := addSizes(headerSize, 2, len(key), len(expected))
	if err != nil {
		return nil, err
	}
	buf := make([]byte, size)
	buf[0] = OpCompareAndDelete
	if err := putUint16Len(buf[1:3], len(key), "key"); err != nil {
		return nil, err
	}
	copy(buf[3:], key)
	copy(buf[3+len(key):], expected)
	return buf, nil
}

// Result values reported by OpCompareAndSwap, mirroring the Deleted pair above
// so the three guarded operations read the same way at a call site.
const (
	// ResultSwapped means this call replaced the key's value.
	ResultSwapped uint64 = 1

	// ResultNotSwapped means the key was absent or held a different value.
	// The store is unchanged and this is a normal outcome, not an error.
	ResultNotSwapped uint64 = 0
)

// EncodeCompareAndSwap creates a binary conditional-replace command.
// Format: [op:1][keyLen:2 LE][key][expectedLen:4 LE][expected][next].
//
// Two length fields rather than one, because this command carries THREE
// variable-length values and only the last can be the unframed remainder.
// EncodeCompareAndDelete gets away with one because `expected` is its tail.
//
// The expected value is framed in FOUR bytes, not two. A two-byte field would
// cap the value a caller may guard on at 65,535 bytes while the value they can
// STORE is bounded only by the Raft entry size — so a caller could write a
// record and then be unable to express a compare against it, which is the
// same class of defect as go-qrdb#52 arriving through the front door. The key
// keeps its two-byte field to match every other command's key framing.
//
// `next` is the unframed remainder, so an empty replacement value is
// representable and is distinct from a delete (which is OpCompareAndDelete).
func EncodeCompareAndSwap(key, expected, next []byte) ([]byte, error) {
	if err := checkUint16Len(len(key), "key"); err != nil {
		return nil, err
	}
	if err := CheckUint32Len(len(expected), "expected value"); err != nil {
		return nil, err
	}
	size, err := addSizes(headerSize, 2, len(key), frameSize, len(expected), len(next))
	if err != nil {
		return nil, err
	}

	buf := make([]byte, size)
	off := 0
	buf[off] = OpCompareAndSwap
	off += headerSize

	if err := putUint16Len(buf[off:], len(key), "key"); err != nil {
		return nil, err
	}
	off += 2
	copy(buf[off:], key)
	off += len(key)

	if err := putUint32Len(buf[off:], len(expected), "expected value"); err != nil {
		return nil, err
	}
	off += frameSize
	copy(buf[off:], expected)
	off += len(expected)

	copy(buf[off:], next)
	return buf, nil
}

// DecodeCompareAndSwap parses a conditional-replace payload, which is the
// command with its op byte already stripped.
//
// Every length is bounded against the bytes that actually remain before it is
// used as a slice bound, through decodeLen, so a corrupt or hostile frame is
// refused rather than panicking the state machine that decodes it (go-qrdb#52).
func DecodeCompareAndSwap(payload []byte) (key, expected, next []byte, err error) {
	off := 0
	if len(payload) < 2 {
		return nil, nil, nil, &InvalidCommandError{Reason: "compare and swap: payload too short"}
	}
	rawKeyLen := binary.LittleEndian.Uint16(payload[off:])
	off += 2
	keyLen, err := decodeLen(uint32(rawKeyLen), len(payload)-off, "compare and swap: key exceeds payload")
	if err != nil {
		return nil, nil, nil, err
	}
	key = payload[off : off+keyLen]
	off += keyLen

	if off+frameSize > len(payload) {
		return nil, nil, nil, &InvalidCommandError{Reason: "compare and swap: missing expected length"}
	}
	rawExpLen := binary.LittleEndian.Uint32(payload[off:])
	off += frameSize
	expLen, err := decodeLen(rawExpLen, len(payload)-off, "compare and swap: expected value exceeds payload")
	if err != nil {
		return nil, nil, nil, err
	}
	expected = payload[off : off+expLen]
	off += expLen

	return key, expected, payload[off:], nil
}

// DecodeCompareAndDelete parses a conditional-delete payload, which is the
// command with its op byte already stripped.
func DecodeCompareAndDelete(payload []byte) (key, expected []byte, err error) {
	if len(payload) < 2 {
		return nil, nil, &InvalidCommandError{Reason: "compare and delete: payload too short"}
	}
	keyLen := int(binary.LittleEndian.Uint16(payload[0:2]))
	if keyLen+2 > len(payload) {
		return nil, nil, &InvalidCommandError{Reason: "compare and delete: key length exceeds payload"}
	}
	return payload[2 : 2+keyLen], payload[2+keyLen:], nil
}

// EncodeMVCCDelete creates a binary MVCC Delete command.
// Format: [op:1][keyLen:2 LE][key].
func EncodeMVCCDelete(key []byte) ([]byte, error) {
	if err := checkUint16Len(len(key), "key"); err != nil {
		return nil, err
	}
	buf := make([]byte, headerSize+2+len(key))
	buf[0] = OpMVCCDelete
	if err := putUint16Len(buf[1:3], len(key), "key"); err != nil {
		return nil, err
	}
	copy(buf[3:], key)
	return buf, nil
}

// EncodeMVCCRange creates a binary MVCC Range command.
// Format: [op:1][startLen:2 LE][start][endLen:2 LE][end][revision:8 LE][limit:8 LE][flags:1].
func EncodeMVCCRange(start, end []byte, revision, limit int64, countOnly, keysOnly bool) ([]byte, error) {
	if err := checkUint16Len(len(start), "range start"); err != nil {
		return nil, err
	}
	if err := checkUint16Len(len(end), "range end"); err != nil {
		return nil, err
	}
	buf := make([]byte, headerSize+2+len(start)+2+len(end)+8+8+1)
	off := 0
	buf[off] = OpMVCCRange
	off++

	if err := putUint16Len(buf[off:], len(start), "range start"); err != nil {
		return nil, err
	}
	off += 2
	copy(buf[off:], start)
	off += len(start)

	if err := putUint16Len(buf[off:], len(end), "range end"); err != nil {
		return nil, err
	}
	off += 2
	copy(buf[off:], end)
	off += len(end)

	binary.LittleEndian.PutUint64(buf[off:], uint64(revision))
	off += 8
	binary.LittleEndian.PutUint64(buf[off:], uint64(limit))
	off += 8

	var flags byte
	if countOnly {
		flags |= RangeFlagCountOnly
	}
	if keysOnly {
		flags |= RangeFlagKeysOnly
	}
	buf[off] = flags

	return buf, nil
}

// EncodeMVCCCompact creates a binary MVCC Compact command.
// Format: [op:1][revision:8 LE].
func EncodeMVCCCompact(revision int64) ([]byte, error) {
	buf := make([]byte, headerSize+8)
	buf[0] = OpMVCCCompact
	binary.LittleEndian.PutUint64(buf[1:], uint64(revision))
	return buf, nil
}

// ---------------------------------------------------------------------------
// Lease encoders
// ---------------------------------------------------------------------------

// EncodeLeaseGrant creates a binary Lease Grant command.
// Format: [op:1][ttl:8 LE].
func EncodeLeaseGrant(ttl int64) ([]byte, error) {
	buf := make([]byte, headerSize+8)
	buf[0] = OpLeaseGrant
	binary.LittleEndian.PutUint64(buf[1:], uint64(ttl))
	return buf, nil
}

// EncodeLeaseRevoke creates a binary Lease Revoke command.
// Format: [op:1][leaseID:8 LE].
func EncodeLeaseRevoke(leaseID int64) ([]byte, error) {
	buf := make([]byte, headerSize+8)
	buf[0] = OpLeaseRevoke
	binary.LittleEndian.PutUint64(buf[1:], uint64(leaseID))
	return buf, nil
}

// EncodeLeaseKeepAlive creates a binary Lease KeepAlive command.
// Format: [op:1][leaseID:8 LE].
func EncodeLeaseKeepAlive(leaseID int64) ([]byte, error) {
	buf := make([]byte, headerSize+8)
	buf[0] = OpLeaseKeepAlive
	binary.LittleEndian.PutUint64(buf[1:], uint64(leaseID))
	return buf, nil
}

// ---------------------------------------------------------------------------
// Index encoders
// ---------------------------------------------------------------------------

// EncodeIndexCreate creates a binary Index Create command.
// Format: [op:1][nameLen:2 LE][name][fieldLen:2 LE][field][flags:1].
func EncodeIndexCreate(name, fieldName string, unique, sparse, numeric bool) ([]byte, error) {
	nameBytes := []byte(name)
	fieldBytes := []byte(fieldName)
	if err := checkUint16Len(len(nameBytes), "index name"); err != nil {
		return nil, err
	}
	if err := checkUint16Len(len(fieldBytes), "index field"); err != nil {
		return nil, err
	}
	buf := make([]byte, headerSize+2+len(nameBytes)+2+len(fieldBytes)+1)
	off := 0

	buf[off] = OpIndexCreate
	off++

	if err := putUint16Len(buf[off:], len(nameBytes), "index name"); err != nil {
		return nil, err
	}
	off += 2
	copy(buf[off:], nameBytes)
	off += len(nameBytes)

	if err := putUint16Len(buf[off:], len(fieldBytes), "index field"); err != nil {
		return nil, err
	}
	off += 2
	copy(buf[off:], fieldBytes)
	off += len(fieldBytes)

	var flags byte
	if unique {
		flags |= IndexFlagUnique
	}
	if sparse {
		flags |= IndexFlagSparse
	}
	if numeric {
		flags |= IndexFlagNumeric
	}
	buf[off] = flags

	return buf, nil
}

// EncodeIndexDrop creates a binary Index Drop command.
// Format: [op:1][nameLen:2 LE][name].
func EncodeIndexDrop(name string) ([]byte, error) {
	nameBytes := []byte(name)
	if err := checkUint16Len(len(nameBytes), "index name"); err != nil {
		return nil, err
	}
	buf := make([]byte, headerSize+2+len(nameBytes))
	buf[0] = OpIndexDrop
	if err := putUint16Len(buf[1:3], len(nameBytes), "index name"); err != nil {
		return nil, err
	}
	copy(buf[3:], nameBytes)
	return buf, nil
}

// ---------------------------------------------------------------------------
// Time-series encoders
// ---------------------------------------------------------------------------

// EncodeTSWrite creates a binary Time-Series Write command.
// Format: [op:1][metricLen:2 LE][metric][timestamp:8 LE][value:8 LE float64][tagCount:2 LE][tags...]
// Each tag: [keyLen:2 LE][key][valLen:2 LE][val].
func EncodeTSWrite(metric string, timestamp int64, value float64, tags map[string]string) ([]byte, error) {
	metricBytes := []byte(metric)

	// Measure and validate together, before anything is allocated.
	if err := checkUint16Len(len(metricBytes), "metric"); err != nil {
		return nil, err
	}
	tagsSize, err := tagBlockSize(tags)
	if err != nil {
		return nil, err
	}

	size, err := tsMetricFrameSize(len(metricBytes), tagsSize)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, size)
	off := 0

	buf[off] = OpTSWrite
	off++

	if err := putUint16Len(buf[off:], len(metricBytes), "metric"); err != nil {
		return nil, err
	}
	off += 2
	copy(buf[off:], metricBytes)
	off += len(metricBytes)

	binary.LittleEndian.PutUint64(buf[off:], uint64(timestamp))
	off += 8

	binary.LittleEndian.PutUint64(buf[off:], math.Float64bits(value))
	off += 8

	if err := putUint16Len(buf[off:], len(tags), "tag count"); err != nil {
		return nil, err
	}
	off += 2

	for k, v := range tags {
		if err := putUint16Len(buf[off:], len(k), "tag key"); err != nil {
			return nil, err
		}
		off += 2
		copy(buf[off:], k)
		off += len(k)

		if err := putUint16Len(buf[off:], len(v), "tag value"); err != nil {
			return nil, err
		}
		off += 2
		copy(buf[off:], v)
		off += len(v)
	}

	return buf, nil
}

// EncodeTSWriteBatch creates a binary Time-Series WriteBatch command.
// Format: [op:1][pointCount:2 LE][points...]
// Each point is encoded as: [metricLen:2 LE][metric][timestamp:8 LE][value:8 LE float64][tagCount:2 LE][tags...].
func EncodeTSWriteBatch(points []TSWriteParams) ([]byte, error) {
	// One measuring pass, before anything proportional to the request is
	// allocated. Every nested field is validated here, so an unencodable point
	// anywhere in the batch is refused at the cost of pointSizes — bounded by
	// the point count, which is the first thing checked — rather than at the
	// cost of the whole encoded batch. Sizing first and validating inside the
	// writing loop, as this did, turns any malformed point into an allocation
	// the size of the entire request.
	if err := checkUint16Len(len(points), "point count"); err != nil {
		return nil, err
	}

	pointSizes := make([]int, len(points))
	size := headerSize + 2
	for i, p := range points {
		metricLen := len(p.Metric)
		if err := checkUint16Len(metricLen, "metric"); err != nil {
			return nil, err
		}
		tagsSize, err := tagBlockSize(p.Tags)
		if err != nil {
			return nil, err
		}

		pointSize := 2 + metricLen + 8 + 8 + 2 + tagsSize
		if err := checkUint16Len(pointSize, "point length"); err != nil {
			return nil, err
		}
		pointSizes[i] = pointSize
		if size, err = addSizes(size, 2, pointSize); err != nil { // 2 for the point length prefix
			return nil, err
		}
	}

	buf := make([]byte, size)
	off := 0

	buf[off] = OpTSWriteBatch
	off++

	if err := putUint16Len(buf[off:], len(points), "point count"); err != nil {
		return nil, err
	}
	off += 2

	for i, p := range points {
		metricBytes := []byte(p.Metric)

		if err := putUint16Len(buf[off:], pointSizes[i], "point length"); err != nil {
			return nil, err
		}
		off += 2

		if err := putUint16Len(buf[off:], len(metricBytes), "metric"); err != nil {
			return nil, err
		}
		off += 2
		copy(buf[off:], metricBytes)
		off += len(metricBytes)

		binary.LittleEndian.PutUint64(buf[off:], uint64(p.Timestamp))
		off += 8

		binary.LittleEndian.PutUint64(buf[off:], math.Float64bits(p.Value))
		off += 8

		if err := putUint16Len(buf[off:], len(p.Tags), "tag count"); err != nil {
			return nil, err
		}
		off += 2

		for k, v := range p.Tags {
			if err := putUint16Len(buf[off:], len(k), "tag key"); err != nil {
				return nil, err
			}
			off += 2
			copy(buf[off:], k)
			off += len(k)

			if err := putUint16Len(buf[off:], len(v), "tag value"); err != nil {
				return nil, err
			}
			off += 2
			copy(buf[off:], v)
			off += len(v)
		}
	}

	return buf, nil
}

// DecodeTSWriteBatch decodes a Time-Series WriteBatch payload into individual
// TSWriteParams.
func DecodeTSWriteBatch(payload []byte) ([]TSWriteParams, error) {
	if len(payload) < 2 {
		return nil, &InvalidCommandError{Reason: "ts write batch: payload too short"}
	}

	off := 0
	pointCount := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2

	points := make([]TSWriteParams, 0, pointCount)
	for i := 0; i < pointCount; i++ {
		if off+2 > len(payload) {
			return nil, &InvalidCommandError{Reason: "ts write batch: truncated point length"}
		}
		pLen := int(binary.LittleEndian.Uint16(payload[off:]))
		off += 2
		if off+pLen > len(payload) {
			return nil, &InvalidCommandError{Reason: "ts write batch: point exceeds payload"}
		}
		p, err := DecodeTSWrite(payload[off : off+pLen])
		if err != nil {
			return nil, err
		}
		points = append(points, p)
		off += pLen
	}

	return points, nil
}

// ---------------------------------------------------------------------------
// Transaction / Batch encoders
// ---------------------------------------------------------------------------

// EncodeOp encodes a single operation for use in transaction and batch payloads.
// Format: [opType:1][keyLen:4 LE][key][valLen:4 LE][value][rangeEndLen:4 LE][rangeEnd][leaseID:8 LE].
func EncodeOp(opType byte, key, value, rangeEnd []byte, leaseID int64) ([]byte, error) {
	// Validate each field, then accumulate through addSize. Summing first and
	// checking after is not equivalent: nothing stops a caller passing the SAME
	// large slice as key, value and rangeEnd, and where int is 32 bits three
	// individually-legal lengths sum past its maximum. The wrapped total then
	// reaches make, which panics or — worse — allocates an undersized buffer
	// that the writes below run off the end of (go-qrdb#52).
	size := 1 + 8
	for _, f := range []struct {
		bytes []byte
		name  string
	}{{key, "key"}, {value, "value"}, {rangeEnd, "range end"}} {
		if err := CheckUint32Len(len(f.bytes), f.name); err != nil {
			return nil, err
		}
		var err error
		if size, err = addSizes(size, frameSize, len(f.bytes)); err != nil {
			return nil, err
		}
	}
	buf := make([]byte, size)
	off := 0

	buf[off] = opType
	off++

	if err := putUint32Len(buf[off:], len(key), "key"); err != nil {
		return nil, err
	}
	off += frameSize
	copy(buf[off:], key)
	off += len(key)

	if err := putUint32Len(buf[off:], len(value), "value"); err != nil {
		return nil, err
	}
	off += frameSize
	copy(buf[off:], value)
	off += len(value)

	if err := putUint32Len(buf[off:], len(rangeEnd), "range end"); err != nil {
		return nil, err
	}
	off += frameSize
	copy(buf[off:], rangeEnd)
	off += len(rangeEnd)

	binary.LittleEndian.PutUint64(buf[off:], uint64(leaseID))

	return buf, nil
}

// EncodeCompare encodes a single compare for transaction payloads.
// Format: [keyLen:4 LE][key][target:1][result:1][valLen:4 LE][value][version:8 LE][createRev:8 LE][modRev:8 LE].
func EncodeCompare(key []byte, target, result byte, value []byte, version, createRevision, modRevision int64) ([]byte, error) {
	// Validate then accumulate, for the reason given in EncodeOp: key and value
	// may share one backing slice, and a checked sum is what makes the total
	// honest rather than the individual lengths.
	size := 1 + 1 + 8 + 8 + 8
	for _, f := range []struct {
		bytes []byte
		name  string
	}{{key, "key"}, {value, "value"}} {
		if err := CheckUint32Len(len(f.bytes), f.name); err != nil {
			return nil, err
		}
		var err error
		if size, err = addSizes(size, frameSize, len(f.bytes)); err != nil {
			return nil, err
		}
	}
	buf := make([]byte, size)
	off := 0

	if err := putUint32Len(buf[off:], len(key), "key"); err != nil {
		return nil, err
	}
	off += frameSize
	copy(buf[off:], key)
	off += len(key)

	buf[off] = target
	off++
	buf[off] = result
	off++

	if err := putUint32Len(buf[off:], len(value), "value"); err != nil {
		return nil, err
	}
	off += frameSize
	copy(buf[off:], value)
	off += len(value)

	binary.LittleEndian.PutUint64(buf[off:], uint64(version))
	off += 8
	binary.LittleEndian.PutUint64(buf[off:], uint64(createRevision))
	off += 8
	binary.LittleEndian.PutUint64(buf[off:], uint64(modRevision))

	return buf, nil
}

// EncodeTransaction creates a binary Transaction command.
// Format: [op:1][compareCount:4 LE][compares...][successCount:4 LE][successOps...][failureCount:4 LE][failureOps...].
func EncodeTransaction(compares, successOps, failureOps [][]byte) ([]byte, error) {
	// Measure and validate all three collections before allocating any of them,
	// so a malformed element in the third does not cost a buffer sized for the
	// whole transaction. framedSlicesSize is the measuring half of the
	// encodeFramedSlices calls below.
	size := headerSize
	for _, items := range [][][]byte{compares, successOps, failureOps} {
		n, err := framedSlicesSize(items)
		if err != nil {
			return nil, err
		}
		if size, err = addSize(size, n); err != nil {
			return nil, err
		}
	}

	buf := make([]byte, size)
	off := 0

	buf[off] = OpTransaction
	off++

	n, err := encodeFramedSlices(buf[off:], compares)
	if err != nil {
		return nil, err
	}
	off += n
	n, err = encodeFramedSlices(buf[off:], successOps)
	if err != nil {
		return nil, err
	}
	off += n
	if _, err := encodeFramedSlices(buf[off:], failureOps); err != nil {
		return nil, err
	}

	return buf, nil
}

// EncodeBatch creates a binary Batch command.
// Format: [op:1][opCount:4 LE][ops...].
func EncodeBatch(ops [][]byte) ([]byte, error) {
	// Measure and validate before allocating, as EncodeTransaction does.
	n, err := framedSlicesSize(ops)
	if err != nil {
		return nil, err
	}
	size, err := addSize(headerSize, n)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, size)
	off := 0

	buf[off] = OpBatch
	off++

	if _, err := encodeFramedSlices(buf[off:], ops); err != nil {
		return nil, err
	}

	return buf, nil
}

// encodeFramedSlices writes a count-prefixed run of length-prefixed byte
// slices into buf and returns the number of bytes written, or an error when a
// count or an element cannot be described by its length field. Both the count and
// each element length are frameSize-byte little-endian values. buf must be
// large enough to hold the whole run.
func encodeFramedSlices(buf []byte, items [][]byte) (int, error) {
	off := 0
	if err := putUint32Len(buf[off:], len(items), "item count"); err != nil {
		return 0, err
	}
	off += frameSize
	for _, item := range items {
		if err := putUint32Len(buf[off:], len(item), "item"); err != nil {
			return 0, err
		}
		off += frameSize
		copy(buf[off:], item)
		off += len(item)
	}
	return off, nil
}

// ---------------------------------------------------------------------------
// Decoders
// ---------------------------------------------------------------------------

// Command represents a decoded extended protocol command.
type Command struct {
	// Op is the operation code.
	Op byte

	// Payload is the raw payload bytes following the op code.
	Payload []byte
}

// DecodeCommand parses the op code and extracts the payload.
func DecodeCommand(data []byte) (Command, error) {
	if len(data) < headerSize {
		return Command{}, &InvalidCommandError{Reason: "data too short"}
	}
	return Command{
		Op:      data[0],
		Payload: data[headerSize:],
	}, nil
}

// DecodeMVCCPut extracts key, value, and leaseID from an MVCC Put payload.
func DecodeMVCCPut(payload []byte) (key, value []byte, leaseID int64, err error) {
	if len(payload) < 2 {
		return nil, nil, 0, &InvalidCommandError{Reason: "mvcc put: payload too short"}
	}
	keyLen := binary.LittleEndian.Uint16(payload[0:2])
	if int(2+keyLen+8) > len(payload) {
		return nil, nil, 0, &InvalidCommandError{Reason: "mvcc put: key length exceeds payload"}
	}
	key = payload[2 : 2+keyLen]
	leaseID = int64(binary.LittleEndian.Uint64(payload[2+keyLen:]))
	value = payload[2+keyLen+8:]
	return key, value, leaseID, nil
}

// DecodeMVCCDelete extracts the key from an MVCC Delete payload.
func DecodeMVCCDelete(payload []byte) (key []byte, err error) {
	if len(payload) < 2 {
		return nil, &InvalidCommandError{Reason: "mvcc delete: payload too short"}
	}
	keyLen := binary.LittleEndian.Uint16(payload[0:2])
	if int(2+keyLen) > len(payload) {
		return nil, &InvalidCommandError{Reason: "mvcc delete: key length exceeds payload"}
	}
	return payload[2 : 2+keyLen], nil
}

// MVCCRangeParams holds decoded parameters for an MVCC Range command.
type MVCCRangeParams struct {
	Start     []byte
	End       []byte
	Revision  int64
	Limit     int64
	CountOnly bool
	KeysOnly  bool
}

// DecodeMVCCRange extracts range parameters from an MVCC Range payload.
func DecodeMVCCRange(payload []byte) (MVCCRangeParams, error) {
	if len(payload) < 2 {
		return MVCCRangeParams{}, &InvalidCommandError{Reason: "mvcc range: payload too short"}
	}

	off := 0
	startLen := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2
	if off+startLen > len(payload) {
		return MVCCRangeParams{}, &InvalidCommandError{Reason: "mvcc range: start length exceeds payload"}
	}
	start := payload[off : off+startLen]
	off += startLen

	if off+2 > len(payload) {
		return MVCCRangeParams{}, &InvalidCommandError{Reason: "mvcc range: missing end length"}
	}
	endLen := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2
	if off+endLen > len(payload) {
		return MVCCRangeParams{}, &InvalidCommandError{Reason: "mvcc range: end length exceeds payload"}
	}
	end := payload[off : off+endLen]
	off += endLen

	if off+17 > len(payload) {
		return MVCCRangeParams{}, &InvalidCommandError{Reason: "mvcc range: missing revision/limit/flags"}
	}
	revision := int64(binary.LittleEndian.Uint64(payload[off:]))
	off += 8
	limit := int64(binary.LittleEndian.Uint64(payload[off:]))
	off += 8
	flags := payload[off]

	return MVCCRangeParams{
		Start:     start,
		End:       end,
		Revision:  revision,
		Limit:     limit,
		CountOnly: flags&RangeFlagCountOnly != 0,
		KeysOnly:  flags&RangeFlagKeysOnly != 0,
	}, nil
}

// DecodeMVCCCompact extracts the revision from an MVCC Compact payload.
func DecodeMVCCCompact(payload []byte) (int64, error) {
	if len(payload) < 8 {
		return 0, &InvalidCommandError{Reason: "mvcc compact: payload too short"}
	}
	return int64(binary.LittleEndian.Uint64(payload[0:8])), nil
}

// DecodeLeaseGrant extracts the TTL from a Lease Grant payload.
func DecodeLeaseGrant(payload []byte) (int64, error) {
	if len(payload) < 8 {
		return 0, &InvalidCommandError{Reason: "lease grant: payload too short"}
	}
	return int64(binary.LittleEndian.Uint64(payload[0:8])), nil
}

// DecodeLeaseRevoke extracts the lease ID from a Lease Revoke payload.
func DecodeLeaseRevoke(payload []byte) (int64, error) {
	if len(payload) < 8 {
		return 0, &InvalidCommandError{Reason: "lease revoke: payload too short"}
	}
	return int64(binary.LittleEndian.Uint64(payload[0:8])), nil
}

// DecodeLeaseKeepAlive extracts the lease ID from a Lease KeepAlive payload.
func DecodeLeaseKeepAlive(payload []byte) (int64, error) {
	if len(payload) < 8 {
		return 0, &InvalidCommandError{Reason: "lease keep-alive: payload too short"}
	}
	return int64(binary.LittleEndian.Uint64(payload[0:8])), nil
}

// DecodeIndexCreate extracts name, field, and flags from an Index Create payload.
func DecodeIndexCreate(payload []byte) (name, fieldName string, unique, sparse, numeric bool, err error) {
	if len(payload) < 2 {
		return "", "", false, false, false, &InvalidCommandError{Reason: "index create: payload too short"}
	}

	off := 0
	nameLen := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2
	if off+nameLen > len(payload) {
		return "", "", false, false, false, &InvalidCommandError{Reason: "index create: name length exceeds payload"}
	}
	name = string(payload[off : off+nameLen])
	off += nameLen

	if off+2 > len(payload) {
		return "", "", false, false, false, &InvalidCommandError{Reason: "index create: missing field length"}
	}
	fieldLen := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2
	if off+fieldLen > len(payload) {
		return "", "", false, false, false, &InvalidCommandError{Reason: "index create: field length exceeds payload"}
	}
	fieldName = string(payload[off : off+fieldLen])
	off += fieldLen

	if off >= len(payload) {
		return "", "", false, false, false, &InvalidCommandError{Reason: "index create: missing flags"}
	}
	flags := payload[off]
	unique = flags&IndexFlagUnique != 0
	sparse = flags&IndexFlagSparse != 0
	numeric = flags&IndexFlagNumeric != 0

	return name, fieldName, unique, sparse, numeric, nil
}

// DecodeIndexDrop extracts the index name from an Index Drop payload.
func DecodeIndexDrop(payload []byte) (string, error) {
	if len(payload) < 2 {
		return "", &InvalidCommandError{Reason: "index drop: payload too short"}
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[0:2]))
	if 2+nameLen > len(payload) {
		return "", &InvalidCommandError{Reason: "index drop: name length exceeds payload"}
	}
	return string(payload[2 : 2+nameLen]), nil
}

// TSWriteParams holds decoded parameters for a time-series write.
type TSWriteParams struct {
	Metric    string
	Timestamp int64
	Value     float64
	Tags      map[string]string
}

// DecodeTSWrite extracts time-series write parameters from a payload.
func DecodeTSWrite(payload []byte) (TSWriteParams, error) {
	if len(payload) < 2 {
		return TSWriteParams{}, &InvalidCommandError{Reason: "ts write: payload too short"}
	}

	off := 0
	metricLen := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2
	if off+metricLen > len(payload) {
		return TSWriteParams{}, &InvalidCommandError{Reason: "ts write: metric length exceeds payload"}
	}
	metric := string(payload[off : off+metricLen])
	off += metricLen

	if off+18 > len(payload) {
		return TSWriteParams{}, &InvalidCommandError{Reason: "ts write: missing timestamp/value/tagCount"}
	}
	timestamp := int64(binary.LittleEndian.Uint64(payload[off:]))
	off += 8
	value := math.Float64frombits(binary.LittleEndian.Uint64(payload[off:]))
	off += 8
	tagCount := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2

	tags := make(map[string]string, tagCount)
	for i := 0; i < tagCount; i++ {
		if off+2 > len(payload) {
			return TSWriteParams{}, &InvalidCommandError{Reason: "ts write: truncated tag key length"}
		}
		kLen := int(binary.LittleEndian.Uint16(payload[off:]))
		off += 2
		if off+kLen > len(payload) {
			return TSWriteParams{}, &InvalidCommandError{Reason: "ts write: tag key exceeds payload"}
		}
		k := string(payload[off : off+kLen])
		off += kLen

		if off+2 > len(payload) {
			return TSWriteParams{}, &InvalidCommandError{Reason: "ts write: truncated tag value length"}
		}
		vLen := int(binary.LittleEndian.Uint16(payload[off:]))
		off += 2
		if off+vLen > len(payload) {
			return TSWriteParams{}, &InvalidCommandError{Reason: "ts write: tag value exceeds payload"}
		}
		v := string(payload[off : off+vLen])
		off += vLen

		tags[k] = v
	}

	return TSWriteParams{
		Metric:    metric,
		Timestamp: timestamp,
		Value:     value,
		Tags:      tags,
	}, nil
}

// DecodedOp holds the decoded fields of a single operation.
type DecodedOp struct {
	Type     byte
	Key      []byte
	Value    []byte
	RangeEnd []byte
	LeaseID  int64
}

// DecodeOp decodes a single operation from its binary representation.
func DecodeOp(data []byte) (DecodedOp, int, error) {
	if len(data) < 1 {
		return DecodedOp{}, 0, &InvalidCommandError{Reason: "op: data too short"}
	}

	off := 0
	opType := data[off]
	off++

	if off+frameSize > len(data) {
		return DecodedOp{}, 0, &InvalidCommandError{Reason: "op: missing key length"}
	}
	rawKeyLen := binary.LittleEndian.Uint32(data[off:])
	off += frameSize
	keyLen, err := decodeLen(rawKeyLen, len(data)-off, "op: key exceeds data")
	if err != nil {
		return DecodedOp{}, 0, err
	}
	key := data[off : off+keyLen]
	off += keyLen

	if off+frameSize > len(data) {
		return DecodedOp{}, 0, &InvalidCommandError{Reason: "op: missing value length"}
	}
	rawValLen := binary.LittleEndian.Uint32(data[off:])
	off += frameSize
	valLen, err := decodeLen(rawValLen, len(data)-off, "op: value exceeds data")
	if err != nil {
		return DecodedOp{}, 0, err
	}
	value := data[off : off+valLen]
	off += valLen

	if off+frameSize > len(data) {
		return DecodedOp{}, 0, &InvalidCommandError{Reason: "op: missing range end length"}
	}
	rawRangeEndLen := binary.LittleEndian.Uint32(data[off:])
	off += frameSize
	reLen, err := decodeLen(rawRangeEndLen, len(data)-off, "op: range end exceeds data")
	if err != nil {
		return DecodedOp{}, 0, err
	}
	rangeEnd := data[off : off+reLen]
	off += reLen

	if off+8 > len(data) {
		return DecodedOp{}, 0, &InvalidCommandError{Reason: "op: missing lease ID"}
	}
	leaseID := int64(binary.LittleEndian.Uint64(data[off:]))
	off += 8

	return DecodedOp{
		Type:     opType,
		Key:      key,
		Value:    value,
		RangeEnd: rangeEnd,
		LeaseID:  leaseID,
	}, off, nil
}

// DecodedCompare holds the decoded fields of a transaction compare.
type DecodedCompare struct {
	Key            []byte
	Target         byte
	Result         byte
	Value          []byte
	Version        int64
	CreateRevision int64
	ModRevision    int64
}

// DecodeCompare decodes a single compare from its binary representation.
func DecodeCompare(data []byte) (DecodedCompare, int, error) {
	if len(data) < frameSize {
		return DecodedCompare{}, 0, &InvalidCommandError{Reason: "compare: data too short"}
	}

	off := 0
	rawKeyLen := binary.LittleEndian.Uint32(data[off:])
	off += frameSize
	keyLen, err := decodeLen(rawKeyLen, len(data)-off, "compare: key exceeds data")
	if err != nil {
		return DecodedCompare{}, 0, err
	}
	key := data[off : off+keyLen]
	off += keyLen

	if off+2 > len(data) {
		return DecodedCompare{}, 0, &InvalidCommandError{Reason: "compare: missing target/result"}
	}
	target := data[off]
	off++
	result := data[off]
	off++

	if off+frameSize > len(data) {
		return DecodedCompare{}, 0, &InvalidCommandError{Reason: "compare: missing value length"}
	}
	rawValLen := binary.LittleEndian.Uint32(data[off:])
	off += frameSize
	valLen, err := decodeLen(rawValLen, len(data)-off, "compare: value exceeds data")
	if err != nil {
		return DecodedCompare{}, 0, err
	}
	value := data[off : off+valLen]
	off += valLen

	if off+24 > len(data) {
		return DecodedCompare{}, 0, &InvalidCommandError{Reason: "compare: missing version/revisions"}
	}
	version := int64(binary.LittleEndian.Uint64(data[off:]))
	off += 8
	createRev := int64(binary.LittleEndian.Uint64(data[off:]))
	off += 8
	modRev := int64(binary.LittleEndian.Uint64(data[off:]))
	off += 8

	return DecodedCompare{
		Key:            key,
		Target:         target,
		Result:         result,
		Value:          value,
		Version:        version,
		CreateRevision: createRev,
		ModRevision:    modRev,
	}, off, nil
}

// DecodedTransaction holds decoded transaction components.
type DecodedTransaction struct {
	Compares   []DecodedCompare
	SuccessOps []DecodedOp
	FailureOps []DecodedOp
}

// maxPrealloc bounds how many elements a decoder will reserve capacity for up
// front. Element counts are read from the payload before the corresponding
// bytes have been shown to exist, and with a 4-byte count field a corrupt or
// hostile header can name billions of elements. Reserving min(count,
// maxPrealloc) keeps the common case allocation-efficient while ensuring a bad
// count costs a bounded allocation rather than an out-of-memory kill; append
// grows the slice normally for genuinely large payloads.
const maxPrealloc = 1024

// preallocSize returns the capacity to reserve for a decoded element count.
// decodeCount reads a frameSize-byte element count and refuses one the payload
// cannot possibly hold.
//
// The conversion is the point. `int(uint32)` is lossless only where int is 64
// bits; where it is 32, a count past MaxInt32 converts NEGATIVE, every
// `for i := 0; i < count` below is skipped, and a hostile frame like
// `ff ff ff ff` decodes as an EMPTY command with a nil error. That is
// fail-open deserialization on the apply path, and it decodes differently on
// different architectures from the same bytes — which a replicated state
// machine cannot tolerate (go-qrdb#52, #73).
//
// The payload bound is the stronger half and is not about width at all: every
// element carries at least its own frameSize length prefix, so a count larger
// than the remaining bytes can frame is a lie whatever the platform. It
// rejects 0x7fffffff on a 64-bit build too, where the conversion is harmless
// and the count is still impossible.
func decodeCount(raw uint32, remaining int, what string) (int, error) {
	if uint64(raw) > uint64(math.MaxInt) {
		return 0, &InvalidCommandError{Reason: what + ": count is not representable"}
	}
	n := int(raw)
	if remaining < 0 || n > remaining/frameSize {
		return 0, &InvalidCommandError{Reason: what + ": count exceeds payload"}
	}
	return n, nil
}

// decodeLen reads a frameSize-byte element length and refuses one the
// remaining bytes cannot hold. The bound is compared in uint64 rather than as
// off+n, which is what makes it correct on 32-bit builds: there int(raw) is
// large enough that the caller's offset arithmetic overflows — a length of
// 0x7fffffff makes off+n negative, so a naive bounds check passes and the
// slice expression panics. remaining is the count of bytes after the length
// field, so a returned n is always a valid slice bound.
func decodeLen(raw uint32, remaining int, reason string) (int, error) {
	if remaining < 0 || uint64(raw) > uint64(remaining) {
		return 0, &InvalidCommandError{Reason: reason}
	}
	return int(raw), nil
}

func preallocSize(count int) int {
	if count < 0 || count > maxPrealloc {
		return maxPrealloc
	}
	return count
}

// DecodeTransaction decodes a full transaction payload.
func DecodeTransaction(payload []byte) (DecodedTransaction, error) {
	off := 0

	if off+frameSize > len(payload) {
		return DecodedTransaction{}, &InvalidCommandError{Reason: "txn: missing compare count"}
	}
	compareCount, err := decodeCount(binary.LittleEndian.Uint32(payload[off:]),
		len(payload)-off-frameSize, "txn: compare")
	if err != nil {
		return DecodedTransaction{}, err
	}
	off += frameSize

	compares := make([]DecodedCompare, 0, preallocSize(compareCount))
	for i := 0; i < compareCount; i++ {
		if off+frameSize > len(payload) {
			return DecodedTransaction{}, &InvalidCommandError{Reason: "txn: truncated compare length"}
		}
		rawLen := binary.LittleEndian.Uint32(payload[off:])
		off += frameSize
		cLen, lerr := decodeLen(rawLen, len(payload)-off, "txn: compare exceeds payload")
		if lerr != nil {
			return DecodedTransaction{}, lerr
		}
		c, _, err := DecodeCompare(payload[off : off+cLen])
		if err != nil {
			return DecodedTransaction{}, err
		}
		compares = append(compares, c)
		off += cLen
	}

	decodeOps := func(label string) ([]DecodedOp, error) {
		if off+frameSize > len(payload) {
			return nil, &InvalidCommandError{Reason: "txn: missing " + label + " count"}
		}
		count, cerr := decodeCount(binary.LittleEndian.Uint32(payload[off:]),
			len(payload)-off-frameSize, "txn: "+label)
		if cerr != nil {
			return nil, cerr
		}
		off += frameSize
		ops := make([]DecodedOp, 0, preallocSize(count))
		for i := 0; i < count; i++ {
			if off+frameSize > len(payload) {
				return nil, &InvalidCommandError{Reason: "txn: truncated " + label + " op length"}
			}
			rawLen := binary.LittleEndian.Uint32(payload[off:])
			off += frameSize
			oLen, lerr := decodeLen(rawLen, len(payload)-off, "txn: "+label+" op exceeds payload")
			if lerr != nil {
				return nil, lerr
			}
			op, _, err := DecodeOp(payload[off : off+oLen])
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
			off += oLen
		}
		return ops, nil
	}

	successOps, err := decodeOps("success")
	if err != nil {
		return DecodedTransaction{}, err
	}

	failureOps, err := decodeOps("failure")
	if err != nil {
		return DecodedTransaction{}, err
	}

	return DecodedTransaction{
		Compares:   compares,
		SuccessOps: successOps,
		FailureOps: failureOps,
	}, nil
}

// DecodeBatch decodes a batch payload into individual operations.
func DecodeBatch(payload []byte) ([]DecodedOp, error) {
	if len(payload) < frameSize {
		return nil, &InvalidCommandError{Reason: "batch: payload too short"}
	}

	off := 0
	opCount, err := decodeCount(binary.LittleEndian.Uint32(payload[off:]),
		len(payload)-off-frameSize, "batch: op")
	if err != nil {
		return nil, err
	}
	off += frameSize

	ops := make([]DecodedOp, 0, preallocSize(opCount))
	for i := 0; i < opCount; i++ {
		if off+frameSize > len(payload) {
			return nil, &InvalidCommandError{Reason: "batch: truncated op length"}
		}
		rawLen := binary.LittleEndian.Uint32(payload[off:])
		off += frameSize
		oLen, lerr := decodeLen(rawLen, len(payload)-off, "batch: op exceeds payload")
		if lerr != nil {
			return nil, lerr
		}
		op, _, err := DecodeOp(payload[off : off+oLen])
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
		off += oLen
	}

	return ops, nil
}

// ---------------------------------------------------------------------------
// Time-series extended encode/decode
// ---------------------------------------------------------------------------

// EncodeTSWritePoint creates a binary Time-Series WritePoint command.
// The on-wire format is identical to EncodeTSWrite but uses a distinct
// opcode so the state machine can validate before persisting.
func EncodeTSWritePoint(metric string, timestamp int64, value float64, tags map[string]string) ([]byte, error) {
	buf, err := EncodeTSWrite(metric, timestamp, value, tags)
	if err != nil {
		return nil, err
	}
	buf[0] = OpTSWritePoint
	return buf, nil
}

// TSDeleteParams holds decoded parameters for a time-series delete.
type TSDeleteParams struct {
	Metric  string
	StartNs int64
	EndNs   int64
	Tags    map[string]string
}

// EncodeTSDeleteSeries creates a binary Time-Series DeleteSeries command.
// Format: [op:1][metricLen:2 LE][metric][startNs:8 LE][endNs:8 LE][tagCount:2 LE][tags...].
func EncodeTSDeleteSeries(metric string, startNs, endNs int64, tags map[string]string) ([]byte, error) {
	metricBytes := []byte(metric)

	// Measure and validate together, before anything is allocated.
	if err := checkUint16Len(len(metricBytes), "metric"); err != nil {
		return nil, err
	}
	tagsSize, err := tagBlockSize(tags)
	if err != nil {
		return nil, err
	}

	size, err := tsMetricFrameSize(len(metricBytes), tagsSize)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, size)
	off := 0

	buf[off] = OpTSDeleteSeries
	off++

	if err := putUint16Len(buf[off:], len(metricBytes), "metric"); err != nil {
		return nil, err
	}
	off += 2
	copy(buf[off:], metricBytes)
	off += len(metricBytes)

	binary.LittleEndian.PutUint64(buf[off:], uint64(startNs))
	off += 8

	binary.LittleEndian.PutUint64(buf[off:], uint64(endNs))
	off += 8

	if err := putUint16Len(buf[off:], len(tags), "tag count"); err != nil {
		return nil, err
	}
	off += 2

	for k, v := range tags {
		if err := putUint16Len(buf[off:], len(k), "tag key"); err != nil {
			return nil, err
		}
		off += 2
		copy(buf[off:], k)
		off += len(k)

		if err := putUint16Len(buf[off:], len(v), "tag value"); err != nil {
			return nil, err
		}
		off += 2
		copy(buf[off:], v)
		off += len(v)
	}

	return buf, nil
}

// DecodeTSDeleteSeries decodes a Time-Series DeleteSeries payload.
func DecodeTSDeleteSeries(payload []byte) (TSDeleteParams, error) {
	if len(payload) < 2 {
		return TSDeleteParams{}, &InvalidCommandError{Reason: "ts delete: payload too short"}
	}

	off := 0
	metricLen := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2
	if off+metricLen > len(payload) {
		return TSDeleteParams{}, &InvalidCommandError{Reason: "ts delete: metric length exceeds payload"}
	}
	metric := string(payload[off : off+metricLen])
	off += metricLen

	if off+18 > len(payload) {
		return TSDeleteParams{}, &InvalidCommandError{Reason: "ts delete: missing startNs/endNs/tagCount"}
	}

	startNs := int64(binary.LittleEndian.Uint64(payload[off:]))
	off += 8

	endNs := int64(binary.LittleEndian.Uint64(payload[off:]))
	off += 8

	tagCount := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2

	tags := make(map[string]string, tagCount)
	for i := 0; i < tagCount; i++ {
		if off+2 > len(payload) {
			return TSDeleteParams{}, &InvalidCommandError{Reason: "ts delete: truncated tag key length"}
		}
		kLen := int(binary.LittleEndian.Uint16(payload[off:]))
		off += 2
		if off+kLen > len(payload) {
			return TSDeleteParams{}, &InvalidCommandError{Reason: "ts delete: tag key exceeds payload"}
		}
		k := string(payload[off : off+kLen])
		off += kLen

		if off+2 > len(payload) {
			return TSDeleteParams{}, &InvalidCommandError{Reason: "ts delete: truncated tag value length"}
		}
		vLen := int(binary.LittleEndian.Uint16(payload[off:]))
		off += 2
		if off+vLen > len(payload) {
			return TSDeleteParams{}, &InvalidCommandError{Reason: "ts delete: tag value exceeds payload"}
		}
		v := string(payload[off : off+vLen])
		off += vLen

		tags[k] = v
	}

	return TSDeleteParams{
		Metric:  metric,
		StartNs: startNs,
		EndNs:   endNs,
		Tags:    tags,
	}, nil
}

// TSRetentionParams holds decoded parameters for a time-series retention policy.
type TSRetentionParams struct {
	Metric       string
	DurationNs   int64
	ResolutionNs int64
	Aggregation  byte
}

// EncodeTSSetRetention creates a binary Time-Series SetRetention command.
// Format: [op:1][metricLen:2 LE][metric][durationNs:8 LE][resolutionNs:8 LE][aggregation:1].
func EncodeTSSetRetention(metric string, durationNs, resolutionNs int64, aggregation byte) ([]byte, error) {
	metricBytes := []byte(metric)
	size := headerSize + 2 + len(metricBytes) + 8 + 8 + 1

	if err := checkUint16Len(len(metricBytes), "metric"); err != nil {
		return nil, err
	}
	buf := make([]byte, size)
	off := 0

	buf[off] = OpTSSetRetention
	off++

	if err := putUint16Len(buf[off:], len(metricBytes), "metric"); err != nil {
		return nil, err
	}
	off += 2
	copy(buf[off:], metricBytes)
	off += len(metricBytes)

	binary.LittleEndian.PutUint64(buf[off:], uint64(durationNs))
	off += 8

	binary.LittleEndian.PutUint64(buf[off:], uint64(resolutionNs))
	off += 8

	buf[off] = aggregation

	return buf, nil
}

// DecodeTSSetRetention decodes a Time-Series SetRetention payload.
func DecodeTSSetRetention(payload []byte) (TSRetentionParams, error) {
	if len(payload) < 2 {
		return TSRetentionParams{}, &InvalidCommandError{Reason: "ts set retention: payload too short"}
	}

	off := 0
	metricLen := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2
	if off+metricLen > len(payload) {
		return TSRetentionParams{}, &InvalidCommandError{Reason: "ts set retention: metric length exceeds payload"}
	}
	metric := string(payload[off : off+metricLen])
	off += metricLen

	if off+17 > len(payload) {
		return TSRetentionParams{}, &InvalidCommandError{Reason: "ts set retention: missing duration/resolution/aggregation"}
	}

	durationNs := int64(binary.LittleEndian.Uint64(payload[off:]))
	off += 8

	resolutionNs := int64(binary.LittleEndian.Uint64(payload[off:]))
	off += 8

	aggregation := payload[off]

	return TSRetentionParams{
		Metric:       metric,
		DurationNs:   durationNs,
		ResolutionNs: resolutionNs,
		Aggregation:  aggregation,
	}, nil
}
