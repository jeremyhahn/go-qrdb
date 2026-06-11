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
)

// SignCSROptions controls how a CSR is signed by the ceremony CA.
type SignCSROptions struct {
	// Profile is the certificate profile name (e.g., "security-officer").
	Profile string

	// ValidityDays is the number of days the certificate is valid.
	ValidityDays int

	// KeyUsage specifies the allowed key usage flags.
	KeyUsage x509.KeyUsage

	// ExtKeyUsage specifies the allowed extended key usage flags.
	ExtKeyUsage []x509.ExtKeyUsage
}

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
type CeremonyCA interface {
	// SignCSR signs a PEM-encoded certificate signing request with the
	// given options and returns the issued certificate.
	SignCSR(ctx context.Context, csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, error)

	// CACertificate returns the CA's own certificate.
	CACertificate() (*x509.Certificate, error)

	// CABundle returns the PEM-encoded CA certificate chain.
	CABundle() ([]byte, error)

	// IssueCertificate generates a new key pair and issues a certificate
	// with the given parameters.
	IssueCertificate(ctx context.Context, req *IssueCertRequest) (*IssuedCert, error)
}
