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

package ceremony

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
)

// ProviderCA is a CeremonyCA backed by a cakey.Provider rather than a raw
// PEM key pair held for the lifetime of the value. Unlike LocalCA, it
// resolves the signer and certificate fresh on every call, so a re-seal,
// re-unseal, or an HSM/TPM/KMS failover behind the provider is reflected
// immediately rather than requiring a new CeremonyCA to be constructed.
//
// This is the CeremonyCA every signing path converges on once Key A is
// reachable through the provider seam (see
// docs/certificate-authority/architecture.md, "The CA key provider seam").
// LocalCA remains for the one case a Provider cannot cover: the pre-store
// bootstrap window, where cakey.StaticProvider wraps the in-memory key and
// is itself a Provider -- so in practice ProviderCA is used there too, and
// LocalCA is kept only for callers still holding raw PEM.
type ProviderCA struct {
	provider cakey.Provider
}

// Compile-time interface check.
var _ CeremonyCA = (*ProviderCA)(nil)

// NewProviderCA returns a CeremonyCA that signs through p. Returns
// *NilCeremonyCAProviderError when p is nil.
func NewProviderCA(p cakey.Provider) (*ProviderCA, error) {
	if p == nil {
		return nil, &NilCeremonyCAProviderError{}
	}
	return &ProviderCA{provider: p}, nil
}

// SignCSR resolves the provider's signer and certificate and signs csrPEM
// under the same subject-replacement rule every CeremonyCA in this package
// applies.
func (c *ProviderCA) SignCSR(ctx context.Context, csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, error) {
	caCert, caKey, err := c.material(ctx)
	if err != nil {
		return nil, err
	}
	return signCSRWith(caCert, caKey, csrPEM, opts)
}

// CACertificate returns the provider's CA certificate. CeremonyCA's method
// of this name takes no context; Certificate is resolved with
// context.Background(), matching how CACertPEM()-shaped accessors elsewhere
// in this codebase treat the CA certificate as public, context-free data.
func (c *ProviderCA) CACertificate() (*x509.Certificate, error) {
	return c.provider.Certificate(context.Background())
}

// CABundle returns the provider's CA certificate, PEM-encoded.
func (c *ProviderCA) CABundle() ([]byte, error) {
	cert, err := c.provider.Certificate(context.Background())
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), nil
}

// IssueCertificate generates a new key pair and issues a leaf certificate
// signed through the provider.
func (c *ProviderCA) IssueCertificate(ctx context.Context, req *IssueCertRequest) (*IssuedCert, error) {
	caCert, caKey, err := c.material(ctx)
	if err != nil {
		return nil, err
	}
	return issueCertificateWith(caCert, caKey, req)
}

// material resolves the provider's certificate and signer together,
// reporting a *ProviderCASignerError or *ProviderCACertificateError naming
// which call failed.
func (c *ProviderCA) material(ctx context.Context) (*x509.Certificate, crypto.Signer, error) {
	cert, err := c.provider.Certificate(ctx)
	if err != nil {
		return nil, nil, &ProviderCACertificateError{Err: err}
	}
	signer, err := c.provider.Signer(ctx)
	if err != nil {
		return nil, nil, &ProviderCASignerError{Err: err}
	}
	return cert, signer, nil
}
