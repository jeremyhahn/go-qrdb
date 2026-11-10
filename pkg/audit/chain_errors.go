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

package audit

import "strconv"

// The errors in this file belong to the replicated Chain's audit half
// (chain.go): event validation, encoding and the Query cursor. The table's
// own errors -- TableWriteError, TableReadError, CorruptTableError and
// InvalidTenantIDError -- live beside the table in pkg/vlog. Both sets stay
// apart from this package's file-sink errors (WriteError, FileError,
// ChainBrokenError, ...) in errors.go, which describe the node-local NDJSON
// sink: the two chains are different durability mechanisms, and giving the
// replicated one its own names keeps the two from drifting into one meaning
// two things.

// EncodeEventError is returned when an AuditEvent could not be marshaled for
// the chain. The event is attacker-shaped only through Attributes, which
// callers must not populate with values json.Marshal refuses (e.g. a channel
// or a cyclic structure reached through "any"); this is a defensive path, not
// one normal callers should ever hit.
type EncodeEventError struct {
	Err error
}

func (e *EncodeEventError) Error() string {
	return "audit event encode failed: " + e.Err.Error()
}

// Unwrap returns the underlying encoding error.
func (e *EncodeEventError) Unwrap() error { return e.Err }

// MissingTimestampError is returned by Chain.Append when the event carries a
// zero Timestamp. The replicated chain cannot default it the way the
// node-local file sink defaults a zero timestamp to time.Now(): Append runs
// identically on every replica of the group, and a value invented at apply
// time would differ from one replica to the next for the same committed
// entry. The caller proposing the command must set Timestamp before
// encoding it.
// MalformedEventError is returned when an entry of an audit-instance append
// does not unmarshal as an AuditEvent: a deterministic consequence of the
// committed entry's own bytes, refused identically on every replica.
type MalformedEventError struct {
	Err error
}

func (e *MalformedEventError) Error() string {
	return "audit: malformed event: " + e.Err.Error()
}

func (e *MalformedEventError) Unwrap() error {
	return e.Err
}

type MissingTimestampError struct{}

func (e *MissingTimestampError) Error() string {
	return "audit: event has no timestamp; the proposer must set one before encoding, not the chain at apply time"
}

// InvalidCursorError is returned when a Query's Filter.Cursor does not decode
// to a sequence number. A cursor is opaque to callers and is only ever
// supposed to be one this package minted, so this is a defensive path: a
// forged or corrupted cursor, not an ordinary input-validation case.
type InvalidCursorError struct {
	Cursor string
}

func (e *InvalidCursorError) Error() string {
	return "audit query: invalid cursor: " + strconv.Quote(e.Cursor)
}

// TenantMismatchError is returned by Chain.Append (and, at encode time, by
// protocol.EncodeAuditAppend) when ev.TenantID is set and disagrees with the
// tenant the command is filed under. An event is routed by a tenantID
// argument separate from ev.TenantID specifically so a platform event can be
// filed under the system tenant's table segment while carrying NO tenant of
// its own (ev.TenantID == ""), which Query's Untenanted matching depends on
// -- so this refuses a genuine conflict rather than silently overwriting
// ev.TenantID ("stamping") in every case, which would make every platform
// event indistinguishable from an ordinary system-tenant one.
type TenantMismatchError struct {
	// EventTenantID is the tenant the event itself claims.
	EventTenantID string
	// RoutingTenantID is the tenant the command is filed under.
	RoutingTenantID string
}

func (e *TenantMismatchError) Error() string {
	return "audit: event claims tenant " + strconv.Quote(e.EventTenantID) +
		" but is filed under " + strconv.Quote(e.RoutingTenantID)
}
