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

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
)

// CeremonyServicer provides ceremony lifecycle operations including
// initialization, certificate claiming, share distribution, CSR signing,
// and status queries.
type CeremonyServicer interface {
	// CeremonyInitialize performs the initialization ceremony, issuing TLS
	// and SO certificates and optionally starting M-of-N enrollment.
	CeremonyInitialize(ctx context.Context, ca ceremony.CeremonyCA) (*CeremonyInitResult, error)

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
func (s *CeremonyService) CeremonyInitialize(ctx context.Context, ca ceremony.CeremonyCA) (*CeremonyInitResult, error) {
	result, err := s.ceremony.Initialize(ctx, ca)
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
