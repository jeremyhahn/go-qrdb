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
// nor a key reference. The default is EncryptionAES256, so defaulting into
// it without key material would either mint a key this package does not
// have, or silently fall back to plaintext -- both wrong. The caller must
// choose: supply BackupOptions.KeyRef (a key-file path for the built-in
// "software" keywrap provider, or a hardware/cloud-KMS reference a
// go-xkms-registered provider resolves) to keep the default, or set
// BackupOptions.Encryption to EncryptionNone outright to get plaintext.
type BackupEncryptionRequiredError struct{}

// Error returns a human-readable description.
func (e *BackupEncryptionRequiredError) Error() string {
	return "service: backups are encrypted by default (aes256); supply a key reference, or explicitly request encryption \"none\""
}

// BackupDecryptionFailedError is returned when a backup's envelope cannot
// be processed: resolving its keywrap.Provider failed (an unregistered
// KeyProviderID, at either create or restore time), wrapping or unwrapping
// its DEK failed (a bad key reference, the wrong KEK, or a tampered
// wrapped DEK), or a shard's own AES-256-GCM authentication failed
// (tampered ciphertext) during restore. All of these collapse to this one
// typed, fail-closed error -- deliberately not distinguished from each
// other -- so a caller cannot use the difference to narrow down a correct
// key.
type BackupDecryptionFailedError struct {
	BackupID string
	Err      error
}

// Error returns a human-readable description.
func (e *BackupDecryptionFailedError) Error() string {
	msg := "service: backup decryption failed"
	if e.BackupID != "" {
		msg += " for " + e.BackupID
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *BackupDecryptionFailedError) Unwrap() error {
	return e.Err
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

// BackupShardIntegrityError is returned when a shard's own AES-256-GCM
// authentication fails during restore -- its stored ciphertext was
// tampered with, or does not belong to this backup (a different shard ID,
// a different backup entirely). It is distinct from
// keywrap.UnwrapFailedError (which names a wrong KEK or a tampered wrapped
// DEK, one layer up): conflating the two previously surfaced a shard
// ciphertext tamper as "wrong key-encrypting key", which is misleading --
// the KEK and the wrapped DEK were never involved in checking a single
// shard's own authentication tag.
type BackupShardIntegrityError struct{}

// Error returns a human-readable description.
func (e *BackupShardIntegrityError) Error() string {
	return "service: shard ciphertext failed authentication (tampered, or belongs to a different shard/backup)"
}

// BackupShardNotFoundError is returned when a specific shard ID has no
// stored payload in a backup's artifact -- distinct from
// *BackupDecryptionFailedError, which names a cryptographic failure. A
// caller asking for a shard this backup never captured gets told exactly
// that, not a decryption failure that never happened.
type BackupShardNotFoundError struct {
	BackupID string
	ShardID  uint64
}

// Error returns a human-readable description.
func (e *BackupShardNotFoundError) Error() string {
	return "service: shard " + strconv.FormatUint(e.ShardID, 10) + " not found in backup " + e.BackupID
}

// BackupManifestInvalidError is returned when a backup's authenticated
// manifest -- the sealed header binding its format version, algorithm,
// key-wrap provider, key reference, and its exact sorted shard set with a
// per-shard digest -- fails to authenticate or decode. Because the
// manifest is sealed under the backup's own DEK, this collapses the same
// two causes *BackupDecryptionFailedError's own Unwrap does (the wrong
// KEK, or tampering) and deliberately does not distinguish them.
type BackupManifestInvalidError struct {
	BackupID string
}

// Error returns a human-readable description.
func (e *BackupManifestInvalidError) Error() string {
	return "service: backup " + e.BackupID + "'s manifest failed to authenticate"
}

// BackupMetadataMismatchError is returned when a backup's stored metadata
// (BackupInfo.Encryption) and its artifact disagree about whether the
// backup is encrypted -- an envelope is present while the metadata claims
// "none", metadata claims encryption but no envelope was stored, or the
// authenticated manifest's own backup ID, destination, or provider does
// not match the metadata it was read alongside. Each of these is a sign
// the backup's metadata was tampered with independently of its
// cryptographic artifact (go-qrdb security review, backup-encrypt-default
// #B2): restore refuses rather than trusting whichever side the caller's
// code path happens to read first.
type BackupMetadataMismatchError struct {
	BackupID string
}

// Error returns a human-readable description.
func (e *BackupMetadataMismatchError) Error() string {
	return "service: backup " + e.BackupID + "'s stored metadata and its artifact disagree about encryption"
}

// BackupVerificationFailedError is RestoreBackup's (and
// DecryptBackupShard's) single top-level, fail-closed verification error:
// at least one target shard failed authentication, digest verification
// against the backup's manifest, or was simply absent from the artifact,
// BEFORE any shard was imported. RestoreBackup verifies and decrypts
// EVERY target shard before importing ANY of them (go-qrdb security
// review, backup-encrypt-default #B1) specifically so a tampered or
// missing shard never leaves an earlier shard already imported with no
// top-level error to show for it.
type BackupVerificationFailedError struct {
	BackupID string
	ShardID  uint64
	Err      error
}

// Error returns a human-readable description.
func (e *BackupVerificationFailedError) Error() string {
	return "service: backup " + e.BackupID + " failed verification at shard " +
		strconv.FormatUint(e.ShardID, 10) + ", before importing any shard: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *BackupVerificationFailedError) Unwrap() error {
	return e.Err
}

// BackupKeyDirNotConfiguredError is returned when the built-in "software"
// key-wrap provider is resolved but no key directory was ever configured
// for it (BackupServiceOption WithKeyDir, or lifecycle.Infra.KeyDir).
// Every key file the software provider reads must live inside one
// configured directory (go-qrdb security review, backup-encrypt-default
// #B4); there is no default directory to fall back to.
type BackupKeyDirNotConfiguredError struct{}

// Error returns a human-readable description.
func (e *BackupKeyDirNotConfiguredError) Error() string {
	return "service: no key directory configured for the \"software\" key-wrap provider " +
		"(set BackupServiceOption WithKeyDir, or lifecycle.Infra.KeyDir)"
}
