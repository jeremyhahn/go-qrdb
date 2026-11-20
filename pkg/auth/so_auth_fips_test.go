// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package auth

import (
	"crypto/fips140"
	"testing"
)

// wantFIPS folds the build-time FIPS module state into the expectation: a
// binary built with the FIPS 140-3 module active is in FIPS mode regardless
// of what the environment asks for.
func wantFIPS(intent bool) bool {
	return intent || fips140.Enabled()
}

// TestFIPSEnabled_HonoursGOFIPS140Off pins the correctness fix: the previous
// implementation tested only that GOFIPS140 was non-empty, so the explicit
// opt-out value "off" selected FIPS behaviour — the exact inverse of what the
// deployment asked for.
func TestFIPSEnabled_HonoursGOFIPS140Off(t *testing.T) {
	t.Setenv("GOFIPS140", "off")

	if got, want := fipsEnabled(), wantFIPS(false); got != want {
		t.Fatalf("fipsEnabled with GOFIPS140=off: got %v, want %v", got, want)
	}
}

// TestFIPSEnabled_GOFIPS140Values covers the remaining environmental states:
// unset is non-FIPS, and a module version or "latest" is a FIPS request.
func TestFIPSEnabled_GOFIPS140Values(t *testing.T) {
	tests := []struct {
		name   string
		value  string
		intent bool
	}{
		{name: "unset", value: "", intent: false},
		{name: "version", value: "v1.0.0", intent: true},
		{name: "latest", value: "latest", intent: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOFIPS140", tc.value)

			if got, want := fipsEnabled(), wantFIPS(tc.intent); got != want {
				t.Fatalf("fipsEnabled with GOFIPS140=%q: got %v, want %v", tc.value, got, want)
			}
		})
	}
}

// TestHashPIN_GOFIPS140Off_UsesArgon2id checks that the FIPS predicate
// actually drives KDF selection: GOFIPS140=off must yield an Argon2id record
// ('A' tag), which verifyPIN still accepts.
func TestHashPIN_GOFIPS140Off_UsesArgon2id(t *testing.T) {
	t.Setenv("GOFIPS140", "off")

	record, err := hashPIN([]byte("gofips140-off-pin"))
	if err != nil {
		t.Fatalf("hashPIN: %v", err)
	}

	wantTag := byte('A')
	if wantFIPS(false) {
		wantTag = 'P'
	}
	if record[0] != wantTag {
		t.Fatalf("hashPIN with GOFIPS140=off: got KDF tag %q, want %q", record[0], wantTag)
	}
	if err := verifyPIN([]byte("gofips140-off-pin"), record); err != nil {
		t.Fatalf("verifyPIN: %v", err)
	}
}
