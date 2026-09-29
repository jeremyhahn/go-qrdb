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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeSPKIPin_ValidCert(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	cert, err := x509.ParseCertificate(certDER)
	require.NoError(t, err)

	pin, err := computeSPKIPin(cert)
	require.NoError(t, err)
	assert.NotEmpty(t, pin)

	// Pin should be deterministic.
	pin2, err := computeSPKIPin(cert)
	require.NoError(t, err)
	assert.Equal(t, pin, pin2)
}

func TestComputeSPKIPin_NilCert(t *testing.T) {
	pin, err := computeSPKIPin(nil)
	assert.Empty(t, pin)
	assert.Error(t, err)

	var spkiErr *SPKIPinError
	assert.ErrorAs(t, err, &spkiErr)
}

// TestThumbprintsEqual pins the constant-time thumbprint comparison,
// including its refusal of an empty presented or enrolled value.
func TestThumbprintsEqual(t *testing.T) {
	assert.True(t, thumbprintsEqual("abc", "abc"))
	assert.False(t, thumbprintsEqual("abc", "abd"))
	assert.False(t, thumbprintsEqual("", ""))
	assert.False(t, thumbprintsEqual("abc", ""))
}

// TestParsePublicKeyPEM covers the accepted and refused inputs.
func TestParsePublicKeyPEM(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	pub, err := parsePublicKeyPEM(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})))
	require.NoError(t, err)
	assert.True(t, key.PublicKey.Equal(pub))

	_, err = parsePublicKeyPEM("not pem")
	assert.Error(t, err)
	_, err = parsePublicKeyPEM(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte{1, 2}})))
	assert.Error(t, err)
}
