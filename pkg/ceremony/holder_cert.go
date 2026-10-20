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
	"net/url"
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
	csr, err := parseAndVerifyCSR(csrPEM)
	if err != nil {
		return nil, nil, err
	}
	// An identity the SAN cannot name -- no trust domain above all -- is
	// refused before any CA is asked, not after one has signed.
	if _, err := opts.URIs(); err != nil {
		return nil, nil, err
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

// parseAndVerifyCSR decodes csrPEM's CERTIFICATE REQUEST block, parses it,
// and checks its self-signature -- the proof the requester actually holds
// the private key it names. It is the one place signHolderCSR and
// validateAdminCredentialInputs both reach for this check, so a CSR can
// never be accepted by one and rejected by the other for the same malformed
// input.
func parseAndVerifyCSR(csrPEM []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return nil, &CSRParseError{Err: ErrInvalidCSR}
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, &CSRParseError{Err: err}
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, &CSRParseError{Err: err}
	}
	return csr, nil
}

// checkIssuedSubject refuses a certificate a CeremonyCA returned unless its
// Subject is CN=opts.Username, OU=[opts.TenantID, opts.Profile], its only
// Subject Alternative Name is the agreeing tenant SPIFFE ID opts.URIs names,
// it certifies the CSR's own key, and it is a leaf
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
	wantURIs, err := opts.URIs()
	if err != nil {
		return err
	}
	if !sameURIs(cert.URIs, wantURIs) || len(cert.DNSNames) != 0 || len(cert.IPAddresses) != 0 || len(cert.EmailAddresses) != 0 {
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

// sameURIs reports whether got is exactly want, in order, compared by their
// string form: a parsed SPIFFE ID round-trips to the form it was issued with.
func sameURIs(got, want []*url.URL) bool {
	return slices.EqualFunc(got, want, func(g, w *url.URL) bool { return g.String() == w.String() })
}
