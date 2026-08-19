// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kvstore

import "errors"

// ErrIndexUnsupported is returned by RegisterEntityIndexes, QueryIndex,
// ScanIndex, QueryCompoundIndex and ScanCompoundIndex on a store that cannot
// maintain secondary indexes.
//
// It exists because the alternative — returning (nil, nil) — is the worst
// answer a query can give. An empty result with a nil error is
// indistinguishable from "the index is maintained and nothing matched", so a
// caller reading its own work queue concludes there is no work, forever, and
// no failure counter anywhere moves. Every store that cannot index MUST
// return this instead, and a caller that receives it either falls back to a
// full scan or fails closed. Silence is not an option the contract offers.
//
// Callers test it with errors.Is, never by string:
//
//	if errors.Is(err, kvstore.ErrIndexUnsupported) { ... }
//
// A DAO built over such a store still answers correctly: pkg/dao probes the
// capability at construction and plans full scans instead of index lookups,
// so query results are right and only the access path is slower.
var ErrIndexUnsupported = errors.New("kvstore: store does not maintain secondary indexes")

// ErrAtomicWriteUnsupported is returned by PutIfAbsent when the store cannot
// perform a write whose outcome is defined under concurrency.
//
// A store reports this instead of degrading to Exists-then-Put. The degraded
// form is the exact TOCTOU that AtomicKVStore exists to eliminate, and it
// delivers it silently: the structural type assertion still succeeds, the
// call still returns (true, nil), and two callers racing to claim the same
// single-use token both win. Refusing is recoverable; a false success is not.
var ErrAtomicWriteUnsupported = errors.New("kvstore: store does not support atomic conditional writes")

// ErrInvalidScanBound is the cause carried by an
// index.InvalidNumericBoundError when a range-scan bound cannot be encoded
// into the index's value space.
//
// It is an error rather than an empty range on purpose: an unparseable field
// VALUE means "this entry has no representable value" and is skipped, while
// an unparseable scan BOUND is a malformed query. Reporting one as an
// empty-but-valid result set is exactly the silent-empty answer the index
// contract forbids.
var ErrInvalidScanBound = errors.New("kvstore: scan bound cannot be encoded for this index")

// UnindexableValueError is returned when a write lands under a key prefix
// that a registered index covers, but the value cannot be decoded to extract
// the indexed field.
//
// It refuses the write rather than storing it unindexed. Storing it would
// report a successful write AND successful index maintenance while the record
// stayed invisible to every index query and outside every unique constraint —
// so a duplicate would be accepted and a lookup would answer "no records",
// both with nil errors. That is the fail-open shape this package exists to
// eliminate, and it is reachable in practice: a DAO configured with a binary
// codec (MsgPack, CBOR) writes values this store cannot read.
type UnindexableValueError struct {
	Key       string
	IndexName string
}

// Error returns a human-readable description.
func (e *UnindexableValueError) Error() string {
	return "kvstore: value at " + e.Key + " cannot be decoded to maintain index " +
		e.IndexName + "; this store indexes JSON-encoded values only"
}

// ConflictingIndexError is returned when an index name is re-registered with
// a definition that differs from the one already in effect.
//
// Registration is idempotent for an identical definition. It is NOT a no-op
// for a conflicting one: silently keeping the first would give the second
// caller an index whose sparse, unique or case-sensitivity behaviour is not
// what it asked for, and it would find out as wrong query results rather than
// as an error.
type ConflictingIndexError struct {
	IndexName string
}

// Error returns a human-readable description of the conflict.
func (e *ConflictingIndexError) Error() string {
	return "kvstore: index " + e.IndexName + " is already registered with a different definition"
}

// SealedError is returned when the KV store is sealed and cannot
// process requests.
type SealedError struct{}

// Error returns a human-readable description indicating the store is sealed.
func (e *SealedError) Error() string {
	return "kv store is sealed"
}

// PutError wraps a put operation error with the target key.
type PutError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the put failure.
func (e *PutError) Error() string {
	return "kvstore put " + e.Key + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *PutError) Unwrap() error {
	return e.Err
}

// GetError wraps a get operation error with the target key.
type GetError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the get failure.
func (e *GetError) Error() string {
	return "kvstore get " + e.Key + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *GetError) Unwrap() error {
	return e.Err
}

// DeleteError wraps a delete operation error with the target key.
type DeleteError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the delete failure.
func (e *DeleteError) Error() string {
	return "kvstore delete " + e.Key + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *DeleteError) Unwrap() error {
	return e.Err
}

// ScanError wraps a scan operation error with the prefix.
type ScanError struct {
	Prefix string
	Err    error
}

// Error returns a human-readable description of the scan failure.
func (e *ScanError) Error() string {
	return "kvstore scan " + e.Prefix + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ScanError) Unwrap() error {
	return e.Err
}

// ListError wraps a list operation error with the prefix.
type ListError struct {
	Prefix string
	Err    error
}

// Error returns a human-readable description of the list failure.
func (e *ListError) Error() string {
	return "kvstore list " + e.Prefix + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ListError) Unwrap() error {
	return e.Err
}

// ExistsError wraps an exists operation error with the target key.
type ExistsError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the exists failure.
func (e *ExistsError) Error() string {
	return "kvstore exists " + e.Key + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ExistsError) Unwrap() error {
	return e.Err
}

// IndexQueryError wraps an index query operation error.
type IndexQueryError struct {
	IndexName string
	Reason    string
}

// Error returns a human-readable description of the index query failure.
func (e *IndexQueryError) Error() string {
	return "kvstore index query " + e.IndexName + ": " + e.Reason
}

// IndexScanError wraps an index scan operation error.
type IndexScanError struct {
	IndexName string
	Reason    string
}

// Error returns a human-readable description of the index scan failure.
func (e *IndexScanError) Error() string {
	return "kvstore index scan " + e.IndexName + ": " + e.Reason
}

// IndexRegistrationError wraps an index registration error.
type IndexRegistrationError struct {
	EntityType string
	Err        error
}

// Error returns a human-readable description of the registration failure.
func (e *IndexRegistrationError) Error() string {
	return "kvstore index registration " + e.EntityType + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *IndexRegistrationError) Unwrap() error {
	return e.Err
}
