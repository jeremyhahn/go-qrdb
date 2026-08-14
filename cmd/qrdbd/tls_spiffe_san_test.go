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

package main

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
)

// TestGenerateNodeCert_SPIFFESANPresent verifies that generateNodeCert embeds
// the supplied SPIFFE URI as a URI SAN in the leaf certificate.
func TestGenerateNodeCert_SPIFFESANPresent(t *testing.T) {
	t.Parallel()

	_, _, caCert, caKey, err := generateCA(caValidity)
	if err != nil {
		t.Fatalf("generateCA: %v", err)
	}

	const trustDomain = "test.cluster"
	const raftAddr = "127.0.0.1:63001"
	nodeURI := spiffe.NodeURI(trustDomain, raftAddr)

	certPEM, _, err := generateNodeCert(caCert, caKey, nodeValidity, nodeURI)
	if err != nil {
		t.Fatalf("generateNodeCert: %v", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no PEM block in certPEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	if len(cert.URIs) == 0 {
		t.Fatal("expected at least one URI SAN, got none")
	}
	found := false
	for _, u := range cert.URIs {
		if u.String() == nodeURI {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected URI SAN %q not found in cert.URIs: %v", nodeURI, cert.URIs)
	}
}

// TestGenerateNodeCert_SPIFFESANExtraction verifies that pkg/auth/spiffe
// can round-trip the node identity through the generated certificate.
func TestGenerateNodeCert_SPIFFESANExtraction(t *testing.T) {
	t.Parallel()

	_, _, caCert, caKey, err := generateCA(caValidity)
	if err != nil {
		t.Fatalf("generateCA: %v", err)
	}

	const trustDomain = "qrdb.cluster"
	const raftAddr = "10.0.0.1:63001"
	nodeURI := spiffe.NodeURI(trustDomain, raftAddr)

	certPEM, _, err := generateNodeCert(caCert, caKey, nodeValidity, nodeURI)
	if err != nil {
		t.Fatalf("generateNodeCert: %v", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no PEM block in certPEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	// pkg/auth/spiffe must be able to extract the node identity from the cert.
	identity, err := spiffe.NodeFromCert(cert, trustDomain)
	if err != nil {
		t.Fatalf("spiffe.NodeFromCert: %v", err)
	}
	if identity.NodeID != raftAddr {
		t.Errorf("NodeID: got %q, want %q", identity.NodeID, raftAddr)
	}
	if identity.TrustDomain != trustDomain {
		t.Errorf("TrustDomain: got %q, want %q", identity.TrustDomain, trustDomain)
	}
}

// TestGenerateNodeCert_NoNodeURI verifies backward compatibility: when nodeURI
// is omitted the generated certificate has no URI SANs and the function
// succeeds without error.
func TestGenerateNodeCert_NoNodeURI(t *testing.T) {
	t.Parallel()

	_, _, caCert, caKey, err := generateCA(caValidity)
	if err != nil {
		t.Fatalf("generateCA: %v", err)
	}

	certPEM, _, err := generateNodeCert(caCert, caKey, nodeValidity)
	if err != nil {
		t.Fatalf("generateNodeCert (no URI): %v", err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no PEM block in certPEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if len(cert.URIs) != 0 {
		t.Errorf("expected no URI SANs when nodeURI is omitted, got %v", cert.URIs)
	}
}

// TestGenerateCATLS_NodeURIThreaded verifies that generateCATLS passes the
// nodeURI through to the leaf certificate.
func TestGenerateCATLS_NodeURIThreaded(t *testing.T) {
	t.Parallel()

	const trustDomain = "acme.cluster"
	const raftAddr = "192.168.1.10:63001"
	nodeURI := spiffe.NodeURI(trustDomain, raftAddr)

	bundle, err := generateCATLS(nodeURI)
	if err != nil {
		t.Fatalf("generateCATLS: %v", err)
	}

	block, _ := pem.Decode(bundle.CertPEM)
	if block == nil {
		t.Fatal("no PEM block in bundle.CertPEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	identity, err := spiffe.NodeFromCert(cert, trustDomain)
	if err != nil {
		t.Fatalf("spiffe.NodeFromCert on CATLSBundle cert: %v", err)
	}
	if identity.NodeID != raftAddr {
		t.Errorf("NodeID: got %q, want %q", identity.NodeID, raftAddr)
	}
}

// TestGenerateNodeCert_InvalidURIFails verifies that generateNodeCert returns
// a *TLSGenerateError when the supplied nodeURI contains a raw control
// character that url.Parse rejects (RFC 3986 / net/url: invalid control
// character in URL).
func TestGenerateNodeCert_InvalidURIFails(t *testing.T) {
	t.Parallel()

	_, _, caCert, caKey, err := generateCA(caValidity)
	if err != nil {
		t.Fatalf("generateCA: %v", err)
	}

	// A null byte embedded in the host component is an ASCII control
	// character (0x00). url.Parse returns an error for any URL that contains
	// raw control characters.
	invalidURI := "spiffe://\x00host/node/127.0.0.1:63001"

	_, _, err = generateNodeCert(caCert, caKey, nodeValidity, invalidURI)
	if err == nil {
		t.Fatal("expected error for URI with null byte, got nil")
	}
	var tlsErr *TLSGenerateError
	if !errors.As(err, &tlsErr) {
		t.Errorf("expected *TLSGenerateError, got %T: %v", err, err)
	}
}

// TestGenerateCATLS_InvalidURIPropagates verifies that generateCATLS
// propagates the *TLSGenerateError returned by generateNodeCert when the
// nodeURI contains an invalid control character.
func TestGenerateCATLS_InvalidURIPropagates(t *testing.T) {
	t.Parallel()

	invalidURI := "spiffe://\x00host/node/127.0.0.1:63001"

	_, err := generateCATLS(invalidURI)
	if err == nil {
		t.Fatal("expected error for invalid URI, got nil")
	}
	var tlsErr *TLSGenerateError
	if !errors.As(err, &tlsErr) {
		t.Errorf("expected *TLSGenerateError, got %T: %v", err, err)
	}
}

// TestGenerateLeafFromCA_InvalidURIPropagates verifies that generateLeafFromCA
// propagates the *TLSGenerateError returned by generateNodeCert when the
// nodeURI contains an invalid control character.
func TestGenerateLeafFromCA_InvalidURIPropagates(t *testing.T) {
	t.Parallel()

	caBundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS for CA: %v", err)
	}

	bundlePEM := append(caBundle.CACertPEM, caBundle.CAKeyPEM...)
	invalidURI := "spiffe://\x00host/node/127.0.0.1:63001"

	_, err = generateLeafFromCA(bundlePEM, invalidURI)
	if err == nil {
		t.Fatal("expected error for invalid URI, got nil")
	}
	var tlsErr *TLSGenerateError
	if !errors.As(err, &tlsErr) {
		t.Errorf("expected *TLSGenerateError, got %T: %v", err, err)
	}
}

// TestGenerateLeafFromCA_NodeURIThreaded verifies that generateLeafFromCA
// passes the nodeURI through to the generated leaf certificate.
func TestGenerateLeafFromCA_NodeURIThreaded(t *testing.T) {
	t.Parallel()

	// Generate a CA bundle (no node URI needed for the CA itself).
	caBundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS for CA: %v", err)
	}

	// Combine CA cert + key as the bundle PEM.
	bundlePEM := append(caBundle.CACertPEM, caBundle.CAKeyPEM...)

	const trustDomain = "leaf.cluster"
	const raftAddr = "172.16.0.5:63001"
	nodeURI := spiffe.NodeURI(trustDomain, raftAddr)

	leafBundle, err := generateLeafFromCA(bundlePEM, nodeURI)
	if err != nil {
		t.Fatalf("generateLeafFromCA: %v", err)
	}

	block, _ := pem.Decode(leafBundle.CertPEM)
	if block == nil {
		t.Fatal("no PEM block in leafBundle.CertPEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	identity, err := spiffe.NodeFromCert(cert, trustDomain)
	if err != nil {
		t.Fatalf("spiffe.NodeFromCert on leaf cert: %v", err)
	}
	if identity.NodeID != raftAddr {
		t.Errorf("NodeID: got %q, want %q", identity.NodeID, raftAddr)
	}
}
