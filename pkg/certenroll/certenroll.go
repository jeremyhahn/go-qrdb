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

// Package certenroll holds the two ways a login certificate comes to exist
// for an account, shared by tenant onboarding (pkg/bootstrap) and the
// certificate-enrolment service (pkg/service) so they cannot drift apart:
//
//   - IssueFromCSR: the account's tenant signs a CSR with its own Key B CA.
//     The requester proved it holds the key (the CSR's self-signature); the
//     server never generates or sees a private key.
//   - VerifyBYOC: a bring-your-own certificate, issued elsewhere, is checked
//     against the tenant's registered trust anchors, and its identity -- the
//     user and tenant the mTLS authenticator will read from it -- must be the
//     account it is enrolled on.
//
// Both return the certificate's SPKI thumbprint (user.ComputeSPKIThumbprint),
// which is what an account's certificate binding records.
package certenroll

import (
	"context"
	"crypto/x509"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// Issued is a certificate IssueFromCSR signed.
type Issued struct {
	CertPEM    []byte
	Serial     string
	Thumbprint string
	NotAfter   time.Time
}

// IssueFromCSR verifies csrPEM (ParseCSR: it parses, its self-signature
// verifies, its key is acceptable) and has issuer sign a client certificate
// for its key naming username in tenantID. Nothing the CSR's Subject says is
// used.
func IssueFromCSR(ctx context.Context, issuer cert.CertProvider, tenantID, username string, csrPEM []byte, ttl time.Duration) (*Issued, error) {
	pub, err := cert.ParseCSR(csrPEM)
	if err != nil {
		return nil, err
	}
	signed, err := issuer.IssueClientCert(ctx, &cert.IssueRequest{
		Username:  username,
		TenantID:  tenantID,
		PublicKey: pub,
		TTL:       ttl,
	})
	if err != nil {
		return nil, err
	}
	certs, err := trustanchor.ParseCertificates(signed.CertPEM)
	if err != nil {
		return nil, &IssuedCertificateUnreadableError{Err: err}
	}
	return &Issued{
		CertPEM:    signed.CertPEM,
		Serial:     signed.Serial,
		Thumbprint: user.ComputeSPKIThumbprint(certs[0]),
		NotAfter:   signed.NotAfter,
	}, nil
}

// BYOC is a bring-your-own certificate VerifyBYOC accepted.
type BYOC struct {
	Certificate *x509.Certificate
	Thumbprint  string
	NotAfter    time.Time
}

// ParseBYOC decodes a bring-your-own certificate: the leaf is the first
// certificate of certPEM, and every other certificate -- the rest of certPEM
// and all of chainPEM -- is an intermediate offered for its chain.
func ParseBYOC(certPEM, chainPEM []byte) (*x509.Certificate, []*x509.Certificate, error) {
	certs, err := trustanchor.ParseCertificates(certPEM)
	if err != nil {
		return nil, nil, &InvalidCertificateError{Err: err}
	}
	intermediates := certs[1:]
	if len(chainPEM) > 0 {
		chain, err := trustanchor.ParseCertificates(chainPEM)
		if err != nil {
			return nil, nil, &InvalidCertificateError{Err: err}
		}
		intermediates = append(intermediates, chain...)
	}
	return certs[0], intermediates, nil
}

// VerifyBYOC accepts a bring-your-own certificate for username in tenantID:
// it must chain to one of tenantID's registered anchors (read through src)
// for client authentication at now, and name exactly that account under the
// mTLS authenticator's own rule (CheckIdentity).
func VerifyBYOC(ctx context.Context, src trustanchor.Source, tenantID, username string, certPEM, chainPEM []byte, trustDomain string, now time.Time) (*BYOC, error) {
	leaf, intermediates, err := ParseBYOC(certPEM, chainPEM)
	if err != nil {
		return nil, err
	}
	if err := trustanchor.VerifyClientCertificate(ctx, src, tenantID, leaf, intermediates, now); err != nil {
		return nil, err
	}
	return accept(leaf, tenantID, username, trustDomain)
}

// VerifyBYOCAgainst is VerifyBYOC over an explicit anchor set: tenant
// onboarding checks the admin's certificate against the anchors in the same
// request before writing anything.
func VerifyBYOCAgainst(anchors []*x509.Certificate, tenantID, username string, certPEM, chainPEM []byte, trustDomain string, now time.Time) (*BYOC, error) {
	leaf, intermediates, err := ParseBYOC(certPEM, chainPEM)
	if err != nil {
		return nil, err
	}
	if err := trustanchor.VerifyClientCertificateAgainst(tenantID, anchors, leaf, intermediates, now); err != nil {
		return nil, err
	}
	return accept(leaf, tenantID, username, trustDomain)
}

func accept(leaf *x509.Certificate, tenantID, username, trustDomain string) (*BYOC, error) {
	if err := CheckIdentity(leaf, tenantID, username, trustDomain); err != nil {
		return nil, err
	}
	return &BYOC{
		Certificate: leaf,
		Thumbprint:  user.ComputeSPKIThumbprint(leaf),
		NotAfter:    leaf.NotAfter,
	}, nil
}

// CheckIdentity reports whether a certificate authenticates as username in
// tenantID: its Common Name is the username and its tenant, derived by the
// mTLS authenticator's own rule (auth.TenantFromClientCert under the node's
// pinned SPIFFE trust domain), is tenantID. A certificate that would log in
// as anyone else is refused with *IdentityMismatchError, however it chains.
func CheckIdentity(c *x509.Certificate, tenantID, username, trustDomain string) error {
	got, _, err := auth.TenantFromClientCert(c, trustDomain)
	if err != nil {
		return &IdentityMismatchError{TenantID: tenantID, Username: username, Err: err}
	}
	if tenantid.Normalize(got) != tenantid.Normalize(tenantID) || c.Subject.CommonName != username {
		return &IdentityMismatchError{
			TenantID: tenantID, Username: username,
			CertTenantID: got, CertUsername: c.Subject.CommonName,
		}
	}
	return nil
}
