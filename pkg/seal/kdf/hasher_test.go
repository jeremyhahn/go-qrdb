// Copyright 2026 Jeremy Hahn
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

package kdf_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal/kdf"
)

// --- Argon2idHasher tests ---

func TestArgon2idHasher_Hash_RoundTrip(t *testing.T) {
	t.Parallel()
	h := kdf.NewArgon2idHasherWithParams(1, 64*1024, 1, 32)
	password := []byte("correct-horse-battery-staple")

	hash, err := h.Hash(password)
	if err != nil {
		t.Fatalf("Hash: unexpected error: %v", err)
	}
	if !strings.HasPrefix(hash, kdf.VersionArgon2id+":") {
		t.Fatalf("Hash: expected prefix %q, got %q", kdf.VersionArgon2id, hash)
	}

	if err := h.Verify(password, hash); err != nil {
		t.Fatalf("Verify: unexpected error: %v", err)
	}
}

func TestArgon2idHasher_Hash_WrongPassword(t *testing.T) {
	t.Parallel()
	h := kdf.NewArgon2idHasherWithParams(1, 64*1024, 1, 32)
	hash, err := h.Hash([]byte("correct"))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	err = h.Verify([]byte("wrong"), hash)
	if !errors.Is(err, kdf.ErrVerificationFailed) {
		t.Fatalf("Verify: expected ErrVerificationFailed, got %v", err)
	}
}

func TestArgon2idHasher_Hash_EmptyPassword(t *testing.T) {
	t.Parallel()
	h := kdf.NewArgon2idHasher()
	_, err := h.Hash(nil)
	if !errors.Is(err, kdf.ErrEmptyPassword) {
		t.Fatalf("Hash(nil): expected ErrEmptyPassword, got %v", err)
	}
	_, err = h.Hash([]byte{})
	if !errors.Is(err, kdf.ErrEmptyPassword) {
		t.Fatalf("Hash(empty): expected ErrEmptyPassword, got %v", err)
	}
}

func TestArgon2idHasher_Verify_EmptyPassword(t *testing.T) {
	t.Parallel()
	h := kdf.NewArgon2idHasher()
	err := h.Verify(nil, "v1:argon2id:abc:def")
	if !errors.Is(err, kdf.ErrEmptyPassword) {
		t.Fatalf("Verify(nil): expected ErrEmptyPassword, got %v", err)
	}
}

func TestArgon2idHasher_Hash_UniqueSalts(t *testing.T) {
	t.Parallel()
	h := kdf.NewArgon2idHasherWithParams(1, 64*1024, 1, 32)
	password := []byte("same-password")

	hash1, err := h.Hash(password)
	if err != nil {
		t.Fatalf("Hash1: %v", err)
	}
	hash2, err := h.Hash(password)
	if err != nil {
		t.Fatalf("Hash2: %v", err)
	}
	if hash1 == hash2 {
		t.Fatal("two hashes of the same password must differ (different salts)")
	}
}

func TestArgon2idHasher_Version(t *testing.T) {
	t.Parallel()
	h := kdf.NewArgon2idHasher()
	if h.Version() != kdf.VersionArgon2id {
		t.Fatalf("Version() = %q, want %q", h.Version(), kdf.VersionArgon2id)
	}
}

// --- PBKDF2SHA256Hasher tests ---

func TestPBKDF2SHA256Hasher_Hash_RoundTrip(t *testing.T) {
	t.Parallel()
	h := kdf.NewPBKDF2SHA256HasherWithParams(1000, 32)
	password := []byte("fips-compliant-password")

	hash, err := h.Hash(password)
	if err != nil {
		t.Fatalf("Hash: unexpected error: %v", err)
	}
	if !strings.HasPrefix(hash, kdf.VersionPBKDF2SHA256+":") {
		t.Fatalf("Hash: expected prefix %q, got %q", kdf.VersionPBKDF2SHA256, hash)
	}

	if err := h.Verify(password, hash); err != nil {
		t.Fatalf("Verify: unexpected error: %v", err)
	}
}

func TestPBKDF2SHA256Hasher_Hash_WrongPassword(t *testing.T) {
	t.Parallel()
	h := kdf.NewPBKDF2SHA256HasherWithParams(1000, 32)
	hash, err := h.Hash([]byte("correct"))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	err = h.Verify([]byte("wrong"), hash)
	if !errors.Is(err, kdf.ErrVerificationFailed) {
		t.Fatalf("Verify: expected ErrVerificationFailed, got %v", err)
	}
}

func TestPBKDF2SHA256Hasher_Hash_EmptyPassword(t *testing.T) {
	t.Parallel()
	h := kdf.NewPBKDF2SHA256Hasher()
	_, err := h.Hash(nil)
	if !errors.Is(err, kdf.ErrEmptyPassword) {
		t.Fatalf("Hash(nil): expected ErrEmptyPassword, got %v", err)
	}
	_, err = h.Hash([]byte{})
	if !errors.Is(err, kdf.ErrEmptyPassword) {
		t.Fatalf("Hash(empty): expected ErrEmptyPassword, got %v", err)
	}
}

func TestPBKDF2SHA256Hasher_Verify_EmptyPassword(t *testing.T) {
	t.Parallel()
	h := kdf.NewPBKDF2SHA256Hasher()
	err := h.Verify(nil, "v2:pbkdf2-sha256:abc:def")
	if !errors.Is(err, kdf.ErrEmptyPassword) {
		t.Fatalf("Verify(nil): expected ErrEmptyPassword, got %v", err)
	}
}

func TestPBKDF2SHA256Hasher_Version(t *testing.T) {
	t.Parallel()
	h := kdf.NewPBKDF2SHA256Hasher()
	if h.Version() != kdf.VersionPBKDF2SHA256 {
		t.Fatalf("Version() = %q, want %q", h.Version(), kdf.VersionPBKDF2SHA256)
	}
}

// --- Cross-version tests ---

// TestCrossVersion_Argon2idHash_VerifyWithPackageVerify verifies that the
// package-level Verify function routes to Argon2id when the hash prefix says
// "v1:argon2id", enabling a FIPS-mode binary to verify an Argon2id hash that
// was produced by a non-FIPS binary.
func TestCrossVersion_Argon2idHash_VerifyWithPackageVerify(t *testing.T) {
	t.Parallel()
	// Use the default (production-params) Argon2id hasher so the dispatch
	// table's verifier uses matching parameters.
	argon := kdf.NewArgon2idHasher()
	password := []byte("cross-version-password")

	hash, err := argon.Hash(password)
	if err != nil {
		t.Fatalf("Argon2id Hash: %v", err)
	}

	// Package-level Verify routes by version prefix to Argon2id regardless
	// of the current GOFIPS140 setting.
	if err := kdf.Verify(password, hash); err != nil {
		t.Fatalf("cross-version Verify (Argon2id hash, package-level Verify): %v", err)
	}
}

// TestCrossVersion_PBKDF2Hash_VerifyWithPackageVerify verifies that the
// package-level Verify function routes to PBKDF2 when the hash prefix says
// "v2:pbkdf2-sha256", enabling a non-FIPS binary to verify a PBKDF2 hash
// that was produced by a FIPS-mode binary.
func TestCrossVersion_PBKDF2Hash_VerifyWithPackageVerify(t *testing.T) {
	t.Parallel()
	// Use the default (production-params) PBKDF2 hasher so the dispatch
	// table's verifier uses matching parameters.
	pbkdf2h := kdf.NewPBKDF2SHA256Hasher()
	password := []byte("cross-version-password-pbkdf2")

	hash, err := pbkdf2h.Hash(password)
	if err != nil {
		t.Fatalf("PBKDF2 Hash: %v", err)
	}

	// Package-level Verify routes by version prefix to PBKDF2 regardless
	// of the current GOFIPS140 setting.
	if err := kdf.Verify(password, hash); err != nil {
		t.Fatalf("cross-version Verify (PBKDF2 hash, package-level Verify): %v", err)
	}
}

// TestCrossVersion_WrongPasswordRejected verifies that cross-version routing
// still rejects wrong passwords.
func TestCrossVersion_WrongPasswordRejected(t *testing.T) {
	t.Parallel()
	argon := kdf.NewArgon2idHasher()
	hash, err := argon.Hash([]byte("correct"))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	err = kdf.Verify([]byte("wrong"), hash)
	if !errors.Is(err, kdf.ErrVerificationFailed) {
		t.Fatalf("expected ErrVerificationFailed, got %v", err)
	}
}

// --- DefaultHasher FIPS selection tests ---

func TestDefaultHasher_NonFIPSMode(t *testing.T) {
	t.Setenv("GOFIPS140", "")
	h := kdf.DefaultHasher()
	if h.Version() != kdf.VersionArgon2id {
		t.Fatalf("DefaultHasher (non-FIPS): expected %q, got %q", kdf.VersionArgon2id, h.Version())
	}
}

func TestDefaultHasher_FIPSMode(t *testing.T) {
	t.Setenv("GOFIPS140", "1")
	h := kdf.DefaultHasher()
	if h.Version() != kdf.VersionPBKDF2SHA256 {
		t.Fatalf("DefaultHasher (FIPS): expected %q, got %q", kdf.VersionPBKDF2SHA256, h.Version())
	}
}

// --- Package-level Hash function ---

func TestHash_Roundtrip(t *testing.T) {
	t.Setenv("GOFIPS140", "")
	password := []byte("pkg-hash-roundtrip")
	hash, err := kdf.Hash(password)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if err := kdf.Verify(password, hash); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// --- Malformed hash rejection ---

func TestVerify_EmptyHash(t *testing.T) {
	t.Parallel()
	err := kdf.Verify([]byte("password"), "")
	if !errors.Is(err, kdf.ErrInvalidHashFormat) {
		t.Fatalf("expected ErrInvalidHashFormat for empty hash, got %v", err)
	}
}

func TestVerify_MissingSegments(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		hash string
	}{
		{"no colons", "abc"},
		{"one colon", "v1:argon2id"},
		{"two colons", "v1:argon2id:salt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := kdf.Verify([]byte("password"), tc.hash)
			if !errors.Is(err, kdf.ErrInvalidHashFormat) {
				t.Fatalf("expected ErrInvalidHashFormat for %q, got %v", tc.hash, err)
			}
		})
	}
}

func TestVerify_UnknownVersion(t *testing.T) {
	t.Parallel()
	// Valid structure but unknown version.
	err := kdf.Verify([]byte("password"), "v9:unknownalgo:abc:def")
	if !errors.Is(err, kdf.ErrUnknownVersion) {
		t.Fatalf("expected ErrUnknownVersion, got %v", err)
	}
}

func TestVerify_InvalidBase64Salt(t *testing.T) {
	t.Parallel()
	err := kdf.Verify([]byte("password"), "v1:argon2id:!!!:def")
	if !errors.Is(err, kdf.ErrInvalidHashFormat) {
		t.Fatalf("expected ErrInvalidHashFormat for invalid base64 salt, got %v", err)
	}
}

func TestVerify_InvalidBase64Derived(t *testing.T) {
	t.Parallel()
	// Valid base64 salt but invalid derived.
	import64 := "YWJjZGVmZ2hpamtsbW5vcA==" // "abcdefghijklmnop" base64
	err := kdf.Verify([]byte("password"), "v1:argon2id:"+import64+":!!!")
	if !errors.Is(err, kdf.ErrInvalidHashFormat) {
		t.Fatalf("expected ErrInvalidHashFormat for invalid base64 derived, got %v", err)
	}
}

// --- rand failure path ---

type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestArgon2idHasher_Hash_RandFailure(t *testing.T) {
	t.Parallel()
	h := kdf.NewArgon2idHasherWithParams(1, 64*1024, 1, 32)
	h.SetRand(errReader{})
	_, err := h.Hash([]byte("password"))
	if err == nil {
		t.Fatal("expected error from failing rand reader, got nil")
	}
}

func TestPBKDF2SHA256Hasher_Hash_RandFailure(t *testing.T) {
	t.Parallel()
	h := kdf.NewPBKDF2SHA256HasherWithParams(1000, 32)
	h.SetRand(errReader{})
	_, err := h.Hash([]byte("password"))
	if err == nil {
		t.Fatal("expected error from failing rand reader, got nil")
	}
}
