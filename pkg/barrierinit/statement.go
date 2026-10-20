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
	"crypto/sha256"
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

// MaxRosterLength bounds Roster, which lists every share holder and so
// outgrows MaxFieldLength well before a Shamir split's 255 shares.
const MaxRosterLength = 128 << 10

// InitStatement is one epoch of a barrier's initialisation, signed by the
// barrier owner. Every field but Signature is covered by the signature.
type InitStatement struct {
	// Barrier is the barrier this statement initialises.
	Barrier clusterseal.Barrier `json:"barrier"`
	// Epoch is 1 for the first initialisation and increases by one with every
	// re-key or strategy change.
	Epoch uint64 `json:"epoch"`
	// IssuedAt is when the statement was signed, in whole seconds since the
	// Unix epoch. It is signed, and a certified statement's certificate is
	// validated at it rather than at the verifier's clock (RFC 5280 section
	// 6.1, validation at a chosen time), so a statement signed while its
	// certificate was valid still verifies after that certificate expires.
	IssuedAt int64 `json:"issued_at"`
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
	// Roster is the encoded share holders' roster RosterHash commits to, which
	// a node holding no key yet admits its first unseal quorum against.
	Roster []byte `json:"roster,omitempty"`
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
	msg = binary.BigEndian.AppendUint64(msg, uint64(s.IssuedAt))
	field([]byte(s.StrategyID))
	field([]byte(s.KeyRef))
	msg = binary.BigEndian.AppendUint64(msg, uint64(s.Threshold))
	field(s.RosterHash)
	field(s.Roster)
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
	// Platform holds the cluster CA, and is set only for a barrier whose owner
	// chose platform custody (auto-unseal): a node certificate chaining to it
	// then signs for the owner, so the barrier stays unattended. Nil for every
	// barrier whose owner keeps custody, so the platform cannot sign for it.
	Platform *x509.CertPool
	// At is the time a statement is verified at: one issued after it is
	// refused. Zero is now. A certificate is validated at the statement's
	// issue time, not at At.
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
// one -- signed by signer, the first admin's key, at issuedAt. It carries no
// key and names no key reference, which no init request carries either. Every
// node serving the barrier verifies it against the first admin pinned when the
// tenant was onboarded, and initializes its own key from it. A nil signer
// signs nothing, and a node then refuses to initialize a barrier that more
// than one node serves.
func SignTenantInit(signer crypto.Signer, tenantID, strategy string, issuedAt time.Time) ([]byte, error) {
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
		IssuedAt:   issuedAt.Unix(),
		StrategyID: strategyID,
	})
	if err != nil {
		return nil, err
	}
	return signed.Marshal()
}

// SignTenantShamirInit returns the encoded statement the second request of a
// Shamir tenant barrier init carries: tenantID's epoch-1 Shamir statement
// signing the split the first request returned -- its threshold, roster and
// unseal key check value -- signed by signer, the first admin's key, at
// issuedAt. Every other node serving the barrier arms its first unseal quorum
// against it. A nil signer signs nothing, as for SignTenantInit.
func SignTenantShamirInit(signer crypto.Signer, tenantID string, threshold int, roster, check []byte, issuedAt time.Time) ([]byte, error) {
	if signer == nil {
		return nil, nil
	}
	hash := sha256.Sum256(roster)
	signed, err := Sign(signer, InitStatement{
		Barrier:        clusterseal.TenantBarrier(tenantID),
		Epoch:          1,
		IssuedAt:       issuedAt.Unix(),
		StrategyID:     seal.StrategyShamir,
		Threshold:      threshold,
		RosterHash:     hash[:],
		Roster:         roster,
		UnsealKeyCheck: check,
	})
	if err != nil {
		return nil, err
	}
	return signed.Marshal()
}

// SignPlatformInit returns the encoded epoch-1 statement the platform claims
// for tenantID's barrier when the tenant chose platform custody (auto-unseal):
// the system-barrier strategy, signed by signer, the creating node's key, under
// certificate, its DER node certificate, at issuedAt. No first admin is asked
// to sign a barrier the platform keeps; every node serving it verifies the
// statement against the cluster CA (Anchors.Platform) as of issuedAt, which
// keeps it verifiable after the node certificate expires, and initializes its
// own key.
func SignPlatformInit(signer crypto.Signer, certificate []byte, tenantID string, issuedAt time.Time) ([]byte, error) {
	s := InitStatement{
		Barrier:           clusterseal.TenantBarrier(tenantID),
		Epoch:             1,
		IssuedAt:          issuedAt.Unix(),
		StrategyID:        seal.StrategySystemBarrier,
		SignerCertificate: certificate,
	}
	if len(certificate) == 0 {
		return nil, s.refuse(ReasonUntrustedCertificate, nil)
	}
	signed, err := Sign(signer, s)
	if err != nil {
		return nil, err
	}
	return signed.Marshal()
}

// Verify reports whether s is well formed and signed by the barrier owner:
// at epoch 1 the pending first admin, later a key certified by the owner's CA
// -- or by the cluster CA, when a.Platform is set because the owner chose
// platform custody. Under platform custody the platform signs epoch 1 too
// (SignPlatformInit): an epoch-1 statement carrying a certificate verifies
// only against a.Platform, never the owner's CA, and is refused outright for a
// barrier whose owner keeps custody. A statement issued after a.At is refused,
// with no allowance for clock skew. Every refusal is an *InitStatementError.
func Verify(s InitStatement, a Anchors) error {
	if err := s.wellFormed(true); err != nil {
		return err
	}
	at := a.At
	if at.IsZero() {
		at = time.Now()
	}
	if s.issued().After(at) {
		return s.refuse(ReasonIssuedInFuture, nil)
	}
	parsed, err := x509.ParsePKIXPublicKey(s.SignerSPKI)
	if err != nil {
		return s.refuse(ReasonMalformedKey, err)
	}
	signer := certifiedSigned
	switch {
	case s.Epoch == 1 && len(s.SignerCertificate) > 0:
		// A certified epoch 1 is the platform's, signing for an owner who
		// chose platform custody: only the cluster CA vouches for it.
		if a.Platform == nil {
			return s.refuse(ReasonNoAnchor, nil)
		}
		a.CA = nil
	case s.Epoch == 1:
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
// certificate that was valid at the statement's issue time and chained then to
// the owner's CA, or to the cluster CA when the owner chose platform custody.
func certifiedSigned(s InitStatement, a Anchors, _ crypto.PublicKey) error {
	if a.CA == nil && a.Platform == nil {
		return s.refuse(ReasonNoAnchor, nil)
	}
	if len(s.SignerCertificate) == 0 {
		return s.refuse(ReasonUntrustedCertificate, nil)
	}
	cert, err := x509.ParseCertificate(s.SignerCertificate)
	if err != nil {
		return s.refuse(ReasonUntrustedCertificate, err)
	}
	issued := s.issued()
	if issued.Before(cert.NotBefore) || issued.After(cert.NotAfter) {
		return s.refuse(ReasonNotValidAtIssue, nil)
	}
	var chainErr error
	for _, roots := range []*x509.CertPool{a.CA, a.Platform} {
		if roots == nil {
			continue
		}
		if _, chainErr = cert.Verify(x509.VerifyOptions{
			Roots:       roots,
			CurrentTime: issued,
			KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}); chainErr == nil {
			break
		}
	}
	if chainErr != nil {
		return s.refuse(ReasonUntrustedCertificate, chainErr)
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
		s.IssuedAt <= 0,
		s.StrategyID == "",
		s.Threshold < 0,
		len(s.SignerSPKI) == 0,
		signed && len(s.Signature) == 0:
		return s.refuse(ReasonMalformed, nil)
	}
	// A Shamir statement signs the split the first node prepared: without
	// its roster and check value no other node could arm its first quorum.
	if s.StrategyID == seal.StrategyShamir && (len(s.Roster) == 0 || len(s.UnsealKeyCheck) == 0) {
		return s.refuse(ReasonMalformed, nil)
	}
	for _, n := range []int{len(s.Barrier), len(s.StrategyID), len(s.KeyRef), len(s.RosterHash),
		len(s.VerifierSalt), len(s.UnsealKeyCheck), len(s.SignerSPKI), len(s.SignerCertificate), len(s.Signature)} {
		if n > MaxFieldLength {
			return s.refuse(ReasonMalformed, nil)
		}
	}
	if len(s.Roster) > MaxRosterLength {
		return s.refuse(ReasonMalformed, nil)
	}
	if len(s.Roster) > 0 {
		if sum := sha256.Sum256(s.Roster); !bytes.Equal(sum[:], s.RosterHash) {
			return s.refuse(ReasonMalformed, nil)
		}
	}
	return nil
}

// MaxEncodedLength bounds an encoded statement: every field at
// MaxFieldLength and Roster at MaxRosterLength, base64-expanded, with room
// for the JSON around them.
const MaxEncodedLength = 16*MaxFieldLength + 2*MaxRosterLength

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
	// Threshold, Roster and UnsealKeyCheck are the Shamir split the claiming
	// node prepared, which a Shamir statement must sign exactly. All zero for
	// every other strategy, whose statement carries none of them.
	Threshold      int
	Roster         []byte
	UnsealKeyCheck []byte
}

// Matches reports whether s names exactly e: a statement signed for other
// parameters than the request carries is refused with *InitStatementError,
// so no node seals under parameters its owner did not sign.
func (s InitStatement) Matches(e Expect) error {
	if s.Barrier != e.Barrier || s.Epoch != e.Epoch || s.StrategyID != e.StrategyID || s.KeyRef != e.KeyRef ||
		s.Threshold != e.Threshold || !bytes.Equal(s.Roster, e.Roster) || !bytes.Equal(s.UnsealKeyCheck, e.UnsealKeyCheck) {
		return s.refuse(ReasonMismatch, nil)
	}
	return nil
}

// issued is the statement's signed issue time.
func (s InitStatement) issued() time.Time {
	return time.Unix(s.IssuedAt, 0)
}

func (s InitStatement) refuse(reason Reason, err error) *InitStatementError {
	return &InitStatementError{Barrier: s.Barrier, Epoch: s.Epoch, Reason: reason, Err: err}
}
