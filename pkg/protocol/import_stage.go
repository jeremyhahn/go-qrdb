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

import "encoding/binary"

// ImportStageKind is what an OpImportStage command writes.
type ImportStageKind byte

const (
	// ImportStageSetMeta writes a session's metadata record. Body: the
	// session's metadata, JSON, non-empty.
	ImportStageSetMeta ImportStageKind = 1

	// ImportStageSetChunk writes one staged chunk. Body: the chunk's raw
	// bytes (may be empty for a zero-length chunk). Seq names which chunk.
	ImportStageSetChunk ImportStageKind = 2
)

// ImportStageCommand is a decoded OpImportStage command.
type ImportStageCommand struct {
	// Kind is what the command writes.
	Kind ImportStageKind
	// SessionID is the import session the record belongs to.
	SessionID string
	// LeaseID is the session's lease: the write binds its key to this
	// lease, exactly as any other lease-bound write does, so the record is
	// deleted when the lease is revoked or expires. 0 (lease.NoLease) is
	// refused by the handler -- a staged record with no lease would never
	// be reclaimed.
	LeaseID int64
	// Seq is an ImportStageSetChunk command's chunk sequence number.
	Seq uint64
	// Body is the record's value: metadata JSON or chunk bytes.
	Body []byte
	// MaxSessions, when non-zero, bounds the number of open sessions
	// TenantID already holds (pkg/importstage.TenantIndexKey): an
	// ImportStageSetMeta command is refused (AppErrorConflict) rather than
	// applied once that many already exist. The count and the write commit
	// at the SAME log position -- inside the same Update() call every
	// replica applies one at a time -- so two concurrent ImportBegin calls
	// can never both observe room for the last slot. Zero means no check
	// (every kind but a quota-checked ImportStageSetMeta).
	MaxSessions uint32
	// TenantID is the caller's tenant, on an ImportStageSetMeta command:
	// the write also stages a tenant-index entry for SessionID (deleted by
	// the same lease cascade as the metadata record), so the quota is
	// counted per tenant across every database that shares this store,
	// independent of whether SessionID itself carries a database
	// namespace. Required (even with MaxSessions == 0) on every
	// ImportStageSetMeta command; empty on ImportStageSetChunk.
	TenantID string
	// MetaBody, when non-empty on an ImportStageSetChunk command, is the
	// session's updated metadata (JSON): the chunk and the metadata that
	// now accounts for it are written in the SAME Update() call, exactly
	// as putRawBatch committed them as one Raft command before this
	// package existed. A crash between the two is not possible because
	// there is no between -- both land in the same ApplyAtomic set.
	MetaBody []byte
}

// EncodeImportStage encodes cmd as an OpImportStage command:
// [op:1][kind:1][sessionIdLen:2 LE][sessionId][leaseId:8 LE][seq:8 LE]
// [maxSessions:4 LE][tenantIdLen:2 LE][tenantId][metaBodyLen:4 LE]
// [metaBody][body].
func EncodeImportStage(cmd ImportStageCommand) ([]byte, error) {
	switch cmd.Kind {
	case ImportStageSetMeta, ImportStageSetChunk:
	default:
		return nil, &InvalidCommandError{Reason: "import stage: unknown kind"}
	}
	if cmd.SessionID == "" {
		return nil, &InvalidCommandError{Reason: "import stage: empty session id"}
	}
	if err := checkUint16Len(len(cmd.SessionID), "import session id"); err != nil {
		return nil, err
	}
	if err := checkUint16Len(len(cmd.TenantID), "import tenant id"); err != nil {
		return nil, err
	}
	buf := make([]byte, headerSize+1+2+len(cmd.SessionID)+8+8+4+2+len(cmd.TenantID)+4+len(cmd.MetaBody)+len(cmd.Body))
	buf[0] = OpImportStage
	buf[1] = byte(cmd.Kind)
	if err := putUint16Len(buf[2:4], len(cmd.SessionID), "import session id"); err != nil {
		return nil, err
	}
	off := 4
	copy(buf[off:], cmd.SessionID)
	off += len(cmd.SessionID)
	binary.LittleEndian.PutUint64(buf[off:off+8], uint64(cmd.LeaseID)) //nolint:gosec // G115: two's-complement round trip, decoded back to int64
	off += 8
	binary.LittleEndian.PutUint64(buf[off:off+8], cmd.Seq)
	off += 8
	binary.LittleEndian.PutUint32(buf[off:off+4], cmd.MaxSessions)
	off += 4
	if err := putUint16Len(buf[off:off+2], len(cmd.TenantID), "import tenant id"); err != nil {
		return nil, err
	}
	off += 2
	copy(buf[off:], cmd.TenantID)
	off += len(cmd.TenantID)
	//nolint:gosec // G115: metadata JSON is well under 2^31 bytes (bounded by ImportSessionConfig)
	binary.LittleEndian.PutUint32(buf[off:off+4], uint32(len(cmd.MetaBody)))
	off += 4
	copy(buf[off:], cmd.MetaBody)
	off += len(cmd.MetaBody)
	copy(buf[off:], cmd.Body)
	return buf, nil
}

// DecodeImportStage decodes an OpImportStage payload. A payload with an
// unknown kind, a session id, tenant id or meta-body length that overruns
// it, or that is too short for its fixed fields is refused.
func DecodeImportStage(payload []byte) (ImportStageCommand, error) {
	if len(payload) < 3 {
		return ImportStageCommand{}, &InvalidCommandError{Reason: "import stage: payload too short"}
	}
	kind := ImportStageKind(payload[0])
	switch kind {
	case ImportStageSetMeta, ImportStageSetChunk:
	default:
		return ImportStageCommand{}, &InvalidCommandError{Reason: "import stage: unknown kind"}
	}
	idLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	if idLen == 0 || 3+idLen+26 > len(payload) {
		return ImportStageCommand{}, &InvalidCommandError{Reason: "import stage: session id overruns payload"}
	}
	cmd := ImportStageCommand{Kind: kind, SessionID: string(payload[3 : 3+idLen])}
	off := 3 + idLen
	cmd.LeaseID = int64(binary.LittleEndian.Uint64(payload[off : off+8])) //nolint:gosec // G115: two's-complement round trip of the encoded int64
	off += 8
	cmd.Seq = binary.LittleEndian.Uint64(payload[off : off+8])
	off += 8
	cmd.MaxSessions = binary.LittleEndian.Uint32(payload[off : off+4])
	off += 4
	tenantLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if off+tenantLen+4 > len(payload) {
		return ImportStageCommand{}, &InvalidCommandError{Reason: "import stage: tenant id overruns payload"}
	}
	if tenantLen > 0 {
		cmd.TenantID = string(payload[off : off+tenantLen])
	}
	off += tenantLen
	metaLen := int(binary.LittleEndian.Uint32(payload[off : off+4]))
	off += 4
	if off+metaLen > len(payload) {
		return ImportStageCommand{}, &InvalidCommandError{Reason: "import stage: meta body overruns payload"}
	}
	if metaLen > 0 {
		cmd.MetaBody = append([]byte(nil), payload[off:off+metaLen]...)
	}
	off += metaLen
	if len(payload) > off {
		cmd.Body = append([]byte(nil), payload[off:]...)
	}
	return cmd, nil
}
