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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

// writeTestCA writes a self-signed CA certificate to the given path
// and returns the parsed certificate so that tests can assert pool
// membership via VerifyOptions.Roots.
func writeTestCA(t *testing.T, path, cn string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert
}

// TestLoadCAFilesUnion_EmptyInputs returns nil pool when no paths.
func TestLoadCAFilesUnion_EmptyInputs(t *testing.T) {
	pool, err := loadCAFilesUnion(nil)
	if err != nil {
		t.Fatalf("nil paths: unexpected error %v", err)
	}
	if pool != nil {
		t.Fatalf("nil paths: expected nil pool")
	}

	pool, err = loadCAFilesUnion([]string{})
	if err != nil {
		t.Fatalf("empty paths: unexpected error %v", err)
	}
	if pool != nil {
		t.Fatalf("empty paths: expected nil pool")
	}

	pool, err = loadCAFilesUnion([]string{"", ""})
	if err != nil {
		t.Fatalf("blank paths: unexpected error %v", err)
	}
	if pool != nil {
		t.Fatalf("blank paths: expected nil pool")
	}
}

// TestLoadCAFilesUnion_TwoValidFiles loads the union of two CA files
// and verifies both certificates are present in the pool.
func TestLoadCAFilesUnion_TwoValidFiles(t *testing.T) {
	dir := t.TempDir()
	caA := filepath.Join(dir, "a.pem")
	caB := filepath.Join(dir, "b.pem")
	certA := writeTestCA(t, caA, "ca-a")
	certB := writeTestCA(t, caB, "ca-b")

	pool, err := loadCAFilesUnion([]string{caA, caB})
	if err != nil {
		t.Fatalf("union: %v", err)
	}
	if pool == nil {
		t.Fatal("expected non-nil pool")
	}

	for _, c := range []*x509.Certificate{certA, certB} {
		if _, err := c.Verify(x509.VerifyOptions{Roots: pool}); err != nil {
			t.Fatalf("certificate %s not in pool: %v", c.Subject.CommonName, err)
		}
	}
}

// TestLoadCAFilesUnion_DedupesByCanonicalFilename ensures the same
// filename referenced via two distinct path inputs (e.g., relative vs
// absolute) loads only once. The success criterion is that the
// returned pool only contains the unique certificates.
func TestLoadCAFilesUnion_DedupesByCanonicalFilename(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "shared.pem")
	cert := writeTestCA(t, caPath, "shared-ca")

	// Use both an absolute path and a path with extra "./" segments to
	// exercise the canonicalization step.
	relForm := filepath.Join(dir, ".", "shared.pem")
	pool, err := loadCAFilesUnion([]string{caPath, relForm, caPath})
	if err != nil {
		t.Fatalf("union: %v", err)
	}
	if pool == nil {
		t.Fatal("expected non-nil pool")
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool}); err != nil {
		t.Fatalf("dedup pool missing cert: %v", err)
	}
}

// TestLoadCAFilesUnion_MissingFile returns a TLSLoadError when the
// referenced file does not exist on disk.
func TestLoadCAFilesUnion_MissingFile(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.pem")

	_, err := loadCAFilesUnion([]string{missing})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	var loadErr *TLSLoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("expected *TLSLoadError, got %T (%v)", err, err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected wrapped os.ErrNotExist, got %v", err)
	}
}

// TestLoadCAFilesUnion_MalformedPEM rejects a file whose bytes do not
// contain any valid CERTIFICATE PEM block.
func TestLoadCAFilesUnion_MalformedPEM(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(bad, []byte("not a pem file"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := loadCAFilesUnion([]string{bad})
	if err == nil {
		t.Fatal("expected error for malformed PEM")
	}
	var loadErr *TLSLoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("expected *TLSLoadError, got %T (%v)", err, err)
	}
	if !errors.Is(err, errNoCertsInPEM) {
		t.Fatalf("expected wrapped errNoCertsInPEM, got %v", err)
	}
}

// TestLoadCAFilesUnion_SkipsBlankPaths confirms that blank-string
// entries in the slice are silently skipped without affecting valid
// neighbors.
func TestLoadCAFilesUnion_SkipsBlankPaths(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "real.pem")
	cert := writeTestCA(t, caPath, "real-ca")

	pool, err := loadCAFilesUnion([]string{"", caPath, ""})
	if err != nil {
		t.Fatalf("union: %v", err)
	}
	if pool == nil {
		t.Fatal("expected non-nil pool")
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool}); err != nil {
		t.Fatalf("cert not in pool: %v", err)
	}
}

// TestLoadTLSFromFilesWithExtra_LayersAdditionalCAs exercises the
// public wrapper used by the daemon to merge primary CA, additional
// CAs, and mTLS client CAs into a single deduplicated pool.
func TestLoadTLSFromFilesWithExtra_LayersAdditionalCAs(t *testing.T) {
	dir := t.TempDir()

	// Generate a CA + node certificate via the existing helper so we
	// have a valid leaf signed by the primary CA.
	caCertPEM, caKeyPEM, caCert, caKey, err := clustertls.GenerateCA(time.Hour)
	if err != nil {
		t.Fatalf("generateCA: %v", err)
	}
	nodeCertPEM, nodeKeyPEM, err := clustertls.GenerateNodeCert(caCert, caKey, time.Hour)
	if err != nil {
		t.Fatalf("generateNodeCert: %v", err)
	}

	caPath := filepath.Join(dir, "ca.pem")
	caKeyPath := filepath.Join(dir, "ca-key.pem")
	certPath := filepath.Join(dir, "node-cert.pem")
	keyPath := filepath.Join(dir, "node-key.pem")
	if err := os.WriteFile(caPath, caCertPEM, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	if err := os.WriteFile(caKeyPath, caKeyPEM, 0o600); err != nil {
		t.Fatalf("write ca key: %v", err)
	}
	if err := os.WriteFile(certPath, nodeCertPEM, 0o600); err != nil {
		t.Fatalf("write node cert: %v", err)
	}
	if err := os.WriteFile(keyPath, nodeKeyPEM, 0o600); err != nil {
		t.Fatalf("write node key: %v", err)
	}

	extraPath := filepath.Join(dir, "extra.pem")
	clientPath := filepath.Join(dir, "client.pem")
	extraCert := writeTestCA(t, extraPath, "extra-ca")
	clientCert := writeTestCA(t, clientPath, "client-ca")

	bundle, err := loadTLSFromFilesWithExtra(
		caPath, certPath, keyPath,
		[]string{extraPath},
		[]string{clientPath, extraPath /* duplicate to exercise dedup */},
	)
	if err != nil {
		t.Fatalf("loadTLSFromFilesWithExtra: %v", err)
	}
	if bundle == nil {
		t.Fatal("expected non-nil bundle")
	}

	// Server-trust pool (RootCAs): primary CA + additionalCAs (extra-ca). The
	// client-only CA must NOT leak into it — that separation is the point of
	// splitting the pools.
	for _, c := range []*x509.Certificate{caCert, extraCert} {
		if _, err := c.Verify(x509.VerifyOptions{Roots: bundle.CACertPool}); err != nil {
			t.Errorf("cert %s must be in server CACertPool: %v", c.Subject.CommonName, err)
		}
	}
	if _, err := clientCert.Verify(x509.VerifyOptions{Roots: bundle.CACertPool}); err == nil {
		t.Error("client-only CA must NOT be trusted by the server CACertPool")
	}

	// Client-auth pool (ClientCAs): primary CA + clientCAFiles (client-ca and
	// the deduplicated extra-ca).
	for _, c := range []*x509.Certificate{caCert, clientCert, extraCert} {
		if _, err := c.Verify(x509.VerifyOptions{Roots: bundle.ClientCACertPool}); err != nil {
			t.Errorf("cert %s must be in ClientCACertPool: %v", c.Subject.CommonName, err)
		}
	}
}
