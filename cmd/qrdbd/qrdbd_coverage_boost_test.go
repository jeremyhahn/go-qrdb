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
	"errors"
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

func TestGenerateCA_Success(t *testing.T) {
	caCertPEM, caKeyPEM, caCert, caKey, err := generateCA(time.Hour)
	if err != nil {
		t.Fatalf("generateCA: %v", err)
	}
	if len(caCertPEM) == 0 {
		t.Error("expected non-empty CA cert PEM")
	}
	if len(caKeyPEM) == 0 {
		t.Error("expected non-empty CA key PEM")
	}
	if caCert == nil {
		t.Error("expected non-nil CA cert")
	}
	if caKey == nil {
		t.Error("expected non-nil CA key")
	}
	if !caCert.IsCA {
		t.Error("expected CA certificate to have IsCA=true")
	}
}

// ---------------------------------------------------------------------------
// generateNodeCert – success
// ---------------------------------------------------------------------------

func TestGenerateNodeCert_Success(t *testing.T) {
	_, _, caCert, caKey, err := generateCA(time.Hour)
	if err != nil {
		t.Fatalf("generateCA: %v", err)
	}

	certPEM, keyPEM, err := generateNodeCert(caCert, caKey, time.Hour)
	if err != nil {
		t.Fatalf("generateNodeCert: %v", err)
	}
	if len(certPEM) == 0 {
		t.Error("expected non-empty cert PEM")
	}
	if len(keyPEM) == 0 {
		t.Error("expected non-empty key PEM")
	}
}

// ---------------------------------------------------------------------------
// generateClientCert – success
// ---------------------------------------------------------------------------

func TestGenerateClientCert_Success(t *testing.T) {
	_, _, caCert, caKey, err := generateCA(time.Hour)
	if err != nil {
		t.Fatalf("generateCA: %v", err)
	}

	certPEM, keyPEM, err := generateClientCert(caCert, caKey, "test-client", "system", time.Hour)
	if err != nil {
		t.Fatalf("generateClientCert: %v", err)
	}
	if len(certPEM) == 0 {
		t.Error("expected non-empty cert PEM")
	}
	if len(keyPEM) == 0 {
		t.Error("expected non-empty key PEM")
	}
}

// ---------------------------------------------------------------------------
// generateCATLS – success
// ---------------------------------------------------------------------------

func TestGenerateCATLS_Success(t *testing.T) {
	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS: %v", err)
	}
	if bundle == nil {
		t.Fatal("expected non-nil bundle")
	}
	if len(bundle.CACertPEM) == 0 {
		t.Error("expected non-empty CACertPEM")
	}
	if bundle.TLSConfig == nil {
		t.Error("expected non-nil TLSConfig")
	}
	if bundle.CACertPool == nil {
		t.Error("expected non-nil CACertPool")
	}
}

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
// defaultMVCCFastLogger – success (no panic)
// ---------------------------------------------------------------------------

func TestDefaultMVCCFastLogger_Success(t *testing.T) {
	log := defaultMVCCFastLogger()
	if log == nil {
		t.Error("expected non-nil logger from defaultMVCCFastLogger()")
	}
}

// ---------------------------------------------------------------------------
// Volume pool errors – additional error constructors
// ---------------------------------------------------------------------------

func TestVolumePoolStorageError_ErrorAndUnwrap(t *testing.T) {
	root := errors.New("pebble open failed")
	e := &VolumePoolStorageError{Err: root}
	if e.Error() == "" {
		t.Error("expected non-empty error string")
	}
	if !errors.Is(e, root) {
		t.Error("expected Unwrap to expose root error")
	}
}

func TestVolumePoolRegisterError_ErrorAndUnwrap(t *testing.T) {
	root := errors.New("duplicate name")
	e := &VolumePoolRegisterError{Name: "backups", Err: root}
	msg := e.Error()
	if msg == "" {
		t.Error("expected non-empty error string")
	}
	if !errors.Is(e, root) {
		t.Error("expected Unwrap to expose root error")
	}
}

func TestVolumePoolStrategyError_WithErr(t *testing.T) {
	root := errors.New("unsupported")
	e := &VolumePoolStrategyError{Strategy: "raid", Err: root}
	msg := e.Error()
	if msg == "" {
		t.Error("expected non-empty error string")
	}
	if !errors.Is(e, root) {
		t.Error("expected Unwrap to expose root error")
	}
}

func TestVolumePoolStrategyError_NilErr(t *testing.T) {
	e := &VolumePoolStrategyError{Strategy: "unknown"}
	msg := e.Error()
	if msg == "" {
		t.Error("expected non-empty error string for nil Err")
	}
}

func TestVolumePoolPathError_ErrorAndUnwrap(t *testing.T) {
	root := errors.New("permission denied")
	e := &VolumePoolPathError{Pool: "pool1", Path: "/data", Err: root}
	msg := e.Error()
	if msg == "" {
		t.Error("expected non-empty error string")
	}
	if !errors.Is(e, root) {
		t.Error("expected Unwrap to expose root error")
	}
}

// ---------------------------------------------------------------------------
// buildLocalCandidates – success path
// ---------------------------------------------------------------------------

func TestBuildLocalCandidates_Success(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	candidates, err := buildLocalCandidates("testpool", []string{dir1, dir2})
	if err != nil {
		t.Fatalf("buildLocalCandidates: %v", err)
	}
	if len(candidates) != 2 {
		t.Errorf("expected 2 candidates, got %d", len(candidates))
	}
}

func TestBuildLocalCandidates_Empty(t *testing.T) {
	candidates, err := buildLocalCandidates("testpool", nil)
	if err != nil {
		t.Fatalf("buildLocalCandidates empty: %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("expected 0 candidates for empty paths, got %d", len(candidates))
	}
}

// ---------------------------------------------------------------------------
// defaultMVCCFastStorageFactory – call the returned factory
// ---------------------------------------------------------------------------

func TestDefaultMVCCFastStorageFactory_CallsFactory(t *testing.T) {
	factory := defaultMVCCFastStorageFactory()
	if factory == nil {
		t.Fatal("expected non-nil factory")
	}
	stg := factory(1, 1)
	if stg == nil {
		t.Fatal("expected non-nil storage from factory")
	}
	_ = stg.Close()
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
