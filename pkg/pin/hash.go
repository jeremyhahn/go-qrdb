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
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/json"
	"hash"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/pbkdf2"

	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"
)

// HashAlgorithm identifies the password-hashing primitive used to protect
// a PIN record.
type HashAlgorithm string

const (
	// HashArgon2id selects Argon2id (memory-hard, the modern default).
	HashArgon2id HashAlgorithm = "argon2id"

	// HashPBKDF2 selects PBKDF2 (FIPS 140-3 approved). Chosen automatically
	// when FIPS 140 mode is in effect.
	HashPBKDF2 HashAlgorithm = "pbkdf2"
)

// Canonical hash-function names used by PBKDF2 records. We avoid taking a
// dependency on any other project's HashName type so pkg/pin is self-
// contained.
const (
	HashSHA256    = "sha256"
	HashSHA384    = "sha384"
	HashSHA512    = "sha512"
	HashSHA512256 = "sha512_256"
)

// HashConfig configures a single hashPINWithConfig invocation. Only the
// fields relevant to the selected Algorithm need to be populated.
type HashConfig struct {
	// Algorithm selects HashArgon2id or HashPBKDF2.
	Algorithm HashAlgorithm

	// Argon2id parameters.
	Time    uint32 // Number of passes.
	Memory  uint32 // Memory in KiB.
	Threads uint8  // Degree of parallelism.
	KeyLen  uint32 // Derived key length in bytes.
	SaltLen int    // Salt length in bytes.

	// PBKDF2 parameters.
	PBKDF2Hash string // One of HashSHA256, HashSHA384, HashSHA512, HashSHA512256.
	Iterations int
}

// DefaultHashConfig returns production-grade Argon2id parameters for
// non-FIPS environments.
func DefaultHashConfig() HashConfig {
	return HashConfig{
		Algorithm: HashArgon2id,
		Time:      3,
		Memory:    64 * 1024, // 64 MiB
		Threads:   4,
		KeyLen:    32,
		SaltLen:   16,
	}
}

// FIPSHashConfig returns PBKDF2-SHA256 with the NIST SP 800-132
// recommended iteration count for FIPS 140 compliance.
func FIPSHashConfig() HashConfig {
	return HashConfig{
		Algorithm:  HashPBKDF2,
		PBKDF2Hash: HashSHA256,
		Iterations: 600000,
		KeyLen:     32,
		SaltLen:    16,
	}
}

// AutoDetectHashConfig returns FIPSHashConfig when FIPS 140 mode is in
// effect, DefaultHashConfig otherwise. Detection uses the stack-wide
// predicate in go-quicraft/pkg/seal (re-exported as seal.FIPSEnabled): the
// Go runtime's FIPS module state first, then GOFIPS140 as an environmental
// signal, with the explicit value "off" honored as disabled. The previous
// implementation consulted crypto/fips140.Enabled alone and so ignored a
// deployment that signalled FIPS through the environment.
func AutoDetectHashConfig() HashConfig {
	if quicraftseal.FIPSEnabled() {
		return FIPSHashConfig()
	}
	return DefaultHashConfig()
}

// PINRecord is the persisted form of a single hashed PIN. It carries the
// algorithm identifier, salt, derived hash, and algorithm-specific
// parameters so verification can reproduce the same derivation without
// out-of-band knowledge.
type PINRecord struct {
	Algorithm string          `json:"algorithm"`
	Hash      []byte          `json:"hash"`
	Salt      []byte          `json:"salt"`
	Params    json.RawMessage `json:"params,omitempty"`
}

// argon2idParams is the JSON-serializable parameter set for Argon2id records.
type argon2idParams struct {
	Time    uint32 `json:"time"`
	Memory  uint32 `json:"memory"`
	Threads uint8  `json:"threads"`
	KeyLen  uint32 `json:"key_len"`
}

// pbkdf2Params is the JSON-serializable parameter set for PBKDF2 records.
type pbkdf2Params struct {
	Hash       string `json:"hash"`
	Iterations int    `json:"iterations"`
	KeyLen     uint32 `json:"key_len"`
}

// hashPINWithConfig hashes pin under cfg and returns a PINRecord ready to
// persist.
func hashPINWithConfig(pin string, cfg HashConfig) (*PINRecord, error) {
	salt := make([]byte, cfg.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}

	var derived []byte
	var params json.RawMessage

	switch cfg.Algorithm {
	case HashArgon2id:
		derived = argon2.IDKey([]byte(pin), salt, cfg.Time, cfg.Memory, cfg.Threads, cfg.KeyLen)
		p, err := json.Marshal(argon2idParams{
			Time: cfg.Time, Memory: cfg.Memory, Threads: cfg.Threads, KeyLen: cfg.KeyLen,
		})
		if err != nil {
			return nil, err
		}
		params = p

	case HashPBKDF2:
		hf, err := resolveHashFunc(cfg.PBKDF2Hash)
		if err != nil {
			return nil, err
		}
		derived = pbkdf2.Key([]byte(pin), salt, cfg.Iterations, int(cfg.KeyLen), hf)
		p, err := json.Marshal(pbkdf2Params{
			Hash: cfg.PBKDF2Hash, Iterations: cfg.Iterations, KeyLen: cfg.KeyLen,
		})
		if err != nil {
			return nil, err
		}
		params = p

	default:
		return nil, ErrUnsupportedHashAlgorithm
	}

	return &PINRecord{
		Algorithm: string(cfg.Algorithm),
		Hash:      derived,
		Salt:      salt,
		Params:    params,
	}, nil
}

// verifyPINRecord re-derives the hash for pin using the record's stored
// algorithm and parameters and returns whether it matches in constant time.
func verifyPINRecord(pin string, record *PINRecord) (bool, error) {
	switch HashAlgorithm(record.Algorithm) {
	case HashArgon2id:
		return verifyArgon2id(pin, record)
	case HashPBKDF2:
		return verifyPBKDF2(pin, record)
	default:
		return false, ErrUnsupportedHashAlgorithm
	}
}

func verifyArgon2id(pin string, record *PINRecord) (bool, error) {
	var p argon2idParams
	if err := json.Unmarshal(record.Params, &p); err != nil {
		// Backward-compatible defaults for legacy records lacking params.
		p = argon2idParams{Time: 3, Memory: 64 * 1024, Threads: 4, KeyLen: 32}
	}
	computed := argon2.IDKey([]byte(pin), record.Salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	return subtle.ConstantTimeCompare(computed, record.Hash) == 1, nil
}

func verifyPBKDF2(pin string, record *PINRecord) (bool, error) {
	var p pbkdf2Params
	if err := json.Unmarshal(record.Params, &p); err != nil {
		return false, err
	}
	hf, err := resolveHashFunc(p.Hash)
	if err != nil {
		return false, err
	}
	computed := pbkdf2.Key([]byte(pin), record.Salt, p.Iterations, int(p.KeyLen), hf)
	return subtle.ConstantTimeCompare(computed, record.Hash) == 1, nil
}

// resolveHashFunc maps a hash name to its stdlib constructor for PBKDF2.
func resolveHashFunc(name string) (func() hash.Hash, error) {
	switch name {
	case HashSHA256:
		return sha256.New, nil
	case HashSHA384:
		return sha512.New384, nil
	case HashSHA512:
		return sha512.New, nil
	case HashSHA512256:
		return sha512.New512_256, nil
	default:
		return nil, &UnsupportedPBKDF2HashError{Hash: name}
	}
}
