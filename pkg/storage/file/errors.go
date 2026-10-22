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

package file

import "strconv"

// JournalCorruptError indicates the commit journal found at open time is
// unreadable — the last batch commit is in an unknown state, so the store
// fails closed rather than silently serving a half-applied batch.
type JournalCorruptError struct {
	Reason string
}

// Error returns a human-readable description.
func (e *JournalCorruptError) Error() string {
	return "file storage: commit journal corrupt: " + e.Reason
}

// JournalWriteError wraps a failure to publish, apply, or clear the
// commit journal during a batch commit.
type JournalWriteError struct {
	Err error
}

// Error returns a human-readable description.
func (e *JournalWriteError) Error() string {
	return "file storage: commit journal: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *JournalWriteError) Unwrap() error {
	return e.Err
}

// FormatMarkerMissingError refuses to open a NON-EMPTY data directory that
// carries no format marker (formatMarkerFileName). The only way a directory
// this engine itself created lacks one is that it was written by a prior,
// incompatible on-disk layout — most concretely, the one-file/one-directory
// per key layouts this hashed, content-addressed one replaced (go-qrdb#592)
// — so opening it as this format would silently misread or overwrite
// whatever is there rather than refusing an upgrade this project does not
// support in place. An EMPTY directory is not refused: there is nothing in
// it this format could misread, so New writes the marker and proceeds.
type FormatMarkerMissingError struct {
	// Dir is the data directory that was refused.
	Dir string
}

// Error returns a human-readable description naming the expected format.
func (e *FormatMarkerMissingError) Error() string {
	return "file storage: " + e.Dir + " is not empty and carries no " + formatMarkerFileName +
		" marker for format version " + formatVersion + " — a data directory written by a" +
		" different on-disk layout cannot be opened in place; there is no migration"
}

// FormatVersionMismatchError refuses to open a data directory whose format
// marker names a version other than the one this build writes and reads —
// an older or newer on-disk layout than this binary speaks.
type FormatVersionMismatchError struct {
	// Found is the version recorded in the directory's marker.
	Found string
	// Want is the version this build requires.
	Want string
}

// Error returns a human-readable description of the mismatch.
func (e *FormatVersionMismatchError) Error() string {
	return "file storage: data directory format version " + e.Found + " does not match this build's " + e.Want +
		" — there is no migration between on-disk formats"
}

// NameKeyLengthError is returned when an encrypted store's record-name key
// file (nameKeyFileName) decrypts to a length other than the one the engine
// writes. Every record name is derived from that key, so the store is not
// served under a key it did not write.
type NameKeyLengthError struct {
	// Path is the record-name key file.
	Path string
	// Len is the length the file decrypted to.
	Len int
	// Want is the length the engine writes.
	Want int
}

// Error returns a human-readable description of the bad key file.
func (e *NameKeyLengthError) Error() string {
	return "file storage: record-name key " + e.Path + " holds " + strconv.Itoa(e.Len) +
		" bytes, want " + strconv.Itoa(e.Want)
}

// AtRestMismatchError refuses to open a data directory whose format marker
// records the other at-rest posture: a plaintext store opened under the seal
// barrier's encryption, which would serve its plaintext records beside new
// ciphertext ones, or an encrypted store opened without it. There is no
// conversion in place; move the data through a backup and restore.
type AtRestMismatchError struct {
	// Dir is the data directory that was refused.
	Dir string
	// Found is the posture the marker records.
	Found string
	// Want is the posture the engine was opened in.
	Want string
}

// Error returns a human-readable description of the mismatch.
func (e *AtRestMismatchError) Error() string {
	return "file storage: " + e.Dir + " was written " + e.Found + " and cannot be opened " + e.Want
}

// RecordCorruptError reports that a stored record could not be parsed back
// into its key and value: the on-disk content is shorter than its own
// declared key-length header, or the header names a length the record
// cannot hold. This is always a genuine fault — bit rot, a truncated write
// with no crash to explain it, or a hand-edited file — never a refusal on a
// client's input, which is validated and encoded before it ever reaches
// disk.
type RecordCorruptError struct {
	// Path is the file the corrupt record was read from.
	Path string
	// Reason describes what about the record was invalid.
	Reason string
}

// Error returns a human-readable description of the corruption.
func (e *RecordCorruptError) Error() string {
	return "file storage: corrupt record at " + e.Path + ": " + e.Reason
}

// KeyMismatchError reports that the key stored in a record does not match
// the key whose hash named the file it was read from. With a cryptographic
// hash (SHA-256) naming the file, this is not a realistic collision; it is
// the signature of disk corruption that happened to leave both headers
// internally consistent, or of a file placed there by something other than
// this engine. Get refuses rather than returning a value that provably
// belongs to a different key than the one asked for.
type KeyMismatchError struct {
	// Path is the file the mismatched record was read from.
	Path string
}

// Error returns a human-readable description of the mismatch.
func (e *KeyMismatchError) Error() string {
	return "file storage: stored key does not match the requested key at " + e.Path
}
