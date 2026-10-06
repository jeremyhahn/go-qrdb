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

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
)

// ---------------------------------------------------------------------------
// Replicated audit op code
// ---------------------------------------------------------------------------
//
// OpAuditAppend carries both mutations the replicated audit hash chain
// accepts -- appending one event, and pruning a retired prefix -- as
// sub-types of ONE opcode, per the owner's ruling that retention needs a
// second deterministic replicated write and that it ride the same opcode and
// the same cluster feature gate (featuregate.AuditAppend) rather than mint a
// second one. Neither sub-type is decoded here: this package only frames the
// command. pkg/audit's Chain.Append/Chain.Prune apply it, assigning the
// sequence number and hash digest at apply time so every replica computes the
// identical chain -- see
// docs/audit/architecture.md#the-hash-chain-stays-linear-under-raft-because-it-is-assigned-at-apply-time.

const (
	// OpAuditAppend is the single opcode behind which every replicated audit
	// table mutation travels. Payload: [subtype:1][tenantIDLen:2 BE][tenantID][body].
	OpAuditAppend byte = 79

	// AuditSubtypeAppend's body is the caller's marshaled AuditEvent, with no
	// PrevHash or zero-Timestamp assumption: Chain.Append overwrites both
	// deterministically.
	AuditSubtypeAppend byte = 1

	// AuditSubtypePrune's body is an 8-byte big-endian cutoff sequence
	// number: every event of the named tenant below it is deleted.
	AuditSubtypePrune byte = 2
)

// EncodeAuditAppend marshals ev and creates a binary
// OpAuditAppend/AuditSubtypeAppend command carrying tenantID.
//
// It refuses, before a command a replica would have to apply is ever built,
// exactly the two conditions pkg/audit.Chain.Append refuses again at apply
// time as a backstop: a zero ev.Timestamp -- Append runs identically on
// every replica and cannot default one without each replica folding a
// different value into its own chain digest -- and an ev.TenantID that
// disagrees with tenantID, the routing argument that decides which tenant's
// range the event is filed under (see audit.TenantMismatchError for why a
// conflict is refused rather than silently overwritten). Catching both here
// means a malformed proposal never reaches Raft at all, rather than being
// refused only after a round trip that every replica would have refused
// identically anyway.
func EncodeAuditAppend(tenantID string, ev audit.AuditEvent) ([]byte, error) {
	if len(tenantID) > 0xFFFF {
		return nil, &InvalidCommandError{Reason: "audit append: tenant id too long"}
	}
	if ev.Timestamp.IsZero() {
		return nil, &audit.MissingTimestampError{}
	}
	if ev.TenantID != "" && ev.TenantID != tenantID {
		return nil, &audit.TenantMismatchError{EventTenantID: ev.TenantID, RoutingTenantID: tenantID}
	}
	eventJSON, err := json.Marshal(ev)
	if err != nil {
		return nil, &audit.EncodeEventError{Err: err}
	}
	size, err := addSizes(headerSize, 1, 2, len(tenantID), len(eventJSON))
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 0, size)
	buf = append(buf, OpAuditAppend, AuditSubtypeAppend)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(tenantID)))
	buf = append(buf, tenantID...)
	buf = append(buf, eventJSON...)
	return buf, nil
}

// EncodeAuditPrune creates a binary OpAuditAppend/AuditSubtypePrune command:
// delete every event of tenantID below cutoff.
func EncodeAuditPrune(tenantID string, cutoff uint64) ([]byte, error) {
	if len(tenantID) > 0xFFFF {
		return nil, &InvalidCommandError{Reason: "audit prune: tenant id too long"}
	}
	size, err := addSizes(headerSize, 1, 2, len(tenantID), 8)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 0, size)
	buf = append(buf, OpAuditAppend, AuditSubtypePrune)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(tenantID)))
	buf = append(buf, tenantID...)
	buf = binary.BigEndian.AppendUint64(buf, cutoff)
	return buf, nil
}

// DecodeAuditCommand splits an OpAuditAppend payload (the op byte already
// stripped, exactly like DecodeLeaseGrant) into its sub-type, tenant id and
// body. The body is AuditSubtypeAppend's raw event JSON or
// AuditSubtypePrune's 8-byte cutoff, undecoded: the caller knows which from
// subtype.
func DecodeAuditCommand(payload []byte) (subtype byte, tenantID string, body []byte, err error) {
	if len(payload) < 3 {
		return 0, "", nil, &InvalidCommandError{Reason: "audit command: payload too short"}
	}
	subtype = payload[0]
	tlen := int(binary.BigEndian.Uint16(payload[1:3]))
	if len(payload) < 3+tlen {
		return 0, "", nil, &InvalidCommandError{Reason: "audit command: tenant id truncated"}
	}
	tenantID = string(payload[3 : 3+tlen])
	body = payload[3+tlen:]
	return subtype, tenantID, body, nil
}
