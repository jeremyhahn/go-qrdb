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

package protocol

import (
	"encoding/binary"
	"encoding/json"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// vlog.NotCompiledError is an absent capability, not a fault. The assertion is
// here because pkg/vlog cannot import this package.
var _ CapabilityError = (*vlog.NotCompiledError)(nil)

// ---------------------------------------------------------------------------
// Verifiable log op code
// ---------------------------------------------------------------------------
//
// OpVerifiableLog carries every mutation a named verifiable log accepts as
// sub-types of ONE opcode behind ONE cluster feature gate
// (featuregate.VerifiableLog), framed exactly as OpAuditAppend is. pkg/vlog's
// Chain applies it, assigning each entry's index and hashing the tree at
// apply time so every replica computes the identical log -- see
// docs/decisions/verifiable-log.md, sections 2 and 4.
//
// Payload (the op byte stripped): [subtype:1][tenantIDLen:2 BE][tenantID][body].
// Every body opens with the log's name within the tenant,
// [databaseLen:2 BE][database][logLen:2 BE][log], followed by the
// sub-type's fields:
//
//	Create:       [descLen:4 BE][descriptor JSON][key record]
//	Append:       [count:4 BE] then count x [entryLen:4 BE][entry]
//	Checkpoint:   [size:8 BE][rootLen:2 BE][root][signed checkpoint bytes]
//	ExpireBodies: [cutoff:8 BE]
//	RotateKey:    [keyLen:4 BE][vlog.HeadKey JSON][key record]
//
// A key record is one presence byte: keyRecordAbsent ends the body, and
// keyRecordPresent is followed by [keyVersion:4 BE][PKCS#8 key record] to the
// end of the body. A body of any other shape is refused. Create and RotateKey
// are behind featuregate.VerifiableLogKeys as well; this is the only Create
// encoding, and no release proposes another.

const (
	// OpVerifiableLog is the single opcode behind which every named
	// verifiable log mutation travels.
	OpVerifiableLog byte = 82

	// VerifiableLogSubtypeCreate registers a new log and its descriptor.
	VerifiableLogSubtypeCreate byte = 1

	// VerifiableLogSubtypeAppend appends one entry or a bounded batch.
	VerifiableLogSubtypeAppend byte = 2

	// VerifiableLogSubtypeCheckpoint records a signed checkpoint for a tree
	// size no larger than the log's.
	VerifiableLogSubtypeCheckpoint byte = 3

	// VerifiableLogSubtypeExpireBodies deletes entry bodies below a cutoff
	// index on a log whose descriptor allows it.
	VerifiableLogSubtypeExpireBodies byte = 4

	// VerifiableLogSubtypeRotateKey makes a new key the log's current
	// head-signing key.
	VerifiableLogSubtypeRotateKey byte = 5

	// VerifiableLogSubtypeAuditAppend appends audit events to the tenant's
	// audit instance, the log system/audit. Only the replicated audit
	// logger proposes it, once featuregate.VerifiableLogAudit is enabled.
	VerifiableLogSubtypeAuditAppend byte = 6
)

// The presence byte of a Create or RotateKey body's key record.
const (
	keyRecordAbsent  byte = 0
	keyRecordPresent byte = 1
)

// EncodeVerifiableLogCreate creates an OpVerifiableLog/Create command
// registering the log id with desc.
func EncodeVerifiableLogCreate(id vlog.LogID, desc vlog.Descriptor) ([]byte, error) {
	return encodeVerifiableLogCreate(id, desc, nil)
}

// EncodeVerifiableLogCreateWithKey encodes the creation of a log with its
// head-signing key, so the key and the log are written by one proposal. The
// body is the length-prefixed descriptor followed by the key's version and its
// PKCS#8 bytes.
func EncodeVerifiableLogCreateWithKey(id vlog.LogID, desc vlog.Descriptor, key vlog.SigningKey) ([]byte, error) {
	if key.Version == 0 || len(key.PKCS8) == 0 {
		return nil, &InvalidCommandError{Reason: "verifiable log create: empty signing key"}
	}
	return encodeVerifiableLogCreate(id, desc, &key)
}

func encodeVerifiableLogCreate(id vlog.LogID, desc vlog.Descriptor, key *vlog.SigningKey) ([]byte, error) {
	return encodeVerifiableLogKeyed(VerifiableLogSubtypeCreate, "verifiable log create: descriptor", id, desc, key)
}

// EncodeVerifiableLogRotateKey creates an OpVerifiableLog/RotateKey command
// making next the log's current head-signing key. A non-nil record is the
// software provider's k/<version> record, written by the same proposal; a
// provider that keeps its key outside go-qrdb passes nil.
func EncodeVerifiableLogRotateKey(id vlog.LogID, next vlog.HeadKey, record *vlog.SigningKey) ([]byte, error) {
	if record != nil && (record.Version == 0 || len(record.PKCS8) == 0) {
		return nil, &InvalidCommandError{Reason: "verifiable log rotate key: empty signing key"}
	}
	return encodeVerifiableLogKeyed(VerifiableLogSubtypeRotateKey, "verifiable log rotate key: key", id, next, record)
}

// encodeVerifiableLogKeyed frames a sub-type whose fields are a
// length-prefixed JSON value and, when key is not nil, the key's version and
// PKCS#8 bytes to the end of the body.
func encodeVerifiableLogKeyed(subtype byte, what string, id vlog.LogID, v any, key *vlog.SigningKey) ([]byte, error) {
	value, err := json.Marshal(v)
	if err != nil {
		return nil, &InvalidCommandError{Reason: what + ": " + err.Error()}
	}
	keySize := 0
	if key != nil {
		keySize = 4 + len(key.PKCS8)
	}
	fieldsSize, err := addSizes(4, len(value), 1, keySize)
	if err != nil {
		return nil, err
	}
	return encodeVerifiableLog(subtype, id, fieldsSize, func(buf []byte) []byte {
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(value)))
		buf = append(buf, value...)
		if key == nil {
			return append(buf, keyRecordAbsent)
		}
		buf = append(buf, keyRecordPresent)
		buf = binary.BigEndian.AppendUint32(buf, key.Version)
		return append(buf, key.PKCS8...)
	})
}

// EncodeVerifiableLogAppend creates an OpVerifiableLog/Append command
// appending entries, in order, to the log id. It refuses an empty batch or
// one larger than vlog.MaxAppendPerCommand, as apply would.
func EncodeVerifiableLogAppend(id vlog.LogID, entries [][]byte) ([]byte, error) {
	return encodeVerifiableLogEntries(VerifiableLogSubtypeAppend, id, entries)
}

// EncodeVerifiableLogAuditAppend encodes an append of audit events to the
// tenant's audit instance, the log system/audit
// (docs/databases/architecture.md §7). Its body is an Append body. Only the
// replicated audit logger proposes it: the public Append surface refuses
// the audit name, so no client can forge an audit event.
func EncodeVerifiableLogAuditAppend(id vlog.LogID, entries [][]byte) ([]byte, error) {
	return encodeVerifiableLogEntries(VerifiableLogSubtypeAuditAppend, id, entries)
}

func encodeVerifiableLogEntries(subtype byte, id vlog.LogID, entries [][]byte) ([]byte, error) {
	if len(entries) == 0 || len(entries) > vlog.MaxAppendPerCommand {
		return nil, &vlog.AppendBatchError{Count: len(entries)}
	}
	fieldsSize, err := framedSlicesSize(entries)
	if err != nil {
		return nil, err
	}
	return encodeVerifiableLog(subtype, id, fieldsSize, func(buf []byte) []byte {
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(entries)))
		for _, e := range entries {
			buf = binary.BigEndian.AppendUint32(buf, uint32(len(e)))
			buf = append(buf, e...)
		}
		return buf
	})
}

// EncodeVerifiableLogCheckpoint creates an OpVerifiableLog/Checkpoint
// command recording signed as the checkpoint of the log id at size, whose
// tree root is root.
func EncodeVerifiableLogCheckpoint(id vlog.LogID, size uint64, root, signed []byte) ([]byte, error) {
	if err := checkUint16Len(len(root), "checkpoint root"); err != nil {
		return nil, err
	}
	fieldsSize, err := addSizes(8, 2, len(root), len(signed))
	if err != nil {
		return nil, err
	}
	return encodeVerifiableLog(VerifiableLogSubtypeCheckpoint, id, fieldsSize, func(buf []byte) []byte {
		buf = binary.BigEndian.AppendUint64(buf, size)
		buf = binary.BigEndian.AppendUint16(buf, uint16(len(root)))
		buf = append(buf, root...)
		return append(buf, signed...)
	})
}

// EncodeVerifiableLogExpireBodies creates an OpVerifiableLog/ExpireBodies
// command deleting the bodies of every entry of the log id below cutoff.
func EncodeVerifiableLogExpireBodies(id vlog.LogID, cutoff uint64) ([]byte, error) {
	return encodeVerifiableLog(VerifiableLogSubtypeExpireBodies, id, 8, func(buf []byte) []byte {
		return binary.BigEndian.AppendUint64(buf, cutoff)
	})
}

// encodeVerifiableLog frames one OpVerifiableLog command: the op byte, the
// sub-type, the tenant id, the log's name, then fieldsSize bytes appended by
// fields.
func encodeVerifiableLog(subtype byte, id vlog.LogID, fieldsSize int, fields func([]byte) []byte) ([]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	for _, f := range []struct{ name, value string }{
		{"verifiable log tenant id", id.TenantID},
		{"verifiable log database", id.Database},
		{"verifiable log name", id.Log},
	} {
		if err := checkUint16Len(len(f.value), f.name); err != nil {
			return nil, err
		}
	}
	size, err := addSizes(headerSize, 1, 2, len(id.TenantID), 2, len(id.Database), 2, len(id.Log), fieldsSize)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 0, size)
	buf = append(buf, OpVerifiableLog, subtype)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(id.TenantID)))
	buf = append(buf, id.TenantID...)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(id.Database)))
	buf = append(buf, id.Database...)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(id.Log)))
	buf = append(buf, id.Log...)
	return fields(buf), nil
}

// DecodeVerifiableLogCommand splits an OpVerifiableLog payload (the op byte
// already stripped) into its sub-type, tenant id and body, exactly as
// DecodeAuditCommand does. The caller decodes the body with the sub-type's
// DecodeVerifiableLog* function.
func DecodeVerifiableLogCommand(payload []byte) (subtype byte, tenantID string, body []byte, err error) {
	if len(payload) < 3 {
		return 0, "", nil, &InvalidCommandError{Reason: "verifiable log command: payload too short"}
	}
	subtype = payload[0]
	tlen := int(binary.BigEndian.Uint16(payload[1:3]))
	if len(payload) < 3+tlen {
		return 0, "", nil, &InvalidCommandError{Reason: "verifiable log command: tenant id truncated"}
	}
	return subtype, string(payload[3 : 3+tlen]), payload[3+tlen:], nil
}

// DecodeVerifiableLogCreate decodes a Create body of tenantID's command.
func DecodeVerifiableLogCreate(tenantID string, body []byte) (vlog.LogID, vlog.Descriptor, *vlog.SigningKey, error) {
	var desc vlog.Descriptor
	id, key, err := decodeVerifiableLogKeyed(tenantID, body, "verifiable log create: descriptor", &desc)
	if err != nil {
		return vlog.LogID{}, vlog.Descriptor{}, nil, err
	}
	return id, desc, key, nil
}

// DecodeVerifiableLogRotateKey decodes a RotateKey body of tenantID's
// command. The record, when present, aliases body.
func DecodeVerifiableLogRotateKey(tenantID string, body []byte) (vlog.LogID, vlog.HeadKey, *vlog.SigningKey, error) {
	var next vlog.HeadKey
	id, record, err := decodeVerifiableLogKeyed(tenantID, body, "verifiable log rotate key: key", &next)
	if err != nil {
		return vlog.LogID{}, vlog.HeadKey{}, nil, err
	}
	return id, next, record, nil
}

// decodeVerifiableLogKeyed decodes the body encodeVerifiableLogKeyed frames:
// the log's name, the JSON value into v and the optional key record.
func decodeVerifiableLogKeyed(tenantID string, body []byte, what string, v any) (vlog.LogID, *vlog.SigningKey, error) {
	id, rest, err := decodeLogName(tenantID, body)
	if err != nil {
		return vlog.LogID{}, nil, err
	}
	if len(rest) < 4 || uint64(len(rest)-4) < uint64(binary.BigEndian.Uint32(rest)) {
		return vlog.LogID{}, nil, &InvalidCommandError{Reason: what + " truncated"}
	}
	vlen := int(binary.BigEndian.Uint32(rest))
	value, rest := rest[4:4+vlen], rest[4+vlen:]
	if err := json.Unmarshal(value, v); err != nil {
		return vlog.LogID{}, nil, &InvalidCommandError{Reason: what + " malformed: " + err.Error()}
	}
	if len(rest) == 0 {
		return vlog.LogID{}, nil, &InvalidCommandError{Reason: what + ": key record truncated"}
	}
	presence, rest := rest[0], rest[1:]
	switch {
	case presence == keyRecordAbsent && len(rest) == 0:
		return id, nil, nil
	case presence == keyRecordAbsent:
		return vlog.LogID{}, nil, &InvalidCommandError{Reason: what + ": trailing bytes"}
	case presence != keyRecordPresent:
		return vlog.LogID{}, nil, &InvalidCommandError{Reason: what + ": key record presence " + strconv.Itoa(int(presence))}
	case len(rest) <= 4:
		return vlog.LogID{}, nil, &InvalidCommandError{Reason: what + ": signing key truncated"}
	}
	return id, &vlog.SigningKey{Version: binary.BigEndian.Uint32(rest), PKCS8: rest[4:len(rest):len(rest)]}, nil
}

// DecodeVerifiableLogAppend decodes an Append body of tenantID's command.
// The entries alias body.
func DecodeVerifiableLogAppend(tenantID string, body []byte) (vlog.LogID, [][]byte, error) {
	id, rest, err := decodeLogName(tenantID, body)
	if err != nil {
		return vlog.LogID{}, nil, err
	}
	if len(rest) < 4 {
		return vlog.LogID{}, nil, &InvalidCommandError{Reason: "verifiable log append: count truncated"}
	}
	count := binary.BigEndian.Uint32(rest)
	rest = rest[4:]
	// Every entry costs at least its 4-byte length, so a count the body
	// cannot hold is refused before it sizes an allocation.
	if uint64(count) > uint64(len(rest))/4 {
		return vlog.LogID{}, nil, &InvalidCommandError{Reason: "verifiable log append: entries truncated"}
	}
	entries := make([][]byte, 0, count)
	for range count {
		if len(rest) < 4 {
			return vlog.LogID{}, nil, &InvalidCommandError{Reason: "verifiable log append: entries truncated"}
		}
		elen := binary.BigEndian.Uint32(rest)
		rest = rest[4:]
		if uint64(elen) > uint64(len(rest)) {
			return vlog.LogID{}, nil, &InvalidCommandError{Reason: "verifiable log append: entries truncated"}
		}
		entries = append(entries, rest[:elen:elen])
		rest = rest[elen:]
	}
	if len(rest) != 0 {
		return vlog.LogID{}, nil, &InvalidCommandError{Reason: "verifiable log append: trailing bytes"}
	}
	return id, entries, nil
}

// DecodeVerifiableLogAuditAppend decodes an AuditAppend body, which is
// framed as an Append body.
func DecodeVerifiableLogAuditAppend(tenantID string, body []byte) (vlog.LogID, [][]byte, error) {
	return DecodeVerifiableLogAppend(tenantID, body)
}

// DecodeVerifiableLogCheckpoint decodes a Checkpoint body of tenantID's
// command. root and signed alias body.
func DecodeVerifiableLogCheckpoint(tenantID string, body []byte) (id vlog.LogID, size uint64, root, signed []byte, err error) {
	id, rest, err := decodeLogName(tenantID, body)
	if err != nil {
		return vlog.LogID{}, 0, nil, nil, err
	}
	if len(rest) < 10 {
		return vlog.LogID{}, 0, nil, nil, &InvalidCommandError{Reason: "verifiable log checkpoint: size truncated"}
	}
	size = binary.BigEndian.Uint64(rest)
	rlen := int(binary.BigEndian.Uint16(rest[8:10]))
	rest = rest[10:]
	if len(rest) < rlen {
		return vlog.LogID{}, 0, nil, nil, &InvalidCommandError{Reason: "verifiable log checkpoint: root truncated"}
	}
	return id, size, rest[:rlen:rlen], rest[rlen:], nil
}

// DecodeVerifiableLogExpireBodies decodes an ExpireBodies body of
// tenantID's command.
func DecodeVerifiableLogExpireBodies(tenantID string, body []byte) (vlog.LogID, uint64, error) {
	id, rest, err := decodeLogName(tenantID, body)
	if err != nil {
		return vlog.LogID{}, 0, err
	}
	if len(rest) != 8 {
		return vlog.LogID{}, 0, &InvalidCommandError{Reason: "verifiable log expire bodies: cutoff malformed"}
	}
	return id, binary.BigEndian.Uint64(rest), nil
}

// decodeLogName reads the database and log names every body opens with and
// returns the rest of the body.
func decodeLogName(tenantID string, body []byte) (vlog.LogID, []byte, error) {
	database, rest, ok := cutUint16Prefixed(body)
	if !ok {
		return vlog.LogID{}, nil, &InvalidCommandError{Reason: "verifiable log command: database truncated"}
	}
	log, rest, ok := cutUint16Prefixed(rest)
	if !ok {
		return vlog.LogID{}, nil, &InvalidCommandError{Reason: "verifiable log command: log name truncated"}
	}
	return vlog.LogID{TenantID: tenantID, Database: string(database), Log: string(log)}, rest, nil
}

// cutUint16Prefixed splits b into the field its 2-byte big-endian length
// prefix names and the bytes after it.
func cutUint16Prefixed(b []byte) (field, rest []byte, ok bool) {
	if len(b) < 2 {
		return nil, nil, false
	}
	n := int(binary.BigEndian.Uint16(b))
	if len(b) < 2+n {
		return nil, nil, false
	}
	return b[2 : 2+n], b[2+n:], true
}
