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

package custodian

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"io"

	"golang.org/x/crypto/hkdf"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

const (
	// envelopeHKDFInfo is the HKDF context/info string binding this envelope
	// format to go-qrdb's Shamir share delivery protocol.
	envelopeHKDFInfo = "go-qrdb/shamir/envelope/v1"

	// envelopeKeyLen is the derived AES-256 content key length in bytes.
	envelopeKeyLen = 32

	// envelopeNonceLen is the AES-GCM nonce length in bytes.
	envelopeNonceLen = 12
)

// SealedEnvelope is the ECDH+AES-256-GCM encrypted container for a Shamir
// share. The envelope is opaque to the server after creation; only the
// recipient's private key can unseal it.
//
// Wire format (all fields required for decryption by the custodian client):
//
//	EphemeralPubKeyPEM — PEM-encoded P-256 ephemeral public key
//	Nonce              — 12-byte AES-GCM nonce (hex-encoded)
//	Ciphertext         — AES-256-GCM ciphertext of the share (hex-encoded)
type SealedEnvelope struct {
	// EphemeralPubKeyPEM is the server-generated ephemeral P-256 public key.
	// The custodian's client performs ECDH(recipientPrivKey, ephemeralPubKey)
	// to recover the shared secret and then derives the content key via
	// HKDF-SHA256.
	EphemeralPubKeyPEM string `json:"ephemeralPubKeyPEM"`

	// Nonce is the 12-byte random AES-GCM nonce used for encryption.
	Nonce []byte `json:"nonce"`

	// Ciphertext is the AES-256-GCM ciphertext. The 16-byte GCM
	// authentication tag is appended in-place (standard Go aeadSeal layout).
	Ciphertext []byte `json:"ciphertext"`
}

// SealShareTo encrypts shareBytes to recipientPubKeyPEM using P-256 ECDH +
// HKDF-SHA256 + AES-256-GCM. The shareBytes slice is zeroed via secure.Wipe
// before this function returns, regardless of success or failure.
//
// The caller must never store or log shareBytes after this call.
//
// NOTE: UnsealShareWith is the custodian-side inverse and is NOT implemented
// in the qrdb daemon. Custodian client software performs ECDH with their
// hardware private key, derives the content key via HKDF-SHA256 with info
// "go-qrdb/shamir/envelope/v1", and decrypts with AES-256-GCM.
// See docs/seal/custodians.md for the full protocol.
func SealShareTo(shareBytes []byte, recipientPubKeyPEM string) (*SealedEnvelope, error) {
	defer secure.Wipe(shareBytes)

	// Parse recipient public key.
	recipientPub, err := parseECDHPublicKey(recipientPubKeyPEM)
	if err != nil {
		return nil, ErrInvalidPublicKey
	}

	// Generate ephemeral P-256 key pair.
	ephemeralPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, ErrEnvelopeSeal
	}

	// Perform ECDH to derive shared secret.
	sharedSecret, err := ephemeralPriv.ECDH(recipientPub)
	if err != nil {
		return nil, ErrEnvelopeSeal
	}
	defer secure.Wipe(sharedSecret)

	// Derive content key via HKDF-SHA256.
	contentKey := make([]byte, envelopeKeyLen)
	hkdfReader := hkdf.New(sha256.New, sharedSecret, nil, []byte(envelopeHKDFInfo))
	if _, err := io.ReadFull(hkdfReader, contentKey); err != nil {
		return nil, ErrEnvelopeSeal
	}
	defer secure.Wipe(contentKey)

	// Encrypt share with AES-256-GCM.
	block, err := aes.NewCipher(contentKey)
	if err != nil {
		return nil, ErrEnvelopeSeal
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrEnvelopeSeal
	}

	nonce := make([]byte, envelopeNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrEnvelopeSeal
	}

	// AAD binds the envelope to the protocol version string.
	ciphertext := gcm.Seal(nil, nonce, shareBytes, []byte(envelopeHKDFInfo))

	// PEM-encode the ephemeral public key.
	ephemeralPubDER, err := x509.MarshalPKIXPublicKey(ephemeralPriv.PublicKey())
	if err != nil {
		return nil, ErrEnvelopeSeal
	}
	ephemeralPubPEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: ephemeralPubDER,
	}))

	return &SealedEnvelope{
		EphemeralPubKeyPEM: ephemeralPubPEM,
		Nonce:              nonce,
		Ciphertext:         ciphertext,
	}, nil
}

// parseECDHPublicKey decodes a PEM-encoded PKIX public key and returns it as
// an *ecdh.PublicKey. Only P-256 keys are accepted.
func parseECDHPublicKey(pemStr string) (*ecdh.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, ErrInvalidPublicKey
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, ErrInvalidPublicKey
	}
	ecdhPub, err := convertToECDHPublicKey(pub)
	if err != nil {
		return nil, ErrInvalidPublicKey
	}
	return ecdhPub, nil
}

// convertToECDHPublicKey converts a crypto.PublicKey to an *ecdh.PublicKey.
// Handles both *ecdh.PublicKey and *ecdsa.PublicKey (for P-256).
func convertToECDHPublicKey(pub any) (*ecdh.PublicKey, error) {
	switch k := pub.(type) {
	case *ecdh.PublicKey:
		return k, nil
	case *ecdsa.PublicKey:
		// Convert ecdsa.PublicKey to ecdh.PublicKey via marshaling.
		ecdhKey, err := k.ECDH()
		if err != nil {
			return nil, ErrInvalidPublicKey
		}
		return ecdhKey, nil
	default:
		return nil, ErrInvalidPublicKey
	}
}
