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

import (
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ID strategy constants for collection document ID generation.
const (
	// IDStrategyUnixNano generates IDs from Unix nanosecond timestamps.
	IDStrategyUnixNano = "unix_nano"

	// IDStrategyIncrement generates monotonically increasing sequential IDs.
	IDStrategyIncrement = "increment"

	// IDStrategyFieldHash generates deterministic IDs by hashing a document field.
	IDStrategyFieldHash = "field_hash"
)

// Collection lifecycle states, persisted on CollectionInfo.State.
//
// A collection's metadata is written in the CREATING state before its
// secondary indexes are built and flipped to ACTIVE only once every one of
// them exists, so no caller can write a document the declared indexes do not
// see (go-qrdb#415). A drop writes the DROPPING state before it tears anything
// down, so no caller can write into a collection whose indexes -- and whose
// unique constraints -- are going away (go-qrdb#425). Only an ACTIVE
// collection serves documents; every state is visible to GetCollectionInfo
// and ListCollections, and a collection left in CREATING or DROPPING by an
// interrupted operation is finished by the sweeper
// (CollectionService.SweepStuckCollections).
const (
	// CollectionStateActive is a collection that serves documents. Metadata
	// written before the state existed carries no state and reads as active.
	CollectionStateActive = "active"

	// CollectionStateCreating is a collection whose secondary indexes are
	// still being built.
	CollectionStateCreating = "creating"

	// CollectionStateDropping is a collection being torn down.
	CollectionStateDropping = "dropping"
)

// collectionMetadataPrefix is the key prefix for collection metadata entries.
const collectionMetadataPrefix = "__sys:collections/"

// collectionDataPrefix is the key prefix for collection document entries.
const collectionDataPrefix = "__col:"

// CollectionWorkPrefix is the key prefix of the collection work journal (see
// CollectionWorkJournal): one entry per collection that entered, or is about
// to enter, a transitional state, keyed
// "__sys:collection-work/{tenant}/{database}/{name}" in the replicated store
// that holds the collection -- the shared system store or a volume tenant's
// own Host -- OUTSIDE every database's keyspace, like the replicated
// placement table. That is what lets the node leading the store find a stuck
// collection in a database its own node-local catalog has never heard of. The
// prefix is under the internal "__sys:" namespace, so it is invisible to user
// KV enumeration, and it is keyed by tenant, so deleting a tenant removes its
// entries with the rest of its keyspace (TenantKeyPrefixes).
const CollectionWorkPrefix = "__sys:collection-work/"

// collectionIndexLedgerPrefix is the key prefix of the index ledger: one entry
// per collection incarnation that declared indexes, naming their owner and
// the indexes it builds (see CollectionService.CreateCollection). It lies
// inside the resolved (tenant, database) namespace, so a DROP DATABASE
// reclaims it with the rest of the database's keys.
const collectionIndexLedgerPrefix = "__sys:collection-index-ledger/"

// IndexDefinition defines a secondary index on a collection field.
type IndexDefinition struct {
	FieldName string `json:"field_name"`
	Unique    bool   `json:"unique,omitempty"`
	Sparse    bool   `json:"sparse,omitempty"`
}

// CollectionInfo describes a collection and its configuration.
type CollectionInfo struct {
	Name          string            `json:"name"`
	Database      string            `json:"database"`
	IDStrategy    string            `json:"id_strategy"`
	FieldHash     string            `json:"field_hash,omitempty"`
	Schema        map[string]string `json:"schema,omitempty"`
	Indexes       []IndexDefinition `json:"indexes,omitempty"`
	DocumentCount int64             `json:"document_count"`
	StorageCodec  string            `json:"storage_codec,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`

	// State is the collection's lifecycle state: CollectionStateActive,
	// CollectionStateCreating or CollectionStateDropping. Every read of the
	// metadata reports it; a record written before the field existed reads
	// as active.
	State string `json:"state,omitempty"`

	// StateSince is when the collection entered State. The sweeper measures
	// how long a CREATING or DROPPING collection has been stuck from it.
	StateSince time.Time `json:"state_since,omitzero"`

	// IndexOwner is the owner recorded on every secondary index this
	// collection created (index.CollectionOwner, go-qrdb#426). It names this
	// INCARNATION of the collection, not only its (database, name): a
	// collection dropped and created again under the same name gets a new
	// owner, so neither incarnation can adopt or remove the other's indexes.
	// The drop removes indexes with an owner-checked drop against it. A record
	// written before ownership existed has it empty: its indexes were created
	// ownerless, and its drop removes the names it declares the way it always
	// did.
	IndexOwner string `json:"index_owner,omitempty"`

	// Incarnation identifies this create of the name. Every create draws a
	// fresh one, so a collection dropped and created again is a different
	// incarnation, and everything it owns lives in a keyspace named by it:
	// its documents (documentPrefix), its ID sequence
	// (collectionSequenceName), its process-local ID generator
	// (collectionIDGenKey) and its indexes' owner. Metadata written before
	// the field existed carries none, and keeps the unsuffixed keys its data
	// already has.
	Incarnation string `json:"incarnation,omitempty"`
}

// CreateCollectionRequest contains parameters for creating a collection.
type CreateCollectionRequest struct {
	Name         string            `json:"name"`
	Database     string            `json:"database"`
	IDStrategy   string            `json:"id_strategy,omitempty"`
	FieldHash    string            `json:"field_hash,omitempty"`
	Indexes      []IndexDefinition `json:"indexes,omitempty"`
	StorageCodec string            `json:"storage_codec,omitempty"`
}

// DropCollectionRequest contains parameters for dropping a collection.
type DropCollectionRequest struct {
	Name     string `json:"name"`
	Database string `json:"database"`
}

// ListCollectionsRequest contains parameters for listing collections.
type ListCollectionsRequest struct {
	Database string `json:"database"`
}

// ListCollectionsResponse is returned with the list of collections.
type ListCollectionsResponse struct {
	Collections []CollectionInfo `json:"collections"`
}

// InsertDocumentRequest contains parameters for inserting a document.
type InsertDocumentRequest struct {
	Collection  string                 `json:"collection"`
	Database    string                 `json:"database"`
	Document    map[string]interface{} `json:"document"`
	Consistency consistency.Level      `json:"consistency,omitempty"`
	WriteMode   writemode.Mode         `json:"write_mode,omitempty"`
}

// InsertDocumentResponse is returned after a successful document insert.
type InsertDocumentResponse struct {
	ID uint64 `json:"id"`

	// Revision identifies the exact stored bytes the insert wrote (see
	// GetDocumentResponse.Revision). In-process only.
	Revision string `json:"-"`

	// Replaced is true when the insert wrote over a stored document, which
	// only a field_hash re-insert of an existing natural key does (see
	// generatedIDMayOverwrite). In-process only.
	Replaced bool `json:"-"`

	// Previous is the document the insert displaced when Replaced, nil
	// otherwise. It is exactly the document the guarded write replaced, so a
	// storage quota can release it without releasing anything twice.
	// In-process only.
	Previous map[string]interface{} `json:"-"`
}

// GetDocumentRequest contains parameters for retrieving a document.
type GetDocumentRequest struct {
	Collection  string            `json:"collection"`
	Database    string            `json:"database"`
	ID          uint64            `json:"id"`
	Consistency consistency.Level `json:"consistency,omitempty"`
}

// GetDocumentResponse is returned with the retrieved document.
type GetDocumentResponse struct {
	ID       uint64                 `json:"id"`
	Document map[string]interface{} `json:"document"`

	// Revision identifies the exact stored bytes the document was read from.
	// A guarded write (UpsertDocumentRequest.IfRevision,
	// DeleteDocumentRequest.IfRevision) takes effect only while the stored
	// document still carries it, which is how a caller that computed its
	// write from this read learns that a concurrent writer got there first
	// (go-qrdb#497). It is an in-process concurrency token for the SQL
	// surface's statement-level writes, not a wire field.
	Revision string `json:"-"`
}

// UpdateDocumentRequest contains parameters for updating a document.
type UpdateDocumentRequest struct {
	Collection  string                 `json:"collection"`
	Database    string                 `json:"database"`
	ID          uint64                 `json:"id"`
	Document    map[string]interface{} `json:"document"`
	Consistency consistency.Level      `json:"consistency,omitempty"`
	WriteMode   writemode.Mode         `json:"write_mode,omitempty"`
}

// UpdateDocumentResponse is returned after a successful document update.
type UpdateDocumentResponse struct {
	ID uint64 `json:"id"`
}

// DeleteDocumentRequest contains parameters for deleting a document.
type DeleteDocumentRequest struct {
	Collection string         `json:"collection"`
	Database   string         `json:"database"`
	ID         uint64         `json:"id"`
	WriteMode  writemode.Mode `json:"write_mode,omitempty"`

	// IfRevision, when set, deletes the document only while the stored
	// document still carries this revision (GetDocumentResponse.Revision);
	// otherwise the delete is refused with CollectionDocumentConflictError
	// and nothing is removed. The guarded delete is synchronous, so
	// WriteMode is not honoured for it. In-process only.
	IfRevision string `json:"-"`
}

// UpsertDocumentRequest contains parameters for a guarded insert-or-replace
// of a document under an explicit ID (go-qrdb#497).
type UpsertDocumentRequest struct {
	Collection string                 `json:"collection"`
	Database   string                 `json:"database"`
	ID         uint64                 `json:"id"`
	Document   map[string]interface{} `json:"document"`

	// IfRevision, when set, makes the write a guarded REPLACE of exactly the
	// document the caller read: it succeeds only while the stored document
	// still carries this revision, and is refused with
	// CollectionDocumentConflictError -- nothing written -- when the document
	// changed, disappeared, or was never there. Empty means insert-or-replace
	// whatever is stored. In-process only.
	IfRevision string `json:"-"`
}

// UpsertDocumentResponse reports what a guarded upsert did.
type UpsertDocumentResponse struct {
	ID uint64 `json:"id"`

	// Replaced is true when the write replaced a stored document and false
	// when it inserted one.
	Replaced bool `json:"replaced"`

	// Previous is the document the write replaced, nil on an insert. It is
	// the exact document the guarded write displaced, not a separate read
	// that a concurrent writer could have changed.
	Previous map[string]interface{} `json:"previous,omitempty"`

	// Revision identifies the stored bytes the write produced.
	Revision string `json:"-"`
}

// DeleteDocumentResponse is returned after a successful document deletion.
type DeleteDocumentResponse struct {
	Deleted bool `json:"deleted"`
}

// ListDocumentsRequest contains parameters for listing documents.
type ListDocumentsRequest struct {
	Collection  string            `json:"collection"`
	Database    string            `json:"database"`
	PageSize    int32             `json:"page_size,omitempty"`
	PageToken   string            `json:"page_token,omitempty"`
	Consistency consistency.Level `json:"consistency,omitempty"`
}

// ListDocumentsResponse is returned with a page of documents.
type ListDocumentsResponse struct {
	Documents     []GetDocumentResponse `json:"documents"`
	NextPageToken string                `json:"next_page_token,omitempty"`
	TotalCount    int64                 `json:"total_count"`
}

// collectionMetadataKey returns the storage key for collection metadata.
func collectionMetadataKey(database, name string) string {
	return collectionMetadataPrefix + database + "/" + name
}

// documentPrefix returns the key prefix of this incarnation's documents,
// "__col:{database}:{name}#{incarnation}/". Every incarnation of a name has a
// keyspace of its own, so nothing still working on a dropped incarnation --
// a late writer, a resumed teardown -- can reach the documents of the one
// that replaced it.
//
// Metadata written before incarnations existed carries none, and keeps the
// unsuffixed prefix "__col:{database}:{name}/" its documents were written
// under: a legacy collection is read, written and dropped where its documents
// already are, with no migration. The next create of the name after such a
// collection is dropped draws an incarnation, and so a keyspace of its own.
func (info *CollectionInfo) documentPrefix() string {
	return collectionDataPrefix + info.Database + ":" + info.Name + incarnationSuffix(info.Incarnation) + "/"
}

// incarnationSuffix is the "#{incarnation}" that scopes a collection's
// per-incarnation names, and nothing for a record that predates
// incarnations. No new collection name contains '#', so the suffix can never
// be confused with a name's own characters.
func incarnationSuffix(incarnation string) string {
	if incarnation == "" {
		return ""
	}
	return incarnationSeparator + incarnation
}

// incarnationSeparator separates a collection's name from its incarnation in
// every per-incarnation key. CreateCollection refuses it in a new name
// (CollectionInvalidNameError), so a name and an incarnation never blur.
const incarnationSeparator = "#"

// collectionIndexName returns the secondary-index name for a field of a
// collection, in the database-scoped hierarchical form
// "{database}.{collection}.{field}" the rest of the index subsystem uses
// (see docs/secondary-indexes/architecture.md, "Index naming").
//
// It is the single formula shared by CreateCollection and DropCollection.
// The two derived the name independently only in the sense that the drop
// derived none at all: it never dropped anything, so the indexes a
// collection created outlived it (go-qrdb#399). Keeping one function means a
// future change to the naming scheme cannot desynchronise them again.
func collectionIndexName(database, collection, field string) string {
	return database + "." + collection + "." + field
}
