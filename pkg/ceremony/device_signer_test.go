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

package ceremony

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"io"
	"testing"
)

// deviceSigner stands in for a key held on a device (TPM, PKCS#11, cloud
// KMS): it offers its public key and signs, and nothing else. It is not an
// *ecdsa.PrivateKey, so no code path can marshal its private half, and a
// test that succeeds with it proves the key bytes were never needed.
type deviceSigner struct {
	key *ecdsa.PrivateKey
}

var _ crypto.Signer = deviceSigner{}

func newDeviceSigner(t *testing.T) deviceSigner {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate device key: %v", err)
	}
	return deviceSigner{key: key}
}

func (s deviceSigner) Public() crypto.PublicKey { return s.key.Public() }

func (s deviceSigner) Sign(r io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return s.key.Sign(r, digest, opts)
}

// The premise the device tests rest on: the signer's private half cannot be
// exported.
func TestDeviceSigner_KeyNotExportable(t *testing.T) {
	if _, err := x509.MarshalPKCS8PrivateKey(newDeviceSigner(t)); err == nil {
		t.Fatal("deviceSigner private key marshalled; it must be opaque")
	}
}
