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

package clusterenroll

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/jeremyhahn/go-enclave"
	"github.com/jeremyhahn/go-truststrap/pkg/p256proto"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/util/securefile"
)

const (
	// P256KeyFile is the filename used to persist the anchor advertiser's
	// P-256 responder key in the data directory, beside NoiseKeyFile. It holds
	// the FIPS-approved suite's identity (TSBOOT1_P256_AESGCM_SHA256); the
	// Noise key and this key are independent, so either suite can be rotated
	// without disturbing the other.
	P256KeyFile = "p256-key.hex"

	// JoinP256KeyFile is the filename used to persist the join listener's
	// P-256 responder key in the data directory, beside JoinKeyFile.
	JoinP256KeyFile = "join-p256-key.hex"
)

// P256KeyFileSizeError is returned when a persisted P-256 responder key file
// does not hold exactly one hex-encoded scalar. It names the length, never the
// contents.
type P256KeyFileSizeError struct {
	Path string
	Got  int
	Want int
}

// Error returns a human-readable description.
func (e *P256KeyFileSizeError) Error() string {
	return "clusterenroll: P-256 responder key file " + e.Path + " holds " +
		strconv.Itoa(e.Got) + " hex characters, want " + strconv.Itoa(e.Want)
}

// newHexEnclave allocates the protected buffer a hex-encoded P-256 scalar is
// staged in. The hex form is as sensitive as the key, so it never lives in an
// ordinary heap slice here; p256proto's persistence API takes the enclave
// itself for the same reason.
func newHexEnclave() (*enclave.MemoryEnclave, error) {
	alloc, err := enclave.ProcessAllocator()
	if err != nil {
		return nil, err
	}
	return alloc.NewSized(p256proto.HexKeySize)
}

// destroyHexEnclave releases a hex-encoding buffer and logs any problem the
// release reports. Destroy wipes the material before anything it can report,
// and a refused page release keeps the enclave on the allocator's ledger for
// the shutdown sweep to retry, so a key load or generation has nothing further
// to do about it.
func destroyHexEnclave(e *enclave.MemoryEnclave) {
	secure.LogRelease("clusterenroll P-256 key encoding buffer", e.Destroy())
}

// loadOrGenerateP256Key loads the P-256 responder key persisted at keyPath, or
// generates and persists a new one when the file is absent. protocol labels any
// *ServerError ("noise" for the anchor advertiser, "join" for the join
// listener) and what names the key in log lines. The caller owns the returned
// key and must Destroy it.
func loadOrGenerateP256Key(keyPath, protocol, what string, logger *slog.Logger) (*p256proto.StaticKey, error) {
	if fileExists(keyPath) {
		key, err := loadP256Key(keyPath)
		if err != nil {
			return nil, &ServerError{Protocol: protocol, Err: err}
		}
		logger.Info("loaded existing "+what+" P-256 responder key", "path", keyPath)
		return key, nil
	}

	key, err := generateP256Key(keyPath)
	if err != nil {
		return nil, &ServerError{Protocol: protocol, Err: err}
	}
	logger.Info("generated and persisted "+what+" P-256 responder key", "path", keyPath)
	return key, nil
}

// loadP256Key reads and decodes the persisted key at keyPath. The file bytes
// are copied into protected memory and the heap copy wiped before anything
// else runs.
func loadP256Key(keyPath string) (*p256proto.StaticKey, error) {
	fileBytes, err := readFileFn(keyPath)
	if err != nil {
		return nil, err
	}
	defer enclave.Wipe(fileBytes)

	trimmed := bytes.TrimSpace(fileBytes)
	if len(trimmed) != p256proto.HexKeySize {
		return nil, &P256KeyFileSizeError{Path: keyPath, Got: len(trimmed), Want: p256proto.HexKeySize}
	}

	hexEnc, err := newHexEnclave()
	if err != nil {
		return nil, err
	}
	defer destroyHexEnclave(hexEnc)

	if err := hexEnc.WithBytes(func(dst []byte) error {
		copy(dst, trimmed)
		return nil
	}); err != nil {
		return nil, err
	}
	return p256proto.DecodeStaticKey(hexEnc)
}

// generateP256Key generates a responder key and persists it at keyPath. The
// hex encoding goes straight from the key's protected pages into a second
// protected buffer and from there to the file, so the scalar is never an
// ordinary heap slice in this package.
func generateP256Key(keyPath string) (*p256proto.StaticKey, error) {
	key, err := p256proto.GenerateStaticKey()
	if err != nil {
		return nil, err
	}

	hexEnc, err := newHexEnclave()
	if err != nil {
		key.Destroy()
		return nil, err
	}
	defer destroyHexEnclave(hexEnc)

	if err := p256proto.EncodeStaticKey(hexEnc, key); err != nil {
		key.Destroy()
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		key.Destroy()
		return nil, err
	}
	// Atomic for the reason startNoiseAdvertiser's key write is: a crash
	// mid-write must not leave a truncated key that refuses every later boot.
	if err := hexEnc.WithBytes(func(b []byte) error {
		return securefile.WriteFileAtomic(keyPath, b, 0o600)
	}); err != nil {
		key.Destroy()
		return nil, err
	}
	return key, nil
}
