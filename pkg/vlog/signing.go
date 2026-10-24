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

package vlog

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"reflect"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// SignatureType names the algorithm a signed head is signed with. The set is
// the two RFC 9162 (Certificate Transparency 2.0) section 2.1.4 allows for a
// signed tree head: Ed25519, and ECDSA over P-256 with SHA-256.
type SignatureType string

const (
	// SignatureEd25519 is an Ed25519 signature (RFC 8032) over the checkpoint
	// body, the software provider's default.
	SignatureEd25519 SignatureType = "ed25519"

	// SignatureECDSAP256SHA256 is an ASN.1 DER ECDSA signature over the
	// SHA-256 digest of the checkpoint body with a P-256 key.
	SignatureECDSAP256SHA256 SignatureType = "ecdsa-p256-sha256"
)

// keyHashes computes each signature type's c2sp.org/signed-note key ID, at
// the revision docs/reference/c2sp-signed-note.md pins: for Ed25519 (type
// 0x01) the SHA-256 of the key name, a newline, 0x01 and the 32-byte key; for
// ECDSA (type 0x02) the SHA-256 of the DER SPKI public key alone.
var keyHashes = map[SignatureType]func(name string, pub crypto.PublicKey) []byte{
	SignatureEd25519: func(name string, pub crypto.PublicKey) []byte {
		h := sha256.New()
		h.Write([]byte(name))
		h.Write([]byte{'\n', 0x01})
		h.Write(pub.(ed25519.PublicKey))
		return h.Sum(nil)
	},
	SignatureECDSAP256SHA256: func(_ string, pub crypto.PublicKey) []byte {
		// A P-256 key always marshals.
		der, _ := x509.MarshalPKIXPublicKey(pub)
		sum := sha256.Sum256(der)
		return sum[:]
	},
}

// FirstKeyVersion is the version of the key a log is created with. Rotation
// appends k/<version+1> in one proposal.
const FirstKeyVersion uint32 = 1

// SigningKey is a log's head-signing key as its k/<version> record holds it:
// the PKCS#8 encoding of an Ed25519 or P-256 private key. The record is in the
// log's table, so the store's at-rest barrier seals it as it seals the log's
// other records, it is in the snapshot, and KV Range never returns it.
type SigningKey struct {
	Version uint32
	PKCS8   []byte
}

// GenerateSigningKey returns a new key of type t at version, from crypto/rand.
func GenerateSigningKey(t SignatureType, version uint32) (SigningKey, error) {
	var (
		key crypto.Signer
		err error
	)
	switch t {
	case SignatureEd25519:
		_, key, err = ed25519.GenerateKey(rand.Reader)
	case SignatureECDSAP256SHA256:
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	default:
		return SigningKey{}, &UnsupportedKeyTypeError{Type: string(t)}
	}
	if err != nil {
		return SigningKey{}, &InvalidSigningKeyError{Reason: "generate: " + err.Error()}
	}
	return NewSigningKey(version, key)
}

// NewSigningKey encodes key as the record at version. A key that is neither
// Ed25519 nor ECDSA P-256 is refused with an *UnsupportedKeyTypeError.
func NewSigningKey(version uint32, key crypto.Signer) (SigningKey, error) {
	if version == 0 {
		return SigningKey{}, &InvalidSigningKeyError{Reason: "key version 0"}
	}
	if _, err := signatureTypeOf(key); err != nil {
		return SigningKey{}, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return SigningKey{}, &InvalidSigningKeyError{Reason: "encode: " + err.Error()}
	}
	return SigningKey{Version: version, PKCS8: der}, nil
}

// Signer decodes the key and returns it with its signature type.
func (k SigningKey) Signer() (crypto.Signer, SignatureType, error) {
	if k.Version == 0 {
		return nil, "", &InvalidSigningKeyError{Reason: "key version 0"}
	}
	parsed, err := x509.ParsePKCS8PrivateKey(k.PKCS8)
	if err != nil {
		return nil, "", &InvalidSigningKeyError{Reason: "decode: " + err.Error()}
	}
	key, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, "", &UnsupportedKeyTypeError{Type: reflect.TypeOf(parsed).String()}
	}
	t, err := signatureTypeOf(key)
	if err != nil {
		return nil, "", err
	}
	return key, t, nil
}

// signatureTypeOf maps a private key to the signature type it signs with.
func signatureTypeOf(key crypto.Signer) (SignatureType, error) {
	return publicSignatureType(key.Public())
}

// publicSignatureType maps a public key to the signature type it verifies.
func publicSignatureType(pub crypto.PublicKey) (SignatureType, error) {
	switch p := pub.(type) {
	case ed25519.PublicKey:
		return SignatureEd25519, nil
	case *ecdsa.PublicKey:
		if p.Curve == elliptic.P256() {
			return SignatureECDSAP256SHA256, nil
		}
		return "", &UnsupportedKeyTypeError{Type: "ecdsa " + p.Curve.Params().Name}
	default:
		return "", &UnsupportedKeyTypeError{Type: reflect.TypeOf(pub).String()}
	}
}

// keyKey is the record holding a log's signing key at version.
func keyKey(id LogID, version uint32) []byte {
	return binary.BigEndian.AppendUint32(append(logPrefix(id), 'k', '/'), version)
}

// ReadSigningKey reads the log's signing key at version. A log without one
// answers a *KeyNotFoundError.
func ReadSigningKey(r storage.Reader, id LogID, version uint32) (*SigningKey, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	if _, _, _, err := readLogHead(r.Get, id); err != nil {
		return nil, err
	}
	v, err := r.Get(keyKey(id, version))
	if err != nil {
		return nil, &TableReadError{Err: err}
	}
	if v == nil {
		return nil, &KeyNotFoundError{ID: id, Version: version}
	}
	return &SigningKey{Version: version, PKCS8: bytes.Clone(v)}, nil
}

// CheckpointBody is the body of the checkpoint for the tree of size entries
// with root, in the c2sp.org/tlog-checkpoint form: the origin, the decimal
// size and the base64 root, each on its own line. It is the message a head
// signature covers.
func CheckpointBody(origin string, size uint64, root []byte) []byte {
	b := make([]byte, 0, len(origin)+base64.StdEncoding.EncodedLen(len(root))+23)
	b = append(b, origin...)
	b = append(b, '\n')
	b = strconv.AppendUint(b, size, 10)
	b = append(b, '\n')
	b = base64.StdEncoding.AppendEncode(b, root)
	return append(b, '\n')
}

// SignedHead is a signed checkpoint: the tree it describes, and the signature
// with its type and the version of the key that made it, so a verifier picks
// the key from the head. Its Encode form is the checkpoint record's value.
type SignedHead struct {
	Size          uint64        `json:"size"`
	Root          []byte        `json:"root"`
	KeyVersion    uint32        `json:"key_version"`
	SignatureType SignatureType `json:"signature_type"`
	Signature     []byte        `json:"signature"`
}

// Encode returns the head's record form.
func (h SignedHead) Encode() []byte {
	// Integers, byte slices and a string always marshal.
	b, _ := json.Marshal(h)
	return b
}

// DecodeSignedHead parses a head's record form.
func DecodeSignedHead(b []byte) (SignedHead, error) {
	var h SignedHead
	if err := json.Unmarshal(b, &h); err != nil {
		return SignedHead{}, &HeadSignatureError{Reason: "malformed signed head: " + err.Error()}
	}
	if _, ok := keyHashes[h.SignatureType]; !ok {
		return SignedHead{}, &UnsupportedKeyTypeError{Type: string(h.SignatureType)}
	}
	return h, nil
}

// verifyFuncs verifies a signature of each type over msg.
var verifyFuncs = map[SignatureType]func(pub crypto.PublicKey, msg, sig []byte) bool{
	SignatureEd25519: func(pub crypto.PublicKey, msg, sig []byte) bool {
		return ed25519.Verify(pub.(ed25519.PublicKey), msg, sig)
	},
	SignatureECDSAP256SHA256: func(pub crypto.PublicKey, msg, sig []byte) bool {
		digest := sha256.Sum256(msg)
		return ecdsa.VerifyASN1(pub.(*ecdsa.PublicKey), digest[:], sig)
	},
}

// VerifyHead checks h's signature over the checkpoint body for origin with
// pub. A key whose type is not the head's signature type fails.
func VerifyHead(pub crypto.PublicKey, origin string, h SignedHead) error {
	t, err := publicSignatureType(pub)
	if err != nil {
		return err
	}
	if t != h.SignatureType {
		return &HeadSignatureError{Reason: "head signed with " + string(h.SignatureType) + ", key is " + string(t)}
	}
	if !verifyFuncs[t](pub, CheckpointBody(origin, h.Size, h.Root), h.Signature) {
		return &HeadSignatureError{Reason: "signature does not verify"}
	}
	return nil
}

// HeadSigner signs a log's checkpoints with the log owner's key. Name, KeyHash
// and Sign have the shape of golang.org/x/mod/sumdb/note's Signer with a
// context added; KeyVersion, SignatureType and Public let the service record
// which key signed and check the signature before it proposes the head.
type HeadSigner interface {
	// Name is the key name the checkpoint's signature line carries.
	Name() string
	// KeyHash is the key hash the signature line carries.
	KeyHash() uint32
	// KeyVersion is the version of the log's key that signs.
	KeyVersion() uint32
	// SignatureType is the type of the signatures Sign returns.
	SignatureType() SignatureType
	// Public is the key a signature verifies with.
	Public() crypto.PublicKey
	// Sign returns the signature over msg, the checkpoint body.
	Sign(ctx context.Context, msg []byte) ([]byte, error)
}

// HeadSignerProvider returns the signer for one log's owner key.
type HeadSignerProvider interface {
	HeadSigner(ctx context.Context, req HeadSignerRequest) (HeadSigner, error)
}

// HeadSignerRequest names the log and the owner's key for it.
type HeadSignerRequest struct {
	TenantID string
	Database string
	Log      string
	// KeyRef is the owner's opaque reference to its key, recorded in the
	// descriptor at create; for the software provider, the key version.
	KeyRef string
}

// SignHead signs the tree of size entries with root, and verifies the
// signature with s.Public before returning the head.
func SignHead(ctx context.Context, s HeadSigner, origin string, size uint64, root []byte) (SignedHead, error) {
	t := s.SignatureType()
	if _, ok := keyHashes[t]; !ok {
		return SignedHead{}, &UnsupportedKeyTypeError{Type: string(t)}
	}
	sig, err := s.Sign(ctx, CheckpointBody(origin, size, root))
	if err != nil {
		return SignedHead{}, &HeadSignatureError{Reason: "sign: " + err.Error()}
	}
	h := SignedHead{Size: size, Root: bytes.Clone(root), KeyVersion: s.KeyVersion(), SignatureType: t, Signature: sig}
	if err := VerifyHead(s.Public(), origin, h); err != nil {
		return SignedHead{}, err
	}
	return h, nil
}

// SigningKeyReader reads a log's signing key at a version, through the group
// that holds the log.
type SigningKeyReader interface {
	ReadSigningKey(ctx context.Context, id LogID, version uint32) (*SigningKey, error)
}

// ParseKeyRef parses a software key reference, the key version in decimal.
func ParseKeyRef(ref string) (uint32, error) {
	v, err := strconv.ParseUint(ref, 10, 32)
	if err != nil || v == 0 {
		return 0, &InvalidSigningKeyError{Reason: "key reference " + strconv.Quote(ref) + " is not a key version"}
	}
	return uint32(v), nil
}

// KeyRef is the software key reference for version.
func KeyRef(version uint32) string {
	return strconv.FormatUint(uint64(version), 10)
}

// KeyHash is the c2sp.org/signed-note key ID of pub under name: the first
// four bytes, big-endian, of the hash keyHashes assigns its signature type.
func KeyHash(name string, pub crypto.PublicKey) (uint32, error) {
	t, err := publicSignatureType(pub)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(keyHashes[t](name, pub)), nil
}
