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

// Package kdf provides password hashing with a versioned format that supports
// cross-version verification across FIPS and non-FIPS environments.
//
// Hash format: {version}:{base64-salt}:{base64-derived}
//
// Registered versions:
//
//	v1:argon2id       — Argon2id (OWASP default; standard mode)
//	v2:pbkdf2-sha256  — PBKDF2-HMAC-SHA256 at 600,000 iterations (FIPS mode)
//
// Both hashers are always registered. The version prefix in the stored hash
// selects the correct algorithm for verification, enabling a FIPS-mode binary
// to verify an Argon2id hash (and vice-versa) without re-hashing.
//
// KDF selection for new hashes is controlled by the GOFIPS140 environment
// variable: when set, PBKDF2-SHA256 is selected; otherwise Argon2id is used.
package kdf

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/pbkdf2"
)

const (
	// VersionArgon2id is the version prefix for Argon2id hashes.
	VersionArgon2id = "v1:argon2id"

	// VersionPBKDF2SHA256 is the version prefix for PBKDF2-SHA256 hashes.
	VersionPBKDF2SHA256 = "v2:pbkdf2-sha256"

	// saltLen is the random salt length in bytes.
	saltLen = 32

	// defaultArgon2idTime is the Argon2id iteration count per OWASP 2024 guidance.
	defaultArgon2idTime uint32 = 3

	// defaultArgon2idMemory is the Argon2id memory parameter in KiB (64 MiB).
	defaultArgon2idMemory uint32 = 64 * 1024

	// defaultArgon2idThreads is the Argon2id parallelism degree.
	defaultArgon2idThreads uint8 = 4

	// defaultArgon2idKeyLen is the Argon2id derived key length in bytes.
	defaultArgon2idKeyLen uint32 = 32

	// defaultPBKDF2Iterations is the PBKDF2 iteration count per NIST SP 800-132.
	defaultPBKDF2Iterations = 600_000

	// defaultPBKDF2KeyLen is the PBKDF2 derived key length in bytes.
	defaultPBKDF2KeyLen = 32

	// envGOFIPS140 is the environment variable that triggers FIPS mode.
	envGOFIPS140 = "GOFIPS140"
)

// Sentinel errors returned by the kdf package.
var (
	// ErrEmptyPassword is returned when an empty password is provided to Hash.
	ErrEmptyPassword = errors.New("kdf: password must not be empty")

	// ErrInvalidHashFormat is returned when the stored hash string is malformed
	// or does not contain exactly three colon-separated segments.
	ErrInvalidHashFormat = errors.New("kdf: invalid hash format")

	// ErrUnknownVersion is returned when the version prefix in the stored hash
	// is not registered.
	ErrUnknownVersion = errors.New("kdf: unknown hash version")

	// ErrVerificationFailed is returned when the derived key does not match
	// the stored hash (wrong password or corrupted hash).
	ErrVerificationFailed = errors.New("kdf: password verification failed")
)

// Hasher hashes a password and verifies a password against a stored hash.
// Implementations must store the algorithm version, salt, and derived key
// in the returned hash string so that Verify is self-contained.
type Hasher interface {
	// Hash derives a hash from password. The returned string is safe to
	// persist; it contains the version prefix, salt, and derived key.
	Hash(password []byte) (string, error)

	// Verify checks password against a stored hash. Returns nil on success,
	// ErrVerificationFailed when the password is wrong, or another error
	// for malformed inputs.
	Verify(password []byte, hash string) error

	// Version returns the version prefix string this hasher produces
	// (e.g., VersionArgon2id or VersionPBKDF2SHA256).
	Version() string
}

// hashers is the dispatch table from version prefix to Hasher.
// Both adapters are always registered so cross-version verification works.
var hashers = map[string]Hasher{
	VersionArgon2id:     newArgon2idHasher(),
	VersionPBKDF2SHA256: newPBKDF2SHA256Hasher(),
}

// DefaultHasher returns the Hasher appropriate for the current environment:
// PBKDF2-SHA256 when GOFIPS140 is set, Argon2id otherwise.
func DefaultHasher() Hasher {
	if os.Getenv(envGOFIPS140) != "" {
		return hashers[VersionPBKDF2SHA256]
	}
	return hashers[VersionArgon2id]
}

// Hash hashes password using the environment-appropriate algorithm.
// This is the primary entry point for callers that do not need to pick
// the algorithm explicitly.
func Hash(password []byte) (string, error) {
	return DefaultHasher().Hash(password)
}

// Verify checks password against a stored hash, automatically routing to the
// correct algorithm based on the version prefix embedded in hash. Both adapters
// are tried in version-prefix order so a FIPS binary can verify an Argon2id
// hash and vice-versa.
func Verify(password []byte, hash string) error {
	version, err := extractVersion(hash)
	if err != nil {
		return err
	}
	h, ok := hashers[version]
	if !ok {
		return ErrUnknownVersion
	}
	return h.Verify(password, hash)
}

// extractVersion parses the version prefix from a stored hash string.
// Accepts both the legacy 4-segment form "{version}:{salt}:{derived}" and the
// 5-segment form "{version}:{params}:{salt}:{derived}" (where version itself
// is two colon-separated tokens like "v1:argon2id" or "v2:pbkdf2-sha256").
func extractVersion(hash string) (string, error) {
	if hash == "" {
		return "", ErrInvalidHashFormat
	}
	parts := strings.Split(hash, ":")
	if len(parts) != 4 && len(parts) != 5 {
		return "", ErrInvalidHashFormat
	}
	return parts[0] + ":" + parts[1], nil
}

// buildHashWithParams assembles a versioned hash string that encodes the
// algorithm parameters inline. Format: "{version}:{params}:{salt}:{derived}"
// where version is "v1:argon2id" or "v2:pbkdf2-sha256" and params is a
// comma-separated key=value list specific to the algorithm.
func buildHashWithParams(version, params string, salt, derived []byte) string {
	return version + ":" + params + ":" +
		base64.StdEncoding.EncodeToString(salt) + ":" +
		base64.StdEncoding.EncodeToString(derived)
}

// parseHashWithParams splits a parameterized hash into components. Accepts
// both the new 5-segment form "{version}:{params}:{salt}:{derived}" (where
// version itself has a colon -> 5 parts) and the legacy 4-segment form
// "{version}:{salt}:{derived}" (4 parts). When params are absent an empty
// string is returned so callers fall back to the hasher's configured values.
func parseHashWithParams(hash string) (version, params string, salt, derived []byte, err error) {
	if hash == "" {
		return "", "", nil, nil, ErrInvalidHashFormat
	}
	parts := strings.Split(hash, ":")
	switch len(parts) {
	case 4:
		// Legacy: "vN:algorithm:salt:derived"
		version = parts[0] + ":" + parts[1]
		salt, err = base64.StdEncoding.DecodeString(parts[2])
		if err != nil || len(salt) == 0 {
			return "", "", nil, nil, ErrInvalidHashFormat
		}
		derived, err = base64.StdEncoding.DecodeString(parts[3])
		if err != nil || len(derived) == 0 {
			return "", "", nil, nil, ErrInvalidHashFormat
		}
		return version, "", salt, derived, nil
	case 5:
		// New: "vN:algorithm:params:salt:derived"
		version = parts[0] + ":" + parts[1]
		params = parts[2]
		salt, err = base64.StdEncoding.DecodeString(parts[3])
		if err != nil || len(salt) == 0 {
			return "", "", nil, nil, ErrInvalidHashFormat
		}
		derived, err = base64.StdEncoding.DecodeString(parts[4])
		if err != nil || len(derived) == 0 {
			return "", "", nil, nil, ErrInvalidHashFormat
		}
		return version, params, salt, derived, nil
	default:
		return "", "", nil, nil, ErrInvalidHashFormat
	}
}

// parseArgon2idParams extracts Argon2id cost parameters from the inline
// params string "t=N,m=N,p=N,k=N". When params is empty the hasher's
// configured defaults are used, preserving legacy hash compatibility.
func parseArgon2idParams(params string, fallback *Argon2idHasher) (time, memory uint32, threads uint8, keyLen uint32, err error) {
	time = fallback.time
	memory = fallback.memory
	threads = fallback.threads
	keyLen = fallback.keyLen
	if params == "" {
		return
	}
	for _, kv := range strings.Split(params, ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return 0, 0, 0, 0, ErrInvalidHashFormat
		}
		n, perr := strconv.ParseUint(v, 10, 32)
		if perr != nil {
			return 0, 0, 0, 0, ErrInvalidHashFormat
		}
		switch k {
		case "t":
			time = uint32(n)
		case "m":
			memory = uint32(n)
		case "p":
			if n > 255 {
				return 0, 0, 0, 0, ErrInvalidHashFormat
			}
			threads = uint8(n)
		case "k":
			keyLen = uint32(n)
		}
	}
	return
}

// parsePBKDF2Params extracts PBKDF2 parameters from "i=N,k=N".
func parsePBKDF2Params(params string, fallback *PBKDF2SHA256Hasher) (iterations, keyLen int, err error) {
	iterations = fallback.iterations
	keyLen = fallback.keyLen
	if params == "" {
		return
	}
	for _, kv := range strings.Split(params, ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return 0, 0, ErrInvalidHashFormat
		}
		n, perr := strconv.Atoi(v)
		if perr != nil || n <= 0 {
			return 0, 0, ErrInvalidHashFormat
		}
		switch k {
		case "i":
			iterations = n
		case "k":
			keyLen = n
		}
	}
	return
}

// generateSalt reads saltLen cryptographically random bytes.
func generateSalt(r io.Reader) ([]byte, error) {
	s := make([]byte, saltLen)
	if _, err := io.ReadFull(r, s); err != nil {
		return nil, err
	}
	return s, nil
}

// --- Argon2idHasher ---

// Argon2idHasher hashes passwords with Argon2id per OWASP 2024 guidance.
// time=3, memory=64 MiB, threads=4, keyLen=32.
type Argon2idHasher struct {
	time    uint32
	memory  uint32
	threads uint8
	keyLen  uint32
	rand    io.Reader
}

// newArgon2idHasher creates an Argon2idHasher with production defaults.
func newArgon2idHasher() *Argon2idHasher {
	return &Argon2idHasher{
		time:    defaultArgon2idTime,
		memory:  defaultArgon2idMemory,
		threads: defaultArgon2idThreads,
		keyLen:  defaultArgon2idKeyLen,
		rand:    rand.Reader,
	}
}

// NewArgon2idHasher returns an Argon2idHasher with production defaults.
// Callers that need custom parameters should use NewArgon2idHasherWithParams.
func NewArgon2idHasher() *Argon2idHasher {
	return newArgon2idHasher()
}

// NewArgon2idHasherWithParams creates an Argon2idHasher with custom parameters.
// time is the iteration count; memory is in KiB; threads is the degree of
// parallelism; keyLen is the derived key length in bytes.
func NewArgon2idHasherWithParams(time, memory uint32, threads uint8, keyLen uint32) *Argon2idHasher {
	return &Argon2idHasher{
		time:    time,
		memory:  memory,
		threads: threads,
		keyLen:  keyLen,
		rand:    rand.Reader,
	}
}

// SetRand replaces the cryptographic random source. Intended for testing only.
func (h *Argon2idHasher) SetRand(r io.Reader) { h.rand = r }

// Version returns the version string this hasher produces.
func (h *Argon2idHasher) Version() string { return VersionArgon2id }

// Hash derives an Argon2id hash from password. A fresh random salt is generated
// for each call. The output encodes the Argon2id cost parameters (time, memory,
// threads, keyLen) so Verify can re-derive with identical parameters even when
// called from a binary whose default hasher uses different costs. Returns
// ErrEmptyPassword for empty or nil password.
func (h *Argon2idHasher) Hash(password []byte) (string, error) {
	if len(password) == 0 {
		return "", ErrEmptyPassword
	}
	salt, err := generateSalt(h.rand)
	if err != nil {
		return "", err
	}
	derived := argon2.IDKey(password, salt, h.time, h.memory, h.threads, h.keyLen)
	params := fmt.Sprintf("t=%d,m=%d,p=%d,k=%d", h.time, h.memory, h.threads, h.keyLen)
	return buildHashWithParams(VersionArgon2id, params, salt, derived), nil
}

// Verify checks password against a stored Argon2id hash. Parameters are parsed
// from the hash itself so cross-binary verification (e.g. a FIPS binary
// verifying a non-FIPS hash) succeeds regardless of the default hasher's costs.
// Returns ErrVerificationFailed when the password is incorrect.
func (h *Argon2idHasher) Verify(password []byte, hash string) error {
	if len(password) == 0 {
		return ErrEmptyPassword
	}
	_, params, salt, stored, err := parseHashWithParams(hash)
	if err != nil {
		return err
	}
	t, mem, threads, keyLen, err := parseArgon2idParams(params, h)
	if err != nil {
		return err
	}
	derived := argon2.IDKey(password, salt, t, mem, threads, keyLen)
	if subtle.ConstantTimeCompare(derived, stored) != 1 {
		return ErrVerificationFailed
	}
	return nil
}

// --- PBKDF2SHA256Hasher ---

// PBKDF2SHA256Hasher hashes passwords with PBKDF2-HMAC-SHA256 per NIST SP 800-132.
// 600,000 iterations, 32-byte key.
type PBKDF2SHA256Hasher struct {
	iterations int
	keyLen     int
	rand       io.Reader
}

// newPBKDF2SHA256Hasher creates a PBKDF2SHA256Hasher with production defaults.
func newPBKDF2SHA256Hasher() *PBKDF2SHA256Hasher {
	return &PBKDF2SHA256Hasher{
		iterations: defaultPBKDF2Iterations,
		keyLen:     defaultPBKDF2KeyLen,
		rand:       rand.Reader,
	}
}

// NewPBKDF2SHA256Hasher returns a PBKDF2SHA256Hasher with production defaults.
func NewPBKDF2SHA256Hasher() *PBKDF2SHA256Hasher {
	return newPBKDF2SHA256Hasher()
}

// NewPBKDF2SHA256HasherWithParams creates a PBKDF2SHA256Hasher with custom
// parameters. iterations is the PBKDF2 iteration count; keyLen is the derived
// key length in bytes.
func NewPBKDF2SHA256HasherWithParams(iterations, keyLen int) *PBKDF2SHA256Hasher {
	return &PBKDF2SHA256Hasher{
		iterations: iterations,
		keyLen:     keyLen,
		rand:       rand.Reader,
	}
}

// SetRand replaces the cryptographic random source. Intended for testing only.
func (h *PBKDF2SHA256Hasher) SetRand(r io.Reader) { h.rand = r }

// Version returns the version string this hasher produces.
func (h *PBKDF2SHA256Hasher) Version() string { return VersionPBKDF2SHA256 }

// Hash derives a PBKDF2-SHA256 hash from password. A fresh random salt is
// generated for each call. The output encodes iteration count and key length
// so Verify can re-derive with identical parameters even when called from a
// binary whose default hasher is configured differently. Returns
// ErrEmptyPassword for empty or nil password.
func (h *PBKDF2SHA256Hasher) Hash(password []byte) (string, error) {
	if len(password) == 0 {
		return "", ErrEmptyPassword
	}
	salt, err := generateSalt(h.rand)
	if err != nil {
		return "", err
	}
	derived := pbkdf2.Key(password, salt, h.iterations, h.keyLen, sha256.New)
	params := fmt.Sprintf("i=%d,k=%d", h.iterations, h.keyLen)
	return buildHashWithParams(VersionPBKDF2SHA256, params, salt, derived), nil
}

// Verify checks password against a stored PBKDF2-SHA256 hash. Parameters are
// parsed from the hash itself so cross-binary verification succeeds.
// Returns ErrVerificationFailed when the password is incorrect.
func (h *PBKDF2SHA256Hasher) Verify(password []byte, hash string) error {
	if len(password) == 0 {
		return ErrEmptyPassword
	}
	_, params, salt, stored, err := parseHashWithParams(hash)
	if err != nil {
		return err
	}
	iter, keyLen, err := parsePBKDF2Params(params, h)
	if err != nil {
		return err
	}
	derived := pbkdf2.Key(password, salt, iter, keyLen, sha256.New)
	if subtle.ConstantTimeCompare(derived, stored) != 1 {
		return ErrVerificationFailed
	}
	return nil
}

// Compile-time interface assertions.
var (
	_ Hasher = (*Argon2idHasher)(nil)
	_ Hasher = (*PBKDF2SHA256Hasher)(nil)
)
