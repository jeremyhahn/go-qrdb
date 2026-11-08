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

import "strconv"

// The errors in this file belong to the replicated Chain (table.go,
// log.go), the cluster-wide hash chain. They are named and shaped after
// pkg/lease's own table errors (TableWriteError, TableReadError,
// CorruptTableError) rather than pkg/audit's file-sink errors (WriteError,
// FileError, ChainBrokenError, ...), which describe the node-local NDJSON
// sink and stay exactly as they are: the two chains are different
// durability mechanisms now, and giving the replicated one its own names
// keeps the two from drifting into one meaning two things.

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

// InvalidTenantIDError is returned when a tenant ID named in an Append,
// Prune or Query call contains '/'. The raw table keys every tenant's
// records by concatenating TablePrefix, the tenant ID and a '/'-delimited
// suffix (table.go's EventKey/headKey); a tenant ID containing '/' would let
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

// The errors below belong to the named logs. Every one except a table fault
// is a deterministic consequence of the committed command and the log's
// replicated state, so a state machine answers it as a refusal; a
// TableWriteError, as above, halts apply.

// InvalidLogNameError is returned when a tenant, database or log name would
// not address exactly one log: it contains '/', which would carve a segment
// inside another log's key range (the defect InvalidTenantIDError closes for
// the audit table), or a database or log name is empty.
type InvalidLogNameError struct {
	// Field is "tenant", "database" or "log".
	Field string

	// Name is the offending value.
	Name string
}

func (e *InvalidLogNameError) Error() string {
	return "vlog: invalid " + e.Field + " name " + strconv.Quote(e.Name)
}

// InvalidDescriptorError is returned when Create is given a descriptor it
// cannot store: an origin that cannot be a checkpoint's first line, or a
// hash other than HashSHA256.
type InvalidDescriptorError struct {
	Reason string
}

func (e *InvalidDescriptorError) Error() string {
	return "vlog: invalid descriptor: " + e.Reason
}

// ReservedLogError is returned when Create, CreateWithKey or AppendEntries
// names a tenant's audit instance (AuditLogID): the instance is created by
// its first audit event and appended to only through AppendAudit, so every
// leaf is an audit event linked into the linear chain.
type ReservedLogError struct {
	ID LogID
}

func (e *ReservedLogError) Error() string {
	return "vlog: log " + e.ID.String() + " is the audit instance; only audit events append to it"
}

// LogExistsError is returned when Create names a log that already exists.
type LogExistsError struct {
	ID LogID
}

func (e *LogExistsError) Error() string {
	return "vlog: log " + e.ID.String() + " already exists"
}

// LogNotFoundError is returned when an operation names a log that was never
// created.
type LogNotFoundError struct {
	ID LogID
}

func (e *LogNotFoundError) Error() string {
	return "vlog: log " + e.ID.String() + " not found"
}

// AppendBatchError is returned when an append carries no entries or more
// than MaxAppendPerCommand.
type AppendBatchError struct {
	Count int
}

func (e *AppendBatchError) Error() string {
	return "vlog: append batch of " + strconv.Itoa(e.Count) + " entries, want 1 to " + strconv.Itoa(MaxAppendPerCommand)
}

// SizeOutOfRangeError is returned when a checkpoint or a proof names a tree
// size larger than the log's, or a consistency proof's first size exceeds
// its second.
type SizeOutOfRangeError struct {
	// Size is the size asked for.
	Size uint64

	// Limit is the largest size it may be.
	Limit uint64
}

func (e *SizeOutOfRangeError) Error() string {
	return "vlog: tree size " + strconv.FormatUint(e.Size, 10) + " exceeds " + strconv.FormatUint(e.Limit, 10)
}

// IndexOutOfRangeError is returned when an inclusion proof or a read names an
// index at or past the tree size it is asked against.
type IndexOutOfRangeError struct {
	Index uint64
	Size  uint64
}

func (e *IndexOutOfRangeError) Error() string {
	return "vlog: index " + strconv.FormatUint(e.Index, 10) + " is out of range for tree size " + strconv.FormatUint(e.Size, 10)
}

// CheckpointRootMismatchError is returned when a checkpoint's root is not
// the tree's root at the checkpoint's size.
type CheckpointRootMismatchError struct {
	Size uint64
}

func (e *CheckpointRootMismatchError) Error() string {
	return "vlog: checkpoint root does not match the tree at size " + strconv.FormatUint(e.Size, 10)
}

// CheckpointExistsError is returned when a checkpoint is proposed for a size
// that already has one: checkpoint records are append-only.
type CheckpointExistsError struct {
	Size uint64
}

func (e *CheckpointExistsError) Error() string {
	return "vlog: a checkpoint for tree size " + strconv.FormatUint(e.Size, 10) + " already exists"
}

// EmptyCheckpointError is returned when a checkpoint carries no signed
// bytes: the record is the signed checkpoint, and there is nothing to store.
type EmptyCheckpointError struct {
	Size uint64
}

func (e *EmptyCheckpointError) Error() string {
	return "vlog: empty checkpoint for tree size " + strconv.FormatUint(e.Size, 10)
}

// ExpireNotAllowedError is returned when ExpireBodies names a log whose
// descriptor does not allow it.
type ExpireNotAllowedError struct {
	ID LogID
}

func (e *ExpireNotAllowedError) Error() string {
	return "vlog: log " + e.ID.String() + " does not allow expiring entry bodies"
}

// UnsupportedKeyTypeError is returned for a head-signing key, or a signed
// head, of a type other than Ed25519 or ECDSA P-256 with SHA-256.
type UnsupportedKeyTypeError struct {
	Type string
}

func (e *UnsupportedKeyTypeError) Error() string {
	return "vlog: unsupported head-signing key type " + strconv.Quote(e.Type)
}

// InvalidSigningKeyError is returned for a head-signing key record or key
// reference that does not decode.
type InvalidSigningKeyError struct {
	Reason string
}

func (e *InvalidSigningKeyError) Error() string {
	return "vlog: invalid head-signing key: " + e.Reason
}

// KeyNotFoundError is returned when a log has no signing key at a version.
type KeyNotFoundError struct {
	ID      LogID
	Version uint32
}

func (e *KeyNotFoundError) Error() string {
	return "vlog: log " + e.ID.String() + " has no signing key version " + strconv.FormatUint(uint64(e.Version), 10)
}

// KeyVersionConflictError is returned when a key rotation names a version
// other than the one after the log's current key: another rotation applied
// first.
type KeyVersionConflictError struct {
	ID LogID

	// Version is the version the rotation named.
	Version uint32

	// Want is the version the next key must have.
	Want uint32
}

func (e *KeyVersionConflictError) Error() string {
	return "vlog: log " + e.ID.String() + " key rotation to version " + strconv.FormatUint(uint64(e.Version), 10) +
		", want version " + strconv.FormatUint(uint64(e.Want), 10)
}

// HeadSignatureError is returned when a signed head does not decode, or its
// signature does not verify.
type HeadSignatureError struct {
	Reason string
}

func (e *HeadSignatureError) Error() string {
	return "vlog: signed head: " + e.Reason
}

// NotCompiledError reports that head signing was not built into this binary:
// the software HeadSigner a build without qrdb_vlog stubs out refuses with it.
// Its Capability method makes it a protocol.CapabilityError, an absent
// capability rather than a fault; pkg/protocol imports this package, so the
// compile-time assertion is in pkg/protocol/vlog.go.
type NotCompiledError struct{}

// Error returns the refusal, naming the build tag that enables head signing.
func (e *NotCompiledError) Error() string {
	return "vlog: head signing not compiled: build with -tags qrdb_vlog"
}

// Capability names the absent subsystem, declaring this an application-level
// refusal rather than a fault. See protocol.CapabilityError.
func (e *NotCompiledError) Capability() string {
	return "vlog"
}
