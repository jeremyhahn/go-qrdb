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
	"errors"
	"testing"
)

// This file pins the Error() message of error types that other tests in
// this package already construct and type-assert on (errors.As) but never
// render, plus a couple of small pure functions the same gap left
// unexercised. Each corresponding behavioral test already proves the type
// is returned under the right condition; these prove what it SAYS.

func TestTenantOnboardingNotConfiguredError_Message(t *testing.T) {
	err := &TenantOnboardingNotConfiguredError{}
	want := "service: tenant onboarding is not configured on this node"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestTenantOnboardingFieldError_Message(t *testing.T) {
	err := &TenantOnboardingFieldError{Field: "admin_username"}
	want := "service: tenant onboarding request is missing admin_username"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestJoinTokenTTLOverflowError_Message(t *testing.T) {
	err := &JoinTokenTTLOverflowError{TTLSeconds: 1 << 62}
	want := "service: ttl_seconds exceeds the maximum convertible to a duration"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestClusterJoinTokenNotConfiguredError_Message(t *testing.T) {
	err := &ClusterJoinTokenNotConfiguredError{}
	want := "service: no cluster join-token store configured; enable member-side CSR join first"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestAuthFactorTenantMismatchError_Message(t *testing.T) {
	err := &AuthFactorTenantMismatchError{Requested: "globex", Context: "acme"}
	want := "service: enroll factor: requested tenant globex does not match context tenant acme"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestAuthFactorListReadError_Message(t *testing.T) {
	err := &AuthFactorListReadError{Key: "__sys:authfactor/acme/alice", Err: errors.New("sealed")}
	want := "service: list factors: read __sys:authfactor/acme/alice: sealed"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// Backup errors
// ---------------------------------------------------------------------------

func TestBackupEncryptionRequiredError_Message(t *testing.T) {
	err := &BackupEncryptionRequiredError{}
	want := "service: backups are encrypted by default (aes256); supply a key reference, or explicitly request encryption \"none\""
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestBackupShardIntegrityError_Message(t *testing.T) {
	err := &BackupShardIntegrityError{}
	want := "service: shard ciphertext failed authentication (tampered, or belongs to a different shard/backup)"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestBackupShardNotFoundError_Message(t *testing.T) {
	err := &BackupShardNotFoundError{BackupID: "b1", ShardID: 7}
	want := "service: shard 7 not found in backup b1"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestBackupManifestInvalidError_Message(t *testing.T) {
	err := &BackupManifestInvalidError{BackupID: "b1"}
	want := "service: backup b1's manifest failed to authenticate"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestBackupMetadataMismatchError_Message(t *testing.T) {
	err := &BackupMetadataMismatchError{BackupID: "b1"}
	want := "service: backup b1's stored metadata and its artifact disagree about encryption"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestBackupVerificationFailedError_Message(t *testing.T) {
	err := &BackupVerificationFailedError{BackupID: "b1", ShardID: 3, Err: errors.New("tampered")}
	want := "service: backup b1 failed verification at shard 3, before importing any shard: tampered"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, err.Err) {
		t.Fatal("Unwrap should allow errors.Is to match the underlying error")
	}
}

func TestBackupEnvelopeVersionError_Message(t *testing.T) {
	err := &BackupEnvelopeVersionError{BackupID: "b1", FormatVersion: 9}
	want := "service: backup b1's envelope format version 9 is not supported by this build"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestBackupManifestAlgorithmError_Message(t *testing.T) {
	err := &BackupManifestAlgorithmError{BackupID: "b1", Algorithm: "rot13"}
	want := "service: backup b1's manifest names unsupported algorithm rot13"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestBackupKeyDirNotConfiguredError_Message(t *testing.T) {
	err := &BackupKeyDirNotConfiguredError{}
	want := "service: no key directory configured for the \"software\" key-wrap provider " +
		"(set BackupServiceOption WithKeyDir, or lifecycle.Infra.KeyDir)"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestBackupPlaintextRestoreNotAllowedError_Message(t *testing.T) {
	err := &BackupPlaintextRestoreNotAllowedError{BackupID: "b1"}
	want := "service: backup b1 is stored as plaintext; restoring it requires an explicit opt-in (AllowPlaintext)"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// ImportSessionHasPrefix (chunked import)
// ---------------------------------------------------------------------------

func TestImportSessionHasPrefix_MatchesDecodedInternalID(t *testing.T) {
	externalID := encodeSessionID("acme/imports/session-1")
	if !ImportSessionHasPrefix(externalID, "acme/imports/") {
		t.Fatal("ImportSessionHasPrefix = false, want true for a matching prefix")
	}
	if ImportSessionHasPrefix(externalID, "globex/imports/") {
		t.Fatal("ImportSessionHasPrefix = true, want false for a non-matching prefix")
	}
}

func TestImportSessionHasPrefix_UndecodableIDReturnsFalse(t *testing.T) {
	if ImportSessionHasPrefix("not-valid-base64url!!", "acme/") {
		t.Fatal("ImportSessionHasPrefix = true for an undecodable session id, want false")
	}
}
