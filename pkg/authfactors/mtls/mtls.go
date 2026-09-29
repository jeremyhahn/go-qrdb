// Copyright 2026 Jeremy Hahn
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

// Package mtls implements the "mtls" authentication factor: proof of
// possession of the private key behind an enrolled X.509 client certificate.
//
// It is a software factor and the one factor go-qrdb ships itself; hardware
// factors (FIDO2, PIV, PKCS#11) are go-xkms implementations of the same
// authfactors.Factor interface. The proof is a signature over a
// server-issued nonce, so it works over every transport — including the
// embedded and Unix-socket transports, which have no TLS session to inspect.
// When the caller DID arrive over a verified TLS session, the server records
// that session's client-certificate thumbprint in the response metadata under
// authfactors.ResponseMetaPeerCertThumbprint, and Verify then additionally
// requires it to be the enrolled certificate: a signature from the enrolled
// key presented over somebody else's session is refused.
package mtls

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
)

const (
	// Name is the factor name.
	Name = "mtls"

	// ParamCertificatePEM is the EnrollParams.MethodParams key carrying the
	// PEM certificate being enrolled.
	ParamCertificatePEM = "certificate_pem"

	// MetaThumbprint is the Enrollment.Metadata / Challenge.MethodData key
	// carrying the lowercase hex SHA-256 thumbprint of the enrolled
	// certificate's DER.
	MetaThumbprint = "thumbprint"

	// MethodDataPublicKey is the Challenge.MethodData key carrying the
	// enrolled certificate's SubjectPublicKeyInfo, standard base64.
	MethodDataPublicKey = "public_key"

	// nonceSize is the challenge nonce length in bytes.
	nonceSize = 32

	// DefaultChallengeTTL is how long a challenge stays answerable.
	DefaultChallengeTTL = 5 * time.Minute
)

// Factor is the "mtls" authfactors.Factor. The zero value is usable and
// issues challenges valid for DefaultChallengeTTL.
type Factor struct {
	// TTL overrides DefaultChallengeTTL when positive.
	TTL time.Duration
}

// Compile-time interface check.
var _ authfactors.Factor = (*Factor)(nil)

// New returns an mtls Factor with the default challenge TTL.
func New() *Factor { return &Factor{} }

// Name returns "mtls".
func (f *Factor) Name() string { return Name }

// Enroll records the public half of the certificate in
// params.MethodParams[ParamCertificatePEM]. The Enrollment's PublicKey is the
// certificate's DER SubjectPublicKeyInfo and its Metadata carries the
// certificate thumbprint.
func (f *Factor) Enroll(_ context.Context, params *authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	if params == nil {
		return nil, &authfactors.EnrollmentInvalidError{Reason: "nil enroll params"}
	}
	raw, _ := params.MethodParams[ParamCertificatePEM].(string)
	cert, err := ParseCertificatePEM([]byte(raw))
	if err != nil {
		return nil, err
	}
	return EnrollmentFromCertificate(params.UserID, params.TenantID, cert), nil
}

// EnrollmentFromCertificate builds the mtls Enrollment for cert without
// round-tripping it through PEM.
func EnrollmentFromCertificate(userID, tenantID string, cert *x509.Certificate) *authfactors.Enrollment {
	thumb := Thumbprint(cert.Raw)
	return &authfactors.Enrollment{
		EnrollmentID: Name + ":" + thumb,
		UserID:       userID,
		FactorName:   Name,
		TenantID:     tenantID,
		PublicKey:    append([]byte(nil), cert.RawSubjectPublicKeyInfo...),
		Metadata:     map[string]string{MetaThumbprint: thumb},
		EnrolledAt:   time.Now().UTC(),
	}
}

// Challenge issues a fresh nonce bound to enrollment. The enrolled public key
// and thumbprint travel in the server-held challenge so Verify needs nothing
// else; they are public material.
func (f *Factor) Challenge(_ context.Context, enrollment *authfactors.Enrollment) (*authfactors.Challenge, error) {
	if enrollment == nil || len(enrollment.PublicKey) == 0 || enrollment.Metadata[MetaThumbprint] == "" {
		return nil, &authfactors.EnrollmentInvalidError{Reason: "mtls enrollment needs a public key and a thumbprint"}
	}
	if _, err := x509.ParsePKIXPublicKey(enrollment.PublicKey); err != nil {
		return nil, &authfactors.EnrollmentInvalidError{EnrollmentID: enrollment.EnrollmentID, Reason: err.Error()}
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ttl := f.TTL
	if ttl <= 0 {
		ttl = DefaultChallengeTTL
	}
	return &authfactors.Challenge{
		ChallengeID:  authfactors.NewChallengeID(),
		EnrollmentID: enrollment.EnrollmentID,
		Nonce:        nonce,
		ExpiresAt:    time.Now().UTC().Add(ttl),
		MethodData: map[string]any{
			MethodDataPublicKey: base64.StdEncoding.EncodeToString(enrollment.PublicKey),
			MetaThumbprint:      enrollment.Metadata[MetaThumbprint],
		},
	}, nil
}

// Verify checks response.Value is the enrolled key's signature over the
// challenge nonce, and — when the server recorded a TLS peer thumbprint in
// response.Metadata — that the peer certificate is the enrolled one.
func (f *Factor) Verify(_ context.Context, challenge *authfactors.Challenge, response *authfactors.Response) error {
	if challenge == nil || response == nil {
		return &authfactors.VerificationFailedError{FactorName: Name, Reason: "missing challenge or response"}
	}
	if challenge.Expired() {
		return &authfactors.ChallengeExpiredError{ChallengeID: challenge.ChallengeID}
	}
	enrolledThumb, _ := challenge.MethodData[MetaThumbprint].(string)
	encodedKey, _ := challenge.MethodData[MethodDataPublicKey].(string)
	der, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil || enrolledThumb == "" {
		return &authfactors.VerificationFailedError{FactorName: Name, Reason: "challenge carries no enrolled key"}
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return &authfactors.VerificationFailedError{FactorName: Name, Reason: "enrolled key unparseable"}
	}
	if peer := response.Metadata[authfactors.ResponseMetaPeerCertThumbprint]; peer != "" &&
		subtle.ConstantTimeCompare([]byte(peer), []byte(enrolledThumb)) != 1 {
		return &authfactors.VerificationFailedError{FactorName: Name, Reason: "TLS peer certificate is not the enrolled certificate"}
	}
	if !VerifyNonceSignature(pub, challenge.Nonce, response.Value) {
		return &authfactors.VerificationFailedError{FactorName: Name, Reason: "signature does not verify"}
	}
	return nil
}

// VerifyNonceSignature reports whether signature is pub's signature over
// nonce: ECDSA (ASN.1 DER) and RSA PKCS#1 v1.5 over SHA-256(nonce), or
// Ed25519 over the nonce itself. Any other key type is refused.
func VerifyNonceSignature(pub crypto.PublicKey, nonce, signature []byte) bool {
	if len(nonce) == 0 || len(signature) == 0 {
		return false
	}
	digest := sha256.Sum256(nonce)
	switch key := pub.(type) {
	case *ecdsa.PublicKey:
		return ecdsa.VerifyASN1(key, digest[:], signature)
	case *rsa.PublicKey:
		return rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature) == nil
	case ed25519.PublicKey:
		return ed25519.Verify(key, nonce, signature)
	default:
		return false
	}
}

// Thumbprint returns the lowercase hex SHA-256 of a certificate's DER. It is
// the canonical certificate identity the enrolment, the roster and the TLS
// peer binding all compare.
func Thumbprint(certDER []byte) string {
	sum := sha256.Sum256(certDER)
	return hex.EncodeToString(sum[:])
}

// ParseCertificatePEM parses the first CERTIFICATE block of pemBytes.
func ParseCertificatePEM(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, &authfactors.EnrollmentInvalidError{Reason: "no PEM CERTIFICATE block"}
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, &authfactors.EnrollmentInvalidError{Reason: err.Error()}
	}
	return cert, nil
}
