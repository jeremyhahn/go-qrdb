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

// The errors in this file belong to the replicated Chain (table.go,
// chain.go), the cluster-wide hash chain. They are named and shaped after
// pkg/lease's own table errors (TableWriteError, TableReadError,
// CorruptTableError) rather than this package's existing file-sink errors
// (WriteError, FileError, ChainBrokenError, ...) in errors.go, which describe
// the node-local NDJSON sink and stay exactly as they are: the two chains are
// different durability mechanisms now, and giving the replicated one its own
// names keeps the two from drifting into one meaning two things.

// TableWriteError is returned when the replicated audit table could not be
// staged into the apply batch. It is a storage fault, not a refusal: the
// state machine must surface it as an apply failure, never as a per-command
// answer, because a fault local to one replica decided as a refusal would let
// that replica diverge from peers whose write succeeded. Mirrors
// lease.TableWriteError for exactly the same reason.
type TableWriteError struct {
	Err error
}

func (e *TableWriteError) Error() string {
	return "audit table write failed: " + e.Err.Error()
}

// Unwrap returns the underlying storage error.
func (e *TableWriteError) Unwrap() error { return e.Err }

// TableReadError is returned when the replicated audit table could not be
// read back from storage while restoring the Chain or answering a Query.
type TableReadError struct {
	Err error
}

func (e *TableReadError) Error() string {
	return "audit table read failed: " + e.Err.Error()
}

// Unwrap returns the underlying storage error.
func (e *TableReadError) Unwrap() error { return e.Err }

// CorruptTableError is returned when a persisted replicated-audit-table
// record does not decode, or contradicts the rest of the table.
type CorruptTableError struct {
	// Key is the offending record's raw key.
	Key string

	// Reason says what is wrong with it.
	Reason string
}

func (e *CorruptTableError) Error() string {
	return "audit table corrupt at " + strconv.Quote(e.Key) + ": " + e.Reason
}

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

// InvalidTenantIDError is returned when a tenant ID named in an Append,
// Prune or Query call contains '/'. The raw table keys every tenant's
// records by concatenating TablePrefix, the tenant ID and a '/'-delimited
// suffix (table.go's eventKey/headKey); a tenant ID containing '/' would let
// a crafted id carve a second segment inside another tenant's range (e.g.
// "acme/evil" sits under "acme"'s own prefix), the exact class of confinement
// defect docs/multi-tenancy/architecture.md's "no exempt prefix" rule exists
// to close for the ordinary keyspace.
type InvalidTenantIDError struct {
	TenantID string
}

func (e *InvalidTenantIDError) Error() string {
	return "audit: tenant id " + strconv.Quote(e.TenantID) + " must not contain '/'"
}
