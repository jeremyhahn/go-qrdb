// Copyright (c) 2026 Jeremy Hahn
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

package ceremony

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
)

// SignCSROptions controls how a CSR is signed by the ceremony CA.
//
// The certificate's identity comes from HERE, never from the CSR. A ceremony
// CA signs under the node's cluster CA, which the mTLS listener trusts for
// client logins, so a CSR's Subject and SANs would otherwise be a login claim
// the registering caller chose. A CeremonyCA MUST sign with RawSubject() as
// the certificate's raw Subject -- CN=Username, OU=[TenantID, Profile], one
// RDN per attribute -- carry NO Subject Alternative Name, use the CSR only for
// its public key and proof of possession, and issue a leaf: not a CA, no
// certificate-signing key usage, key usage exactly KeyUsage, and extended key
// usages exactly the set ExtKeyUsage names (order aside, no duplicates).
// The ceremony re-checks every certificate a CA returns (checkIssuedSubject)
// and refuses one that does not conform, fail-closed, with *CertSubjectError
// or *IssuedCertCapabilityError; nothing is recorded for it.
type SignCSROptions struct {
	// TenantID is the tenant the certificate authenticates in: its first
	// Subject OU. It is the signing scope's tenant -- the system tenant for
	// the node ceremony and recovery scope, the tenant itself for a
	// tenant-barrier officer. Required.
	TenantID string

	// Username is the certificate's Common Name: the officer, holder or user
	// the ceremony registered. Required.
	Username string

	// Profile is the certificate profile name (e.g., "security-officer"),
	// the second Subject OU.
	Profile string

	// ValidityDays is the number of days the certificate is valid.
	ValidityDays int

	// KeyUsage specifies the allowed key usage flags.
	KeyUsage x509.KeyUsage

	// ExtKeyUsage specifies the allowed extended key usage flags.
	ExtKeyUsage []x509.ExtKeyUsage
}

// Subject is the Subject a CeremonyCA must give the certificate:
// CN=Username, OU=[TenantID, Profile], with Profile omitted when empty. A
// missing TenantID or Username is a *CertSubjectError, and nothing may be
// signed.
func (o *SignCSROptions) Subject() (pkix.Name, error) {
	if o.TenantID == "" {
		return pkix.Name{}, &CertSubjectError{Field: "TenantID"}
	}
	if o.Username == "" {
		return pkix.Name{}, &CertSubjectError{Field: "Username"}
	}
	ou := []string{o.TenantID}
	if o.Profile != "" {
		ou = append(ou, o.Profile)
	}
	return pkix.Name{CommonName: o.Username, OrganizationalUnit: ou}, nil
}

// RawSubject is Subject DER-encoded with every attribute in its OWN relative
// distinguished name, in order: CN, then the tenant OU, then the profile OU.
// A CeremonyCA signs with this as the certificate's RawSubject.
//
// It matters because the authenticator reads the tenant from the FIRST OU,
// and encoding/x509 puts several OUs of a pkix.Name into one RDN -- a DER SET,
// whose members are sorted by encoding, so the shorter value comes first. A
// tenant longer than its profile ("tenant-list" against "custodian") would
// then parse back with the PROFILE as the first OU, and the certificate would
// authenticate in a tenant named after the profile.
func (o *SignCSROptions) RawSubject() ([]byte, error) {
	name, err := o.Subject()
	if err != nil {
		return nil, err
	}
	rdns := pkix.RDNSequence{
		{{Type: oidCommonName, Value: name.CommonName}},
	}
	for _, ou := range name.OrganizationalUnit {
		rdns = append(rdns, pkix.RelativeDistinguishedNameSET{{Type: oidOrganizationalUnit, Value: ou}})
	}
	der, err := asn1.Marshal(rdns)
	if err != nil {
		return nil, &CertEncodeError{Err: err}
	}
	return der, nil
}

// The X.520 attribute types RawSubject writes.
var (
	oidCommonName         = asn1.ObjectIdentifier{2, 5, 4, 3}
	oidOrganizationalUnit = asn1.ObjectIdentifier{2, 5, 4, 11}
)

// IssueCertRequest describes a certificate to be issued directly by the CA,
// including key generation.
type IssueCertRequest struct {
	// CommonName is the subject common name for the certificate.
	CommonName string

	// SANs is the list of Subject Alternative Names.
	SANs []string

	// KeyUsage specifies the allowed key usage flags.
	KeyUsage x509.KeyUsage

	// ExtKeyUsage specifies the allowed extended key usage flags.
	ExtKeyUsage []x509.ExtKeyUsage

	// ValidityDays is the number of days the certificate is valid.
	ValidityDays int
}

// IssuedCert holds a certificate and its corresponding private key,
// both as parsed objects and PEM-encoded bytes.
type IssuedCert struct {
	// Certificate is the parsed x509 certificate.
	Certificate *x509.Certificate

	// CertPEM is the PEM-encoded certificate.
	CertPEM []byte

	// KeyPEM is the PEM-encoded private key.
	KeyPEM []byte
}

// CeremonyCA is the interface for the certificate authority used during
// the initialization ceremony. It provides CSR signing, direct certificate
// issuance, and CA bundle retrieval.
//
// SignCSR MUST follow the SignCSROptions contract: sign with
// SignCSROptions.RawSubject() as the raw Subject, no Subject Alternative
// Name, nothing from the CSR but its key, and a leaf certificate whose key
// usage is exactly SignCSROptions.KeyUsage and whose extended key usages are
// exactly the set SignCSROptions.ExtKeyUsage names. An
// implementation that does not -- including one composed in from outside
// this module -- has its certificates refused, fail-closed, with
// *CertSubjectError or *IssuedCertCapabilityError.
type CeremonyCA interface {
	// SignCSR signs a PEM-encoded certificate signing request with the
	// given options and returns the issued certificate.
	SignCSR(ctx context.Context, csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, error)

	// CACertificate returns the CA's own certificate, resolved without ever
	// generating one as a side effect of being asked -- see
	// cakey.CertOnlyProvider, which ProviderCA's implementation prefers
	// when the wrapped Provider offers it. ctx bounds the resolution; a
	// caller on a TLS handshake's trust path derives it from the
	// handshake's own hello.Context() with a bounded timeout, never
	// context.Background().
	CACertificate(ctx context.Context) (*x509.Certificate, error)

	// CABundle returns the PEM-encoded CA certificate chain.
	CABundle() ([]byte, error)

	// IssueCertificate generates a new key pair and issues a certificate
	// with the given parameters.
	IssueCertificate(ctx context.Context, req *IssueCertRequest) (*IssuedCert, error)
}
