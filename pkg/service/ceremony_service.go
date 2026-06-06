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

package service

import (
	"context"
	"encoding/hex"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
)

// CeremonyServicer provides ceremony lifecycle operations including
// initialization, certificate claiming, share distribution, CSR signing,
// and status queries.
type CeremonyServicer interface {
	// CeremonyInitialize performs the initialization ceremony, issuing TLS
	// and SO certificates and optionally starting M-of-N enrollment. The
	// opts argument carries per-request parameters (notably the SO and user
	// PINs); per-request PINs take precedence over the construction-time
	// configuration when non-empty. opts may be nil.
	CeremonyInitialize(ctx context.Context, ca ceremony.CeremonyCA, opts *CeremonyInitializeOptions) (*CeremonyInitResult, error)

	// CeremonyBeginClaimCert starts the certificate claim handshake for
	// the given officer username, returning a challenge nonce.
	CeremonyBeginClaimCert(ctx context.Context, username string) (*CeremonyChallenge, error)

	// CeremonyCompleteClaimCert completes the certificate claim handshake
	// by verifying the officer's nonce signature and returning the certificate.
	CeremonyCompleteClaimCert(ctx context.Context, username, nonceHex string, signature []byte) (*CeremonyClaimCertResult, error)

	// CeremonyClaimShare retrieves the Shamir share for the given officer
	// username.
	CeremonyClaimShare(ctx context.Context, username string) ([]byte, error)

	// CeremonySignCSR signs a CSR during the ceremony for an officer or
	// service, returning the issued certificate.
	CeremonySignCSR(ctx context.Context, username, soPin, csrPEM, role string) (*CeremonySignCSRResult, error)

	// CeremonyStatus returns the current ceremony state and SPKI pin.
	CeremonyStatus(ctx context.Context) (*CeremonyStatusInfo, error)

	// CeremonyClaimShareBegin issues a factor challenge nonce for the given
	// custodian. The returned challenge must be presented to
	// CeremonyClaimShareComplete.
	CeremonyClaimShareBegin(ctx context.Context, custodianID string) (*CeremonyCustodianChallenge, error)

	// CeremonyClaimShareComplete verifies the custodian's factor response for
	// the challenge issued by CeremonyClaimShareBegin. On success it marks the
	// share envelope as delivered and returns the custodian metadata.
	CeremonyClaimShareComplete(ctx context.Context, custodianID, challengeID string, factorResponse []byte) (*custodian.Custodian, error)

	// CeremonySubmitUnsealShare accepts a plaintext Shamir share from a
	// custodian along with factor proof of identity. When the configured
	// threshold is met the barrier is unsealed automatically.
	CeremonySubmitUnsealShare(ctx context.Context, custodianID, challengeID string, factorResponse, shareBytes []byte) (*CeremonyUnsealShareResult, error)

	// CeremonyEnrollExternalOfficer enrols an officer whose key material
	// lives outside go-qrdb. The administrator supplies trust material —
	// either an X.509 certificate (validated against the configured CA
	// pools) or a raw SPKI public key — along with an assigned share
	// index and an auth factor. When ShareBytes is supplied, it is sealed
	// to the officer's public key and the base64 ciphertext is returned.
	CeremonyEnrollExternalOfficer(ctx context.Context, req *CeremonyEnrollExternalOfficerRequest) (*CeremonyEnrollExternalOfficerResponse, error)
}

// CeremonyInitializeOptions carries per-request parameters for the
// initialization ceremony. Per-request PINs take precedence over the
// construction-time CeremonyConfig PINs when non-empty; empty fields fall
// back to the configured values. When a required PIN is absent from both
// the request and the configuration, the ceremony fails closed.
type CeremonyInitializeOptions struct {
	// SOPin is the Security Officer PIN supplied for this ceremony.
	SOPin string

	// UserPin is the user-level PIN supplied for this ceremony.
	UserPin string
}

// CeremonyEnrollExternalOfficerRequest is the service-layer request for
// enrolling an external officer.
type CeremonyEnrollExternalOfficerRequest struct {
	// Username is the unique identifier for the officer.
	Username string

	// CertPEM is the X.509 certificate PEM. Mutually exclusive with PubKeyPEM.
	CertPEM string

	// PubKeyPEM is the SPKI-encoded public key PEM. Mutually exclusive with CertPEM.
	PubKeyPEM string

	// ShareIndex is the 1-based Shamir share index to assign.
	ShareIndex int

	// AuthFactor is the auth factor identifier ("mtls", "webauthn", "piv").
	AuthFactor string

	// ShareBytes is the optional plaintext Shamir share to seal. When
	// nil or empty, the response carries no SealedShare.
	ShareBytes []byte
}

// CeremonyEnrollExternalOfficerResponse is the service-layer response for
// enrolling an external officer.
type CeremonyEnrollExternalOfficerResponse struct {
	// SealedShare is the base64-encoded sealed share envelope. Empty when
	// no ShareBytes were supplied.
	SealedShare string
}

// CeremonyCustodianChallenge is the service-layer challenge returned by
// CeremonyClaimShareBegin. It is serialised directly in the REST response.
type CeremonyCustodianChallenge struct {
	// CustodianID is the unique ID of the challenged custodian.
	CustodianID string `json:"custodianID"`

	// Nonce is the 32-byte random challenge the custodian must sign.
	Nonce []byte `json:"nonce"`

	// ExpiresAt is the UTC deadline for the response.
	ExpiresAt time.Time `json:"expiresAt"`
}

// CeremonyUnsealShareResult is the service-layer result returned by
// CeremonySubmitUnsealShare. It is serialised directly in the REST response.
type CeremonyUnsealShareResult struct {
	// Submitted is the number of shares accepted so far.
	Submitted int `json:"submitted"`

	// Required is the threshold needed to unseal.
	Required int `json:"required"`

	// Unsealed is true when the threshold was reached and the barrier unsealed.
	Unsealed bool `json:"unsealed"`
}

// CeremonyInitResult contains the output of a successful ceremony
// initialization including certificates, keys, and enrollment state.
type CeremonyInitResult struct {
	// SPKIPin is the base64-encoded SHA-256 hash of the CA certificate's
	// SubjectPublicKeyInfo.
	SPKIPin string

	// CACertPEM is the PEM-encoded CA certificate.
	CACertPEM []byte

	// TLSCertPEM is the PEM-encoded TLS certificate issued for the server.
	TLSCertPEM []byte

	// SOCertPEM is the PEM-encoded Security Officer certificate.
	SOCertPEM []byte

	// SOKeyPEM is the PEM-encoded Security Officer private key.
	SOKeyPEM []byte

	// State is the ceremony state after initialization.
	State string

	// ShareCount is the number of Shamir shares generated (0 for single admin).
	ShareCount int
}

// CeremonyChallenge contains the challenge nonce issued during the
// certificate claim handshake.
type CeremonyChallenge struct {
	// NonceHex is the hex-encoded random challenge bytes the officer must sign.
	NonceHex string

	// ExpiresAt is the deadline by which the officer must respond.
	ExpiresAt time.Time
}

// CeremonyClaimCertResult contains the output of a successful certificate
// claim operation.
type CeremonyClaimCertResult struct {
	// CertPEM is the PEM-encoded officer certificate.
	CertPEM []byte

	// CACertPEM is the PEM-encoded CA certificate for trust chain verification.
	CACertPEM []byte
}

// CeremonySignCSRResult contains the output of a successful CSR signing
// operation during the ceremony.
type CeremonySignCSRResult struct {
	// CertPEM is the PEM-encoded certificate issued from the CSR.
	CertPEM []byte
}

// CeremonyStatusInfo describes the current state of the initialization
// ceremony.
type CeremonyStatusInfo struct {
	// State is the current ceremony phase.
	State string

	// SPKIPin is the base64-encoded SHA-256 hash of the CA certificate's
	// SubjectPublicKeyInfo. Empty if the CA has not been initialized.
	SPKIPin string
}

// CeremonyService implements CeremonyServicer by delegating to a
// ceremony.CeremonyService instance.
type CeremonyService struct {
	ceremony *ceremony.CeremonyService
}

// Compile-time interface check.
var _ CeremonyServicer = (*CeremonyService)(nil)

// NewCeremonyService creates a new CeremonyServicer backed by the given
// ceremony.CeremonyService.
func NewCeremonyService(c *ceremony.CeremonyService) CeremonyServicer {
	return &CeremonyService{ceremony: c}
}

// CeremonyInitialize performs the initialization ceremony by delegating
// to the underlying CeremonyService's Initialize method and mapping the
// result to the service-layer CeremonyInitResult type.
func (s *CeremonyService) CeremonyInitialize(ctx context.Context, ca ceremony.CeremonyCA, opts *CeremonyInitializeOptions) (*CeremonyInitResult, error) {
	var ceremonyOpts *ceremony.InitializeOptions
	if opts != nil {
		ceremonyOpts = &ceremony.InitializeOptions{
			SOPin:   opts.SOPin,
			UserPin: opts.UserPin,
		}
	}
	result, err := s.ceremony.Initialize(ctx, ca, ceremonyOpts)
	if err != nil {
		return nil, err
	}

	return &CeremonyInitResult{
		SPKIPin:    result.SPKIPin,
		CACertPEM:  result.CACertPEM,
		TLSCertPEM: result.TLSCertPEM,
		SOCertPEM:  result.SOCertPEM,
		SOKeyPEM:   result.SOKeyPEM,
		State:      string(result.State),
		ShareCount: result.ShareCount,
	}, nil
}

// CeremonyBeginClaimCert starts the certificate claim handshake by
// delegating to the underlying CeremonyService's BeginClaimCert method.
// The raw nonce bytes are converted to a hex-encoded string.
func (s *CeremonyService) CeremonyBeginClaimCert(ctx context.Context, username string) (*CeremonyChallenge, error) {
	challenge, err := s.ceremony.BeginClaimCert(ctx, username)
	if err != nil {
		return nil, err
	}

	return &CeremonyChallenge{
		NonceHex:  hex.EncodeToString(challenge.Nonce),
		ExpiresAt: challenge.ExpiresAt,
	}, nil
}

// CeremonyCompleteClaimCert completes the certificate claim handshake by
// delegating to the underlying CeremonyService's CompleteClaimCert method
// and mapping the result to the service-layer CeremonyClaimCertResult type.
func (s *CeremonyService) CeremonyCompleteClaimCert(ctx context.Context, username, nonceHex string, signature []byte) (*CeremonyClaimCertResult, error) {
	result, err := s.ceremony.CompleteClaimCert(ctx, username, nonceHex, signature)
	if err != nil {
		return nil, err
	}

	return &CeremonyClaimCertResult{
		CertPEM:   result.CertPEM,
		CACertPEM: result.CACertPEM,
	}, nil
}

// CeremonyClaimShare retrieves the Shamir share for the given officer
// username by delegating to the underlying CeremonyService's ClaimShare
// method.
func (s *CeremonyService) CeremonyClaimShare(ctx context.Context, username string) ([]byte, error) {
	return s.ceremony.ClaimShare(ctx, username)
}

// CeremonySignCSR signs a CSR during the ceremony by delegating to the
// underlying CeremonyService's SignCSRInit method and mapping the result
// to the service-layer CeremonySignCSRResult type.
func (s *CeremonyService) CeremonySignCSR(ctx context.Context, username, soPin, csrPEM, role string) (*CeremonySignCSRResult, error) {
	result, err := s.ceremony.SignCSRInit(ctx, username, soPin, []byte(csrPEM), role)
	if err != nil {
		return nil, err
	}

	return &CeremonySignCSRResult{
		CertPEM: result.CertPEM,
	}, nil
}

// CeremonyClaimShareBegin delegates to the underlying CeremonyService's
// ClaimShareChallenge method and maps the challenge to the service-layer type.
func (s *CeremonyService) CeremonyClaimShareBegin(ctx context.Context, custodianID string) (*CeremonyCustodianChallenge, error) {
	ch, err := s.ceremony.ClaimShareChallenge(ctx, custodianID)
	if err != nil {
		return nil, err
	}
	return &CeremonyCustodianChallenge{
		CustodianID: ch.CustodianID,
		Nonce:       ch.Nonce,
		ExpiresAt:   ch.ExpiresAt,
	}, nil
}

// CeremonyClaimShareComplete delegates to the underlying CeremonyService's
// VerifyClaimAndDeliver method. The raw factorResponse bytes are wrapped in
// an authfactors.Response before delegation.
func (s *CeremonyService) CeremonyClaimShareComplete(ctx context.Context, custodianID, challengeID string, factorResponse []byte) (*custodian.Custodian, error) {
	resp := &authfactors.Response{Value: factorResponse}
	return s.ceremony.VerifyClaimAndDeliver(ctx, custodianID, challengeID, resp)
}

// CeremonySubmitUnsealShare delegates to the underlying CeremonyService's
// SubmitUnsealShare method, wrapping the raw factorResponse bytes in an
// authfactors.Response and mapping the result to the service-layer type.
func (s *CeremonyService) CeremonySubmitUnsealShare(ctx context.Context, custodianID, challengeID string, factorResponse, shareBytes []byte) (*CeremonyUnsealShareResult, error) {
	req := ceremony.SubmitUnsealShareRequest{
		CustodianID:    custodianID,
		ChallengeID:    challengeID,
		FactorResponse: &authfactors.Response{Value: factorResponse},
		ShareBytes:     shareBytes,
	}
	result, err := s.ceremony.SubmitUnsealShare(ctx, req)
	if err != nil {
		return nil, err
	}
	return &CeremonyUnsealShareResult{
		Submitted: result.Submitted,
		Required:  result.Required,
		Unsealed:  result.Unsealed,
	}, nil
}

// CeremonyEnrollExternalOfficer delegates to the underlying CeremonyService's
// EnrollExternalOfficer method, mapping the service-layer request/response.
func (s *CeremonyService) CeremonyEnrollExternalOfficer(ctx context.Context, req *CeremonyEnrollExternalOfficerRequest) (*CeremonyEnrollExternalOfficerResponse, error) {
	if req == nil {
		return nil, ceremony.ErrInvalidOfficerRecord
	}
	resp, err := s.ceremony.EnrollExternalOfficer(ctx, ceremony.EnrollExternalOfficerRequest{
		Username:   req.Username,
		CertPEM:    req.CertPEM,
		PubKeyPEM:  req.PubKeyPEM,
		ShareIndex: req.ShareIndex,
		AuthFactor: req.AuthFactor,
		ShareBytes: req.ShareBytes,
	})
	if err != nil {
		return nil, err
	}
	return &CeremonyEnrollExternalOfficerResponse{SealedShare: resp.SealedShare}, nil
}

// CeremonyStatus returns the current ceremony state and SPKI pin by
// querying the underlying CeremonyService. If the CA has not been
// initialized, the SPKIPin field is returned as an empty string.
func (s *CeremonyService) CeremonyStatus(_ context.Context) (*CeremonyStatusInfo, error) {
	state := s.ceremony.State()

	pin, err := s.ceremony.SPKIPin()
	if err != nil {
		// SPKIPin returns an error when the CA is not yet initialized.
		// This is expected before the ceremony completes initialization.
		pin = ""
	}

	return &CeremonyStatusInfo{
		State:   string(state),
		SPKIPin: pin,
	}, nil
}
