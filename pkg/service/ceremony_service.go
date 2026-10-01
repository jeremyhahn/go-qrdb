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
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// CeremonyServicer provides the initialization ceremony: first-boot
// initialization (which enrols the initial share holders and initializes the
// barrier's Shamir custody), login-certificate issuance under the SO PIN,
// and status. Share-holder operations after initialization — registration,
// claims, quorum submissions, rekey and revocation — are BarrierServicer
// operations, because they belong to a barrier key domain.
type CeremonyServicer interface {
	// CeremonyInitialize performs the initialization ceremony. opts carries
	// the per-request PINs (established or verified through the
	// PINBackend), the threshold, the factor policy and the initial holders.
	CeremonyInitialize(ctx context.Context, ca ceremony.CeremonyCA, opts *CeremonyInitializeOptions) (*CeremonyInitResult, error)

	// CeremonySignCSR issues a login certificate under SO PIN authorization.
	// The CA assigns the certificate identity.
	CeremonySignCSR(ctx context.Context, username, soPin, csrPEM, role string) (*CeremonySignCSRResult, error)

	// CeremonyStatus returns the current ceremony state and SPKI pin.
	CeremonyStatus(ctx context.Context) (*CeremonyStatusInfo, error)
}

// CeremonyInitializeOptions carries the per-request parameters of the
// initialization ceremony.
type CeremonyInitializeOptions struct {
	// SOPin and UserPin are required.
	SOPin   string
	UserPin string

	// CredentialSealStrategy identifies how credentials are sealed at rest.
	CredentialSealStrategy string

	// Threshold is M; >= 2 initializes Shamir custody over Officers.
	Threshold int

	// Policy is the factor policy of the initial share set.
	Policy seal.FactorPolicy

	// Officers are the initial share holders, Security Officers and key
	// custodians alike.
	Officers []CeremonyOfficerConfig

	// Hostname and SANs describe the server TLS certificate.
	Hostname string
	SANs     []string
}

// CeremonyOfficerConfig names one initial share holder. CSRPEM is a PEM
// string, the canonical wire form.
type CeremonyOfficerConfig struct {
	// Username is the holder's unique identifier.
	Username string

	// Role is so or custodian; empty means so.
	Role seal.HolderRole

	// CSRPEM supplies the holder's public key.
	CSRPEM string
}

// CeremonyInitResult contains the output of a successful initialization.
type CeremonyInitResult struct {
	// SPKIPin is the base64-encoded SHA-256 hash of the CA certificate's
	// SubjectPublicKeyInfo.
	SPKIPin string

	// CACertPEM is the PEM-encoded CA certificate.
	CACertPEM []byte

	// TLSCertPEM is the PEM-encoded TLS certificate issued for the server.
	TLSCertPEM []byte

	// State is the ceremony state after initialization.
	State string

	// ShareCount is the number of share envelopes distributed.
	ShareCount int

	// Holders are the enrolled holders' issued certificates, in request
	// order. Each must reach its holder, who claims their envelope with it.
	Holders []CeremonyHolderCert
}

// CeremonyHolderCert is one holder's issued certificate.
type CeremonyHolderCert struct {
	Username string
	Role     seal.HolderRole
	CertPEM  []byte
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

// CeremonyServiceCore extracts the underlying *ceremony.CeremonyService from
// the given CeremonyServicer when it is backed by one. The bool return is
// false for any other implementation.
func CeremonyServiceCore(svc CeremonyServicer) (*ceremony.CeremonyService, bool) {
	if cs, ok := svc.(*CeremonyService); ok {
		return cs.ceremony, true
	}
	return nil, false
}

// CeremonyInitialize performs the initialization ceremony. The authenticated
// caller is recorded as the enroller, who may not be among the holders.
func (s *CeremonyService) CeremonyInitialize(ctx context.Context, ca ceremony.CeremonyCA, opts *CeremonyInitializeOptions) (*CeremonyInitResult, error) {
	if opts == nil {
		return nil, ErrNilRequest
	}
	officers := make([]ceremony.OfficerConfig, len(opts.Officers))
	for i, o := range opts.Officers {
		officers[i] = ceremony.OfficerConfig{Username: o.Username, Role: o.Role, CSRPEM: []byte(o.CSRPEM)}
	}
	copts := &ceremony.InitializeOptions{
		SOPin:                  opts.SOPin,
		UserPin:                opts.UserPin,
		CredentialSealStrategy: opts.CredentialSealStrategy,
		Threshold:              opts.Threshold,
		Policy:                 opts.Policy,
		Officers:               officers,
		Hostname:               opts.Hostname,
		SANs:                   opts.SANs,
	}
	if id := auth.IdentityFromContext(ctx); id != nil {
		copts.Enroller = id.Subject
	}
	result, err := s.ceremony.Initialize(ctx, ca, copts)
	if err != nil {
		return nil, err
	}
	out := &CeremonyInitResult{
		SPKIPin:    result.SPKIPin,
		CACertPEM:  result.CACertPEM,
		TLSCertPEM: result.TLSCertPEM,
		State:      string(result.State),
		ShareCount: result.ShareCount,
	}
	for i, h := range result.Holders {
		out.Holders = append(out.Holders, CeremonyHolderCert{Username: opts.Officers[i].Username, Role: h.Role, CertPEM: h.CertPEM})
	}
	return out, nil
}

// CeremonySignCSR issues a login certificate under SO PIN authorization.
func (s *CeremonyService) CeremonySignCSR(ctx context.Context, username, soPin, csrPEM, role string) (*CeremonySignCSRResult, error) {
	result, err := s.ceremony.SignCSRInit(ctx, username, soPin, []byte(csrPEM), role)
	if err != nil {
		var locked *pin.SOPINLockedError
		if errors.As(err, &locked) {
			return nil, &SignCSRThrottledError{RetryAfter: locked.RetryAfter}
		}
		return nil, err
	}
	return &CeremonySignCSRResult{CertPEM: result.CertPEM}, nil
}

// CeremonyStatus returns the current ceremony state and SPKI pin. Before the
// CA is wired the SPKI pin is empty.
func (s *CeremonyService) CeremonyStatus(_ context.Context) (*CeremonyStatusInfo, error) {
	pin, err := s.ceremony.SPKIPin()
	if err != nil {
		pin = ""
	}
	return &CeremonyStatusInfo{State: string(s.ceremony.State()), SPKIPin: pin}, nil
}
