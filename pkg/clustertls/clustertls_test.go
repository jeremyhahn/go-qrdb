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

package clustertls_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

// TestGenerateCABundle_ValidMaterial verifies that GenerateCABundle produces a
// self-signed CA and a leaf certificate with the expected KeyUsage and
// ExtKeyUsage values.
func TestGenerateCABundle_ValidMaterial(t *testing.T) {
	t.Parallel()

	m, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil Material")
	}
	if len(m.CACertPEM) == 0 {
		t.Error("CACertPEM is empty")
	}
	if len(m.CAKeyPEM) == 0 {
		t.Error("CAKeyPEM is empty")
	}
	if len(m.CertPEM) == 0 {
		t.Error("CertPEM is empty")
	}
	if len(m.KeyPEM) == 0 {
		t.Error("KeyPEM is empty")
	}

	// Parse and validate CA certificate.
	caBlock, _ := pem.Decode(m.CACertPEM)
	if caBlock == nil {
		t.Fatal("no PEM block in CACertPEM")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate(CA): %v", err)
	}
	if !caCert.IsCA {
		t.Error("CA certificate IsCA = false")
	}
	if caCert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("CA cert missing KeyUsageCertSign")
	}
	if caCert.KeyUsage&x509.KeyUsageCRLSign == 0 {
		t.Error("CA cert missing KeyUsageCRLSign")
	}

	// Parse and validate leaf certificate.
	leafBlock, _ := pem.Decode(m.CertPEM)
	if leafBlock == nil {
		t.Fatal("no PEM block in CertPEM")
	}
	leafCert, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate(leaf): %v", err)
	}
	if leafCert.IsCA {
		t.Error("leaf certificate IsCA = true")
	}
	if leafCert.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		t.Error("leaf cert missing KeyUsageDigitalSignature")
	}

	hasServer := false
	hasClient := false
	for _, eku := range leafCert.ExtKeyUsage {
		switch eku {
		case x509.ExtKeyUsageServerAuth:
			hasServer = true
		case x509.ExtKeyUsageClientAuth:
			hasClient = true
		}
	}
	if !hasServer {
		t.Error("leaf cert missing ExtKeyUsageServerAuth")
	}
	if !hasClient {
		t.Error("leaf cert missing ExtKeyUsageClientAuth")
	}

	// Leaf must be verifiable against the CA.
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := leafCert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		t.Errorf("leaf cert verification failed: %v", err)
	}
}

// TestGenerateCABundle_SPIFFENodeURIPresent verifies that a nodeURI is embedded
// as a URI SAN in the leaf certificate when provided.
func TestGenerateCABundle_SPIFFENodeURIPresent(t *testing.T) {
	t.Parallel()

	const nodeURI = "spiffe://test.cluster/node/127.0.0.1:63001"
	m, err := clustertls.GenerateCABundle(nodeURI)
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}

	block, _ := pem.Decode(m.CertPEM)
	if block == nil {
		t.Fatal("no PEM block in CertPEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
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

// TestGenerateCABundle_InvalidURIReturnsGenerateError verifies that a
// syntactically invalid (non-empty) nodeURI returns a *GenerateError.
func TestGenerateCABundle_InvalidURIReturnsGenerateError(t *testing.T) {
	t.Parallel()

	// A null byte embedded in the host component is an ASCII control character
	// that url.Parse rejects.
	invalidURI := "spiffe://\x00host/node/127.0.0.1:63001"

	_, err := clustertls.GenerateCABundle(invalidURI)
	if err == nil {
		t.Fatal("expected error for invalid URI, got nil")
	}
	var genErr *clustertls.GenerateError
	if !errors.As(err, &genErr) {
		t.Errorf("expected *clustertls.GenerateError, got %T: %v", err, err)
	}
}

// TestGenerateLeafFromCA_RoundTrip verifies that GenerateLeafFromCA produces
// a valid leaf certificate verifiable against the CA from the original bundle.
func TestGenerateLeafFromCA_RoundTrip(t *testing.T) {
	t.Parallel()

	orig, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}

	bundlePEM := append(append([]byte{}, orig.CACertPEM...), orig.CAKeyPEM...)
	m, err := clustertls.GenerateLeafFromCA(bundlePEM)
	if err != nil {
		t.Fatalf("GenerateLeafFromCA: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil Material")
	}

	// CA cert in the new Material must match the original.
	if string(m.CACertPEM) != string(orig.CACertPEM) {
		t.Error("CACertPEM mismatch between original and leaf Material")
	}

	// Leaf cert must be verifiable against the CA.
	caBlock, _ := pem.Decode(m.CACertPEM)
	if caBlock == nil {
		t.Fatal("no PEM block in CACertPEM")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate(CA): %v", err)
	}
	leafBlock, _ := pem.Decode(m.CertPEM)
	if leafBlock == nil {
		t.Fatal("no PEM block in CertPEM")
	}
	leafCert, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate(leaf): %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := leafCert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		t.Errorf("leaf cert verification failed: %v", err)
	}
}

// TestGenerateLeafFromCA_NoCertBlock verifies that a *KeyError is returned
// when the PEM data contains no CERTIFICATE block.
func TestGenerateLeafFromCA_NoCertBlock(t *testing.T) {
	t.Parallel()

	_, err := clustertls.GenerateLeafFromCA([]byte("not-pem-data"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var keyErr *clustertls.KeyError
	if !errors.As(err, &keyErr) {
		t.Errorf("expected *clustertls.KeyError, got %T: %v", err, err)
	}
	if keyErr.Kind != "CA certificate" {
		t.Errorf("expected Kind = %q, got %q", "CA certificate", keyErr.Kind)
	}
}

// TestGenerateLeafFromCA_NoKeyBlock verifies that a *KeyError is returned
// when the PEM data has a CERTIFICATE block but no EC PRIVATE KEY block.
func TestGenerateLeafFromCA_NoKeyBlock(t *testing.T) {
	t.Parallel()

	_, _, _, _, err := clustertls.GenerateCA(time.Hour)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	caCertPEM, _, _, _, err := clustertls.GenerateCA(time.Hour)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	_, err = clustertls.GenerateLeafFromCA(caCertPEM)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var keyErr *clustertls.KeyError
	if !errors.As(err, &keyErr) {
		t.Errorf("expected *clustertls.KeyError, got %T: %v", err, err)
	}
	if keyErr.Kind != "CA private key" {
		t.Errorf("expected Kind = %q, got %q", "CA private key", keyErr.Kind)
	}
}

// TestPersistAndLoadFromDir verifies that PersistMaterial writes all files
// and that LoadFromDir round-trips the full Material.
func TestPersistAndLoadFromDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	m, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}

	if err := clustertls.PersistMaterial(m, dir); err != nil {
		t.Fatalf("PersistMaterial: %v", err)
	}

	// Verify file permissions.
	for _, name := range []string{"ca.pem", "node-cert.pem", "node-key.pem", "ca-key.pem"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("stat %s: %v", name, err)
			continue
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s permissions = %o, want 0o600", name, info.Mode().Perm())
		}
	}

	loaded, err := clustertls.LoadFromDir(dir)
	if err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil Material from LoadFromDir")
	}

	if string(loaded.CACertPEM) != string(m.CACertPEM) {
		t.Error("CACertPEM mismatch after round-trip")
	}
	if string(loaded.CAKeyPEM) != string(m.CAKeyPEM) {
		t.Error("CAKeyPEM mismatch after round-trip")
	}
	if string(loaded.CertPEM) != string(m.CertPEM) {
		t.Error("CertPEM mismatch after round-trip")
	}
	if string(loaded.KeyPEM) != string(m.KeyPEM) {
		t.Error("KeyPEM mismatch after round-trip")
	}
}

// TestLoadFromDir_AbsentFilesReturnsNil verifies that LoadFromDir returns
// (nil, nil) when the required cert files are not present in the directory.
func TestLoadFromDir_AbsentFilesReturnsNil(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	m, err := clustertls.LoadFromDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m != nil {
		t.Fatalf("expected nil Material, got non-nil")
	}
}

// TestLoadFromDir_PartialFilesReturnsError verifies that LoadFromDir fails
// closed with a *PartialMaterialError when only some required files exist (not
// all three): a partial directory is treated as corruption rather than silently
// regenerating a fresh CA.
func TestLoadFromDir_PartialFilesReturnsError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Write only ca.pem and node-cert.pem; omit node-key.pem.
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), []byte("cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node-cert.pem"), []byte("cert"), 0o600); err != nil {
		t.Fatal(err)
	}

	m, err := clustertls.LoadFromDir(dir)
	if m != nil {
		t.Fatal("expected nil Material when node-key.pem is absent")
	}
	var partErr *clustertls.PartialMaterialError
	if !errors.As(err, &partErr) {
		t.Fatalf("expected *PartialMaterialError, got %T: %v", err, err)
	}
	if len(partErr.Missing) != 1 || filepath.Base(partErr.Missing[0]) != "node-key.pem" {
		t.Errorf("Missing = %v, want [.../node-key.pem]", partErr.Missing)
	}
}

// TestPersistMaterial_WriteError verifies that PersistMaterial returns a
// *WriteError when the target path cannot be created (e.g. it is a regular
// file, not a directory).
func TestPersistMaterial_WriteError(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	// Create a regular file at the path we want as the dataDir so MkdirAll fails.
	badDir := filepath.Join(tmp, "blockfile")
	if err := os.WriteFile(badDir, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := &clustertls.Material{
		CACertPEM: []byte("ca"),
		CertPEM:   []byte("cert"),
		KeyPEM:    []byte("key"),
	}
	err := clustertls.PersistMaterial(m, badDir)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var writeErr *clustertls.WriteError
	if !errors.As(err, &writeErr) {
		t.Errorf("expected *clustertls.WriteError, got %T: %v", err, err)
	}
}

// TestPersistMaterial_WithoutCAKey verifies that PersistMaterial omits
// ca-key.pem when CAKeyPEM is empty and that LoadFromDir returns empty CAKeyPEM.
func TestPersistMaterial_WithoutCAKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	m := &clustertls.Material{}
	orig, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}
	m.CACertPEM = orig.CACertPEM
	m.CertPEM = orig.CertPEM
	m.KeyPEM = orig.KeyPEM
	// CAKeyPEM intentionally left empty.

	if err := clustertls.PersistMaterial(m, dir); err != nil {
		t.Fatalf("PersistMaterial: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "ca-key.pem")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ca-key.pem should not exist when CAKeyPEM is empty")
	}

	loaded, err := clustertls.LoadFromDir(dir)
	if err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	if len(loaded.CAKeyPEM) != 0 {
		t.Error("expected empty CAKeyPEM when ca-key.pem absent")
	}
}

// TestLocalSANs_ContainsLoopback verifies that LocalSANs always includes
// localhost and 127.0.0.1.
func TestLocalSANs_ContainsLoopback(t *testing.T) {
	t.Parallel()

	dnsNames, ips := clustertls.LocalSANs()

	foundLocalhost := false
	for _, n := range dnsNames {
		if n == "localhost" {
			foundLocalhost = true
			break
		}
	}
	if !foundLocalhost {
		t.Error("LocalSANs missing localhost in dnsNames")
	}

	foundLoopback := false
	loopback := []byte{127, 0, 0, 1}
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil && string(ip4) == string(loopback) {
			foundLoopback = true
			break
		}
	}
	if !foundLoopback {
		t.Error("LocalSANs missing 127.0.0.1 in ips")
	}
}

// TestGenerateCA_ValidCert verifies that GenerateCA produces a self-signed
// CA certificate with the correct attributes.
func TestGenerateCA_ValidCert(t *testing.T) {
	t.Parallel()

	caCertPEM, caKeyPEM, caCert, caKey, err := clustertls.GenerateCA(time.Hour)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	if len(caCertPEM) == 0 {
		t.Error("caCertPEM is empty")
	}
	if len(caKeyPEM) == 0 {
		t.Error("caKeyPEM is empty")
	}
	if caCert == nil {
		t.Error("caCert is nil")
	}
	if caKey == nil {
		t.Error("caKey is nil")
	}
	if !caCert.IsCA {
		t.Error("generated certificate IsCA = false")
	}
}

// TestGenerateClientCert_ClientAuthOnly verifies that GenerateClientCert
// produces a certificate with only ExtKeyUsageClientAuth.
func TestGenerateClientCert_ClientAuthOnly(t *testing.T) {
	t.Parallel()

	_, _, caCert, caKey, err := clustertls.GenerateCA(time.Hour)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	certPEM, keyPEM, err := clustertls.GenerateClientCert(caCert, caKey, "admin", "tenant-1", time.Hour)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		t.Error("empty PEM output")
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no PEM block in certPEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	if cert.Subject.CommonName != "admin" {
		t.Errorf("CN = %q, want %q", cert.Subject.CommonName, "admin")
	}
	if len(cert.Subject.OrganizationalUnit) == 0 || cert.Subject.OrganizationalUnit[0] != "tenant-1" {
		t.Errorf("OU = %v, want [tenant-1]", cert.Subject.OrganizationalUnit)
	}

	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageServerAuth {
			t.Error("client cert must not have ExtKeyUsageServerAuth")
		}
	}
	hasClientAuth := false
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageClientAuth {
			hasClientAuth = true
		}
	}
	if !hasClientAuth {
		t.Error("client cert missing ExtKeyUsageClientAuth")
	}
}

// ---------------------------------------------------------------------------
// IssueLeafForKey tests
// ---------------------------------------------------------------------------

// TestIssueLeafForKey_VerifiesAgainstCA verifies that IssueLeafForKey produces
// a node leaf certificate whose public key matches the supplied external key
// and that the certificate chains to the CA.
func TestIssueLeafForKey_VerifiesAgainstCA(t *testing.T) {
	t.Parallel()

	caCertPEM, caKeyPEM, _, _, err := clustertls.GenerateCA(time.Hour)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	externalKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	certPEM, err := clustertls.IssueLeafForKey(caCertPEM, caKeyPEM, &externalKey.PublicKey)
	if err != nil {
		t.Fatalf("IssueLeafForKey: %v", err)
	}
	if len(certPEM) == 0 {
		t.Fatal("expected non-empty certPEM")
	}

	// Parse CA cert for pool.
	caBlock, _ := pem.Decode(caCertPEM)
	if caBlock == nil {
		t.Fatal("no PEM block in caCertPEM")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate(CA): %v", err)
	}

	// Parse leaf cert.
	leafBlock, _ := pem.Decode(certPEM)
	if leafBlock == nil {
		t.Fatal("no PEM block in certPEM")
	}
	leafCert, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate(leaf): %v", err)
	}

	// Leaf must chain to the CA.
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := leafCert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		t.Errorf("leaf cert verification failed: %v", err)
	}

	// Leaf public key must match the supplied external key.
	leafPub, ok := leafCert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatal("leaf cert public key is not *ecdsa.PublicKey")
	}
	if !leafPub.Equal(&externalKey.PublicKey) {
		t.Error("leaf cert public key does not match the supplied external public key")
	}
}

// TestIssueLeafForKey_SPIFFESANPresent verifies that IssueLeafForKey embeds
// the nodeURI as a URI SAN when one is provided.
func TestIssueLeafForKey_SPIFFESANPresent(t *testing.T) {
	t.Parallel()

	caCertPEM, caKeyPEM, _, _, err := clustertls.GenerateCA(time.Hour)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	externalKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	const nodeURI = "spiffe://hw.cluster/node/10.0.0.1:63001"
	certPEM, err := clustertls.IssueLeafForKey(caCertPEM, caKeyPEM, &externalKey.PublicKey, nodeURI)
	if err != nil {
		t.Fatalf("IssueLeafForKey: %v", err)
	}

	leafBlock, _ := pem.Decode(certPEM)
	if leafBlock == nil {
		t.Fatal("no PEM block in certPEM")
	}
	cert, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
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

// TestIssueLeafForKey_BadCACert verifies that IssueLeafForKey returns a
// *KeyError for the CA certificate when the supplied caCertPEM is not valid
// PEM.
func TestIssueLeafForKey_BadCACert(t *testing.T) {
	t.Parallel()

	externalKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	_, err = clustertls.IssueLeafForKey([]byte("not-a-pem"), nil, &externalKey.PublicKey)
	if err == nil {
		t.Fatal("expected error for invalid CA cert PEM, got nil")
	}
	var keyErr *clustertls.KeyError
	if !errors.As(err, &keyErr) {
		t.Errorf("expected *clustertls.KeyError, got %T: %v", err, err)
	}
	if keyErr.Kind != "CA certificate" {
		t.Errorf("expected Kind = %q, got %q", "CA certificate", keyErr.Kind)
	}
}

// TestIssueLeafForKey_BadCAKey verifies that IssueLeafForKey returns a
// *KeyError for the CA private key when the supplied caKeyPEM is not a valid
// EC PRIVATE KEY PEM block.
func TestIssueLeafForKey_BadCAKey(t *testing.T) {
	t.Parallel()

	caCertPEM, _, _, _, err := clustertls.GenerateCA(time.Hour)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	externalKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	_, err = clustertls.IssueLeafForKey(caCertPEM, []byte("not-a-key"), &externalKey.PublicKey)
	if err == nil {
		t.Fatal("expected error for invalid CA key PEM, got nil")
	}
	var keyErr *clustertls.KeyError
	if !errors.As(err, &keyErr) {
		t.Errorf("expected *clustertls.KeyError, got %T: %v", err, err)
	}
	if keyErr.Kind != "CA private key" {
		t.Errorf("expected Kind = %q, got %q", "CA private key", keyErr.Kind)
	}
}

// TestGenerateNodeCert_WorksAfterTemplateRefactor confirms that GenerateNodeCert
// still produces a verifiable leaf certificate after the nodeLeafTemplate
// refactor that extracted the template construction into a shared helper.
func TestGenerateNodeCert_WorksAfterTemplateRefactor(t *testing.T) {
	t.Parallel()

	_, _, caCert, caKey, err := clustertls.GenerateCA(time.Hour)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	certPEM, keyPEM, err := clustertls.GenerateNodeCert(caCert, caKey, time.Hour)
	if err != nil {
		t.Fatalf("GenerateNodeCert: %v", err)
	}
	if len(certPEM) == 0 {
		t.Error("expected non-empty certPEM")
	}
	if len(keyPEM) == 0 {
		t.Error("expected non-empty keyPEM")
	}

	leafBlock, _ := pem.Decode(certPEM)
	if leafBlock == nil {
		t.Fatal("no PEM block in certPEM")
	}
	leafCert, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := leafCert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		t.Errorf("leaf cert verification failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// PersistCA / LoadCAFromDir tests
// ---------------------------------------------------------------------------

// TestPersistCA_LoadCAFromDir_RoundTrip verifies that PersistCA writes ca.pem
// and ca-key.pem with 0o600 permissions and that LoadCAFromDir round-trips the
// bytes exactly.
func TestPersistCA_LoadCAFromDir_RoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	caCertPEM, caKeyPEM, _, _, err := clustertls.GenerateCA(time.Hour)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	if err := clustertls.PersistCA(caCertPEM, caKeyPEM, dir); err != nil {
		t.Fatalf("PersistCA: %v", err)
	}

	// Verify file permissions.
	for _, name := range []string{"ca.pem", "ca-key.pem"} {
		info, statErr := os.Stat(filepath.Join(dir, name))
		if statErr != nil {
			t.Errorf("stat %s: %v", name, statErr)
			continue
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s permissions = %o, want 0o600", name, info.Mode().Perm())
		}
	}

	loadedCert, loadedKey, err := clustertls.LoadCAFromDir(dir)
	if err != nil {
		t.Fatalf("LoadCAFromDir: %v", err)
	}
	if len(loadedCert) == 0 {
		t.Fatal("expected non-empty caCertPEM from LoadCAFromDir")
	}
	if len(loadedKey) == 0 {
		t.Fatal("expected non-empty caKeyPEM from LoadCAFromDir")
	}

	if string(loadedCert) != string(caCertPEM) {
		t.Error("caCertPEM mismatch after round-trip")
	}
	if string(loadedKey) != string(caKeyPEM) {
		t.Error("caKeyPEM mismatch after round-trip")
	}
}

// TestLoadCAFromDir_AbsentFilesReturnsNil verifies that LoadCAFromDir returns
// (nil, nil, nil) when neither ca.pem nor ca-key.pem is present.
func TestLoadCAFromDir_AbsentFilesReturnsNil(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	caCertPEM, caKeyPEM, err := clustertls.LoadCAFromDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if caCertPEM != nil {
		t.Error("expected nil caCertPEM when files absent")
	}
	if caKeyPEM != nil {
		t.Error("expected nil caKeyPEM when files absent")
	}
}

// TestLoadCAFromDir_OnlyOneMissing verifies that LoadCAFromDir returns
// (nil, nil, nil) when only one of ca.pem / ca-key.pem is present, so a
// partial CA write is not treated as a valid cached state.
func TestLoadCAFromDir_OnlyOneMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Write ca.pem but not ca-key.pem.
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), []byte("cert"), 0o600); err != nil {
		t.Fatal(err)
	}

	caCertPEM, caKeyPEM, err := clustertls.LoadCAFromDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if caCertPEM != nil {
		t.Error("expected nil caCertPEM when ca-key.pem absent")
	}
	if caKeyPEM != nil {
		t.Error("expected nil caKeyPEM when ca-key.pem absent")
	}
}

// TestPersistCA_WriteError verifies that PersistCA returns a *WriteError when
// the data directory path is an existing regular file (so MkdirAll fails).
func TestPersistCA_WriteError(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	badDir := filepath.Join(tmp, "blockfile")
	if err := os.WriteFile(badDir, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := clustertls.PersistCA([]byte("cert"), []byte("key"), badDir)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var writeErr *clustertls.WriteError
	if !errors.As(err, &writeErr) {
		t.Errorf("expected *clustertls.WriteError, got %T: %v", err, err)
	}
}
