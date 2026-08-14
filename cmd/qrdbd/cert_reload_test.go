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
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// test helpers
// ---------------------------------------------------------------------------

// testCertPair generates an ECDSA P-256 self-signed certificate and
// returns the PEM-encoded certificate and private key bytes.
func testCertPair(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"cert-reload-test"},
			CommonName:   "localhost",
		},
		DNSNames:              []string{"localhost"},
		NotBefore:             time.Now().Add(-1 * time.Minute),
		NotAfter:              time.Now().Add(1 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM
}

// writeCertFiles writes the PEM-encoded cert and key to files inside
// dir and returns the file paths.
func writeCertFiles(t *testing.T, dir string, certPEM, keyPEM []byte) (certPath, keyPath string) {
	t.Helper()

	certPath = filepath.Join(dir, "server.crt")
	keyPath = filepath.Join(dir, "server.key")

	require.NoError(t, os.WriteFile(certPath, certPEM, 0o600))
	require.NoError(t, os.WriteFile(keyPath, keyPEM, 0o600))

	return certPath, keyPath
}

// testLogger returns a slog.Logger that discards output. Tests that
// need to verify log messages can replace this with a custom handler.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// ---------------------------------------------------------------------------
// TestCertReloader_InitialLoad
// ---------------------------------------------------------------------------

func TestCertReloader_InitialLoad(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := testCertPair(t)
	certPath, keyPath := writeCertFiles(t, dir, certPEM, keyPEM)

	r, err := NewCertReloader(certPath, keyPath, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop() })

	loaded := r.cert.Load()
	require.NotNil(t, loaded, "initial certificate must be loaded")

	// Parse the original PEM to compare leaf fingerprints.
	expected, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)

	assert.Equal(t, expected.Certificate, loaded.Certificate,
		"loaded certificate DER bytes must match the original")
}

// TestCertReloader_SeparateDirectories verifies the reloader watches both
// parent directories when the cert and key live under different paths (a common
// Kubernetes secret layout), exercising the keyDir != certDir watch branch.
func TestCertReloader_SeparateDirectories(t *testing.T) {
	certPEM, keyPEM := testCertPair(t)
	certDir := t.TempDir()
	keyDir := t.TempDir()
	certPath := filepath.Join(certDir, "tls.crt")
	keyPath := filepath.Join(keyDir, "tls.key")
	require.NoError(t, os.WriteFile(certPath, certPEM, 0o600))
	require.NoError(t, os.WriteFile(keyPath, keyPEM, 0o600))

	r, err := NewCertReloader(certPath, keyPath, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop() })

	require.NotNil(t, r.cert.Load(), "initial certificate must be loaded")
}

func TestCertReloader_InitialLoad_InvalidCert(t *testing.T) {
	dir := t.TempDir()

	certPath := filepath.Join(dir, "bad.crt")
	keyPath := filepath.Join(dir, "bad.key")
	require.NoError(t, os.WriteFile(certPath, []byte("not-a-cert"), 0o600))
	require.NoError(t, os.WriteFile(keyPath, []byte("not-a-key"), 0o600))

	_, err := NewCertReloader(certPath, keyPath, testLogger())
	require.Error(t, err)

	var reloadErr *CertReloadError
	assert.True(t, errors.As(err, &reloadErr), "error must be CertReloadError")
}

func TestCertReloader_InitialLoad_MissingFile(t *testing.T) {
	dir := t.TempDir()

	certPath := filepath.Join(dir, "nonexistent.crt")
	keyPath := filepath.Join(dir, "nonexistent.key")

	_, err := NewCertReloader(certPath, keyPath, testLogger())
	require.Error(t, err)

	var reloadErr *CertReloadError
	assert.True(t, errors.As(err, &reloadErr), "error must be CertReloadError")
}

// ---------------------------------------------------------------------------
// TestCertReloader_GetCertificate
// ---------------------------------------------------------------------------

func TestCertReloader_GetCertificate(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := testCertPair(t)
	certPath, keyPath := writeCertFiles(t, dir, certPEM, keyPEM)

	r, err := NewCertReloader(certPath, keyPath, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop() })

	cert, err := r.GetCertificate(nil)
	require.NoError(t, err)
	require.NotNil(t, cert)

	expected, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)

	assert.Equal(t, expected.Certificate, cert.Certificate,
		"GetCertificate must return the currently loaded cert")
}

func TestCertReloader_GetCertificate_WithClientHello(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := testCertPair(t)
	certPath, keyPath := writeCertFiles(t, dir, certPEM, keyPEM)

	r, err := NewCertReloader(certPath, keyPath, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop() })

	hello := &tls.ClientHelloInfo{ServerName: "localhost"}
	cert, err := r.GetCertificate(hello)
	require.NoError(t, err)
	require.NotNil(t, cert, "GetCertificate must work with non-nil ClientHelloInfo")
}

// ---------------------------------------------------------------------------
// TestCertReloader_Reload
// ---------------------------------------------------------------------------

func TestCertReloader_Reload(t *testing.T) {
	dir := t.TempDir()
	origCert, origKey := testCertPair(t)
	certPath, keyPath := writeCertFiles(t, dir, origCert, origKey)

	r, err := NewCertReloader(certPath, keyPath, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop() })

	// Generate a new certificate and overwrite the files.
	newCert, newKey := testCertPair(t)
	require.NoError(t, os.WriteFile(certPath, newCert, 0o600))
	require.NoError(t, os.WriteFile(keyPath, newKey, 0o600))

	// Manually reload.
	require.NoError(t, r.Reload())

	loaded, err := r.GetCertificate(nil)
	require.NoError(t, err)

	expected, err := tls.X509KeyPair(newCert, newKey)
	require.NoError(t, err)

	assert.Equal(t, expected.Certificate, loaded.Certificate,
		"after Reload the new certificate must be served")
}

func TestCertReloader_Reload_PreservesOldOnError(t *testing.T) {
	dir := t.TempDir()
	origCert, origKey := testCertPair(t)
	certPath, keyPath := writeCertFiles(t, dir, origCert, origKey)

	r, err := NewCertReloader(certPath, keyPath, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop() })

	// Capture the original loaded cert for comparison.
	origLoaded := r.cert.Load()

	// Overwrite with invalid data.
	require.NoError(t, os.WriteFile(certPath, []byte("garbage"), 0o600))

	err = r.Reload()
	require.Error(t, err)

	var reloadErr *CertReloadError
	assert.True(t, errors.As(err, &reloadErr))

	// The original certificate must still be served.
	current, err := r.GetCertificate(nil)
	require.NoError(t, err)
	assert.Equal(t, origLoaded.Certificate, current.Certificate,
		"failed reload must preserve the previous certificate")
}

// ---------------------------------------------------------------------------
// TestCertReloader_FileWatch
// ---------------------------------------------------------------------------

func TestCertReloader_FileWatch(t *testing.T) {
	dir := t.TempDir()
	origCert, origKey := testCertPair(t)
	certPath, keyPath := writeCertFiles(t, dir, origCert, origKey)

	r, err := NewCertReloader(certPath, keyPath, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop() })

	origLoaded := r.cert.Load()

	// Generate a new certificate and overwrite the files to trigger
	// fsnotify events.
	newCert, newKey := testCertPair(t)
	require.NoError(t, os.WriteFile(keyPath, newKey, 0o600))
	require.NoError(t, os.WriteFile(certPath, newCert, 0o600))

	// Wait for the debounce timer to fire and reload to complete.
	// The debounce interval is 100ms; we poll up to 2 seconds.
	expected, err := tls.X509KeyPair(newCert, newKey)
	require.NoError(t, err)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current := r.cert.Load()
		if current != origLoaded {
			assert.Equal(t, expected.Certificate, current.Certificate,
				"auto-reloaded certificate must match the newly written files")
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("certificate was not auto-reloaded within 2 seconds")
}

func TestCertReloader_FileWatch_IgnoresUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	origCert, origKey := testCertPair(t)
	certPath, keyPath := writeCertFiles(t, dir, origCert, origKey)

	r, err := NewCertReloader(certPath, keyPath, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop() })

	origLoaded := r.cert.Load()

	// Write an unrelated file in the same directory.
	unrelated := filepath.Join(dir, "unrelated.txt")
	require.NoError(t, os.WriteFile(unrelated, []byte("hello"), 0o600))

	// Give sufficient time for any spurious reload.
	time.Sleep(300 * time.Millisecond)

	current := r.cert.Load()
	assert.Equal(t, origLoaded, current,
		"writing an unrelated file must not trigger a reload")
}

// ---------------------------------------------------------------------------
// TestCertReloader_InvalidCert (Reload with bad data)
// ---------------------------------------------------------------------------

func TestCertReloader_InvalidCert(t *testing.T) {
	dir := t.TempDir()
	origCert, origKey := testCertPair(t)
	certPath, keyPath := writeCertFiles(t, dir, origCert, origKey)

	r, err := NewCertReloader(certPath, keyPath, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop() })

	origLoaded := r.cert.Load()

	// Write invalid PEM data to the cert file.
	require.NoError(t, os.WriteFile(certPath, []byte("invalid-pem-data"), 0o600))
	require.NoError(t, os.WriteFile(keyPath, []byte("invalid-key-data"), 0o600))

	err = r.Reload()
	require.Error(t, err)

	var reloadErr *CertReloadError
	require.True(t, errors.As(err, &reloadErr))
	assert.Contains(t, reloadErr.Error(), "TLS certificate reload failed")

	// The old cert must still be in place.
	current, err := r.GetCertificate(nil)
	require.NoError(t, err)
	assert.Equal(t, origLoaded.Certificate, current.Certificate)
}

func TestCertReloader_InvalidCert_MismatchedKeyPair(t *testing.T) {
	dir := t.TempDir()
	origCert, origKey := testCertPair(t)
	certPath, keyPath := writeCertFiles(t, dir, origCert, origKey)

	r, err := NewCertReloader(certPath, keyPath, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop() })

	// Generate a second pair and mix cert from pair 1 with key from pair 2.
	_, otherKey := testCertPair(t)
	require.NoError(t, os.WriteFile(keyPath, otherKey, 0o600))

	err = r.Reload()
	require.Error(t, err)

	var reloadErr *CertReloadError
	assert.True(t, errors.As(err, &reloadErr))
}

// ---------------------------------------------------------------------------
// TestCertReloader_Stop
// ---------------------------------------------------------------------------

func TestCertReloader_Stop(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := testCertPair(t)
	certPath, keyPath := writeCertFiles(t, dir, certPEM, keyPEM)

	r, err := NewCertReloader(certPath, keyPath, testLogger())
	require.NoError(t, err)

	require.NoError(t, r.Stop())

	// GetCertificate must still work after stop (returns last loaded).
	cert, err := r.GetCertificate(nil)
	require.NoError(t, err)
	require.NotNil(t, cert)
}

func TestCertReloader_Stop_Idempotent(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := testCertPair(t)
	certPath, keyPath := writeCertFiles(t, dir, certPEM, keyPEM)

	r, err := NewCertReloader(certPath, keyPath, testLogger())
	require.NoError(t, err)

	require.NoError(t, r.Stop())

	// Second call must not panic and must return nil.
	assert.NotPanics(t, func() {
		err := r.Stop()
		assert.NoError(t, err)
	})
}

func TestCertReloader_Stop_NoReloadAfterStop(t *testing.T) {
	dir := t.TempDir()
	origCert, origKey := testCertPair(t)
	certPath, keyPath := writeCertFiles(t, dir, origCert, origKey)

	r, err := NewCertReloader(certPath, keyPath, testLogger())
	require.NoError(t, err)

	origLoaded := r.cert.Load()
	require.NoError(t, r.Stop())

	// Write new certs after stop.
	newCert, newKey := testCertPair(t)
	require.NoError(t, os.WriteFile(certPath, newCert, 0o600))
	require.NoError(t, os.WriteFile(keyPath, newKey, 0o600))

	// Wait longer than debounce interval.
	time.Sleep(300 * time.Millisecond)

	current := r.cert.Load()
	assert.Equal(t, origLoaded, current,
		"filesystem events after Stop must not trigger a reload")
}

// ---------------------------------------------------------------------------
// TestCertReloader_CertReloadError
// ---------------------------------------------------------------------------

func TestCertReloadError_Error(t *testing.T) {
	inner := errors.New("permission denied")
	e := &CertReloadError{Path: "/etc/tls/cert.pem", Err: inner}

	msg := e.Error()
	assert.Contains(t, msg, "TLS certificate reload failed")
	assert.Contains(t, msg, "/etc/tls/cert.pem")
	assert.Contains(t, msg, "permission denied")
}

func TestCertReloadError_Unwrap(t *testing.T) {
	inner := errors.New("io error")
	e := &CertReloadError{Path: "/tmp/cert.pem", Err: inner}

	assert.Equal(t, inner, e.Unwrap())
	assert.True(t, errors.Is(e, inner))
}

// ---------------------------------------------------------------------------
// TestCertReloader_KeyInDifferentDir
// ---------------------------------------------------------------------------

func TestCertReloader_KeyInDifferentDir(t *testing.T) {
	certDir := t.TempDir()
	keyDir := t.TempDir()

	certPEM, keyPEM := testCertPair(t)

	certPath := filepath.Join(certDir, "server.crt")
	keyPath := filepath.Join(keyDir, "server.key")
	require.NoError(t, os.WriteFile(certPath, certPEM, 0o600))
	require.NoError(t, os.WriteFile(keyPath, keyPEM, 0o600))

	r, err := NewCertReloader(certPath, keyPath, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop() })

	// Verify the certificate was loaded correctly.
	loaded, err := r.GetCertificate(nil)
	require.NoError(t, err)
	require.NotNil(t, loaded)

	expected, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)

	assert.Equal(t, expected.Certificate, loaded.Certificate,
		"certificate loaded from separate dirs must match the original")

	// Verify manual reload works when key is in a different directory.
	newCert, newKey := testCertPair(t)
	require.NoError(t, os.WriteFile(certPath, newCert, 0o600))
	require.NoError(t, os.WriteFile(keyPath, newKey, 0o600))

	require.NoError(t, r.Reload())

	reloaded, err := r.GetCertificate(nil)
	require.NoError(t, err)

	expectedNew, err := tls.X509KeyPair(newCert, newKey)
	require.NoError(t, err)

	assert.Equal(t, expectedNew.Certificate, reloaded.Certificate,
		"reloaded certificate from separate dirs must match the new pair")
}

// ---------------------------------------------------------------------------
// TestCertReloader_FileWatch_KeyDirChange
// ---------------------------------------------------------------------------

func TestCertReloader_FileWatch_KeyDirChange(t *testing.T) {
	certDir := t.TempDir()
	keyDir := t.TempDir()

	origCert, origKey := testCertPair(t)

	certPath := filepath.Join(certDir, "server.crt")
	keyPath := filepath.Join(keyDir, "server.key")
	require.NoError(t, os.WriteFile(certPath, origCert, 0o600))
	require.NoError(t, os.WriteFile(keyPath, origKey, 0o600))

	r, err := NewCertReloader(certPath, keyPath, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop() })

	origLoaded := r.cert.Load()

	// Generate a new certificate pair and overwrite both files.
	newCert, newKey := testCertPair(t)
	require.NoError(t, os.WriteFile(keyPath, newKey, 0o600))
	require.NoError(t, os.WriteFile(certPath, newCert, 0o600))

	// Wait for the debounce timer to fire and reload to complete.
	expected, err := tls.X509KeyPair(newCert, newKey)
	require.NoError(t, err)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current := r.cert.Load()
		if current != origLoaded {
			assert.Equal(t, expected.Certificate, current.Certificate,
				"auto-reloaded certificate must match the newly written files when key is in separate dir")
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("certificate was not auto-reloaded within 2 seconds when key is in a separate directory")
}
