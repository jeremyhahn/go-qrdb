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

package transport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// tlsTestMaterial holds file paths to generated PEM material.
type tlsTestMaterial struct {
	caFile   string
	certFile string
	keyFile  string
}

// newTLSTestMaterial generates a self-signed certificate and writes CA,
// cert, and key PEM files into a temporary directory.
func newTLSTestMaterial(t *testing.T) tlsTestMaterial {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "qrdb-tls-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	dir := t.TempDir()
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	mat := tlsTestMaterial{
		caFile:   filepath.Join(dir, "ca.pem"),
		certFile: filepath.Join(dir, "cert.pem"),
		keyFile:  filepath.Join(dir, "key.pem"),
	}
	for path, data := range map[string][]byte{
		mat.caFile:   certPEM,
		mat.certFile: certPEM,
		mat.keyFile:  keyPEM,
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return mat
}

// writeTLSTestFile writes data to a file in a temporary directory and
// returns its path.
func writeTLSTestFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// ---------------------------------------------------------------------------
// BuildTLSConfig tests
// ---------------------------------------------------------------------------

func TestBuildTLSConfig_CustomReturnedAsCopy(t *testing.T) {
	custom := &tls.Config{ServerName: "example"}
	got, err := BuildTLSConfig(TLSBuildOptions{Custom: custom, Enabled: false})
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}
	if got == custom {
		t.Fatal("expected a clone, got the caller's Custom pointer")
	}
	if got.ServerName != "example" {
		t.Errorf("ServerName = %q, want %q", got.ServerName, "example")
	}
}

// TestBuildTLSConfig_CustomMutationDoesNotReachCaller pins #949: a
// transport that mutates the config it is handed (net/http appends "h2"
// to NextProtos) must not change the caller's config, which may be shared
// with a client that cannot speak what the mutation offers.
func TestBuildTLSConfig_CustomMutationDoesNotReachCaller(t *testing.T) {
	custom := &tls.Config{ServerName: "example"}
	got, err := BuildTLSConfig(TLSBuildOptions{Custom: custom})
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}
	got.NextProtos = append(got.NextProtos, "h2")
	got.ServerName = "changed"
	if len(custom.NextProtos) != 0 {
		t.Errorf("caller NextProtos = %v, want empty", custom.NextProtos)
	}
	if custom.ServerName != "example" {
		t.Errorf("caller ServerName = %q, want %q", custom.ServerName, "example")
	}
}

// TestBuildTLSConfig_CustomNextProtosNotShared pins #949 beyond the
// pointer: tls.Config.Clone copies the NextProtos slice header, so a clone
// whose NextProtos is appended to (by the ALPN merge here, or by net/http
// adding "h2") writes into the caller's backing array whenever it has
// spare capacity.
func TestBuildTLSConfig_CustomNextProtosNotShared(t *testing.T) {
	tests := []struct {
		name       string
		nextProtos []string
	}{
		{name: "no merge", nextProtos: nil},
		{name: "ALPN merge", nextProtos: []string{"qrdb"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backing := make([]string, 1, 4)
			backing[0] = "http/1.1"
			custom := &tls.Config{NextProtos: backing[:1]}
			got, err := BuildTLSConfig(TLSBuildOptions{Custom: custom, NextProtos: tt.nextProtos})
			if err != nil {
				t.Fatalf("BuildTLSConfig: %v", err)
			}
			got.NextProtos = append(got.NextProtos, "h2")
			for _, spare := range backing[len(backing):cap(backing)] {
				if spare != "" {
					t.Errorf("caller's NextProtos backing array was written: %q", backing[:cap(backing)])
					break
				}
			}
			if len(custom.NextProtos) != 1 || custom.NextProtos[0] != "http/1.1" {
				t.Errorf("caller NextProtos = %q, want [http/1.1]", custom.NextProtos)
			}
		})
	}
}

func TestBuildTLSConfig_CustomWithNextProtosClonesAndAppends(t *testing.T) {
	custom := &tls.Config{ServerName: "example", NextProtos: []string{"h3"}}
	got, err := BuildTLSConfig(TLSBuildOptions{
		Custom:     custom,
		NextProtos: []string{"qrdb-quic/3"},
	})
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}
	if got == custom {
		t.Fatal("expected a clone, got the original Custom pointer")
	}
	if got.ServerName != "example" {
		t.Errorf("clone lost ServerName, got %q", got.ServerName)
	}
	if len(got.NextProtos) != 2 || got.NextProtos[0] != "h3" || got.NextProtos[1] != "qrdb-quic/3" {
		t.Errorf("NextProtos = %v, want [h3 qrdb-quic]", got.NextProtos)
	}
	if len(custom.NextProtos) != 1 {
		t.Errorf("original Custom NextProtos mutated: %v", custom.NextProtos)
	}
}

func TestBuildTLSConfig_CustomWithNextProtosAlreadyPresent(t *testing.T) {
	custom := &tls.Config{NextProtos: []string{"qrdb-quic/3", "h3"}}
	got, err := BuildTLSConfig(TLSBuildOptions{
		Custom:     custom,
		NextProtos: []string{"qrdb-quic/3"},
	})
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}
	if len(got.NextProtos) != 2 {
		t.Errorf("expected no duplicate ALPN entries, got %v", got.NextProtos)
	}
}

func TestBuildTLSConfig_DisabledReturnsNil(t *testing.T) {
	got, err := BuildTLSConfig(TLSBuildOptions{Enabled: false})
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil config when disabled, got %+v", got)
	}
}

func TestBuildTLSConfig_EnabledMinimal(t *testing.T) {
	got, err := BuildTLSConfig(TLSBuildOptions{Enabled: true})
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}
	if got.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %d, want TLS 1.3", got.MinVersion)
	}
	if got.RootCAs != nil {
		t.Error("expected nil RootCAs with no CA file")
	}
	if len(got.Certificates) != 0 {
		t.Errorf("expected no client certificates, got %d", len(got.Certificates))
	}
}

func TestBuildTLSConfig_EnabledWithNextProtos(t *testing.T) {
	got, err := BuildTLSConfig(TLSBuildOptions{
		Enabled:    true,
		NextProtos: []string{"qrdb-quic/3"},
	})
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}
	if len(got.NextProtos) != 1 || got.NextProtos[0] != "qrdb-quic/3" {
		t.Errorf("NextProtos = %v, want [qrdb-quic]", got.NextProtos)
	}
}

func TestBuildTLSConfig_WithCAFile(t *testing.T) {
	mat := newTLSTestMaterial(t)
	got, err := BuildTLSConfig(TLSBuildOptions{Enabled: true, CAFile: mat.caFile})
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}
	if got.RootCAs == nil {
		t.Error("expected RootCAs to be populated from CA file")
	}
}

// TestBuildTLSConfig_WithClientCert proves the loaded client certificate is
// carried via GetClientCertificate, not the static Certificates field:
// crypto/tls's default selector for Certificates only offers an entry whose
// issuer appears in the server's CertificateRequest.AcceptableCAs, and
// silently sends none otherwise -- exactly what a server composed over
// pkg/clienttrust.Pool does for every tenant's Key B CA (go-qrdb#591's
// review, B2). GetClientCertificate bypasses that selection entirely,
// unconditionally presenting the one certificate the caller configured.
func TestBuildTLSConfig_WithClientCert(t *testing.T) {
	mat := newTLSTestMaterial(t)
	got, err := BuildTLSConfig(TLSBuildOptions{
		Enabled:  true,
		CAFile:   mat.caFile,
		CertFile: mat.certFile,
		KeyFile:  mat.keyFile,
	})
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}
	if len(got.Certificates) != 0 {
		t.Fatalf("expected the static Certificates field to stay empty, got %d entries", len(got.Certificates))
	}
	if got.GetClientCertificate == nil {
		t.Fatal("expected GetClientCertificate to be set")
	}
	cert, err := got.GetClientCertificate(&tls.CertificateRequestInfo{})
	if err != nil {
		t.Fatalf("GetClientCertificate: %v", err)
	}
	if cert == nil || len(cert.Certificate) == 0 {
		t.Fatal("expected GetClientCertificate to return the loaded certificate")
	}
}

func TestBuildTLSConfig_CertFileWithoutKeyFileSkipped(t *testing.T) {
	mat := newTLSTestMaterial(t)
	got, err := BuildTLSConfig(TLSBuildOptions{Enabled: true, CertFile: mat.certFile})
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}
	if len(got.Certificates) != 0 {
		t.Errorf("expected cert without key to be skipped, got %d certificates", len(got.Certificates))
	}
}

func TestBuildTLSConfig_MissingCAFile(t *testing.T) {
	_, err := BuildTLSConfig(TLSBuildOptions{
		Enabled: true,
		CAFile:  "/nonexistent/path/ca.pem",
	})
	if err == nil {
		t.Fatal("expected error for missing CA file")
	}
	var tlsErr *TLSError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("expected *TLSError, got %T: %v", err, err)
	}
	var readErr *CAFileReadError
	if !errors.As(err, &readErr) {
		t.Fatalf("expected *CAFileReadError cause, got %v", err)
	}
	if readErr.Path != "/nonexistent/path/ca.pem" {
		t.Errorf("CAFileReadError.Path = %q", readErr.Path)
	}
}

func TestBuildTLSConfig_BadCAPEM(t *testing.T) {
	path := writeTLSTestFile(t, "bad-ca.pem", []byte("this is not valid PEM data"))

	_, err := BuildTLSConfig(TLSBuildOptions{Enabled: true, CAFile: path})
	if err == nil {
		t.Fatal("expected error for invalid CA PEM")
	}
	var tlsErr *TLSError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("expected *TLSError, got %T: %v", err, err)
	}
	var parseErr *CAParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected *CAParseError cause, got %v", err)
	}
	if parseErr.Path != path {
		t.Errorf("CAParseError.Path = %q, want %q", parseErr.Path, path)
	}
}

func TestBuildTLSConfig_CertKeyMismatch(t *testing.T) {
	mat := newTLSTestMaterial(t)

	// Generate an unrelated key so the pair intentionally mismatches.
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate other key: %v", err)
	}
	otherDER, err := x509.MarshalECPrivateKey(otherKey)
	if err != nil {
		t.Fatalf("marshal other key: %v", err)
	}
	mismatchedKey := writeTLSTestFile(t, "mismatched-key.pem",
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: otherDER}))

	_, err = BuildTLSConfig(TLSBuildOptions{
		Enabled:  true,
		CertFile: mat.certFile,
		KeyFile:  mismatchedKey,
	})
	if err == nil {
		t.Fatal("expected error for mismatched cert/key pair")
	}
	var tlsErr *TLSError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("expected *TLSError, got %T: %v", err, err)
	}
	var loadErr *CertificateLoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("expected *CertificateLoadError cause, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// LoadCertPool tests
// ---------------------------------------------------------------------------

func TestLoadCertPool_Valid(t *testing.T) {
	mat := newTLSTestMaterial(t)
	pool, err := LoadCertPool(mat.caFile)
	if err != nil {
		t.Fatalf("LoadCertPool: %v", err)
	}
	if pool == nil {
		t.Fatal("expected non-nil cert pool")
	}
}

func TestLoadCertPool_MissingFile(t *testing.T) {
	_, err := LoadCertPool("/nonexistent/path/ca.pem")
	if err == nil {
		t.Fatal("expected error for missing CA file")
	}
	var readErr *CAFileReadError
	if !errors.As(err, &readErr) {
		t.Fatalf("expected *CAFileReadError, got %T: %v", err, err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected error chain to reach fs.ErrNotExist, got %v", err)
	}
}

func TestLoadCertPool_BadPEM(t *testing.T) {
	path := writeTLSTestFile(t, "bad-ca.pem", []byte("not a certificate"))
	_, err := LoadCertPool(path)
	if err == nil {
		t.Fatal("expected error for invalid CA PEM")
	}
	var parseErr *CAParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected *CAParseError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// TLS error type tests
// ---------------------------------------------------------------------------

func TestCAFileReadError_Error(t *testing.T) {
	inner := errors.New("permission denied")
	err := &CAFileReadError{Path: "/etc/ssl/ca.pem", Err: inner}
	expected := "transport: failed to read CA file /etc/ssl/ca.pem: permission denied"
	if got := err.Error(); got != expected {
		t.Errorf("CAFileReadError.Error() = %q, want %q", got, expected)
	}
}

func TestCAParseError_Error(t *testing.T) {
	err := &CAParseError{Path: "/etc/ssl/ca.pem"}
	expected := "transport: failed to parse CA certificate from /etc/ssl/ca.pem"
	if got := err.Error(); got != expected {
		t.Errorf("CAParseError.Error() = %q, want %q", got, expected)
	}
}

func TestCertificateLoadError_Error(t *testing.T) {
	inner := errors.New("key pair mismatch")
	err := &CertificateLoadError{Err: inner}
	expected := "transport: failed to load client certificate: key pair mismatch"
	if got := err.Error(); got != expected {
		t.Errorf("CertificateLoadError.Error() = %q, want %q", got, expected)
	}
}

func TestCertificateLoadError_Unwrap(t *testing.T) {
	inner := errors.New("key pair mismatch")
	err := &CertificateLoadError{Err: inner}
	if !errors.Is(err, inner) {
		t.Error("errors.Is should find the wrapped inner error")
	}
}
