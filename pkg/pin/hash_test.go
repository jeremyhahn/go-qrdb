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

package pin

import (
	"crypto/fips140"
	"encoding/json"
	"errors"
	"testing"
)

// =============================================================================
// HashConfig helpers
// =============================================================================

func TestDefaultHashConfig_Argon2id(t *testing.T) {
	cfg := DefaultHashConfig()
	if cfg.Algorithm != HashArgon2id {
		t.Fatalf("Algorithm = %q, want %q", cfg.Algorithm, HashArgon2id)
	}
	if cfg.Time == 0 || cfg.Memory == 0 || cfg.Threads == 0 || cfg.KeyLen == 0 || cfg.SaltLen == 0 {
		t.Fatalf("DefaultHashConfig has zero field(s): %+v", cfg)
	}
}

func TestFIPSHashConfig_PBKDF2(t *testing.T) {
	cfg := FIPSHashConfig()
	if cfg.Algorithm != HashPBKDF2 {
		t.Fatalf("Algorithm = %q, want %q", cfg.Algorithm, HashPBKDF2)
	}
	if cfg.PBKDF2Hash != HashSHA256 {
		t.Fatalf("PBKDF2Hash = %q, want %q", cfg.PBKDF2Hash, HashSHA256)
	}
	if cfg.Iterations < 100000 {
		t.Fatalf("Iterations = %d, want >= 100000 (NIST SP 800-132)", cfg.Iterations)
	}
}

func TestAutoDetectHashConfig_MatchesRuntimeMode(t *testing.T) {
	cfg := AutoDetectHashConfig()
	if fips140.Enabled() {
		if cfg.Algorithm != HashPBKDF2 {
			t.Fatalf("FIPS enabled but Algorithm = %q, want %q", cfg.Algorithm, HashPBKDF2)
		}
	} else {
		if cfg.Algorithm != HashArgon2id {
			t.Fatalf("FIPS disabled but Algorithm = %q, want %q", cfg.Algorithm, HashArgon2id)
		}
	}
}

// =============================================================================
// hashPINWithConfig + verifyPINRecord — Argon2id
// =============================================================================

func TestHashPINWithConfig_Argon2id_RoundTrip(t *testing.T) {
	cfg := DefaultHashConfig()
	cfg.Time = 1
	cfg.Memory = 8 * 1024 // keep test fast
	cfg.Threads = 1
	cfg.SaltLen = 8

	record, err := hashPINWithConfig("test-pin", cfg)
	if err != nil {
		t.Fatalf("hashPINWithConfig: %v", err)
	}
	if record.Algorithm != string(HashArgon2id) {
		t.Fatalf("Algorithm = %q, want %q", record.Algorithm, HashArgon2id)
	}
	if len(record.Salt) != cfg.SaltLen {
		t.Fatalf("len(Salt) = %d, want %d", len(record.Salt), cfg.SaltLen)
	}
	if len(record.Hash) != int(cfg.KeyLen) {
		t.Fatalf("len(Hash) = %d, want %d", len(record.Hash), cfg.KeyLen)
	}
	ok, err := verifyPINRecord("test-pin", record)
	if err != nil {
		t.Fatalf("verifyPINRecord: %v", err)
	}
	if !ok {
		t.Fatal("verifyPINRecord with correct PIN: want true")
	}
	ok, err = verifyPINRecord("wrong-pin", record)
	if err != nil {
		t.Fatalf("verifyPINRecord (wrong): %v", err)
	}
	if ok {
		t.Fatal("verifyPINRecord with wrong PIN: want false")
	}
}

func TestVerifyArgon2id_LegacyRecordWithoutParams(t *testing.T) {
	// Manually craft a record that uses defaults but omits the Params
	// field — exercises the fallback branch in verifyArgon2id.
	cfg := DefaultHashConfig()
	cfg.Time = 3
	cfg.Memory = 64 * 1024
	cfg.Threads = 4
	cfg.KeyLen = 32
	cfg.SaltLen = 16

	full, err := hashPINWithConfig("legacy-pin", cfg)
	if err != nil {
		t.Fatalf("hashPINWithConfig: %v", err)
	}
	legacy := &PINRecord{
		Algorithm: full.Algorithm,
		Hash:      full.Hash,
		Salt:      full.Salt,
		// Params intentionally nil.
	}
	ok, err := verifyPINRecord("legacy-pin", legacy)
	if err != nil {
		t.Fatalf("verifyPINRecord legacy: %v", err)
	}
	if !ok {
		t.Fatal("legacy record must verify with fallback defaults")
	}
}

// =============================================================================
// hashPINWithConfig + verifyPINRecord — PBKDF2
// =============================================================================

func TestHashPINWithConfig_PBKDF2_AllSupportedHashes(t *testing.T) {
	cases := []struct {
		name string
		hash string
	}{
		{"SHA256", HashSHA256},
		{"SHA384", HashSHA384},
		{"SHA512", HashSHA512},
		{"SHA512_256", HashSHA512256},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := HashConfig{
				Algorithm:  HashPBKDF2,
				PBKDF2Hash: tc.hash,
				Iterations: 1000, // keep tests fast
				KeyLen:     32,
				SaltLen:    16,
			}
			record, err := hashPINWithConfig("pbkdf2-pin", cfg)
			if err != nil {
				t.Fatalf("hashPINWithConfig: %v", err)
			}
			if record.Algorithm != string(HashPBKDF2) {
				t.Fatalf("Algorithm = %q, want %q", record.Algorithm, HashPBKDF2)
			}
			ok, err := verifyPINRecord("pbkdf2-pin", record)
			if err != nil {
				t.Fatalf("verifyPINRecord: %v", err)
			}
			if !ok {
				t.Fatal("verifyPINRecord with correct PIN: want true")
			}
			ok, err = verifyPINRecord("wrong-pin", record)
			if err != nil {
				t.Fatalf("verifyPINRecord (wrong): %v", err)
			}
			if ok {
				t.Fatal("verifyPINRecord with wrong PIN: want false")
			}
		})
	}
}

func TestHashPINWithConfig_PBKDF2_UnsupportedHashRejected(t *testing.T) {
	cfg := HashConfig{
		Algorithm:  HashPBKDF2,
		PBKDF2Hash: "md5",
		Iterations: 1000,
		KeyLen:     32,
		SaltLen:    16,
	}
	_, err := hashPINWithConfig("any-pin", cfg)
	if err == nil {
		t.Fatal("hashPINWithConfig with bogus hash: want err, got nil")
	}
	var bad *ErrUnsupportedPBKDF2Hash
	if !errors.As(err, &bad) {
		t.Fatalf("err = %v, want *ErrUnsupportedPBKDF2Hash", err)
	}
	if bad.Hash != "md5" {
		t.Fatalf("bad.Hash = %q, want %q", bad.Hash, "md5")
	}
}

func TestVerifyPBKDF2_CorruptedParams(t *testing.T) {
	record := &PINRecord{
		Algorithm: string(HashPBKDF2),
		Hash:      []byte{1, 2, 3},
		Salt:      []byte{4, 5, 6},
		Params:    []byte("not-json"),
	}
	_, err := verifyPINRecord("anything", record)
	if err == nil {
		t.Fatal("verifyPINRecord with corrupted params: want err, got nil")
	}
}

func TestVerifyPBKDF2_UnsupportedHashInRecord(t *testing.T) {
	// Hand-craft a record whose params reference an unsupported hash —
	// exercises the second resolveHashFunc branch in verifyPBKDF2.
	params, err := json.Marshal(pbkdf2Params{Hash: "blake2", Iterations: 1000, KeyLen: 32})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	record := &PINRecord{
		Algorithm: string(HashPBKDF2),
		Hash:      []byte{1, 2, 3},
		Salt:      []byte{4, 5, 6},
		Params:    params,
	}
	_, err = verifyPINRecord("anything", record)
	if err == nil {
		t.Fatal("verifyPINRecord with unsupported hash: want err, got nil")
	}
}

// =============================================================================
// hashPINWithConfig errors
// =============================================================================

func TestHashPINWithConfig_UnsupportedAlgorithm(t *testing.T) {
	cfg := HashConfig{
		Algorithm: "bcrypt",
		SaltLen:   16,
	}
	_, err := hashPINWithConfig("any", cfg)
	if !errors.Is(err, ErrUnsupportedHashAlgorithm) {
		t.Fatalf("err = %v, want ErrUnsupportedHashAlgorithm", err)
	}
}

func TestVerifyPINRecord_UnsupportedAlgorithm(t *testing.T) {
	record := &PINRecord{Algorithm: "bcrypt"}
	_, err := verifyPINRecord("any", record)
	if !errors.Is(err, ErrUnsupportedHashAlgorithm) {
		t.Fatalf("err = %v, want ErrUnsupportedHashAlgorithm", err)
	}
}

// =============================================================================
// resolveHashFunc
// =============================================================================

func TestResolveHashFunc_AllSupported(t *testing.T) {
	cases := []string{HashSHA256, HashSHA384, HashSHA512, HashSHA512256}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			hf, err := resolveHashFunc(name)
			if err != nil {
				t.Fatalf("resolveHashFunc(%q): %v", name, err)
			}
			if hf == nil {
				t.Fatalf("resolveHashFunc(%q) returned nil constructor", name)
			}
			h := hf()
			if h == nil {
				t.Fatalf("constructor returned nil hash for %q", name)
			}
			if _, err := h.Write([]byte("ping")); err != nil {
				t.Fatalf("hash.Write: %v", err)
			}
			if len(h.Sum(nil)) == 0 {
				t.Fatalf("hash sum empty for %q", name)
			}
		})
	}
}

func TestResolveHashFunc_Unsupported(t *testing.T) {
	_, err := resolveHashFunc("ripemd160")
	if err == nil {
		t.Fatal("resolveHashFunc(bogus): want err, got nil")
	}
	var bad *ErrUnsupportedPBKDF2Hash
	if !errors.As(err, &bad) {
		t.Fatalf("err = %v, want *ErrUnsupportedPBKDF2Hash", err)
	}
}
