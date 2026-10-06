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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/certenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// revokeOnBindFailureTimeout bounds the detached revoke SignCertificate
// issues when binding a freshly-signed certificate fails. It runs on its
// own context rather than the request's, which may already be the reason
// the bind failed, so it needs its own bound against a wedged store.
const revokeOnBindFailureTimeout = 10 * time.Second

// CertEnrollmentServicer is the one service every transport's certificate
// enrolment and trust-anchor verbs delegate to. Authorization happens before
// it is called: every verb is tenant-self (the request's tenant is the
// caller's own), which the transports enforce through the ScopeSelfStrict
// permissions certs:issue, certs:enroll and trust_anchors:*.
type CertEnrollmentServicer interface {
	// SignCertificate signs a CSR for an EXISTING account with its tenant's
	// own Key B CA and binds the certificate's SPKI to the account in the
	// same operation. A CSR for an account that does not exist is refused
	// with *user.UserNotFoundError before anything is signed.
	SignCertificate(ctx context.Context, req *SignCertificateRequest) (*EnrolledCertificate, error)

	// EnrollCertificate binds a bring-your-own certificate to an EXISTING
	// account after verifying it chains to one of the tenant's registered
	// trust anchors and authenticates as exactly that account.
	EnrollCertificate(ctx context.Context, req *EnrollCertificateRequest) (*EnrolledCertificate, error)

	// AddTrustAnchors registers every CA certificate in a PEM bundle as one
	// of tenantID's trust anchors.
	AddTrustAnchors(ctx context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error)

	// ListTrustAnchors returns tenantID's trust anchors.
	ListTrustAnchors(ctx context.Context, tenantID string) ([]trustanchor.Anchor, error)

	// RemoveTrustAnchor withdraws one of tenantID's trust anchors.
	RemoveTrustAnchor(ctx context.Context, tenantID, fingerprint string) error
}

// SignCertificateRequest asks for a certificate for Username in TenantID.
type SignCertificateRequest struct {
	TenantID string
	Username string
	CSRPEM   []byte
	// TTL is the validity period; zero is the issuer's default.
	TTL time.Duration
}

// EnrollCertificateRequest enrols a bring-your-own certificate for Username
// in TenantID. CertPEM holds the leaf first; any further certificates in it,
// and all of ChainPEM, are offered as intermediates.
type EnrollCertificateRequest struct {
	TenantID string
	Username string
	CertPEM  []byte
	ChainPEM []byte
}

// EnrolledCertificate is a certificate now bound to an account. CertPEM is
// the signed certificate for SignCertificate and the enrolled one for
// EnrollCertificate; Serial is empty for a bring-your-own certificate, which
// this cluster did not issue.
type EnrolledCertificate struct {
	UserID     uint64
	Username   string
	TenantID   string
	CertPEM    []byte
	Serial     string
	Thumbprint string
	NotAfter   time.Time
}

// certEnrollmentUsers is the slice of the user service enrolment needs.
type certEnrollmentUsers interface {
	GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error)
	AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*user.User, error)
}

// CertEnrollmentService is the production CertEnrollmentServicer.
type CertEnrollmentService struct {
	certs       cert.CertProvider
	users       certEnrollmentUsers
	anchors     trustanchor.Registry
	trustDomain string
	now         func() time.Time
	audit       audit.AuditLogger
	auditPolicy audit.FailurePolicy
}

// Compile-time interface check.
var _ CertEnrollmentServicer = (*CertEnrollmentService)(nil)

// CertEnrollmentOption configures a CertEnrollmentService.
type CertEnrollmentOption func(*CertEnrollmentService)

// WithEnrollmentTrustDomain sets the SPIFFE trust domain the node's mTLS
// authenticator pins (auth.spiffe_trust_domain), so a bring-your-own
// certificate's identity is read exactly as a login will read it.
func WithEnrollmentTrustDomain(trustDomain string) CertEnrollmentOption {
	return func(s *CertEnrollmentService) { s.trustDomain = trustDomain }
}

// WithEnrollmentClock overrides the clock used to verify bring-your-own
// certificates.
func WithEnrollmentClock(now func() time.Time) CertEnrollmentOption {
	return func(s *CertEnrollmentService) {
		if now != nil {
			s.now = now
		}
	}
}

// WithEnrollmentAuditLogger installs the compliance audit trail
// SignCertificate and EnrollCertificate record cert.issue and cert.enroll
// on. A nil logger disables emission, so the option is safe to apply
// unconditionally.
func WithEnrollmentAuditLogger(l audit.AuditLogger) CertEnrollmentOption {
	return func(s *CertEnrollmentService) { s.audit = l }
}

// WithEnrollmentAuditPolicy installs the node's audit failure posture for
// cert-enrolment's gating records (auth.audit_failure_policy): what
// SignCertificate and EnrollCertificate do when the trail cannot record an
// issuance or enrolment before it takes effect. The zero value,
// audit.FailClosed, is the default.
func WithEnrollmentAuditPolicy(p audit.FailurePolicy) CertEnrollmentOption {
	return func(s *CertEnrollmentService) { s.auditPolicy = p }
}

// NewCertEnrollmentService returns the production CertEnrollmentServicer. A
// nil certs disables SignCertificate and a nil anchors disables
// EnrollCertificate and the trust-anchor verbs; each then answers
// *CertEnrollmentNotConfiguredError.
func NewCertEnrollmentService(certs cert.CertProvider, users certEnrollmentUsers, anchors trustanchor.Registry, opts ...CertEnrollmentOption) *CertEnrollmentService {
	s := &CertEnrollmentService{certs: certs, users: users, anchors: anchors, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// account resolves the existing account a certificate is for, refusing one
// that certTargetGuard does not allow this caller to target.
func (s *CertEnrollmentService) account(ctx context.Context, tenantID, username string) (*user.User, error) {
	if s.users == nil {
		return nil, &CertEnrollmentNotConfiguredError{Capability: "user accounts"}
	}
	if username == "" {
		return nil, &CertEnrollmentFieldError{Field: "username"}
	}
	u, err := s.users.GetUserByUsername(ctx, tenantID, username)
	if err != nil {
		return nil, err
	}
	if err := certTargetGuard(ctx, tenantID, u); err != nil {
		return nil, err
	}
	return u, nil
}

// SignCertificate implements CertEnrollmentServicer.
func (s *CertEnrollmentService) SignCertificate(ctx context.Context, req *SignCertificateRequest) (*EnrolledCertificate, error) {
	if s.certs == nil {
		return nil, &CertEnrollmentNotConfiguredError{Capability: "certificate issuance"}
	}
	if req == nil || len(req.CSRPEM) == 0 {
		return nil, &CertEnrollmentFieldError{Field: "csr_pem"}
	}
	if req.TTL < 0 {
		return nil, &CertEnrollmentFieldError{Field: "ttl"}
	}
	// The CSR is judged before the account is looked up or anything is
	// signed: a malformed request costs neither a store read nor a signature.
	if _, err := cert.ParseCSR(req.CSRPEM); err != nil {
		return nil, err
	}
	u, err := s.account(ctx, req.TenantID, req.Username)
	if err != nil {
		return nil, err
	}

	ev := certEnrollmentEvent(ctx, req.TenantID, ActionCertIssue, map[string]any{"username": u.Username})
	if err := s.gateCertAudit(ctx, ev); err != nil {
		return nil, err
	}

	issued, err := certenroll.IssueFromCSR(ctx, s.certs, req.TenantID, u.Username, req.CSRPEM, req.TTL)
	if err != nil {
		s.settleCertAudit(ctx, ev, err)
		return nil, err
	}
	if _, err := s.users.AddCertBinding(ctx, u.ID, issued.Thumbprint, issued.NotAfter.Unix()); err != nil {
		// A certificate bound to nobody must not outlive the request: under
		// the "enrolled" binding posture it would still log in as an account
		// with no other bindings. Revoke it; the binding failure is the
		// answer either way.
		//
		// The revoke runs on a DETACHED, bounded context, not the request's
		// own: a binding failure is frequently the request's own context
		// already being canceled or past its deadline, and revoking with
		// THAT context would be refused for the same reason the bind just
		// was, leaving the orphaned certificate un-revoked. Detaching keeps
		// the revoke running after the request's context ends; the bound
		// timeout keeps it from running forever if the store is wedged.
		revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revokeOnBindFailureTimeout)
		defer cancel()
		if _, rerr := s.certs.RevokeCert(revokeCtx, issued.Serial); rerr != nil {
			bindErr := &CertBindingFailedError{Serial: issued.Serial, Err: err, RevokeErr: rerr}
			s.settleCertAudit(ctx, ev, bindErr)
			return nil, bindErr
		}
		bindErr := &CertBindingFailedError{Serial: issued.Serial, Err: err}
		s.settleCertAudit(ctx, ev, bindErr)
		return nil, bindErr
	}
	s.settleCertAudit(ctx, ev, nil)
	return &EnrolledCertificate{
		UserID:     u.ID,
		Username:   u.Username,
		TenantID:   req.TenantID,
		CertPEM:    issued.CertPEM,
		Serial:     issued.Serial,
		Thumbprint: issued.Thumbprint,
		NotAfter:   issued.NotAfter,
	}, nil
}

// EnrollCertificate implements CertEnrollmentServicer.
func (s *CertEnrollmentService) EnrollCertificate(ctx context.Context, req *EnrollCertificateRequest) (*EnrolledCertificate, error) {
	if s.anchors == nil {
		return nil, &CertEnrollmentNotConfiguredError{Capability: "trust anchors"}
	}
	if req == nil || len(req.CertPEM) == 0 {
		return nil, &CertEnrollmentFieldError{Field: "cert_pem"}
	}
	u, err := s.account(ctx, req.TenantID, req.Username)
	if err != nil {
		return nil, err
	}

	ev := certEnrollmentEvent(ctx, req.TenantID, ActionCertEnroll, map[string]any{"username": u.Username})
	if err := s.gateCertAudit(ctx, ev); err != nil {
		return nil, err
	}

	byoc, err := certenroll.VerifyBYOC(ctx, s.anchors, req.TenantID, u.Username, req.CertPEM, req.ChainPEM, s.trustDomain, s.now())
	if err != nil {
		s.settleCertAudit(ctx, ev, err)
		return nil, err
	}
	if _, err := s.users.AddCertBinding(ctx, u.ID, byoc.Thumbprint, byoc.NotAfter.Unix()); err != nil {
		bindErr := &CertBindingFailedError{Err: err}
		s.settleCertAudit(ctx, ev, bindErr)
		return nil, bindErr
	}
	s.settleCertAudit(ctx, ev, nil)
	return &EnrolledCertificate{
		UserID:     u.ID,
		Username:   u.Username,
		TenantID:   req.TenantID,
		CertPEM:    trustanchor.EncodePEM(byoc.Certificate),
		Thumbprint: byoc.Thumbprint,
		NotAfter:   byoc.NotAfter,
	}, nil
}

// AddTrustAnchors implements CertEnrollmentServicer.
//
// The trust-anchor verbs are confined to the caller's own tenant here, in the
// one place every transport reaches (tenant.Confine): an anchor decides which
// certificates log in to its tenant, so adding one to another tenant would
// mint logins there. An empty tenantID is the caller's own; any other tenant
// is refused with *tenant.CrossTenantDeniedError, the system tenant included,
// as trust_anchors:*'s ScopeSelfStrict refuses it.
func (s *CertEnrollmentService) AddTrustAnchors(ctx context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error) {
	if s.anchors == nil {
		return nil, &CertEnrollmentNotConfiguredError{Capability: "trust anchors"}
	}
	own, err := tenant.Confine(ctx, tenantID, "add trust anchors")
	if err != nil {
		return nil, err
	}
	return s.anchors.Add(ctx, own, bundlePEM)
}

// ListTrustAnchors implements CertEnrollmentServicer, confined to the
// caller's own tenant as AddTrustAnchors is.
func (s *CertEnrollmentService) ListTrustAnchors(ctx context.Context, tenantID string) ([]trustanchor.Anchor, error) {
	if s.anchors == nil {
		return nil, &CertEnrollmentNotConfiguredError{Capability: "trust anchors"}
	}
	own, err := tenant.Confine(ctx, tenantID, "list trust anchors")
	if err != nil {
		return nil, err
	}
	return s.anchors.List(ctx, own)
}

// RemoveTrustAnchor implements CertEnrollmentServicer, confined to the
// caller's own tenant as AddTrustAnchors is.
func (s *CertEnrollmentService) RemoveTrustAnchor(ctx context.Context, tenantID, fingerprint string) error {
	if s.anchors == nil {
		return &CertEnrollmentNotConfiguredError{Capability: "trust anchors"}
	}
	own, err := tenant.Confine(ctx, tenantID, "remove a trust anchor")
	if err != nil {
		return err
	}
	return s.anchors.Remove(ctx, own, fingerprint)
}
