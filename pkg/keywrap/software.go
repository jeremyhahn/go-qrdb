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

package keywrap

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// SoftwareProviderID is the registered id of qrdb's built-in Provider.
const SoftwareProviderID = "software"

// softwareKeyLength is the exact length a software provider's key file
// must be. Not truncated, padded, or hashed to size: a key file of any
// other length is refused outright.
const softwareKeyLength = 32

// softwareKeyReadLimit bounds how many bytes a key file read ever consumes,
// regardless of the file's actual size: exactly one more than the valid
// length, so reading from an arbitrarily large (or infinite, e.g.
// /dev/zero) file costs the same constant, small amount of work as reading
// a correctly-sized one, and "too long" is distinguished from "exactly
// right" without ever learning how much too long.
const softwareKeyReadLimit = softwareKeyLength + 1

// softwareNonceLength is the AES-256-GCM nonce length Wrap/Unwrap use: a
// fresh 96-bit nonce per Wrap call, prepended to the ciphertext.
const softwareNonceLength = 12

// wipeFn is secure.Wipe, indirected so a test can observe the one thing
// this provider must do with its in-memory key copy and cannot otherwise
// see -- the same device sdk/go/transport/embedded's own wipeFn is, for
// the same reason. Never swapped outside a test.
var wipeFn = secure.Wipe

// randReader is crypto/rand's global Reader, indirected so a test can
// force Wrap's nonce generation to fail -- a branch otherwise unreachable
// in production (the system entropy source does not fail). Never swapped
// outside a test.
var randReader io.Reader = rand.Reader

// newGCM constructs the AES-256-GCM AEAD over a key, indirected so a test
// can force the construction-failure branch -- otherwise unreachable in
// production, since readKeyFile only ever hands this a key already
// confirmed to be exactly 32 bytes, which aes.NewCipher accepts
// unconditionally, and cipher.NewGCM never fails over a valid
// cipher.Block. Never swapped outside a test.
var newGCM = func(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// SoftwareProvider is qrdb's built-in Provider: it wraps and unwraps with
// an operator-supplied 32-byte key read from a key file, using
// AES-256-GCM from the Go standard library (crypto/aes + crypto/cipher,
// both FIPS-capable).
//
// Every key file this provider ever opens lives directly inside ONE
// configured directory, opened once at construction as an *os.Root
// (NewSoftwareProvider) -- the same confinement discipline
// pkg/audit's trailDir holds for the compliance trail. keyRef is a bare
// file NAME inside that directory, never a path: it must not contain a
// path separator, and must not be "", "." or "..". A keyRef that fails
// that check, or that names something other than a regular, exactly
// 32-byte file, is refused with *KeyAccessError -- a single, detail-free
// error for every one of those causes, logged with full detail on the
// server only (see KeyAccessError's own doc comment for why: a caller of
// Wrap/Unwrap may be relaying a value a remote client chose, and this
// provider must not become a filesystem oracle for it).
//
// Every Wrap/Unwrap call reads the key file fresh, uses it, and zeroes its
// own in-memory copy (secure.Wipe) before returning -- the key is never
// cached across calls.
type SoftwareProvider struct {
	root *os.Root
	dir  string // server-log detail only; never returned to a caller
}

// Compile-time interface check.
var _ Provider = (*SoftwareProvider)(nil)

// NewSoftwareProvider returns a Provider whose key files live only inside
// keyDir, opened as an *os.Root. Returns *KeyDirectoryError when keyDir
// cannot be opened (missing, not a directory, no permission) -- a
// construction-time, operator-facing failure, distinct from the
// detail-free *KeyAccessError every Wrap/Unwrap failure returns.
func NewSoftwareProvider(keyDir string) (*SoftwareProvider, error) {
	root, err := os.OpenRoot(keyDir)
	if err != nil {
		return nil, &KeyDirectoryError{Dir: keyDir, Err: err}
	}
	return &SoftwareProvider{root: root, dir: keyDir}, nil
}

// ProviderID returns "software".
func (p *SoftwareProvider) ProviderID() string {
	return SoftwareProviderID
}

// Wrap encrypts dek with AES-256-GCM under the 32-byte key named by keyRef
// (a bare file name inside this provider's configured key directory),
// returning a fresh 12-byte nonce prepended to the ciphertext.
func (p *SoftwareProvider) Wrap(ctx context.Context, keyRef string, dek []byte) ([]byte, error) {
	gcm, key, err := p.gcm(keyRef)
	if err != nil {
		return nil, err
	}
	defer wipeFn(key)

	nonce := make([]byte, softwareNonceLength)
	if _, err := io.ReadFull(randReader, nonce); err != nil {
		return nil, &WrapFailedError{Err: err}
	}
	return gcm.Seal(nonce, nonce, dek, nil), nil
}

// Unwrap decrypts wrapped (a nonce prepended to AES-256-GCM ciphertext,
// exactly what Wrap returned) under the 32-byte key named by keyRef.
// Returns *UnwrapFailedError when authentication fails -- the wrong key,
// or wrapped was tampered with -- and *KeyAccessError when keyRef itself
// could not be resolved to a usable key file.
func (p *SoftwareProvider) Unwrap(ctx context.Context, keyRef string, wrapped []byte) ([]byte, error) {
	gcm, key, err := p.gcm(keyRef)
	if err != nil {
		return nil, err
	}
	defer wipeFn(key)

	if len(wrapped) < softwareNonceLength {
		return nil, &UnwrapFailedError{}
	}
	nonce, ciphertext := wrapped[:softwareNonceLength], wrapped[softwareNonceLength:]
	dek, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, &UnwrapFailedError{}
	}
	return dek, nil
}

// gcm reads and validates the key file named by keyRef and returns a ready
// cipher.AEAD over it, along with the raw key bytes so the caller can wipe
// them once done. The returned AEAD does not retain a reference to key.
func (p *SoftwareProvider) gcm(keyRef string) (cipher.AEAD, []byte, error) {
	key, err := p.readKeyFile(keyRef)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		wipeFn(key)
		slog.Error("keywrap: constructing the AEAD cipher over an otherwise-valid key failed",
			"dir", p.dir, "error", err)
		return nil, nil, &WrapFailedError{Err: err}
	}
	return gcm, key, nil
}

// validKeyRefName reports whether keyRef is an acceptable bare file name:
// non-empty, not "." or "..", and free of any path separator. os.Root
// itself already refuses a name that would escape the root, but this
// check rejects the attempt before ever asking the filesystem, and gives
// every rejection reason (a separator, "..", or an outright escape) the
// identical, detail-free KeyAccessError.
func validKeyRefName(keyRef string) bool {
	if keyRef == "" || keyRef == "." || keyRef == ".." {
		return false
	}
	return !strings.ContainsAny(keyRef, `/\`)
}

// readKeyFile opens keyRef inside this provider's confined key directory
// and returns its contents, refusing with *KeyAccessError -- carrying no
// detail of its own -- unless keyRef is a bare name resolving to a
// regular, exactly 32-byte file. Every rejection reason is logged via slog
// with full detail (the directory, the rejected name, the underlying OS
// error or the file's mode) on the server only; see KeyAccessError's own
// doc comment for why none of that crosses into the returned error.
//
// The read is capped at softwareKeyReadLimit bytes regardless of the
// file's actual size, so a key reference naming an arbitrarily large (or
// infinite, e.g. /dev/zero) file costs the same bounded amount of work as
// a correctly-sized one -- never a denial-of-service lever.
func (p *SoftwareProvider) readKeyFile(keyRef string) ([]byte, error) {
	if !validKeyRefName(keyRef) {
		slog.Error("keywrap: rejected key reference naming something other than a bare file name",
			"dir", p.dir, "key_ref", keyRef)
		return nil, &KeyAccessError{}
	}

	f, err := p.root.Open(keyRef)
	if err != nil {
		slog.Error("keywrap: key file open failed",
			"dir", p.dir, "key_ref", keyRef, "error", err)
		return nil, &KeyAccessError{}
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		slog.Error("keywrap: key file stat failed",
			"dir", p.dir, "key_ref", keyRef, "error", err)
		return nil, &KeyAccessError{}
	}
	if !info.Mode().IsRegular() {
		slog.Error("keywrap: key reference does not name a regular file",
			"dir", p.dir, "key_ref", keyRef, "mode", info.Mode().String())
		return nil, &KeyAccessError{}
	}

	data, err := io.ReadAll(io.LimitReader(f, softwareKeyReadLimit))
	if err != nil {
		wipeFn(data)
		slog.Error("keywrap: key file read failed",
			"dir", p.dir, "key_ref", keyRef, "error", err)
		return nil, &KeyAccessError{}
	}
	if len(data) != softwareKeyLength {
		wipeFn(data)
		slog.Error("keywrap: key file is not exactly 32 bytes",
			"dir", p.dir, "key_ref", keyRef, "read_at_least", len(data))
		return nil, &KeyAccessError{}
	}
	return data, nil
}
