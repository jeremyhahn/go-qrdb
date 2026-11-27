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

package objstore

import (
	"io/fs"
	"strconv"
)

// UnsupportedProviderError indicates the requested go-objstore provider is not
// registered in this build. Cloud providers are compiled in only when
// go-objstore's matching build tag is set (e.g. -tags awss3 for "s3").
type UnsupportedProviderError struct {
	Provider string
}

func (e *UnsupportedProviderError) Error() string {
	return "objstore: unsupported provider " + e.Provider +
		` (cloud providers require go-objstore's build tag, e.g. -tags awss3 for s3)`
}

// InvalidRemoteError indicates NewRemote was given a nil store or an unusable
// object prefix (empty, leading "/", or containing a NUL).
type InvalidRemoteError struct {
	Prefix string
	Reason string
}

func (e *InvalidRemoteError) Error() string {
	return "objstore: invalid remote storage (" + e.Reason + "): prefix " + strconv.Quote(e.Prefix)
}

// InvalidCachedRemoteError indicates NewCachedRemote was given a nil inner
// storage or an empty cache directory.
type InvalidCachedRemoteError struct {
	Dir    string
	Reason string
}

func (e *InvalidCachedRemoteError) Error() string {
	return "objstore: invalid cached remote storage (dir " + strconv.Quote(e.Dir) + "): " + e.Reason
}

// InvalidObjectNameError indicates a CachedRemote was given an object name
// that does not map to exactly one file directly in its cache directory
// (empty, ".", "..", or an escaped name holding a path separator). It is
// refused before the inner storage is called.
type InvalidObjectNameError struct {
	Name   string
	Reason string
}

func (e *InvalidObjectNameError) Error() string {
	return "objstore: invalid object name " + strconv.Quote(e.Name) + ": " + e.Reason
}

// InvalidEncryptedRemoteError indicates NewEncryptedRemote was given a nil
// inner remote storage or a nil barrier.
type InvalidEncryptedRemoteError struct {
	Reason string
}

func (e *InvalidEncryptedRemoteError) Error() string {
	return "objstore: invalid encrypted remote storage (" + e.Reason + ")"
}

// FormatMarkerMissingError refuses to open a NON-EMPTY store that carries no
// format marker object (formatMarkerName). A store this engine created always
// has one, so its absence means the objects were written by something else —
// most concretely, the earlier layout that used each key verbatim as its
// object name — and opening it in this layout would silently hide or
// overwrite them rather than refuse an upgrade this project does not support
// in place. An EMPTY store is not refused: New writes the marker and proceeds.
// NamespaceOwnedError is returned by New when the provider namespace it opens
// is already recorded as another owner's (see WithOwner).
type NamespaceOwnedError struct {
	Provider string
	Owner    string
	Want     string
}

func (e *NamespaceOwnedError) Error() string {
	return "objstore: " + strconv.Quote(e.Provider) + " namespace is owned by " + strconv.Quote(e.Owner) +
		", not " + strconv.Quote(e.Want)
}

// NamespaceOwnedByAnother marks the refusal for a caller that cannot import
// this package: the namespace is held by another owner, so an open as the
// wanted owner reaches none of its objects.
func (e *NamespaceOwnedError) NamespaceOwnedByAnother() bool { return true }

// NamespaceRetainedError is returned by Engine.ReleaseOwner when the
// namespace still holds objects this engine did not write: they stay, and
// so does the owner marker that reserves the namespace for them. The
// release is not done; a later ReleaseOwner, once those objects are gone,
// deletes the marker. It names no object.
type NamespaceRetainedError struct {
	// Provider is the namespace's provider.
	Provider string
	// Remaining is the number of objects, other than the owner marker,
	// left in the namespace.
	Remaining int
}

func (e *NamespaceRetainedError) Error() string {
	return "objstore: " + e.Provider + " namespace keeps its owner marker: " +
		strconv.Itoa(e.Remaining) + " object(s) not written by this owner remain"
}

// ClaimUndoError is returned by New when an open that wrote its owner marker
// failed afterwards (Err) and giving that claim back failed too (Undo): the
// namespace may stay reserved for an owner whose open never completed, so
// the caller records it for a later release, as that owner: ReleaseOwner for
// a namespace the open claimed new (Fresh), ReleaseOwnerMarker for one that
// held data before, whose records and format marker are not the open's.
type ClaimUndoError struct {
	// Provider is the provider of the failed open.
	Provider string
	// Err is the failure of the open.
	Err error
	// Undo is the failure of giving the claim back.
	Undo error
	// Fresh reports that the namespace held no object before the open
	// claimed it (Engine.FreshlyClaimed): only then may the later release
	// reclaim the whole namespace.
	Fresh bool
}

func (e *ClaimUndoError) Error() string {
	return "objstore: " + strconv.Quote(e.Provider) + " open failed (" + e.Err.Error() +
		") and its namespace claim was not given back: " + e.Undo.Error()
}

// Unwrap returns the open's failure and the undo's.
func (e *ClaimUndoError) Unwrap() []error { return []error{e.Err, e.Undo} }

// NamespaceClaimUndoFailed marks the error for a caller that cannot import
// this package: the namespace claim of a failed open is still in place.
func (e *ClaimUndoError) NamespaceClaimUndoFailed() bool { return true }

// NamespaceClaimFresh reports, for a caller that cannot import this package,
// whether the failed open claimed the namespace new (Fresh).
func (e *ClaimUndoError) NamespaceClaimFresh() bool { return e.Fresh }

type FormatMarkerMissingError struct {
	// Provider is the go-objstore provider whose store was refused.
	Provider string
}

// Error returns a human-readable description naming the expected format.
func (e *FormatMarkerMissingError) Error() string {
	return "objstore: the " + e.Provider + " store is not empty and carries no " + formatMarkerName +
		" marker for format version " + formatVersion + " — objects written by a different layout" +
		" cannot be opened in place; there is no migration"
}

// FormatVersionMismatchError refuses to open a store whose format marker
// names a version other than the one this build writes and reads.
type FormatVersionMismatchError struct {
	// Found is the version recorded in the store's marker.
	Found string
	// Want is the version this build requires.
	Want string
}

// LSMFormatMarkerMissingError reports a store that EnsureLSMFormat refuses:
// it is not empty, yet carries no format marker for the LSM layout. Its
// objects were written by something other than this layout, and there is no
// migration (object-storage-lsm OS-5 A).
type LSMFormatMarkerMissingError struct{}

func (e *LSMFormatMarkerMissingError) Error() string {
	return "objstore: the store is not empty and carries no " + formatMarkerName +
		" marker for the LSM layout version " + lsmFormatVersion + " — there is no migration"
}

// ArchiveTierUnsupportedError reports an LSM store (object-storage-lsm)
// requested on an archive-tier provider. Archive tiers are backup and archive
// destinations only: their reads are delayed or billed per restore, so they
// cannot serve the SSTable reads of a live database.
type ArchiveTierUnsupportedError struct {
	// Provider is the archive-tier go-objstore provider that was refused.
	Provider string
}

// Error returns a human-readable description of the refusal.
func (e *ArchiveTierUnsupportedError) Error() string {
	return "objstore: provider " + strconv.Quote(e.Provider) +
		" is an archive tier (backup and archive destinations only) and cannot hold LSM segments"
}

// Error returns a human-readable description of the mismatch.
func (e *FormatVersionMismatchError) Error() string {
	return "objstore: store format version " + e.Found + " does not match this build's " + e.Want +
		" — there is no migration between object layouts"
}

// RecordCorruptError reports that the record stored under a hashed object
// name could not be parsed into its key and value.
type RecordCorruptError struct {
	// Name is the object the corrupt record was read from.
	Name string
	// Reason describes what about the record was invalid.
	Reason string
}

// Error returns a human-readable description of the corruption.
func (e *RecordCorruptError) Error() string {
	return "objstore: corrupt record at " + e.Name + ": " + e.Reason
}

// KeyMismatchError reports that the key stored in a hashed record does not
// hash to the object name it was read from, or does not equal the key that
// was requested. With SHA-256 naming the object this is not a realistic
// collision; it is a misplaced or tampered object, and it is refused rather
// than returned as another key's value.
type KeyMismatchError struct {
	// Name is the object the mismatched record was read from.
	Name string
}

// JournalCorruptError indicates the commit journal found at open time is
// unreadable — the last batch commit is in an unknown state, so the store
// fails closed rather than silently serving a half-applied batch.
type JournalCorruptError struct {
	Reason string
}

// InTreeJournalError reports a store opened with a commit journal inside its
// data tree: an interrupted batch left by a build that kept the journal there.
// This build keeps the journal in a sibling tree and would never see it, so
// the store is refused rather than served half-applied. There is no recovery;
// restore the store from a backup.
type InTreeJournalError struct {
	// Provider is the go-objstore provider whose store was refused.
	Provider string
}

// Error implements the error interface.
func (e *InTreeJournalError) Error() string {
	return "objstore: the " + e.Provider + " store holds a commit journal (" + journalName +
		") inside its data tree, left by an interrupted batch of an earlier layout" +
		" — it cannot be recovered; there is no migration"
}

// Error returns a human-readable description.
func (e *JournalCorruptError) Error() string {
	return "objstore: commit journal corrupt: " + e.Reason
}

// JournalWriteError wraps a failure to publish, apply, or clear the commit
// journal during a batch commit.
type JournalWriteError struct {
	Err error
}

// Error returns a human-readable description.
func (e *JournalWriteError) Error() string {
	return "objstore: commit journal: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *JournalWriteError) Unwrap() error {
	return e.Err
}

// GroupAccessibleRootError refuses a filesystem store root, opened with
// WithOwnerOnlyTree, that its group or other users can access.
type GroupAccessibleRootError struct {
	// Path is the refused root.
	Path string
	// Mode is the root's permission bits.
	Mode fs.FileMode
}

// Error returns a human-readable description of the refusal.
func (e *GroupAccessibleRootError) Error() string {
	return "objstore: store root " + strconv.Quote(e.Path) + " has mode " + e.Mode.String() +
		"; it must be accessible to its owner only (0700)"
}

// Error returns a human-readable description of the mismatch.
func (e *KeyMismatchError) Error() string {
	return "objstore: stored key does not match the object name " + e.Name
}
