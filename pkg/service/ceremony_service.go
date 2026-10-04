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
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
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

	// CeremonySignCSR issues a system-tenant certificate for a role under SO
	// PIN authorization: a holder certificate for roles "so" and "custodian",
	// a login certificate for the others. The CA assigns the identity.
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

	// SANs is the list of Subject Alternative Names for TLS certificates.
	SANs []string

	// AdminCSRPEM, when non-empty, selects the CSR-sign path for the
	// bootstrap admin's certificate (go-qrdb#548). Mutually exclusive with
	// AdminCertPEM.
	AdminCSRPEM string

	// AdminCertPEM, when non-empty, selects the bring-your-own-certificate
	// path for the bootstrap admin: a certificate the admin already holds,
	// validated against AdminCertPoolPEM (or the ceremony's own CA bundle
	// when that is empty) and adopted unchanged. Mutually exclusive with
	// AdminCSRPEM.
	AdminCertPEM string

	// AdminCertPoolPEM is the PEM-encoded trust anchor bundle AdminCertPEM
	// must chain to. Optional.
	AdminCertPoolPEM string

	// AdminGenerate selects the generate path explicitly (go-qrdb#548
	// review): mutually exclusive with AdminCSRPEM and AdminCertPEM. NOT
	// suitable for production. When AdminCSRPEM, AdminCertPEM and
	// AdminGenerate are ALL unset, CeremonyInitialize refuses --
	// there is no credential-less default.
	AdminGenerate bool

	// Secret is the passphrase (or other strategy-specific credential) the
	// system barrier is initialized with (go-qrdb#548): qrdb init is the
	// only system bootstrap, bringing the barrier up itself as part of
	// this same call. Required when the barrier's registered strategy
	// needs one (e.g. "passphrase"); must be empty for "none". Ignored in
	// M-of-N mode, where the barrier stays sealed until officers later
	// reconstruct it from their shares.
	//
	// []byte, not string (go-qrdb#560 review item 7): a Go string is
	// immutable and can never be wiped from memory after use. Every
	// caller that populates this from a wire string (holderwire.go, the
	// JSON/embedded path) necessarily makes that one, unavoidable
	// string-to-[]byte copy at the point of receipt; from here down,
	// through ceremony.InitializeOptions.Secret (already []byte), the
	// value is wipeable and CeremonyInitialize no longer has to make a
	// SECOND, equally unwiped copy of its own to hand off to the core
	// ceremony package.
	Secret []byte
}

// CeremonyOfficerConfig names one initial share holder. CSRPEM is a PEM
// string, the canonical wire form. It carries the same fields
// BarrierOfficerRegisterRequest does (go-qrdb#4578 review item 1):
// CeremonyInitialize is the ONLY way to establish Shamir custody over the
// system barrier, so it is also the only place a holder can get a separate
// envelope key, an extra factor, or a designated factor.
type CeremonyOfficerConfig struct {
	// Username is the holder's unique identifier.
	Username string

	// Role is so or custodian; empty means so.
	Role seal.HolderRole

	// CSRPEM supplies the holder's public key.
	CSRPEM string

	// EncryptionKEM and EncryptionKey name an optional separate
	// share-envelope recipient key (HPKE suite name and key encoding).
	EncryptionKEM string
	EncryptionKey []byte

	// Factors are additional authentication factors to enrol.
	Factors []ceremony.FactorEnrolment

	// Designated names the designated factor; empty means "mtls".
	Designated string
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

	// AdminCertPEM is the bootstrap admin's issued or adopted certificate
	// (go-qrdb#548). Always present on success.
	AdminCertPEM []byte

	// AdminKeyPEM is the generated private key, present ONLY when
	// AdminCredentialSource is "generated". Returned exactly once.
	AdminKeyPEM []byte

	// AdminCredentialSource records which path produced AdminCertPEM: "csr",
	// "byoc", or "generated".
	AdminCredentialSource string
}

// WipeAdminKeyPEM zeroes r's generated admin key material in place and clears
// the field. Transports call this immediately after writing AdminKeyPEM into
// their response (go-qrdb#548). A nil receiver or an empty key is a no-op.
func (r *CeremonyInitResult) WipeAdminKeyPEM() {
	if r == nil {
		return
	}
	secure.Wipe(r.AdminKeyPEM)
	r.AdminKeyPEM = nil
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
		officers[i] = ceremony.OfficerConfig{
			Username:      o.Username,
			Role:          o.Role,
			CSRPEM:        []byte(o.CSRPEM),
			EncryptionKEM: o.EncryptionKEM,
			EncryptionKey: o.EncryptionKey,
			Factors:       o.Factors,
			Designated:    o.Designated,
		}
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
		AdminCSRPEM:            []byte(opts.AdminCSRPEM),
		AdminCertPEM:           []byte(opts.AdminCertPEM),
		AdminCertPoolPEM:       []byte(opts.AdminCertPoolPEM),
		AdminGenerate:          opts.AdminGenerate,
		Secret:                 opts.Secret,
	}
	if id := auth.IdentityFromContext(ctx); id != nil {
		copts.Enroller = id.Subject
	}
	result, err := s.ceremony.Initialize(ctx, ca, copts)
	if err != nil {
		return nil, err
	}
	out := ceremonyInitResultFromCore(result)
	for i, h := range result.Holders {
		out.Holders = append(out.Holders, CeremonyHolderCert{Username: opts.Officers[i].Username, Role: h.Role, CertPEM: h.CertPEM})
	}
	return out, nil
}

// ceremonyInitResultFromCore maps the ceremony-package result onto the
// service-layer type, except Holders: zipping a RegisterOfficerResult (which
// carries no username of its own) with its officer needs the caller's own
// request, so CeremonyInitialize fills Holders in itself after calling this.
// CeremonyReissueAdminCredential's result never carries holders at all, so
// it does not need to.
func ceremonyInitResultFromCore(result *ceremony.InitResult) *CeremonyInitResult {
	return &CeremonyInitResult{
		SPKIPin:               result.SPKIPin,
		CACertPEM:             result.CACertPEM,
		TLSCertPEM:            result.TLSCertPEM,
		State:                 string(result.State),
		ShareCount:            result.ShareCount,
		AdminCertPEM:          result.AdminCertPEM,
		AdminKeyPEM:           result.AdminKeyPEM,
		AdminCredentialSource: string(result.AdminCredentialSource),
	}
}

// CeremonySignCSR issues a system-tenant certificate for a role under SO PIN
// authorization (ceremony.CeremonyService.SignCSRInit).
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
func (s *CeremonyService) CeremonyStatus(ctx context.Context) (*CeremonyStatusInfo, error) {
	pin, err := s.ceremony.SPKIPin(ctx)
	if err != nil {
		pin = ""
	}
	return &CeremonyStatusInfo{State: string(s.ceremony.State()), SPKIPin: pin}, nil
}
