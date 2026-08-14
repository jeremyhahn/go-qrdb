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
	// MVCC operations (10-19)

	// OpMVCCPut stores a key-value pair with MVCC revision tracking.
	// Payload: [keyLen:2 LE][key][leaseID:8 LE][value]
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
	// Payload: [keyLen:2 LE][key]
	OpMVCCDelete byte = 11

	// OpMVCCRange queries a range of keys in the MVCC store.
	// Payload: [startLen:2 LE][start][endLen:2 LE][end][revision:8 LE][limit:8 LE][flags:1]
	OpMVCCRange byte = 12

	// OpMVCCCompact compacts revisions up to the specified revision.
	// Payload: [revision:8 LE]
	OpMVCCCompact byte = 13

	// Transaction operations (20-29)

	// OpTransaction executes an atomic compare-and-swap transaction.
	// Payload: [compareCount:2 LE][compares...][successCount:2 LE][successOps...][failureCount:2 LE][failureOps...]
	OpTransaction byte = 20

	// Batch operations (30-39)

	// OpBatch executes multiple operations atomically.
	// Payload: [opCount:2 LE][ops...]
	OpBatch byte = 30

	// Lease operations (40-49)

	// OpLeaseGrant creates a new lease.
	// Payload: [ttl:8 LE]
	OpLeaseGrant byte = 40

	// OpLeaseRevoke expires a lease and deletes attached keys.
	// Payload: [leaseID:8 LE]
	OpLeaseRevoke byte = 41

	// OpLeaseKeepAlive resets a lease's TTL.
	// Payload: [leaseID:8 LE]
	OpLeaseKeepAlive byte = 42

	// Index operations (50-59)

	// OpIndexCreate creates a new secondary index.
	// Payload: [nameLen:2 LE][name][fieldLen:2 LE][field][flags:1]
	OpIndexCreate byte = 50

	// OpIndexDrop removes a secondary index.
	// Payload: [nameLen:2 LE][name]
	OpIndexDrop byte = 51

	// Time-series operations (60-69)

	// OpTSWrite stores a single time-series data point.
	// Payload: [metricLen:2 LE][metric][timestamp:8 LE][value:8 LE float64][tagCount:2 LE][tags...]
	OpTSWrite byte = 60

	// OpTSWriteBatch stores multiple time-series data points.
	// Payload: [pointCount:2 LE][points...]
	OpTSWriteBatch byte = 61

	// OpTSWritePoint stores a single validated time-series data point.
	// Payload: [metricLen:2 LE][metric][timestamp:8 LE][value:8 LE float64][tagCount:2 LE][tags...]
	OpTSWritePoint byte = 62

	// OpTSDeleteSeries deletes time-series data matching the criteria.
	// Payload: [metricLen:2 LE][metric][startNs:8 LE][endNs:8 LE][tagCount:2 LE][tags...]
	OpTSDeleteSeries byte = 63

	// OpTSSetRetention sets the retention policy for a metric.
	// Payload: [metricLen:2 LE][metric][durationNs:8 LE][resolutionNs:8 LE][aggregation:1]
	OpTSSetRetention byte = 64
)

// Range flags (bit mask for OpMVCCRange)
const (
	// RangeFlagCountOnly returns only the count without values.
	RangeFlagCountOnly byte = 1 << 0

	// RangeFlagKeysOnly returns keys without their values.
	RangeFlagKeysOnly byte = 1 << 1
)

// Index flags (bit mask for OpIndexCreate)
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
// Format: [op:1][keyLen:2 LE][key][leaseID:8 LE][value]
func EncodeMVCCPut(key, value []byte, leaseID int64) []byte {
	buf := make([]byte, headerSize+2+len(key)+8+len(value))
	buf[0] = OpMVCCPut
	binary.LittleEndian.PutUint16(buf[1:3], uint16(len(key)))
	copy(buf[3:], key)
	binary.LittleEndian.PutUint64(buf[3+len(key):], uint64(leaseID))
	copy(buf[3+len(key)+8:], value)
	return buf
}

// EncodeMVCCPutIfAbsent creates a binary MVCC conditional-put command.
// Format: [op:1][keyLen:2 LE][key][leaseID:8 LE][value] — identical to
// EncodeMVCCPut apart from the opcode, so the decoder needs no new path.
func EncodeMVCCPutIfAbsent(key, value []byte, leaseID int64) []byte {
	buf := make([]byte, headerSize+2+len(key)+8+len(value))
	buf[0] = OpMVCCPutIfAbsent
	binary.LittleEndian.PutUint16(buf[1:3], uint16(len(key)))
	copy(buf[3:], key)
	binary.LittleEndian.PutUint64(buf[3+len(key):], uint64(leaseID))
	copy(buf[3+len(key)+8:], value)
	return buf
}

// EncodeMVCCDelete creates a binary MVCC Delete command.
// Format: [op:1][keyLen:2 LE][key]
func EncodeMVCCDelete(key []byte) []byte {
	buf := make([]byte, headerSize+2+len(key))
	buf[0] = OpMVCCDelete
	binary.LittleEndian.PutUint16(buf[1:3], uint16(len(key)))
	copy(buf[3:], key)
	return buf
}

// EncodeMVCCRange creates a binary MVCC Range command.
// Format: [op:1][startLen:2 LE][start][endLen:2 LE][end][revision:8 LE][limit:8 LE][flags:1]
func EncodeMVCCRange(start, end []byte, revision, limit int64, countOnly, keysOnly bool) []byte {
	buf := make([]byte, headerSize+2+len(start)+2+len(end)+8+8+1)
	off := 0
	buf[off] = OpMVCCRange
	off++

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(start)))
	off += 2
	copy(buf[off:], start)
	off += len(start)

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(end)))
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

	return buf
}

// EncodeMVCCCompact creates a binary MVCC Compact command.
// Format: [op:1][revision:8 LE]
func EncodeMVCCCompact(revision int64) []byte {
	buf := make([]byte, headerSize+8)
	buf[0] = OpMVCCCompact
	binary.LittleEndian.PutUint64(buf[1:], uint64(revision))
	return buf
}

// ---------------------------------------------------------------------------
// Lease encoders
// ---------------------------------------------------------------------------

// EncodeLeaseGrant creates a binary Lease Grant command.
// Format: [op:1][ttl:8 LE]
func EncodeLeaseGrant(ttl int64) []byte {
	buf := make([]byte, headerSize+8)
	buf[0] = OpLeaseGrant
	binary.LittleEndian.PutUint64(buf[1:], uint64(ttl))
	return buf
}

// EncodeLeaseRevoke creates a binary Lease Revoke command.
// Format: [op:1][leaseID:8 LE]
func EncodeLeaseRevoke(leaseID int64) []byte {
	buf := make([]byte, headerSize+8)
	buf[0] = OpLeaseRevoke
	binary.LittleEndian.PutUint64(buf[1:], uint64(leaseID))
	return buf
}

// EncodeLeaseKeepAlive creates a binary Lease KeepAlive command.
// Format: [op:1][leaseID:8 LE]
func EncodeLeaseKeepAlive(leaseID int64) []byte {
	buf := make([]byte, headerSize+8)
	buf[0] = OpLeaseKeepAlive
	binary.LittleEndian.PutUint64(buf[1:], uint64(leaseID))
	return buf
}

// ---------------------------------------------------------------------------
// Index encoders
// ---------------------------------------------------------------------------

// EncodeIndexCreate creates a binary Index Create command.
// Format: [op:1][nameLen:2 LE][name][fieldLen:2 LE][field][flags:1]
func EncodeIndexCreate(name, fieldName string, unique, sparse, numeric bool) []byte {
	nameBytes := []byte(name)
	fieldBytes := []byte(fieldName)
	buf := make([]byte, headerSize+2+len(nameBytes)+2+len(fieldBytes)+1)
	off := 0

	buf[off] = OpIndexCreate
	off++

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(nameBytes)))
	off += 2
	copy(buf[off:], nameBytes)
	off += len(nameBytes)

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(fieldBytes)))
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

	return buf
}

// EncodeIndexDrop creates a binary Index Drop command.
// Format: [op:1][nameLen:2 LE][name]
func EncodeIndexDrop(name string) []byte {
	nameBytes := []byte(name)
	buf := make([]byte, headerSize+2+len(nameBytes))
	buf[0] = OpIndexDrop
	binary.LittleEndian.PutUint16(buf[1:3], uint16(len(nameBytes)))
	copy(buf[3:], nameBytes)
	return buf
}

// ---------------------------------------------------------------------------
// Time-series encoders
// ---------------------------------------------------------------------------

// EncodeTSWrite creates a binary Time-Series Write command.
// Format: [op:1][metricLen:2 LE][metric][timestamp:8 LE][value:8 LE float64][tagCount:2 LE][tags...]
// Each tag: [keyLen:2 LE][key][valLen:2 LE][val]
func EncodeTSWrite(metric string, timestamp int64, value float64, tags map[string]string) []byte {
	metricBytes := []byte(metric)

	// Calculate total size.
	size := headerSize + 2 + len(metricBytes) + 8 + 8 + 2
	for k, v := range tags {
		size += 2 + len(k) + 2 + len(v)
	}

	buf := make([]byte, size)
	off := 0

	buf[off] = OpTSWrite
	off++

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(metricBytes)))
	off += 2
	copy(buf[off:], metricBytes)
	off += len(metricBytes)

	binary.LittleEndian.PutUint64(buf[off:], uint64(timestamp))
	off += 8

	binary.LittleEndian.PutUint64(buf[off:], math.Float64bits(value))
	off += 8

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(tags)))
	off += 2

	for k, v := range tags {
		binary.LittleEndian.PutUint16(buf[off:], uint16(len(k)))
		off += 2
		copy(buf[off:], k)
		off += len(k)

		binary.LittleEndian.PutUint16(buf[off:], uint16(len(v)))
		off += 2
		copy(buf[off:], v)
		off += len(v)
	}

	return buf
}

// EncodeTSWriteBatch creates a binary Time-Series WriteBatch command.
// Format: [op:1][pointCount:2 LE][points...]
// Each point is encoded as: [metricLen:2 LE][metric][timestamp:8 LE][value:8 LE float64][tagCount:2 LE][tags...]
func EncodeTSWriteBatch(points []TSWriteParams) []byte {
	// Calculate total size.
	size := headerSize + 2
	for _, p := range points {
		metricBytes := []byte(p.Metric)
		pointSize := 2 + len(metricBytes) + 8 + 8 + 2
		for k, v := range p.Tags {
			pointSize += 2 + len(k) + 2 + len(v)
		}
		size += 2 + pointSize // 2 for point length prefix
	}

	buf := make([]byte, size)
	off := 0

	buf[off] = OpTSWriteBatch
	off++

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(points)))
	off += 2

	for _, p := range points {
		metricBytes := []byte(p.Metric)
		pointSize := 2 + len(metricBytes) + 8 + 8 + 2
		for k, v := range p.Tags {
			pointSize += 2 + len(k) + 2 + len(v)
		}

		binary.LittleEndian.PutUint16(buf[off:], uint16(pointSize))
		off += 2

		binary.LittleEndian.PutUint16(buf[off:], uint16(len(metricBytes)))
		off += 2
		copy(buf[off:], metricBytes)
		off += len(metricBytes)

		binary.LittleEndian.PutUint64(buf[off:], uint64(p.Timestamp))
		off += 8

		binary.LittleEndian.PutUint64(buf[off:], math.Float64bits(p.Value))
		off += 8

		binary.LittleEndian.PutUint16(buf[off:], uint16(len(p.Tags)))
		off += 2

		for k, v := range p.Tags {
			binary.LittleEndian.PutUint16(buf[off:], uint16(len(k)))
			off += 2
			copy(buf[off:], k)
			off += len(k)

			binary.LittleEndian.PutUint16(buf[off:], uint16(len(v)))
			off += 2
			copy(buf[off:], v)
			off += len(v)
		}
	}

	return buf
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
// Format: [opType:1][keyLen:4 LE][key][valLen:4 LE][value][rangeEndLen:4 LE][rangeEnd][leaseID:8 LE]
func EncodeOp(opType byte, key, value, rangeEnd []byte, leaseID int64) []byte {
	size := 1 + frameSize + len(key) + frameSize + len(value) + frameSize + len(rangeEnd) + 8
	buf := make([]byte, size)
	off := 0

	buf[off] = opType
	off++

	binary.LittleEndian.PutUint32(buf[off:], uint32(len(key)))
	off += frameSize
	copy(buf[off:], key)
	off += len(key)

	binary.LittleEndian.PutUint32(buf[off:], uint32(len(value)))
	off += frameSize
	copy(buf[off:], value)
	off += len(value)

	binary.LittleEndian.PutUint32(buf[off:], uint32(len(rangeEnd)))
	off += frameSize
	copy(buf[off:], rangeEnd)
	off += len(rangeEnd)

	binary.LittleEndian.PutUint64(buf[off:], uint64(leaseID))

	return buf
}

// EncodeCompare encodes a single compare for transaction payloads.
// Format: [keyLen:4 LE][key][target:1][result:1][valLen:4 LE][value][version:8 LE][createRev:8 LE][modRev:8 LE]
func EncodeCompare(key []byte, target, result byte, value []byte, version, createRevision, modRevision int64) []byte {
	size := frameSize + len(key) + 1 + 1 + frameSize + len(value) + 8 + 8 + 8
	buf := make([]byte, size)
	off := 0

	binary.LittleEndian.PutUint32(buf[off:], uint32(len(key)))
	off += frameSize
	copy(buf[off:], key)
	off += len(key)

	buf[off] = target
	off++
	buf[off] = result
	off++

	binary.LittleEndian.PutUint32(buf[off:], uint32(len(value)))
	off += frameSize
	copy(buf[off:], value)
	off += len(value)

	binary.LittleEndian.PutUint64(buf[off:], uint64(version))
	off += 8
	binary.LittleEndian.PutUint64(buf[off:], uint64(createRevision))
	off += 8
	binary.LittleEndian.PutUint64(buf[off:], uint64(modRevision))

	return buf
}

// EncodeTransaction creates a binary Transaction command.
// Format: [op:1][compareCount:4 LE][compares...][successCount:4 LE][successOps...][failureCount:4 LE][failureOps...]
func EncodeTransaction(compares, successOps, failureOps [][]byte) []byte {
	size := headerSize + frameSize + frameSize + frameSize
	for _, c := range compares {
		size += frameSize + len(c)
	}
	for _, op := range successOps {
		size += frameSize + len(op)
	}
	for _, op := range failureOps {
		size += frameSize + len(op)
	}

	buf := make([]byte, size)
	off := 0

	buf[off] = OpTransaction
	off++

	off += encodeFramedSlices(buf[off:], compares)
	off += encodeFramedSlices(buf[off:], successOps)
	encodeFramedSlices(buf[off:], failureOps)

	return buf
}

// EncodeBatch creates a binary Batch command.
// Format: [op:1][opCount:4 LE][ops...]
func EncodeBatch(ops [][]byte) []byte {
	size := headerSize + frameSize
	for _, op := range ops {
		size += frameSize + len(op)
	}

	buf := make([]byte, size)
	off := 0

	buf[off] = OpBatch
	off++

	encodeFramedSlices(buf[off:], ops)

	return buf
}

// encodeFramedSlices writes a count-prefixed run of length-prefixed byte
// slices into buf and returns the number of bytes written. Both the count and
// each element length are frameSize-byte little-endian values. buf must be
// large enough to hold the whole run.
func encodeFramedSlices(buf []byte, items [][]byte) int {
	off := 0
	binary.LittleEndian.PutUint32(buf[off:], uint32(len(items)))
	off += frameSize
	for _, item := range items {
		binary.LittleEndian.PutUint32(buf[off:], uint32(len(item)))
		off += frameSize
		copy(buf[off:], item)
		off += len(item)
	}
	return off
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
	keyLen := int(binary.LittleEndian.Uint32(data[off:]))
	off += frameSize
	if keyLen < 0 || off+keyLen > len(data) {
		return DecodedOp{}, 0, &InvalidCommandError{Reason: "op: key exceeds data"}
	}
	key := data[off : off+keyLen]
	off += keyLen

	if off+frameSize > len(data) {
		return DecodedOp{}, 0, &InvalidCommandError{Reason: "op: missing value length"}
	}
	valLen := int(binary.LittleEndian.Uint32(data[off:]))
	off += frameSize
	if valLen < 0 || off+valLen > len(data) {
		return DecodedOp{}, 0, &InvalidCommandError{Reason: "op: value exceeds data"}
	}
	value := data[off : off+valLen]
	off += valLen

	if off+frameSize > len(data) {
		return DecodedOp{}, 0, &InvalidCommandError{Reason: "op: missing range end length"}
	}
	reLen := int(binary.LittleEndian.Uint32(data[off:]))
	off += frameSize
	if reLen < 0 || off+reLen > len(data) {
		return DecodedOp{}, 0, &InvalidCommandError{Reason: "op: range end exceeds data"}
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
	keyLen := int(binary.LittleEndian.Uint32(data[off:]))
	off += frameSize
	if keyLen < 0 || off+keyLen > len(data) {
		return DecodedCompare{}, 0, &InvalidCommandError{Reason: "compare: key exceeds data"}
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
	valLen := int(binary.LittleEndian.Uint32(data[off:]))
	off += frameSize
	if valLen < 0 || off+valLen > len(data) {
		return DecodedCompare{}, 0, &InvalidCommandError{Reason: "compare: value exceeds data"}
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
	compareCount := int(binary.LittleEndian.Uint32(payload[off:]))
	off += frameSize

	compares := make([]DecodedCompare, 0, preallocSize(compareCount))
	for i := 0; i < compareCount; i++ {
		if off+frameSize > len(payload) {
			return DecodedTransaction{}, &InvalidCommandError{Reason: "txn: truncated compare length"}
		}
		cLen := int(binary.LittleEndian.Uint32(payload[off:]))
		off += frameSize
		if cLen < 0 || off+cLen > len(payload) {
			return DecodedTransaction{}, &InvalidCommandError{Reason: "txn: compare exceeds payload"}
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
		count := int(binary.LittleEndian.Uint32(payload[off:]))
		off += frameSize
		ops := make([]DecodedOp, 0, preallocSize(count))
		for i := 0; i < count; i++ {
			if off+frameSize > len(payload) {
				return nil, &InvalidCommandError{Reason: "txn: truncated " + label + " op length"}
			}
			oLen := int(binary.LittleEndian.Uint32(payload[off:]))
			off += frameSize
			if oLen < 0 || off+oLen > len(payload) {
				return nil, &InvalidCommandError{Reason: "txn: " + label + " op exceeds payload"}
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
	opCount := int(binary.LittleEndian.Uint32(payload[off:]))
	off += frameSize

	ops := make([]DecodedOp, 0, preallocSize(opCount))
	for i := 0; i < opCount; i++ {
		if off+frameSize > len(payload) {
			return nil, &InvalidCommandError{Reason: "batch: truncated op length"}
		}
		oLen := int(binary.LittleEndian.Uint32(payload[off:]))
		off += frameSize
		if oLen < 0 || off+oLen > len(payload) {
			return nil, &InvalidCommandError{Reason: "batch: op exceeds payload"}
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
func EncodeTSWritePoint(metric string, timestamp int64, value float64, tags map[string]string) []byte {
	buf := EncodeTSWrite(metric, timestamp, value, tags)
	buf[0] = OpTSWritePoint
	return buf
}

// TSDeleteParams holds decoded parameters for a time-series delete.
type TSDeleteParams struct {
	Metric  string
	StartNs int64
	EndNs   int64
	Tags    map[string]string
}

// EncodeTSDeleteSeries creates a binary Time-Series DeleteSeries command.
// Format: [op:1][metricLen:2 LE][metric][startNs:8 LE][endNs:8 LE][tagCount:2 LE][tags...]
func EncodeTSDeleteSeries(metric string, startNs, endNs int64, tags map[string]string) []byte {
	metricBytes := []byte(metric)

	size := headerSize + 2 + len(metricBytes) + 8 + 8 + 2
	for k, v := range tags {
		size += 2 + len(k) + 2 + len(v)
	}

	buf := make([]byte, size)
	off := 0

	buf[off] = OpTSDeleteSeries
	off++

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(metricBytes)))
	off += 2
	copy(buf[off:], metricBytes)
	off += len(metricBytes)

	binary.LittleEndian.PutUint64(buf[off:], uint64(startNs))
	off += 8

	binary.LittleEndian.PutUint64(buf[off:], uint64(endNs))
	off += 8

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(tags)))
	off += 2

	for k, v := range tags {
		binary.LittleEndian.PutUint16(buf[off:], uint16(len(k)))
		off += 2
		copy(buf[off:], k)
		off += len(k)

		binary.LittleEndian.PutUint16(buf[off:], uint16(len(v)))
		off += 2
		copy(buf[off:], v)
		off += len(v)
	}

	return buf
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
// Format: [op:1][metricLen:2 LE][metric][durationNs:8 LE][resolutionNs:8 LE][aggregation:1]
func EncodeTSSetRetention(metric string, durationNs, resolutionNs int64, aggregation byte) []byte {
	metricBytes := []byte(metric)
	size := headerSize + 2 + len(metricBytes) + 8 + 8 + 1

	buf := make([]byte, size)
	off := 0

	buf[off] = OpTSSetRetention
	off++

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(metricBytes)))
	off += 2
	copy(buf[off:], metricBytes)
	off += len(metricBytes)

	binary.LittleEndian.PutUint64(buf[off:], uint64(durationNs))
	off += 8

	binary.LittleEndian.PutUint64(buf[off:], uint64(resolutionNs))
	off += 8

	buf[off] = aggregation

	return buf
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
