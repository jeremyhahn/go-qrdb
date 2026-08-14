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

//go:build codec_json && raft

// Package kdf white-box tests for unexported helpers.
package kdf

import (
	"encoding/base64"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// parseHashWithParams
// ---------------------------------------------------------------------------

// TestParseHashWithParams_FiveSegmentValid verifies the new 5-segment format is
// parsed correctly, returning version, params, salt, and derived.
func TestParseHashWithParams_FiveSegmentValid(t *testing.T) {
	t.Parallel()
	salt := []byte("saltsaltsaltsalt")
	derived := []byte("derivedderivedderivedderived1234")
	b64salt := base64.StdEncoding.EncodeToString(salt)
	b64derived := base64.StdEncoding.EncodeToString(derived)
	hash := "v1:argon2id:t=1,m=64,p=1,k=32:" + b64salt + ":" + b64derived

	version, params, gotSalt, gotDerived, err := parseHashWithParams(hash)
	if err != nil {
		t.Fatalf("parseHashWithParams (5-segment): %v", err)
	}
	if version != "v1:argon2id" {
		t.Errorf("version = %q, want %q", version, "v1:argon2id")
	}
	if params != "t=1,m=64,p=1,k=32" {
		t.Errorf("params = %q, want %q", params, "t=1,m=64,p=1,k=32")
	}
	if string(gotSalt) != string(salt) {
		t.Errorf("salt mismatch")
	}
	if string(gotDerived) != string(derived) {
		t.Errorf("derived mismatch")
	}
}

// TestParseHashWithParams_FourSegmentLegacy verifies the legacy 4-segment format.
func TestParseHashWithParams_FourSegmentLegacy(t *testing.T) {
	t.Parallel()
	salt := []byte("saltsaltsaltsalt")
	derived := []byte("derivedderivedderivedderived1234")
	b64salt := base64.StdEncoding.EncodeToString(salt)
	b64derived := base64.StdEncoding.EncodeToString(derived)
	hash := "v1:argon2id:" + b64salt + ":" + b64derived

	version, params, _, _, err := parseHashWithParams(hash)
	if err != nil {
		t.Fatalf("parseHashWithParams (4-segment): %v", err)
	}
	if version != "v1:argon2id" {
		t.Errorf("version = %q, want %q", version, "v1:argon2id")
	}
	if params != "" {
		t.Errorf("params should be empty for legacy 4-segment, got %q", params)
	}
}

// TestParseHashWithParams_InvalidSegments verifies that wrong segment counts return errors.
func TestParseHashWithParams_InvalidSegments(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		hash string
	}{
		{"empty", ""},
		{"three parts", "v1:argon2id:abc"},
		{"six parts", "v1:argon2id:p:s:d:extra"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, _, _, err := parseHashWithParams(tc.hash)
			if !errors.Is(err, ErrInvalidHashFormat) {
				t.Errorf("expected ErrInvalidHashFormat for %q, got %v", tc.hash, err)
			}
		})
	}
}

// TestParseHashWithParams_InvalidBase64InFiveSegment verifies bad base64 in 5-segment format.
func TestParseHashWithParams_InvalidBase64SaltFive(t *testing.T) {
	t.Parallel()
	b64derived := base64.StdEncoding.EncodeToString([]byte("derivedvalue1234"))
	hash := "v1:argon2id:t=1:" + "!!!" + ":" + b64derived
	_, _, _, _, err := parseHashWithParams(hash)
	if !errors.Is(err, ErrInvalidHashFormat) {
		t.Errorf("expected ErrInvalidHashFormat for bad salt (5-seg), got %v", err)
	}
}

// TestParseHashWithParams_InvalidBase64DerivedFive verifies bad base64 in derived (5-segment).
func TestParseHashWithParams_InvalidBase64DerivedFive(t *testing.T) {
	t.Parallel()
	b64salt := base64.StdEncoding.EncodeToString([]byte("saltsaltsaltsalt"))
	hash := "v1:argon2id:t=1:" + b64salt + ":!!!"
	_, _, _, _, err := parseHashWithParams(hash)
	if !errors.Is(err, ErrInvalidHashFormat) {
		t.Errorf("expected ErrInvalidHashFormat for bad derived (5-seg), got %v", err)
	}
}

// ---------------------------------------------------------------------------
// parseArgon2idParams
// ---------------------------------------------------------------------------

// TestParseArgon2idParams_EmptyParamsUsesDefaults verifies that empty params
// returns the fallback hasher's configured values.
func TestParseArgon2idParams_EmptyParamsUsesDefaults(t *testing.T) {
	t.Parallel()
	h := NewArgon2idHasherWithParams(2, 32768, 2, 16)
	time, memory, threads, keyLen, err := parseArgon2idParams("", h)
	if err != nil {
		t.Fatalf("parseArgon2idParams (empty): %v", err)
	}
	if time != 2 || memory != 32768 || threads != 2 || keyLen != 16 {
		t.Errorf("defaults not preserved: t=%d m=%d p=%d k=%d", time, memory, threads, keyLen)
	}
}

// TestParseArgon2idParams_ValidParams verifies all four parameters are parsed.
func TestParseArgon2idParams_ValidParams(t *testing.T) {
	t.Parallel()
	h := NewArgon2idHasherWithParams(1, 64*1024, 1, 32)
	time, memory, threads, keyLen, err := parseArgon2idParams("t=3,m=131072,p=4,k=64", h)
	if err != nil {
		t.Fatalf("parseArgon2idParams: %v", err)
	}
	if time != 3 || memory != 131072 || threads != 4 || keyLen != 64 {
		t.Errorf("params not parsed correctly: t=%d m=%d p=%d k=%d", time, memory, threads, keyLen)
	}
}

// TestParseArgon2idParams_MissingEquals verifies that a malformed "key" without
// "=" returns ErrInvalidHashFormat.
func TestParseArgon2idParams_MissingEquals(t *testing.T) {
	t.Parallel()
	h := NewArgon2idHasherWithParams(1, 64*1024, 1, 32)
	_, _, _, _, err := parseArgon2idParams("t3", h)
	if !errors.Is(err, ErrInvalidHashFormat) {
		t.Errorf("expected ErrInvalidHashFormat for missing =, got %v", err)
	}
}

// TestParseArgon2idParams_InvalidInt verifies that a non-numeric value
// returns ErrInvalidHashFormat.
func TestParseArgon2idParams_InvalidInt(t *testing.T) {
	t.Parallel()
	h := NewArgon2idHasherWithParams(1, 64*1024, 1, 32)
	_, _, _, _, err := parseArgon2idParams("t=abc", h)
	if !errors.Is(err, ErrInvalidHashFormat) {
		t.Errorf("expected ErrInvalidHashFormat for non-numeric value, got %v", err)
	}
}

// TestParseArgon2idParams_ThreadsOverflow verifies that threads > 255
// returns ErrInvalidHashFormat.
func TestParseArgon2idParams_ThreadsOverflow(t *testing.T) {
	t.Parallel()
	h := NewArgon2idHasherWithParams(1, 64*1024, 1, 32)
	_, _, _, _, err := parseArgon2idParams("p=300", h)
	if !errors.Is(err, ErrInvalidHashFormat) {
		t.Errorf("expected ErrInvalidHashFormat for threads > 255, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// parsePBKDF2Params
// ---------------------------------------------------------------------------

// TestParsePBKDF2Params_EmptyUsesDefaults verifies fallback to configured values.
func TestParsePBKDF2Params_EmptyUsesDefaults(t *testing.T) {
	t.Parallel()
	h := NewPBKDF2SHA256HasherWithParams(500, 24)
	iter, keyLen, err := parsePBKDF2Params("", h)
	if err != nil {
		t.Fatalf("parsePBKDF2Params (empty): %v", err)
	}
	if iter != 500 || keyLen != 24 {
		t.Errorf("defaults not preserved: i=%d k=%d", iter, keyLen)
	}
}

// TestParsePBKDF2Params_ValidParams verifies both parameters are parsed.
func TestParsePBKDF2Params_ValidParams(t *testing.T) {
	t.Parallel()
	h := NewPBKDF2SHA256HasherWithParams(1000, 32)
	iter, keyLen, err := parsePBKDF2Params("i=600000,k=32", h)
	if err != nil {
		t.Fatalf("parsePBKDF2Params: %v", err)
	}
	if iter != 600000 || keyLen != 32 {
		t.Errorf("wrong values: i=%d k=%d", iter, keyLen)
	}
}

// TestParsePBKDF2Params_MissingEquals verifies malformed params return error.
func TestParsePBKDF2Params_MissingEquals(t *testing.T) {
	t.Parallel()
	h := NewPBKDF2SHA256HasherWithParams(1000, 32)
	_, _, err := parsePBKDF2Params("i1000", h)
	if !errors.Is(err, ErrInvalidHashFormat) {
		t.Errorf("expected ErrInvalidHashFormat for missing =, got %v", err)
	}
}

// TestParsePBKDF2Params_ZeroValue verifies that i=0 is rejected.
func TestParsePBKDF2Params_ZeroValue(t *testing.T) {
	t.Parallel()
	h := NewPBKDF2SHA256HasherWithParams(1000, 32)
	_, _, err := parsePBKDF2Params("i=0", h)
	if !errors.Is(err, ErrInvalidHashFormat) {
		t.Errorf("expected ErrInvalidHashFormat for i=0, got %v", err)
	}
}
