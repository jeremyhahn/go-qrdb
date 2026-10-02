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
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"slices"
)

// signHolderCSR is the one path by which this package has the ceremony CA
// sign anything. It verifies the CSR's proof of possession, has ca sign it
// under opts -- which name the registered holder (or the user SignCSRInit
// was asked for) and the scope's tenant -- and then refuses the result unless
// it is exactly what opts describe (checkIssuedSubject).
//
// The CA is the node's cluster CA, trusted by the mTLS listener for client
// logins, so a certificate's Subject is a login claim. It is therefore never
// taken from the CSR: before v0.28.1, a tenant Admin registering an officer
// with a CSR for "CN=admin, OU=system" received a certificate that
// authenticated as the system tenant's admin. A CA error is a
// *CertEncodeError.
func signHolderCSR(ctx context.Context, ca CeremonyCA, csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, *x509.CertificateRequest, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return nil, nil, &CSRParseError{Err: ErrInvalidCSR}
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, nil, &CSRParseError{Err: err}
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, nil, &CSRParseError{Err: err}
	}

	cert, err := ca.SignCSR(ctx, csrPEM, opts)
	if err != nil {
		return nil, nil, &CertEncodeError{Err: err}
	}
	if err := checkIssuedSubject(cert, csr, opts); err != nil {
		return nil, nil, err
	}
	return cert, csr, nil
}

// checkIssuedSubject refuses a certificate a CeremonyCA returned unless its
// Subject is CN=opts.Username, OU=[opts.TenantID, opts.Profile], it carries no
// Subject Alternative Name, it certifies the CSR's own key, and it is a leaf
// with exactly the requested usages: not a CA, no certificate-signing key
// usage, key usage exactly opts.KeyUsage, and extended key usages exactly the
// set opts.ExtKeyUsage names (order aside, no duplicates, nothing unknown). It
// holds every CeremonyCA implementation -- including ones composed in from
// outside this module -- to the SignCSROptions contract, rather than trusting
// each to.
func checkIssuedSubject(cert *x509.Certificate, csr *x509.CertificateRequest, opts *SignCSROptions) error {
	want, err := opts.Subject()
	if err != nil {
		return err
	}
	if cert.Subject.CommonName != want.CommonName {
		return &CertSubjectError{Field: "CommonName"}
	}
	if !slices.Equal(cert.Subject.OrganizationalUnit, want.OrganizationalUnit) {
		return &CertSubjectError{Field: "OrganizationalUnit"}
	}
	if len(cert.URIs) != 0 || len(cert.DNSNames) != 0 || len(cert.IPAddresses) != 0 || len(cert.EmailAddresses) != 0 {
		return &CertSubjectError{Field: "SubjectAltName"}
	}
	pub, ok := cert.PublicKey.(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !pub.Equal(csr.PublicKey) {
		return &CertSubjectError{Field: "PublicKey"}
	}
	if cert.IsCA {
		return &IssuedCertCapabilityError{Field: "IsCA"}
	}
	if cert.KeyUsage&x509.KeyUsageCertSign != 0 || cert.KeyUsage != opts.KeyUsage {
		return &IssuedCertCapabilityError{Field: "KeyUsage"}
	}
	if !sameExtKeyUsages(cert.ExtKeyUsage, opts.ExtKeyUsage) || len(cert.UnknownExtKeyUsage) != 0 {
		return &IssuedCertCapabilityError{Field: "ExtKeyUsage"}
	}
	return nil
}

// sameExtKeyUsages reports whether got is exactly the set want names: the
// same usages, in any order, with none repeated.
func sameExtKeyUsages(got, want []x509.ExtKeyUsage) bool {
	if len(got) != len(want) {
		return false
	}
	g := slices.Clone(got)
	w := slices.Clone(want)
	slices.Sort(g)
	slices.Sort(w)
	for i := 1; i < len(g); i++ {
		if g[i] == g[i-1] {
			return false
		}
	}
	return slices.Equal(g, w)
}
