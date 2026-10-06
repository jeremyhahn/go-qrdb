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

// Package certtest is a TEST helper: it plays the part of a client that
// generates its own key pair and has a cert.CertProvider sign it, returning
// both halves so a test can present the certificate. Production code never
// holds a client's private key and must not import this package.
package certtest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
)

// Request names the certificate a test wants.
type Request struct {
	Username string
	TenantID string
	TTL      time.Duration
}

// Bundle is a test client's certificate and the key it generated for it.
type Bundle struct {
	CertPEM  []byte
	KeyPEM   []byte
	Serial   string
	NotAfter time.Time
}

// Generate makes an ECDSA P-256 key, has prov issue a certificate for it,
// and returns both.
func Generate(ctx context.Context, prov cert.CertProvider, req *Request) (*Bundle, error) {
	key, keyPEM, err := NewKey()
	if err != nil {
		return nil, err
	}
	issued, err := prov.IssueClientCert(ctx, &cert.IssueRequest{
		Username:  req.Username,
		TenantID:  req.TenantID,
		PublicKey: &key.PublicKey,
		TTL:       req.TTL,
	})
	if err != nil {
		return nil, err
	}
	return &Bundle{CertPEM: issued.CertPEM, KeyPEM: keyPEM, Serial: issued.Serial, NotAfter: issued.NotAfter}, nil
}

// NewKey returns a fresh ECDSA P-256 key and its PEM encoding.
func NewKey() (*ecdsa.PrivateKey, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

// NewCSR returns a fresh ECDSA P-256 key's PEM encoding and a CSR for it.
func NewCSR(commonName string) (keyPEM, csrPEM []byte, err error) {
	key, keyPEM, err := NewKey()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.CertificateRequest{}
	tmpl.Subject.CommonName = commonName
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, nil, err
	}
	return keyPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// NewCAPEM returns a fresh self-signed CA certificate named commonName, in
// PEM: a trust anchor a test can register.
func NewCAPEM(commonName string) ([]byte, error) {
	key, _, err := NewKey()
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}
