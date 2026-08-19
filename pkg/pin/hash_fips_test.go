// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package pin

import (
	"crypto/fips140"
	"testing"
)

// wantAlgorithm returns the hash algorithm AutoDetectHashConfig must pick for
// a given FIPS intent; a FIPS-module build is in FIPS mode regardless of the
// environment.
func wantAlgorithm(fipsIntent bool) HashAlgorithm {
	if fipsIntent || fips140.Enabled() {
		return HashPBKDF2
	}
	return HashArgon2id
}

// TestAutoDetectHashConfig_GOFIPS140 covers the environmental signal that the
// previous implementation ignored entirely: detection consulted
// crypto/fips140.Enabled alone, so a deployment that asked for FIPS through
// GOFIPS140 silently got Argon2id — and one that asked for GOFIPS140=off must
// still get Argon2id.
func TestAutoDetectHashConfig_GOFIPS140(t *testing.T) {
	tests := []struct {
		name   string
		value  string
		intent bool
	}{
		{name: "off", value: "off", intent: false},
		{name: "unset", value: "", intent: false},
		{name: "version", value: "v1.0.0", intent: true},
		{name: "latest", value: "latest", intent: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOFIPS140", tc.value)

			cfg := AutoDetectHashConfig()
			if want := wantAlgorithm(tc.intent); cfg.Algorithm != want {
				t.Fatalf("AutoDetectHashConfig with GOFIPS140=%q: Algorithm = %q, want %q",
					tc.value, cfg.Algorithm, want)
			}
		})
	}
}

// TestAutoDetectHashConfig_GOFIPS140Off_RoundTrips checks that the config the
// detector returns under GOFIPS140=off is usable end to end, and that the
// record carries the algorithm needed to verify it later.
func TestAutoDetectHashConfig_GOFIPS140Off_RoundTrips(t *testing.T) {
	t.Setenv("GOFIPS140", "off")

	cfg := AutoDetectHashConfig()
	cfg.Time = 1
	cfg.Memory = 8 * 1024

	record, err := hashPINWithConfig("auto-detect-pin", cfg)
	if err != nil {
		t.Fatalf("hashPINWithConfig: %v", err)
	}
	if record.Algorithm != string(wantAlgorithm(false)) {
		t.Fatalf("record.Algorithm = %q, want %q", record.Algorithm, wantAlgorithm(false))
	}
	ok, err := verifyPINRecord("auto-detect-pin", record)
	if err != nil {
		t.Fatalf("verifyPINRecord: %v", err)
	}
	if !ok {
		t.Fatal("verifyPINRecord: record did not verify against the original PIN")
	}
}
