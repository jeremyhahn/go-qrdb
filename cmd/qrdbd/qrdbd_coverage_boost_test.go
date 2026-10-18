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

//go:build !integration

// qrdbd_coverage_boost_test.go adds unit-level tests for uncovered code paths
// in cmd/qrdbd that do not require starting a server or binding network ports.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// generateReplicaID – basic success
// ---------------------------------------------------------------------------

func TestGenerateReplicaID_Basic(t *testing.T) {
	id, err := generateReplicaID()
	if err != nil {
		t.Fatalf("generateReplicaID: %v", err)
	}
	if id == 0 {
		t.Error("expected non-zero replica ID")
	}
	// 48-bit mask: max value is 0xFFFFFFFFFFFF.
	if id > 0xFFFFFFFFFFFF {
		t.Errorf("replica ID exceeds 48-bit range: %d", id)
	}
}

// ---------------------------------------------------------------------------
// generateCA – success and error paths
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// generateNodeCert – success
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// generateCATLS – success
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// canonicalizePath – basic success
// ---------------------------------------------------------------------------

func TestCanonicalizePathBoost_Absolute(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "test.pem")
	if err := os.WriteFile(p, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := canonicalizePath(p)
	if err != nil {
		t.Fatalf("canonicalizePath: %v", err)
	}
	if got == "" {
		t.Error("expected non-empty canonical path")
	}
}

func TestCanonicalizePathBoost_NonExistentReturnsAbs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "nonexistent.pem")
	got, err := canonicalizePath(p)
	if err != nil {
		t.Fatalf("canonicalizePath non-existent: %v", err)
	}
	if got == "" {
		t.Error("expected non-empty path")
	}
}

// ---------------------------------------------------------------------------
// CertReloader – keyDir != certDir path
// ---------------------------------------------------------------------------

func TestCertReloader_DifferentDirs(t *testing.T) {
	certDir := t.TempDir()
	keyDir := t.TempDir()

	certPath := filepath.Join(certDir, "server.crt")
	keyPath := filepath.Join(keyDir, "server.key")

	certPEM, keyPEM := makeTestCertBoost(t)
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := NewCertReloader(certPath, keyPath, slog.Default())
	if err != nil {
		t.Fatalf("NewCertReloader with separate dirs: %v", err)
	}
	defer func() { _ = r.Stop() }()

	if r.cert.Load() == nil {
		t.Error("expected loaded certificate")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeTestCertBoost generates a self-signed ECDSA P-256 cert + key PEM pair.
func makeTestCertBoost(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	// Verify the pair is valid.
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		t.Fatalf("key pair invalid: %v", err)
	}
	return
}
