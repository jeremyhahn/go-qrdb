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

// Package barrierinit is the barrier owner's signed init statement: what every
// node serving an isolated barrier initialises its own key from, so that no
// key is ever carried between nodes (docs/barrier-seal/architecture.md
// "Per-node barrier keys").
//
// The owner -- the pending first admin at epoch 1, a holder of a certificate
// from the owner's CA after that -- signs an InitStatement naming the sealing
// strategy and its public parameters. Book claims epoch 1 with a guarded
// create-if-absent and advances later epochs with a guarded compare-and-set,
// both through the existing guarded write (no Raft opcode of its own), under
// Prefix in the replicated system store or the standalone one. A Watcher on
// every node delivers each statement that verifies, once per epoch, to the
// node's barrier initialisation.
//
// The statement is public: it holds no key, no share and no passphrase
// verifier an attacker could guess against. A statement the platform or a
// single compromised node writes -- naming a key the platform controls, say --
// does not verify, and every honest node refuses it with *InitStatementError.
// KeyRef is opaque here: qrdb ships no hardware or cloud key code, only the
// seam an injected strategy reads it through.
package barrierinit

import (
	"bytes"
	"crypto"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// messageContext domain-separates the signed encoding from every other
// message the same key signs (initproof.Message, the mtls challenge).
const messageContext = "qrdb-barrier-init-statement-v1"

// MaxFieldLength bounds every variable-length field, so a statement cannot be
// made large enough to burden the system store or the nodes verifying it.
const MaxFieldLength = 16 << 10

// InitStatement is one epoch of a barrier's initialisation, signed by the
// barrier owner. Every field but Signature is covered by the signature.
type InitStatement struct {
	// Barrier is the barrier this statement initialises.
	Barrier clusterseal.Barrier `json:"barrier"`
	// Epoch is 1 for the first initialisation and increases by one with every
	// re-key or strategy change.
	Epoch uint64 `json:"epoch"`
	// StrategyID names the sealing strategy every node seals its own key with.
	StrategyID seal.StrategyID `json:"strategy_id"`
	// KeyRef is the strategy's opaque reference to the tenant's key -- a KMS
	// key or a hardware slot, read only by the injected strategy. Empty for a
	// strategy that names none.
	KeyRef string `json:"key_ref,omitempty"`
	// Threshold is the Shamir threshold; 0 for every other strategy.
	Threshold int `json:"threshold,omitempty"`
	// RosterHash commits to the share holders' roster for a Shamir strategy.
	RosterHash []byte `json:"roster_hash,omitempty"`
	// VerifierSalt is the passphrase KDF salt. No check value is carried for a
	// passphrase: it would be an offline-guessing oracle.
	VerifierSalt []byte `json:"verifier_salt,omitempty"`
	// UnsealKeyCheck is a check value of a high-entropy unseal key (Shamir),
	// which reveals nothing worth guessing at.
	UnsealKeyCheck []byte `json:"unseal_key_check,omitempty"`
	// SignerSPKI is the DER SubjectPublicKeyInfo of the signing key. The
	// whole key is carried, not a hash of it: epoch 1 is anchored by the
	// pending first admin's thumbprint, which cannot verify a signature.
	SignerSPKI []byte `json:"signer_spki"`
	// SignerCertificate is the DER certificate, chaining to the owner's CA,
	// that a statement after epoch 1 is signed under.
	SignerCertificate []byte `json:"signer_certificate,omitempty"`
	// Signature is SignerSPKI's signature over Message.
	Signature []byte `json:"signature"`
}

// Message is the encoding the signature covers: the context, then every field
// but Signature, each byte or string field prefixed with its uint32
// big-endian length and each integer as a uint64 big-endian, so no two
// statements share an encoding.
func (s InitStatement) Message() []byte {
	var msg []byte
	field := func(b []byte) {
		msg = binary.BigEndian.AppendUint32(msg, uint32(len(b)))
		msg = append(msg, b...)
	}
	field([]byte(messageContext))
	field([]byte(s.Barrier))
	msg = binary.BigEndian.AppendUint64(msg, s.Epoch)
	field([]byte(s.StrategyID))
	field([]byte(s.KeyRef))
	msg = binary.BigEndian.AppendUint64(msg, uint64(s.Threshold))
	field(s.RosterHash)
	field(s.VerifierSalt)
	field(s.UnsealKeyCheck)
	field(s.SignerSPKI)
	field(s.SignerCertificate)
	return msg
}

// Anchors is what a node trusts a statement's signer through.
type Anchors struct {
	// FirstAdmin is the SPKI thumbprint (user.PublicKeySPKIThumbprint) of the
	// pending first admin recorded at create: the only signer of epoch 1.
	FirstAdmin string
	// CA holds the owner's CA certificate, pinned in the system tenant: a
	// later epoch's SignerCertificate must chain to it.
	CA *x509.CertPool
	// At is when the certificate must be valid; zero is now.
	At time.Time
}

// Sign records signer's public key in s and signs it. A device-held key signs
// through the same crypto.Signer seam the init proof uses.
func Sign(signer crypto.Signer, s InitStatement) (InitStatement, error) {
	if signer == nil {
		return InitStatement{}, &mtls.UnsupportedSignerError{}
	}
	spki, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return InitStatement{}, s.refuse(ReasonMalformedKey, err)
	}
	s.SignerSPKI, s.Signature = spki, nil
	if err := s.wellFormed(false); err != nil {
		return InitStatement{}, err
	}
	sig, err := mtls.SignNonce(signer, s.Message())
	if err != nil {
		return InitStatement{}, err
	}
	s.Signature = sig
	return s, nil
}

// SignTenantInit returns the encoded statement a tenant barrier init carries
// on every transport (init_statement): tenantID's epoch-1 statement for
// strategy -- the passphrase strategy when empty, as the node reads an unnamed
// one -- signed by signer, the first admin's key. It carries no key and names
// no key reference, which no init request carries either. Every node serving
// the barrier verifies it against the first admin pinned when the tenant was
// onboarded, and initializes its own key from it. A nil signer signs nothing,
// and a node then refuses to initialize a barrier that more than one node
// serves.
func SignTenantInit(signer crypto.Signer, tenantID, strategy string) ([]byte, error) {
	if signer == nil {
		return nil, nil
	}
	strategyID := seal.StrategyID(strategy)
	if strategyID == "" {
		strategyID = seal.StrategyPassphrase
	}
	signed, err := Sign(signer, InitStatement{
		Barrier:    clusterseal.TenantBarrier(tenantID),
		Epoch:      1,
		StrategyID: strategyID,
	})
	if err != nil {
		return nil, err
	}
	return signed.Marshal()
}

// Verify reports whether s is well formed and signed by the barrier owner:
// at epoch 1 the pending first admin, later a key certified by the owner's CA.
// Every refusal is an *InitStatementError.
func Verify(s InitStatement, a Anchors) error {
	if err := s.wellFormed(true); err != nil {
		return err
	}
	parsed, err := x509.ParsePKIXPublicKey(s.SignerSPKI)
	if err != nil {
		return s.refuse(ReasonMalformedKey, err)
	}
	signer := certifiedSigned
	if s.Epoch == 1 {
		signer = firstAdminSigned
	}
	if err := signer(s, a, parsed); err != nil {
		return err
	}
	if !mtls.VerifyNonceSignature(parsed, s.Message(), s.Signature) {
		return s.refuse(ReasonBadSignature, nil)
	}
	return nil
}

// firstAdminSigned checks that the signing key is the pending first admin's.
func firstAdminSigned(s InitStatement, a Anchors, pub crypto.PublicKey) error {
	if a.FirstAdmin == "" {
		return s.refuse(ReasonNoAnchor, nil)
	}
	tp, err := user.PublicKeySPKIThumbprint(pub)
	if err != nil {
		return s.refuse(ReasonMalformedKey, err)
	}
	if tp != a.FirstAdmin {
		return s.refuse(ReasonWrongSigner, nil)
	}
	return nil
}

// certifiedSigned checks that the signing key is the subject of a client
// certificate that chains to the owner's CA and is valid at a.At.
func certifiedSigned(s InitStatement, a Anchors, _ crypto.PublicKey) error {
	if a.CA == nil {
		return s.refuse(ReasonNoAnchor, nil)
	}
	if len(s.SignerCertificate) == 0 {
		return s.refuse(ReasonUntrustedCertificate, nil)
	}
	cert, err := x509.ParseCertificate(s.SignerCertificate)
	if err != nil {
		return s.refuse(ReasonUntrustedCertificate, err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:       a.CA,
		CurrentTime: a.At,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return s.refuse(ReasonUntrustedCertificate, err)
	}
	if !bytes.Equal(cert.RawSubjectPublicKeyInfo, s.SignerSPKI) {
		return s.refuse(ReasonWrongSigner, nil)
	}
	return nil
}

// wellFormed checks every field's presence and size; signed also requires a
// signature.
func (s InitStatement) wellFormed(signed bool) error {
	switch {
	case s.Barrier.Kind() == clusterseal.KindInvalid,
		s.Epoch == 0,
		s.StrategyID == "",
		s.Threshold < 0,
		len(s.SignerSPKI) == 0,
		signed && len(s.Signature) == 0:
		return s.refuse(ReasonMalformed, nil)
	}
	for _, n := range []int{len(s.Barrier), len(s.StrategyID), len(s.KeyRef), len(s.RosterHash),
		len(s.VerifierSalt), len(s.UnsealKeyCheck), len(s.SignerSPKI), len(s.SignerCertificate), len(s.Signature)} {
		if n > MaxFieldLength {
			return s.refuse(ReasonMalformed, nil)
		}
	}
	return nil
}

// MaxEncodedLength bounds an encoded statement: every field at
// MaxFieldLength, base64-expanded, with room for the JSON around them.
const MaxEncodedLength = 16 * MaxFieldLength

// Marshal encodes s as a client sends it with an init request.
func (s InitStatement) Marshal() ([]byte, error) {
	return json.Marshal(s)
}

// Parse decodes a statement a client sent. An oversized or undecodable one is
// refused with *InitStatementError; Parse does not verify the signature.
func Parse(raw []byte) (InitStatement, error) {
	if len(raw) == 0 || len(raw) > MaxEncodedLength {
		return InitStatement{}, &InitStatementError{Reason: ReasonMalformed}
	}
	var s InitStatement
	if err := json.Unmarshal(raw, &s); err != nil {
		return InitStatement{}, &InitStatementError{Reason: ReasonMalformed, Err: err}
	}
	return s, nil
}

// Expect is what a statement must name to accompany one init request.
type Expect struct {
	Barrier    clusterseal.Barrier
	Epoch      uint64
	StrategyID seal.StrategyID
	KeyRef     string
}

// Matches reports whether s names exactly e: a statement signed for other
// parameters than the request carries is refused with *InitStatementError,
// so no node seals under parameters its owner did not sign.
func (s InitStatement) Matches(e Expect) error {
	if s.Barrier != e.Barrier || s.Epoch != e.Epoch || s.StrategyID != e.StrategyID || s.KeyRef != e.KeyRef {
		return s.refuse(ReasonMismatch, nil)
	}
	return nil
}

func (s InitStatement) refuse(reason Reason, err error) *InitStatementError {
	return &InitStatementError{Barrier: s.Barrier, Epoch: s.Epoch, Reason: reason, Err: err}
}
