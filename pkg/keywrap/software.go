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
	"os"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// SoftwareProviderID is the registered id of qrdb's built-in Provider.
const SoftwareProviderID = "software"

// softwareKeyLength is the exact length a software provider's key file
// must be. Not truncated, padded, or hashed to size: a key file of any
// other length is refused outright.
const softwareKeyLength = 32

// softwareNonceLength is the AES-256-GCM nonce length Wrap/Unwrap use: a
// fresh 96-bit nonce per Wrap call, prepended to the ciphertext.
const softwareNonceLength = 12

// wipeFn is secure.Wipe, indirected so a test can observe the one thing
// this provider must do with its in-memory key copy and cannot otherwise
// see -- the same device sdk/go/transport/embedded's own wipeFn is, for
// the same reason. Never swapped outside a test.
var wipeFn = secure.Wipe

// SoftwareProvider is qrdb's built-in Provider: it wraps and unwraps with
// an operator-supplied 32-byte key read from a key file, using
// AES-256-GCM from the Go standard library (crypto/aes + crypto/cipher,
// both FIPS-capable). keyRef IS the key file's path -- this package never
// inspects it beyond opening and reading it.
//
// Every Wrap/Unwrap call reads the key file fresh, uses it, and zeroes its
// own in-memory copy (secure.Wipe) before returning -- the key is never
// cached across calls.
type SoftwareProvider struct{}

// Compile-time interface check.
var _ Provider = (*SoftwareProvider)(nil)

// NewSoftwareProvider returns a Provider backed by a 32-byte key file
// named by keyRef at Wrap/Unwrap time.
func NewSoftwareProvider() *SoftwareProvider {
	return &SoftwareProvider{}
}

// ProviderID returns "software".
func (p *SoftwareProvider) ProviderID() string {
	return SoftwareProviderID
}

// Wrap encrypts dek with AES-256-GCM under the 32-byte key read from the
// file at keyRef, returning a fresh 12-byte nonce prepended to the
// ciphertext. Refuses with *InvalidKeyLengthError when the key file is not
// exactly 32 bytes.
func (p *SoftwareProvider) Wrap(ctx context.Context, keyRef string, dek []byte) ([]byte, error) {
	gcm, key, err := p.gcm(keyRef)
	if err != nil {
		return nil, err
	}
	defer wipeFn(key)

	nonce := make([]byte, softwareNonceLength)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, &WrapFailedError{Err: err}
	}
	return gcm.Seal(nonce, nonce, dek, nil), nil
}

// Unwrap decrypts wrapped (a nonce prepended to AES-256-GCM ciphertext,
// exactly what Wrap returned) under the 32-byte key read from the file at
// keyRef. Returns *UnwrapFailedError when authentication fails -- the
// wrong key, or wrapped was tampered with -- and *InvalidKeyLengthError
// when the key file itself is not exactly 32 bytes.
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

// gcm reads and validates the key file at keyRef and returns a ready
// cipher.AEAD over it, along with the raw key bytes so the caller can wipe
// them once done. The returned AEAD does not retain a reference to key.
func (p *SoftwareProvider) gcm(keyRef string) (cipher.AEAD, []byte, error) {
	key, err := readKeyFile(keyRef)
	if err != nil {
		return nil, nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		wipeFn(key)
		return nil, nil, &WrapFailedError{Err: err}
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		wipeFn(key)
		return nil, nil, &WrapFailedError{Err: err}
	}
	return gcm, key, nil
}

// readKeyFile reads keyRef and refuses with *InvalidKeyLengthError unless
// it is exactly softwareKeyLength bytes. keyRef is never read from a flag
// value or an environment variable by this package -- that policy is the
// CLI's, not this provider's -- it only ever opens the path it is given.
func readKeyFile(keyRef string) ([]byte, error) {
	data, err := os.ReadFile(keyRef)
	if err != nil {
		return nil, &KeyFileError{Path: keyRef, Err: err}
	}
	if len(data) != softwareKeyLength {
		wipeFn(data)
		return nil, &InvalidKeyLengthError{Length: len(data)}
	}
	return data, nil
}
