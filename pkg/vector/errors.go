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

package vector

import (
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// InvalidRecordError reports that a stored namespace, vector, or
// mutation-log record could not be decoded: the bytes read back are not what
// this package's encoders produce.
type InvalidRecordError struct {
	Record string
	Reason string
}

func (e *InvalidRecordError) Error() string {
	return "vector: invalid " + e.Record + " record: " + e.Reason
}

// UnsupportedRecordVersionError reports that a stored record's format
// version is not one this build can decode.
type UnsupportedRecordVersionError struct {
	Record  string
	Version byte
}

func (e *UnsupportedRecordVersionError) Error() string {
	return "vector: " + e.Record + " record format version " + strconv.Itoa(int(e.Version)) + " is not supported"
}

// NamespaceExistsError reports that a namespace create named a namespace
// that already exists. Namespace geometry is immutable
// (docs/vectors/engineering.md "Invariants"), so a create is never merged
// into an existing namespace's definition.
type NamespaceExistsError struct {
	Namespace string
}

func (e *NamespaceExistsError) Error() string {
	return "vector: namespace " + strconv.Quote(e.Namespace) + " already exists"
}

// Refusal declares this a deterministic refusal of the committed command,
// not a fault: every replica reaches it identically from the same log
// position, so consensus advances with this result instead of halting (see
// protocol.RefusalError).
func (e *NamespaceExistsError) Refusal() string { return "namespace already exists" }

// NamespaceNotFoundError reports that a command named a namespace this
// database has no record of. A namespace is never auto-created
// (docs/vectors/engineering.md "Error semantics").
type NamespaceNotFoundError struct {
	Namespace string
}

func (e *NamespaceNotFoundError) Error() string {
	return "vector: namespace " + strconv.Quote(e.Namespace) + " not found"
}

// Refusal declares this a deterministic refusal; see NamespaceExistsError.Refusal.
func (e *NamespaceNotFoundError) Refusal() string { return "namespace not found" }

// InvalidNamespaceNameError reports that a command naming a namespace --
// create, delete, or any command that reads or writes an existing one --
// gave a name that is not a legal RFC 1123 DNS label (see
// ValidNamespaceName). Every vector apply handler validates the namespace
// name it was given before it reads the namespace record, so this error can
// surface from any of them, not only create and delete.
type InvalidNamespaceNameError struct {
	Namespace string
}

func (e *InvalidNamespaceNameError) Error() string {
	return "vector: namespace name " + strconv.Quote(e.Namespace) + " is not a valid namespace name"
}

// Refusal declares this a deterministic refusal; see NamespaceExistsError.Refusal.
func (e *InvalidNamespaceNameError) Refusal() string { return "invalid namespace name" }

// InvalidDistanceMetricError reports that a namespace create named a
// distance metric this build does not recognise.
type InvalidDistanceMetricError struct {
	Namespace string
	Metric    byte
}

func (e *InvalidDistanceMetricError) Error() string {
	return "vector: namespace " + strconv.Quote(e.Namespace) + ": unknown distance metric " + strconv.Itoa(int(e.Metric))
}

// Refusal declares this a deterministic refusal; see NamespaceExistsError.Refusal.
func (e *InvalidDistanceMetricError) Refusal() string { return "unknown distance metric" }

// InvalidIndexTypeError reports that a namespace create or an index
// reconfigure named an index type this build does not recognise.
type InvalidIndexTypeError struct {
	Namespace string
	IndexType byte
}

func (e *InvalidIndexTypeError) Error() string {
	return "vector: namespace " + strconv.Quote(e.Namespace) + ": unknown index type " + strconv.Itoa(int(e.IndexType))
}

// Refusal declares this a deterministic refusal; see NamespaceExistsError.Refusal.
func (e *InvalidIndexTypeError) Refusal() string { return "unknown index type" }

// InvalidDimensionError reports that a namespace create named a dimension of
// zero, which no embedding can satisfy.
type InvalidDimensionError struct {
	Namespace string
}

func (e *InvalidDimensionError) Error() string {
	return "vector: namespace " + strconv.Quote(e.Namespace) + ": dimension must be greater than zero"
}

// Refusal declares this a deterministic refusal; see NamespaceExistsError.Refusal.
func (e *InvalidDimensionError) Refusal() string { return "invalid dimension" }

// DimensionMismatchError reports that a vector's embedding length does not
// equal its namespace's fixed dimension. A mismatch fails rather than
// coercing: a silently padded or truncated embedding produces plausible but
// wrong neighbours (docs/vectors/engineering.md "Error semantics").
type DimensionMismatchError struct {
	Namespace string
	Got       int
	Want      uint32
}

func (e *DimensionMismatchError) Error() string {
	return "vector: namespace " + strconv.Quote(e.Namespace) + ": embedding has dimension " +
		strconv.Itoa(e.Got) + ", want " + strconv.FormatUint(uint64(e.Want), 10)
}

// Refusal declares this a deterministic refusal; see NamespaceExistsError.Refusal.
func (e *DimensionMismatchError) Refusal() string { return "embedding dimension mismatch" }

// ForgedKeyError reports that an ordinary user-facing key-value write named a
// key under the reserved vector namespace (KeyPrefix). Only this package's
// own apply handlers (pkg/engine/vector_handlers.go) may write a "__vec:"
// key; nothing else has a legitimate reason to, so every other write path
// refuses one outright rather than letting a forged record reach the store,
// where a later vector command would decode it as a corrupt namespace or
// vector record and HALT APPLY on every replica (go-qrdb vector-replicated
// review, B3) -- the internal-key discovery guard (pkg/service) stops the
// read side of this from ordinary enumeration, but promises nothing about
// Put/Delete, which this error closes for vector specifically.
type ForgedKeyError struct {
	Key []byte
}

func (e *ForgedKeyError) Error() string {
	return "vector: key " + strconv.Quote(string(e.Key)) + " is in the reserved " + KeyPrefix + " namespace"
}

// Refusal declares this a deterministic refusal; see NamespaceExistsError.Refusal.
func (e *ForgedKeyError) Refusal() string { return "key in reserved vector namespace" }

// IDOverflowError reports that an upsert named, or would have auto-allocated,
// an ID of math.MaxUint64, which the namespace's ID allocator cannot record
// as "next": NextID = ID + 1 wraps to 0, a reuse of the first ID this
// namespace ever hands out. Refused rather than silently wrapped
// (go-qrdb vector-replicated review, N1).
type IDOverflowError struct {
	Namespace string
}

func (e *IDOverflowError) Error() string {
	return "vector: namespace " + strconv.Quote(e.Namespace) +
		": vector ID math.MaxUint64 would overflow the namespace's ID allocator"
}

// Refusal declares this a deterministic refusal; see NamespaceExistsError.Refusal.
func (e *IDOverflowError) Refusal() string { return "id allocator would overflow" }

// DuplicateIDError reports that a batch delete named the same vector ID more
// than once. A batch command is refused outright rather than silently
// de-duplicated: processing the same ID twice in one apply -- delete,
// then delete again -- is the second occurrence finding the record its own
// first occurrence already removed, which is "vector not found" for a
// well-formed ID that was never missing, after the first occurrence has
// already staged its writes. Refusing before anything is staged is what
// atomicity requires (go-qrdb vector-replicated review, B2).
type DuplicateIDError struct {
	Namespace string
	ID        uint64
}

func (e *DuplicateIDError) Error() string {
	return "vector: namespace " + strconv.Quote(e.Namespace) + ": vector " +
		strconv.FormatUint(e.ID, 10) + " named more than once in one batch"
}

// Refusal declares this a deterministic refusal; see NamespaceExistsError.Refusal.
func (e *DuplicateIDError) Refusal() string { return "duplicate id in batch" }

// VectorNotFoundError reports that a get, delete, or metadata update named a
// vector ID this namespace does not hold.
type VectorNotFoundError struct {
	Namespace string
	ID        uint64
}

func (e *VectorNotFoundError) Error() string {
	return "vector: namespace " + strconv.Quote(e.Namespace) + ": vector " +
		strconv.FormatUint(e.ID, 10) + " not found"
}

// Refusal declares this a deterministic refusal; see NamespaceExistsError.Refusal.
func (e *VectorNotFoundError) Refusal() string { return "vector not found" }

// Compile-time interface checks: every refusal above declares itself to
// pkg/statemachine's Apply classifier (protocol.RefusalError) rather than
// being named in a list elsewhere, so a refusal returned as a plain Go error
// from pkg/engine's vector handlers is encoded as a result instead of
// halting consensus (see protocol.CapabilityError's doc comment, "Why an
// interface and not a list", which applies identically here).
var (
	_ protocol.RefusalError = (*NamespaceExistsError)(nil)
	_ protocol.RefusalError = (*NamespaceNotFoundError)(nil)
	_ protocol.RefusalError = (*InvalidNamespaceNameError)(nil)
	_ protocol.RefusalError = (*InvalidDistanceMetricError)(nil)
	_ protocol.RefusalError = (*InvalidIndexTypeError)(nil)
	_ protocol.RefusalError = (*InvalidDimensionError)(nil)
	_ protocol.RefusalError = (*DimensionMismatchError)(nil)
	_ protocol.RefusalError = (*VectorNotFoundError)(nil)
	_ protocol.RefusalError = (*DuplicateIDError)(nil)
	_ protocol.RefusalError = (*IDOverflowError)(nil)
	_ protocol.RefusalError = (*ForgedKeyError)(nil)
)

// IsForgedKey reports whether key lies in the reserved vector namespace, for
// a write path that accepts an arbitrary caller-supplied key. See
// ForgedKeyError.
func IsForgedKey(key []byte) bool {
	return len(key) >= len(KeyPrefix) && string(key[:len(KeyPrefix)]) == KeyPrefix
}

// RefuseForgedKeys returns a *ForgedKeyError for the first of keys that lies
// in the reserved vector namespace, or nil if none do. Call it BEFORE
// staging any write, so a user command naming several keys -- a batch, a
// transaction -- refuses the whole command rather than writing the keys
// that precede the forged one.
func RefuseForgedKeys(keys ...[]byte) error {
	for _, k := range keys {
		if IsForgedKey(k) {
			return &ForgedKeyError{Key: k}
		}
	}
	return nil
}
