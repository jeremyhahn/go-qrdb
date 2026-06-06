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

//go:build integration

// Package integration_test exercises FIPS KDF selection end-to-end: verifies
// that toggling GOFIPS140 causes the software strategy to select PBKDF2-SHA256
// or Argon2id respectively, and that the kdf package DefaultHasher returns the
// correct algorithm version.
package integration_test

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal/kdf"
)

// TestFIPSKDF_DefaultHasher_FIPSMode verifies that DefaultHasher returns
// PBKDF2SHA256Hasher when GOFIPS140=1 is set.
func TestFIPSKDF_DefaultHasher_FIPSMode(t *testing.T) {
	t.Setenv("GOFIPS140", "1")
	h := kdf.DefaultHasher()
	if h.Version() != kdf.VersionPBKDF2SHA256 {
		t.Fatalf("FIPS mode: expected DefaultHasher to be %q, got %q",
			kdf.VersionPBKDF2SHA256, h.Version())
	}
}

// TestFIPSKDF_DefaultHasher_NonFIPSMode verifies that DefaultHasher returns
// Argon2idHasher when GOFIPS140 is unset.
func TestFIPSKDF_DefaultHasher_NonFIPSMode(t *testing.T) {
	t.Setenv("GOFIPS140", "")
	h := kdf.DefaultHasher()
	if h.Version() != kdf.VersionArgon2id {
		t.Fatalf("non-FIPS mode: expected DefaultHasher to be %q, got %q",
			kdf.VersionArgon2id, h.Version())
	}
}

// TestFIPSKDF_Hash_FIPSModeProducesCorrectPrefix verifies the hash string
// produced in FIPS mode starts with the v2:pbkdf2-sha256 prefix.
func TestFIPSKDF_Hash_FIPSModeProducesCorrectPrefix(t *testing.T) {
	t.Setenv("GOFIPS140", "1")
	hash, err := kdf.Hash([]byte("fips-password"))
	if err != nil {
		t.Fatalf("kdf.Hash: %v", err)
	}
	if len(hash) < len(kdf.VersionPBKDF2SHA256)+1 {
		t.Fatalf("hash too short: %q", hash)
	}
	prefix := hash[:len(kdf.VersionPBKDF2SHA256)]
	if prefix != kdf.VersionPBKDF2SHA256 {
		t.Fatalf("FIPS hash prefix = %q, want %q", prefix, kdf.VersionPBKDF2SHA256)
	}
}

// TestFIPSKDF_Hash_NonFIPSModeProducesCorrectPrefix verifies the hash string
// produced in non-FIPS mode starts with the v1:argon2id prefix.
func TestFIPSKDF_Hash_NonFIPSModeProducesCorrectPrefix(t *testing.T) {
	t.Setenv("GOFIPS140", "")
	hash, err := kdf.Hash([]byte("standard-password"))
	if err != nil {
		t.Fatalf("kdf.Hash: %v", err)
	}
	if len(hash) < len(kdf.VersionArgon2id)+1 {
		t.Fatalf("hash too short: %q", hash)
	}
	prefix := hash[:len(kdf.VersionArgon2id)]
	if prefix != kdf.VersionArgon2id {
		t.Fatalf("non-FIPS hash prefix = %q, want %q", prefix, kdf.VersionArgon2id)
	}
}

// TestFIPSKDF_CrossVerify_FIPSHashVerifiedByNonFIPSBinary simulates a FIPS
// binary producing a hash that is then verified by a non-FIPS binary.
// Both use the package-level Verify which routes by version prefix.
func TestFIPSKDF_CrossVerify_FIPSHashVerifiedByNonFIPSBinary(t *testing.T) {
	password := []byte("cross-binary-password")

	// Produce hash in FIPS mode.
	t.Setenv("GOFIPS140", "1")
	fipsHash, err := kdf.Hash(password)
	if err != nil {
		t.Fatalf("FIPS Hash: %v", err)
	}

	// Verify in non-FIPS mode: DefaultHasher is Argon2id, but Verify routes
	// by version prefix in the hash so PBKDF2 path is used.
	t.Setenv("GOFIPS140", "")
	if err := kdf.Verify(password, fipsHash); err != nil {
		t.Fatalf("non-FIPS Verify of FIPS hash: %v", err)
	}
}

// TestFIPSKDF_CrossVerify_NonFIPSHashVerifiedByFIPSBinary simulates a non-FIPS
// binary producing a hash that is then verified by a FIPS binary.
func TestFIPSKDF_CrossVerify_NonFIPSHashVerifiedByFIPSBinary(t *testing.T) {
	password := []byte("cross-binary-password-2")

	// Produce hash in non-FIPS mode (Argon2id with fast test params).
	t.Setenv("GOFIPS140", "")
	argonHasher := kdf.NewArgon2idHasherWithParams(1, 64*1024, 1, 32)
	nonFIPSHash, err := argonHasher.Hash(password)
	if err != nil {
		t.Fatalf("non-FIPS Hash: %v", err)
	}

	// Verify in FIPS mode: DefaultHasher is PBKDF2, but Verify routes by
	// version prefix so Argon2id path is used.
	t.Setenv("GOFIPS140", "1")
	if err := kdf.Verify(password, nonFIPSHash); err != nil {
		t.Fatalf("FIPS Verify of non-FIPS hash: %v", err)
	}
}
