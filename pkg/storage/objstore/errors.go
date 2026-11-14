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

// FormatMarkerMissingError refuses to open a NON-EMPTY store that carries no
// format marker object (formatMarkerName). A store this engine created always
// has one, so its absence means the objects were written by something else —
// most concretely, the earlier layout that used each key verbatim as its
// object name — and opening it in this layout would silently hide or
// overwrite them rather than refuse an upgrade this project does not support
// in place. An EMPTY store is not refused: New writes the marker and proceeds.
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

// Error returns a human-readable description of the mismatch.
func (e *KeyMismatchError) Error() string {
	return "objstore: stored key does not match the object name " + e.Name
}
