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

// ---------------------------------------------------------------------------
// Backup errors
// ---------------------------------------------------------------------------

// BackupNotFoundError is returned when a backup ID does not exist.
type BackupNotFoundError struct {
	ID string
}

// Error returns a human-readable description.
func (e *BackupNotFoundError) Error() string {
	return "service: backup not found: " + e.ID
}

// BackupEmptyDestinationError is returned when a backup request has
// an empty destination.
type BackupEmptyDestinationError struct{}

// Error returns a human-readable description.
func (e *BackupEmptyDestinationError) Error() string {
	return "service: backup destination must not be empty"
}

// BackupEmptyIDError is returned when a backup operation is called
// with an empty backup ID.
type BackupEmptyIDError struct{}

// Error returns a human-readable description.
func (e *BackupEmptyIDError) Error() string {
	return "service: backup ID must not be empty"
}

// BackupEncryptionKeyRequiredError is returned when encryption is
// EXPLICITLY requested (BackupOptions.Encryption names a non-none
// algorithm) but no key is provided.
type BackupEncryptionKeyRequiredError struct{}

// Error returns a human-readable description.
func (e *BackupEncryptionKeyRequiredError) Error() string {
	return "service: encryption key is required when encryption is enabled"
}

// BackupEncryptionRequiredError is returned when a backup request supplies
// neither an explicit encryption choice (BackupOptions.Encryption is empty)
// nor an encryption key. The default is EncryptionAES256, so defaulting
// into it without key material would either mint a key this package does
// not have, or silently fall back to plaintext -- both wrong. The caller
// must choose: supply an encryption key (or a KMS key reference, where the
// deployment provides one) to keep the default, or set
// BackupOptions.Encryption to EncryptionNone outright to get plaintext.
type BackupEncryptionRequiredError struct{}

// Error returns a human-readable description.
func (e *BackupEncryptionRequiredError) Error() string {
	return "service: backups are encrypted by default (aes256); supply an encryption key (or KMS key reference), or explicitly request encryption \"none\""
}

// BackupDecryptionKeyRequiredError is returned when restoring an
// encrypted backup without providing a decryption key.
type BackupDecryptionKeyRequiredError struct{}

// Error returns a human-readable description.
func (e *BackupDecryptionKeyRequiredError) Error() string {
	return "service: decryption key is required for encrypted backups"
}

// BackupCreateError is returned when backup creation fails.
type BackupCreateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *BackupCreateError) Error() string {
	return "service: backup creation failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *BackupCreateError) Unwrap() error {
	return e.Err
}

// BackupRestoreError is returned when backup restoration fails.
type BackupRestoreError struct {
	BackupID string
	Err      error
}

// Error returns a human-readable description.
func (e *BackupRestoreError) Error() string {
	return "service: backup restore failed for " + e.BackupID + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *BackupRestoreError) Unwrap() error {
	return e.Err
}

// BackupChecksumMismatchError is returned when a backup's checksum
// does not match the computed checksum.
type BackupChecksumMismatchError struct {
	Expected string
	Actual   string
}

// Error returns a human-readable description.
func (e *BackupChecksumMismatchError) Error() string {
	return "service: backup checksum mismatch: expected " + e.Expected + ", got " + e.Actual
}

// BackupInvalidCompressionError is returned when an unrecognized
// compression type is specified.
type BackupInvalidCompressionError struct {
	Compression string
}

// Error returns a human-readable description.
func (e *BackupInvalidCompressionError) Error() string {
	return "service: invalid backup compression type: " + e.Compression
}

// BackupInvalidEncryptionError is returned when an unrecognized
// encryption type is specified.
type BackupInvalidEncryptionError struct {
	Encryption string
}

// Error returns a human-readable description.
func (e *BackupInvalidEncryptionError) Error() string {
	return "service: invalid backup encryption type: " + e.Encryption
}
