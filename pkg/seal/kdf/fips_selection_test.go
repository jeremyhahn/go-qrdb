// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package kdf_test

import (
	"crypto/fips140"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal/kdf"
)

// wantVersion returns the hash version DefaultHasher must select for a given
// FIPS intent. A binary built with the FIPS 140-3 module active is in FIPS
// mode no matter what the environment says, so the expectation folds that in.
func wantVersion(fipsIntent bool) string {
	if fipsIntent || fips140.Enabled() {
		return kdf.VersionPBKDF2SHA256
	}
	return kdf.VersionArgon2id
}

// TestDefaultHasher_GOFIPS140Off_SelectsArgon2id pins the regression this
// change fixes: GOFIPS140=off is an explicit request for NON-FIPS operation
// and must not be read as "the variable is set, therefore FIPS".
func TestDefaultHasher_GOFIPS140Off_SelectsArgon2id(t *testing.T) {
	t.Setenv("GOFIPS140", "off")

	got := kdf.DefaultHasher().Version()
	if want := wantVersion(false); got != want {
		t.Fatalf("DefaultHasher with GOFIPS140=off: got %q, want %q", got, want)
	}
}

// TestDefaultHasher_GOFIPS140Version_SelectsPBKDF2 covers the values the Go
// toolchain actually accepts as a FIPS request (a module version, or "latest").
func TestDefaultHasher_GOFIPS140Version_SelectsPBKDF2(t *testing.T) {
	for _, value := range []string{"v1.0.0", "latest", "1"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("GOFIPS140", value)

			got := kdf.DefaultHasher().Version()
			if want := wantVersion(true); got != want {
				t.Fatalf("DefaultHasher with GOFIPS140=%s: got %q, want %q", value, got, want)
			}
		})
	}
}

// TestHash_GOFIPS140Off_ProducesVerifiableArgon2id checks the end-to-end
// path: the package-level Hash writes an Argon2id record under
// GOFIPS140=off, and Verify routes it back by version prefix.
func TestHash_GOFIPS140Off_ProducesVerifiableArgon2id(t *testing.T) {
	t.Setenv("GOFIPS140", "off")

	password := []byte("gofips140-off-roundtrip")
	hash, err := kdf.Hash(password)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if want := wantVersion(false); len(hash) < len(want) || hash[:len(want)] != want {
		t.Fatalf("Hash with GOFIPS140=off: got prefix %q, want %q", hash, want)
	}
	if err := kdf.Verify(password, hash); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}
