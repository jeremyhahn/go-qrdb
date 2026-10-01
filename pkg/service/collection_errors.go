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

package service

import "strconv"

// CollectionAlreadyExistsError is returned when attempting to create a
// collection that already exists.
type CollectionAlreadyExistsError struct {
	Database   string
	Collection string
}

// Error returns a human-readable description.
func (e *CollectionAlreadyExistsError) Error() string {
	return "service: collection already exists: " + e.Database + "/" + e.Collection
}

// CollectionNotFoundError is returned when an operation references a
// collection that does not exist.
type CollectionNotFoundError struct {
	Database   string
	Collection string
}

// Error returns a human-readable description.
func (e *CollectionNotFoundError) Error() string {
	return "service: collection not found: " + e.Database + "/" + e.Collection
}

// CollectionEmptyNameError is returned when a collection name is empty.
type CollectionEmptyNameError struct{}

// Error returns a human-readable description.
func (e *CollectionEmptyNameError) Error() string {
	return "service: collection name must not be empty"
}

// CollectionInvalidNameError is returned when a new collection's name cannot
// be used. A name containing '#' is refused: '#' separates a collection's
// name from its incarnation in every per-incarnation key
// (__col:{database}:{name}#{incarnation}/), so refusing it at creation makes a
// new collection's keys impossible to confuse with any other's by
// construction. Only CreateCollection checks it; a collection that already
// exists under such a name keeps working.
type CollectionInvalidNameError struct {
	Name   string
	Reason string
}

// Error returns a human-readable description.
func (e *CollectionInvalidNameError) Error() string {
	return "service: invalid collection name " + strconv.Quote(e.Name) + ": " + e.Reason
}

// CollectionReservedFieldError is returned when a document carries a field
// the stored layout cannot hold as sent. A stored document is a flat object
// whose "id" key is the document's identity, so a field named "id" would be
// silently replaced by it; and an object whose only other key is an
// object-valued "fields" is read as the legacy {"id", "fields"} wrapper, so
// such a document would be read back unwrapped. Either would be a write
// reported as successful that stored something other than what was sent. The
// document is refused before anything is stored; the identity is named with
// "_id", and a field can be renamed.
type CollectionReservedFieldError struct {
	Field  string
	Reason string
}

// Error returns a human-readable description.
func (e *CollectionReservedFieldError) Error() string {
	return "service: document field " + strconv.Quote(e.Field) + " cannot be stored: " + e.Reason
}

// CollectionEmptyDatabaseError is returned when a database name is empty
// in a collection operation.
type CollectionEmptyDatabaseError struct{}

// Error returns a human-readable description.
func (e *CollectionEmptyDatabaseError) Error() string {
	return "service: database name must not be empty for collection operation"
}

// CollectionInvalidIDStrategyError is returned when an unsupported ID
// strategy is specified.
type CollectionInvalidIDStrategyError struct {
	Strategy string
}

// Error returns a human-readable description.
func (e *CollectionInvalidIDStrategyError) Error() string {
	return "service: invalid collection ID strategy: " + e.Strategy
}

// CollectionMissingFieldHashError is returned when the field_hash ID
// strategy is specified but no field name is provided.
type CollectionMissingFieldHashError struct{}

// Error returns a human-readable description.
func (e *CollectionMissingFieldHashError) Error() string {
	return "service: field_hash ID strategy requires a field_hash field name"
}

// CollectionDocumentNotFoundError is returned when a document is not
// found within a collection.
type CollectionDocumentNotFoundError struct {
	Database   string
	Collection string
	ID         uint64
}

// Error returns a human-readable description.
func (e *CollectionDocumentNotFoundError) Error() string {
	return "service: document not found: " + e.Database + "/" +
		e.Collection + "/" + strconv.FormatUint(e.ID, 10)
}

// CollectionDocumentExistsError is returned when InsertDocument would have
// written over a document already stored under the ID the insert resolved
// to.
//
// It is the mirror of CollectionDocumentNotFoundError, which UpdateDocument
// and DeleteDocument return when the document is absent: insert requires
// absence exactly as update requires presence, and that pair is what keeps
// the two verbs distinguishable. Before this existed, an insert whose ID
// named a stored document overwrote it and reported success, so a caller
// could not tell a new document from a destroyed one (go-qrdb#447).
//
// Explicit records whether the ID came from the document's own "_id" field
// or from the collection's ID strategy, because the two are different
// faults: an explicit ID that is taken is a caller claiming an identity
// another document holds, while a GENERATED ID that is taken means the
// collection's own allocator produced a value already in use. The message
// says which.
type CollectionDocumentExistsError struct {
	Database   string
	Collection string
	ID         uint64
	Explicit   bool
}

// Error returns a human-readable description.
func (e *CollectionDocumentExistsError) Error() string {
	origin := "generated"
	if e.Explicit {
		origin = "explicit"
	}
	return "service: document already exists: " + e.Database + "/" +
		e.Collection + "/" + strconv.FormatUint(e.ID, 10) + " (" + origin + " id)"
}

// CollectionEmptyDocumentError is returned when an insert or update
// request contains an empty document.
type CollectionEmptyDocumentError struct{}

// Error returns a human-readable description.
func (e *CollectionEmptyDocumentError) Error() string {
	return "service: document must not be empty"
}

// CollectionInvalidDocumentIDError is returned when a document ID of 0
// is provided for an operation that requires a valid ID.
type CollectionInvalidDocumentIDError struct{}

// Error returns a human-readable description.
func (e *CollectionInvalidDocumentIDError) Error() string {
	return "service: document ID must not be zero"
}

// CollectionMetadataError is returned when a collection metadata
// operation fails.
type CollectionMetadataError struct {
	Op  string
	Err error
}

// Error returns a human-readable description.
func (e *CollectionMetadataError) Error() string {
	return "service: collection metadata " + e.Op + " failed: " + e.Err.Error()
}

// OutcomeUnknown reports whether the failure leaves a document write's
// outcome unknown: the write itself failed and may have committed, its
// post-write fence could not read the metadata back, or a fenced-out write
// could not be retracted. A caller must treat such a document as possibly
// stored -- a quota reservation is kept, a transport reports an internal
// error rather than the lifecycle refusal a failed retraction wraps. Every
// other operation fails before anything is stored.
func (e *CollectionMetadataError) OutcomeUnknown() bool {
	return collectionWriteOutcomeUnknownOps[e.Op]
}

// collectionWriteOutcomeUnknownOps are the CollectionMetadataError operations
// whose failure leaves a document write's outcome unknown.
var collectionWriteOutcomeUnknownOps = map[string]bool{
	"insert-document": true,
	"update-document": true,
	"upsert-document": true,
	"fence":           true,
	"retract-write":   true,
}

// Unwrap returns the underlying error.
func (e *CollectionMetadataError) Unwrap() error {
	return e.Err
}

// CollectionInvalidCodecError is returned when a storage codec is specified
// that is not registered in the current build.
type CollectionInvalidCodecError struct {
	Codec string
}

// Error returns a human-readable description.
func (e *CollectionInvalidCodecError) Error() string {
	return "service: invalid or unsupported storage codec: " + strconv.Quote(e.Codec)
}

// CollectionNotActiveError is returned when a document verb names a
// collection that exists but is not in the active state: its indexes are
// still being built (CollectionStateCreating) or it is being torn down
// (CollectionStateDropping). The collection resolves and is reported by
// GetCollectionInfo and ListCollections with its state; it does not serve
// documents (go-qrdb#415, go-qrdb#425).
type CollectionNotActiveError struct {
	Database   string
	Collection string
	State      string
}

// Error returns a human-readable description.
func (e *CollectionNotActiveError) Error() string {
	return "service: collection " + e.Database + "/" + e.Collection + " is " + e.State + ", not active"
}

// CollectionStateConflictError is returned when a collection lifecycle
// transition lost a race: the metadata changed between the read that decided
// the transition and the guarded write that would have made it. Op names the
// transition ("activate", "mark-dropping"). Nothing was changed by the refused
// write; the caller re-reads the collection to see its current state.
type CollectionStateConflictError struct {
	Database   string
	Collection string
	Op         string
}

// Error returns a human-readable description.
func (e *CollectionStateConflictError) Error() string {
	return "service: collection " + e.Database + "/" + e.Collection + " changed concurrently during " + e.Op
}

// CollectionDocumentsChangingError is returned, wrapped in a
// CollectionMetadataError, when a teardown keeps finding documents that
// changed under it after several passes. The drop is left DROPPING and is
// finished by a retry or by the sweeper.
type CollectionDocumentsChangingError struct {
	Database   string
	Collection string
}

// Error returns a human-readable description.
func (e *CollectionDocumentsChangingError) Error() string {
	return "service: documents of collection " + e.Database + "/" + e.Collection + " kept changing during teardown"
}

// CollectionWriteSupersededError is a document write refused by its
// post-write fence -- its collection was dropped, or dropped and created
// again, while the write was in flight -- whose document somebody else had
// already removed or replaced before the refused write could retract it: the
// teardown reclaiming the dead incarnation's keyspace, or a concurrent writer
// to the same key. Err is the refusal, and the write is refused exactly as it
// says.
//
// It is distinguished for accounting alone: the write DID store its document,
// and whoever removed or displaced it has already accounted for it, so a
// storage quota keeps the write's reservation rather than releasing the same
// bytes a second time.
type CollectionWriteSupersededError struct {
	Err error
}

// Error returns the refusal's description.
func (e *CollectionWriteSupersededError) Error() string {
	return e.Err.Error()
}

// Unwrap returns the refusal.
func (e *CollectionWriteSupersededError) Unwrap() error {
	return e.Err
}

// CollectionInconsistentRecordError is returned by the sweeper for a stored
// collection record or index-ledger entry whose fields do not derive from one
// another: it is stored under a key its own database and name do not derive,
// or it claims an index owner -- or names indexes -- its own (database, name,
// incarnation) does not derive. The sweeper acts on nobody's request, so it
// refuses to tear down or drop anything on the word of such a record; the
// record is left in place for an operator.
type CollectionInconsistentRecordError struct {
	Database   string
	Collection string
	Reason     string
}

// Error returns a human-readable description.
func (e *CollectionInconsistentRecordError) Error() string {
	return "service: collection " + e.Database + "/" + e.Collection + " record is inconsistent: " + e.Reason
}

// CollectionInvalidIndexError is returned when a collection index definition
// cannot be built: it names no field.
type CollectionInvalidIndexError struct {
	Reason string
}

// Error returns a human-readable description.
func (e *CollectionInvalidIndexError) Error() string {
	return "service: invalid collection index definition: " + e.Reason
}

// CollectionDocumentConflictError is returned by a guarded document write
// whose precondition no longer holds: the stored document changed, appeared or
// disappeared between the read the caller based its write on and the write
// itself. Nothing was written. It is the refusal that stops a concurrent
// writer's value from being overwritten with nobody told (go-qrdb#497).
type CollectionDocumentConflictError struct {
	Database   string
	Collection string
	ID         uint64
}

// Error returns a human-readable description.
func (e *CollectionDocumentConflictError) Error() string {
	return "service: document " + strconv.FormatUint(e.ID, 10) + " in collection " +
		e.Database + "/" + e.Collection + " changed concurrently"
}
