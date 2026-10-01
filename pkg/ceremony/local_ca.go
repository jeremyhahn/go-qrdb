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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/url"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
)

// localCASerialBits is the bit width of randomly generated certificate
// serial numbers issued by LocalCA.
const localCASerialBits = 128

// LocalCA is a software CeremonyCA backed by an in-process CA certificate
// and private key. It is the canonical CeremonyCA for deployments where the
// node's own cluster CA signs Security Officer client certificates: the
// signed officer certificates chain to the same CA the server trusts for
// mTLS client authentication, so a registered officer can immediately open
// an authenticated session to claim their share.
//
// Hardware-backed CAs (PKCS#11, TPM, cloud KMS) are NOT implemented here;
// those live in go-xkms and are injected through the same CeremonyCA
// interface.
type LocalCA struct {
	caCert    *x509.Certificate
	caKey     crypto.Signer
	caCertPEM []byte
}

// Compile-time interface check.
var _ CeremonyCA = (*LocalCA)(nil)

// NewLocalCA parses the PEM-encoded CA certificate and private key and
// returns a LocalCA that signs with them. The key may be encoded as SEC1
// ("EC PRIVATE KEY") or PKCS#8 ("PRIVATE KEY"). Returns ErrNilCA when
// either input is empty and a typed parse error when decoding fails.
func NewLocalCA(caCertPEM, caKeyPEM []byte) (*LocalCA, error) {
	if len(caCertPEM) == 0 || len(caKeyPEM) == 0 {
		return nil, ErrNilCA
	}

	certBlock, _ := pem.Decode(caCertPEM)
	if certBlock == nil {
		return nil, &CACertParseError{Err: errors.New("no PEM block in CA certificate")}
	}
	caCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, &CACertParseError{Err: err}
	}

	caKey, err := parseCAPrivateKeyPEM(caKeyPEM)
	if err != nil {
		return nil, err
	}

	return &LocalCA{
		caCert:    caCert,
		caKey:     caKey,
		caCertPEM: caCertPEM,
	}, nil
}

// SignCSR signs a PEM-encoded certificate signing request with the local CA
// key. The subject is taken from the CSR; key usage, extended key usage,
// and validity come from opts.
func (c *LocalCA) SignCSR(_ context.Context, csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, error) {
	if opts == nil {
		return nil, &CSRParseError{Err: errors.New("nil sign options")}
	}

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

	serial, err := randomSerial()
	if err != nil {
		return nil, &CertEncodeError{Err: err}
	}

	subject := csr.Subject
	if opts.Profile != "" {
		subject.OrganizationalUnit = append(subject.OrganizationalUnit, opts.Profile)
	}

	// The CSR's URI SANs are copied into the certificate, so a tenant-scoped
	// SPIFFE ID among them is a tenant claim this CA would sign. The mTLS
	// authenticator prefers that SAN over the Subject OU, so one that names a
	// different tenant than the OU yields a certificate that authenticates as
	// different tenants depending on whether the node has a trust-domain pin
	// (go-qrdb#519). Refuse it, as GenerateClientCert does.
	if err := checkTenantSANsAgreeWithOU(csr.URIs, subject.OrganizationalUnit); err != nil {
		return nil, err
	}

	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               subject,
		DNSNames:              csr.DNSNames,
		IPAddresses:           csr.IPAddresses,
		EmailAddresses:        csr.EmailAddresses,
		URIs:                  csr.URIs,
		NotBefore:             time.Now().Add(-1 * time.Minute),
		NotAfter:              time.Now().AddDate(0, 0, opts.ValidityDays),
		KeyUsage:              opts.KeyUsage,
		ExtKeyUsage:           opts.ExtKeyUsage,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, c.caCert, csr.PublicKey, c.caKey)
	if err != nil {
		return nil, &CertEncodeError{Err: err}
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, &CertEncodeError{Err: err}
	}
	return cert, nil
}

// checkTenantSANsAgreeWithOU refuses URI SANs that would give a signed
// certificate a tenant other than its first Subject OU: a malformed SPIFFE
// tenant ID (which the authenticator refuses outright), or a well-formed one
// naming a different tenant -- or naming one when the Subject has no OU. URIs
// that are not tenant SPIFFE IDs (node identities, other schemes) are left
// alone.
func checkTenantSANsAgreeWithOU(uris []*url.URL, ou []string) error {
	for _, u := range uris {
		id, ok, err := spiffe.ParseID(u)
		if err != nil {
			return &CSRTenantSANError{URI: u.String(), Reason: "malformed SPIFFE tenant identity"}
		}
		if !ok {
			continue
		}
		if len(ou) == 0 || id.TenantID != ou[0] {
			return &CSRTenantSANError{URI: u.String(), Reason: "its tenant does not match the certificate's Subject OU"}
		}
	}
	return nil
}

// CACertificate returns the parsed CA certificate.
func (c *LocalCA) CACertificate() (*x509.Certificate, error) {
	return c.caCert, nil
}

// CABundle returns the PEM-encoded CA certificate chain.
func (c *LocalCA) CABundle() ([]byte, error) {
	return c.caCertPEM, nil
}

// IssueCertificate generates a new ECDSA P-256 key pair and issues a leaf
// certificate signed by the local CA with the given parameters.
func (c *LocalCA) IssueCertificate(_ context.Context, req *IssueCertRequest) (*IssuedCert, error) {
	if req == nil {
		return nil, &CertEncodeError{Err: errors.New("nil issue request")}
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, &CertEncodeError{Err: err}
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, &CertEncodeError{Err: err}
	}

	dnsNames, ipAddresses := splitSANs(req.SANs)
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: req.CommonName},
		DNSNames:              dnsNames,
		IPAddresses:           ipAddresses,
		NotBefore:             time.Now().Add(-1 * time.Minute),
		NotAfter:              time.Now().AddDate(0, 0, req.ValidityDays),
		KeyUsage:              req.KeyUsage,
		ExtKeyUsage:           req.ExtKeyUsage,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, c.caCert, &leafKey.PublicKey, c.caKey)
	if err != nil {
		return nil, &CertEncodeError{Err: err}
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, &CertEncodeError{Err: err}
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return nil, &CertEncodeError{Err: err}
	}

	return &IssuedCert{
		Certificate: cert,
		CertPEM:     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:      pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	}, nil
}

// parseCAPrivateKeyPEM decodes a PEM-encoded private key in SEC1 or PKCS#8
// form and returns it as a crypto.Signer.
func parseCAPrivateKeyPEM(keyPEM []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, &CAKeyParseError{Err: errors.New("no PEM block in CA key")}
	}

	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, &CAKeyParseError{Err: err}
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, &CAKeyParseError{Err: errors.New("CA key does not implement crypto.Signer")}
	}
	return signer, nil
}

// randomSerial returns a cryptographically random certificate serial number.
func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), localCASerialBits))
}

// splitSANs partitions SAN strings into DNS names and IP addresses.
func splitSANs(sans []string) (dnsNames []string, ipAddresses []net.IP) {
	for _, san := range sans {
		if ip := net.ParseIP(san); ip != nil {
			ipAddresses = append(ipAddresses, ip)
			continue
		}
		dnsNames = append(dnsNames, san)
	}
	return dnsNames, ipAddresses
}
