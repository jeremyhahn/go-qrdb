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

package qrdbdharness

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestCA mints a CA in a temp directory or fails the test.
func newTestCA(t *testing.T, opts ...CAOption) *CA {
	t.Helper()
	ca, err := NewCA(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	return ca
}

// parseLeaf decodes a PEM certificate or fails the test.
func parseLeaf(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("parseLeaf: no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parseLeaf: %v", err)
	}
	return cert
}

func TestNewCADefaults(t *testing.T) {
	dir := t.TempDir()
	ca, err := NewCA(dir)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	if want := filepath.Join(dir, caFileName); ca.CertPath != want {
		t.Errorf("CertPath = %q, want %q", ca.CertPath, want)
	}
	onDisk, err := os.ReadFile(ca.CertPath)
	if err != nil {
		t.Fatalf("read CA cert: %v", err)
	}
	if string(onDisk) != string(ca.CertPEM) {
		t.Error("on-disk CA certificate differs from CertPEM")
	}
	if ca.Pool == nil {
		t.Fatal("Pool is nil")
	}

	cert := parseLeaf(t, ca.CertPEM)
	if !cert.IsCA {
		t.Error("CA certificate is not marked IsCA")
	}
	if cert.Subject.CommonName != DefaultCACommonName {
		t.Errorf("CommonName = %q, want %q", cert.Subject.CommonName, DefaultCACommonName)
	}
	if got := cert.Subject.Organization; len(got) != 1 || got[0] != DefaultOrganization {
		t.Errorf("Organization = %v, want [%s]", got, DefaultOrganization)
	}
	if remaining := time.Until(cert.NotAfter); remaining > DefaultValidity+time.Minute {
		t.Errorf("validity %v exceeds default %v", remaining, DefaultValidity)
	}
	if !cert.NotBefore.Before(time.Now()) {
		t.Error("certificate is not yet valid")
	}
}

func TestNewCAOptions(t *testing.T) {
	ca := newTestCA(t,
		WithCAOrganization("acme"),
		WithCACommonName("acme-root"),
		WithCAValidity(30*time.Minute),
	)
	cert := parseLeaf(t, ca.CertPEM)
	if cert.Subject.CommonName != "acme-root" {
		t.Errorf("CommonName = %q, want acme-root", cert.Subject.CommonName)
	}
	if got := cert.Subject.Organization; len(got) != 1 || got[0] != "acme" {
		t.Errorf("Organization = %v, want [acme]", got)
	}
	if remaining := time.Until(cert.NotAfter); remaining > 31*time.Minute {
		t.Errorf("validity %v exceeds requested 30m", remaining)
	}
}

func TestNewCAOptionsIgnoreZeroValues(t *testing.T) {
	ca := newTestCA(t,
		WithCAOrganization(""),
		WithCACommonName(""),
		WithCAValidity(0),
	)
	cert := parseLeaf(t, ca.CertPEM)
	if cert.Subject.CommonName != DefaultCACommonName {
		t.Errorf("CommonName = %q, want the default %q", cert.Subject.CommonName, DefaultCACommonName)
	}
	if got := cert.Subject.Organization; len(got) != 1 || got[0] != DefaultOrganization {
		t.Errorf("Organization = %v, want the default", got)
	}
}

func TestNewCAErrors(t *testing.T) {
	t.Run("dir path is a file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(file, []byte("x"), fileMode); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		_, err := NewCA(file)
		var caErr *CAError
		if !errors.As(err, &caErr) {
			t.Fatalf("err = %v, want *CAError", err)
		}
		if caErr.Op != "mkdir" {
			t.Errorf("Op = %q, want mkdir", caErr.Op)
		}
	})

	t.Run("cert path is a directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, caFileName), dirMode); err != nil {
			t.Fatalf("Mkdir: %v", err)
		}
		_, err := NewCA(dir)
		var caErr *CAError
		if !errors.As(err, &caErr) {
			t.Fatalf("err = %v, want *CAError", err)
		}
		if caErr.Op != "write cert" {
			t.Errorf("Op = %q, want write cert", caErr.Op)
		}
		if caErr.Unwrap() == nil {
			t.Error("Unwrap returned nil")
		}
	})
}

func TestIssueClientCertificate(t *testing.T) {
	ca := newTestCA(t)
	dir := t.TempDir()

	kp, err := ca.Issue(dir, WithSubject("admin", "system"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if want := filepath.Join(dir, "admin-cert.pem"); kp.CertPath != want {
		t.Errorf("CertPath = %q, want %q", kp.CertPath, want)
	}
	if want := filepath.Join(dir, "admin-key.pem"); kp.KeyPath != want {
		t.Errorf("KeyPath = %q, want %q", kp.KeyPath, want)
	}
	for _, path := range []string{kp.CertPath, kp.KeyPath} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", path, statErr)
		}
		if info.Mode().Perm() != fileMode {
			t.Errorf("%s mode = %v, want %v", path, info.Mode().Perm(), fileMode)
		}
	}

	cert := parseLeaf(t, kp.CertPEM)
	if cert.Subject.CommonName != "admin" {
		t.Errorf("CommonName = %q, want admin", cert.Subject.CommonName)
	}
	if got := cert.Subject.OrganizationalUnit; len(got) != 1 || got[0] != "system" {
		t.Errorf("OrganizationalUnit = %v, want [system]", got)
	}
	if len(cert.DNSNames) != 0 || len(cert.IPAddresses) != 0 {
		t.Errorf("client certificate carries SANs: dns=%v ips=%v", cert.DNSNames, cert.IPAddresses)
	}
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     ca.Pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("client certificate does not verify against the CA: %v", err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     ca.Pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err == nil {
		t.Error("client certificate unexpectedly usable for server auth")
	}
}

func TestIssueServerCertificate(t *testing.T) {
	ca := newTestCA(t)
	kp, err := ca.Issue(t.TempDir(),
		WithSubject("test-node", ""),
		WithCertOrganization("acme"),
		WithCertValidity(45*time.Minute),
		WithServerAuth(),
	)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	cert := parseLeaf(t, kp.CertPEM)
	if len(cert.Subject.OrganizationalUnit) != 0 {
		t.Errorf("OrganizationalUnit = %v, want none", cert.Subject.OrganizationalUnit)
	}
	if got := cert.Subject.Organization; len(got) != 1 || got[0] != "acme" {
		t.Errorf("Organization = %v, want [acme]", got)
	}
	if remaining := time.Until(cert.NotAfter); remaining > 46*time.Minute {
		t.Errorf("validity %v exceeds requested 45m", remaining)
	}
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     ca.Pool,
		DNSName:   "localhost",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Errorf("node certificate does not verify for localhost: %v", err)
	}

	var loopback, unspecified bool
	for _, ip := range cert.IPAddresses {
		if ip.IsLoopback() {
			loopback = true
		}
		if ip.IsUnspecified() {
			unspecified = true
		}
	}
	if !loopback {
		t.Error("node certificate has no loopback SAN")
	}
	if !unspecified {
		t.Error("node certificate has no unspecified-address SAN")
	}
}

func TestIssueRejectsInvalidSubjects(t *testing.T) {
	ca := newTestCA(t)
	tests := []struct {
		name       string
		commonName string
	}{
		{name: "empty", commonName: ""},
		{name: "slash", commonName: "tenant/user"},
		{name: "dot", commonName: "."},
		{name: "dotdot", commonName: ".."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ca.Issue(t.TempDir(), WithSubject(tc.commonName, "system"))
			var subjErr *InvalidSubjectError
			if !errors.As(err, &subjErr) {
				t.Fatalf("err = %v, want *InvalidSubjectError", err)
			}
			if subjErr.CommonName != tc.commonName {
				t.Errorf("CommonName = %q, want %q", subjErr.CommonName, tc.commonName)
			}
		})
	}
}

func TestIssueWriteErrors(t *testing.T) {
	ca := newTestCA(t)

	t.Run("dir path is a file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, []byte("x"), fileMode); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		_, err := ca.Issue(file, WithSubject("admin", "system"))
		var certErr *CertError
		if !errors.As(err, &certErr) {
			t.Fatalf("err = %v, want *CertError", err)
		}
		if certErr.Op != "mkdir" {
			t.Errorf("Op = %q, want mkdir", certErr.Op)
		}
	})

	t.Run("cert path is a directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "admin-cert.pem"), dirMode); err != nil {
			t.Fatalf("Mkdir: %v", err)
		}
		_, err := ca.Issue(dir, WithSubject("admin", "system"))
		var certErr *CertError
		if !errors.As(err, &certErr) || certErr.Op != "write cert" {
			t.Fatalf("err = %v, want *CertError{Op: write cert}", err)
		}
	})

	t.Run("key path is a directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "admin-key.pem"), dirMode); err != nil {
			t.Fatalf("Mkdir: %v", err)
		}
		_, err := ca.Issue(dir, WithSubject("admin", "system"))
		var certErr *CertError
		if !errors.As(err, &certErr) || certErr.Op != "write key" {
			t.Fatalf("err = %v, want *CertError{Op: write key}", err)
		}
	})
}

func TestIssueSPIFFEID(t *testing.T) {
	ca := newTestCA(t)
	tests := []struct {
		name        string
		trustDomain string
		path        string
		want        string
	}{
		{name: "plain path", trustDomain: "qrdb.local", path: "node/127.0.0.1:9000", want: "spiffe://qrdb.local/node/127.0.0.1:9000"},
		{name: "leading slash", trustDomain: "qrdb.local", path: "/node/a", want: "spiffe://qrdb.local/node/a"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kp, err := ca.Issue(t.TempDir(),
				nodeSubject("test-node"),
				WithServerAuth(),
				WithSPIFFEID(tc.trustDomain, tc.path),
			)
			if err != nil {
				t.Fatalf("Issue: %v", err)
			}
			cert := parseLeaf(t, kp.CertPEM)
			if len(cert.URIs) != 1 {
				t.Fatalf("URIs = %v, want exactly one SPIFFE SAN", cert.URIs)
			}
			if got := cert.URIs[0].String(); got != tc.want {
				t.Errorf("SPIFFE SAN = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("unparseable", func(t *testing.T) {
		_, err := ca.Issue(t.TempDir(),
			nodeSubject("test-node"),
			WithSPIFFEID("bad domain\x7f%zz", "node/a"),
		)
		var certErr *CertError
		if !errors.As(err, &certErr) {
			t.Fatalf("err = %v, want *CertError", err)
		}
		if !strings.Contains(certErr.Op, "parse spiffe id") {
			t.Errorf("Op = %q, want a spiffe parse failure", certErr.Op)
		}
	})
}

// nodeSubject is shorthand for the node subject used throughout this file.
func nodeSubject(commonName string) CertOption {
	return WithSubject(commonName, "")
}

// writeCAKey marshals a CA's private key next to its certificate so LoadCA has
// something to adopt, mirroring the ca.pem/ca-key.pem pair qrdbd persists.
func writeCAKey(t *testing.T, ca *CA, pkcs8 bool) string {
	t.Helper()
	var (
		der   []byte
		err   error
		block string
	)
	if pkcs8 {
		der, err = x509.MarshalPKCS8PrivateKey(ca.key)
		block = "PRIVATE KEY"
	} else {
		der, err = x509.MarshalECPrivateKey(ca.key)
		block = "EC PRIVATE KEY"
	}
	if err != nil {
		t.Fatalf("marshal CA key: %v", err)
	}
	path := filepath.Join(filepath.Dir(ca.CertPath), "ca-key.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: block, Bytes: der}), fileMode); err != nil {
		t.Fatalf("write CA key: %v", err)
	}
	return path
}

func TestLoadCA(t *testing.T) {
	for _, pkcs8 := range []bool{false, true} {
		name := "sec1"
		if pkcs8 {
			name = "pkcs8"
		}
		t.Run(name, func(t *testing.T) {
			original := newTestCA(t)
			keyPath := writeCAKey(t, original, pkcs8)

			loaded, err := LoadCA(original.CertPath, keyPath)
			if err != nil {
				t.Fatalf("LoadCA: %v", err)
			}
			if loaded.CertPath != original.CertPath {
				t.Errorf("CertPath = %q, want %q", loaded.CertPath, original.CertPath)
			}
			if string(loaded.CertPEM) != string(original.CertPEM) {
				t.Error("loaded CertPEM differs from the original")
			}

			// A certificate the adopted CA issues must chain to the original.
			kp, err := loaded.Issue(t.TempDir(), WithSubject("admin", "system"))
			if err != nil {
				t.Fatalf("Issue from adopted CA: %v", err)
			}
			if _, err := parseLeaf(t, kp.CertPEM).Verify(x509.VerifyOptions{
				Roots:     original.Pool,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
			}); err != nil {
				t.Errorf("certificate from the adopted CA does not chain to the original: %v", err)
			}
		})
	}
}

func TestLoadCAErrors(t *testing.T) {
	ca := newTestCA(t)
	keyPath := writeCAKey(t, ca, false)
	dir := t.TempDir()

	notPEM := filepath.Join(dir, "not-pem.pem")
	if err := os.WriteFile(notPEM, []byte("definitely not pem"), fileMode); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	badCert := filepath.Join(dir, "bad-cert.pem")
	if err := os.WriteFile(badCert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("garbage")}), fileMode); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	badKey := filepath.Join(dir, "bad-key.pem")
	if err := os.WriteFile(badKey, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("garbage")}), fileMode); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	edKey := filepath.Join(dir, "ed25519-key.pem")
	_, ed, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	edDER, err := x509.MarshalPKCS8PrivateKey(ed)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	if err := os.WriteFile(edKey, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: edDER}), fileMode); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	missing := filepath.Join(dir, "absent.pem")

	tests := []struct {
		name     string
		certPath string
		keyPath  string
		wantOp   string
	}{
		{name: "missing cert", certPath: missing, keyPath: keyPath, wantOp: "read cert"},
		{name: "missing key", certPath: ca.CertPath, keyPath: missing, wantOp: "read key"},
		{name: "cert not pem", certPath: notPEM, keyPath: keyPath, wantOp: "decode cert"},
		{name: "cert not a certificate", certPath: badCert, keyPath: keyPath, wantOp: "parse cert"},
		{name: "key not pem", certPath: ca.CertPath, keyPath: notPEM, wantOp: "decode key"},
		{name: "key not parseable", certPath: ca.CertPath, keyPath: badKey, wantOp: "parse key"},
		{name: "key not ecdsa", certPath: ca.CertPath, keyPath: edKey, wantOp: "parse key"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadCA(tc.certPath, tc.keyPath)
			var caErr *CAError
			if !errors.As(err, &caErr) {
				t.Fatalf("err = %v, want *CAError", err)
			}
			if caErr.Op != tc.wantOp {
				t.Errorf("Op = %q, want %q", caErr.Op, tc.wantOp)
			}
		})
	}

	if _, err := parseECPrivateKey([]byte("neither encoding")); err == nil {
		t.Error("parseECPrivateKey accepted garbage")
	}
}

func TestKeyPairTLSCertificate(t *testing.T) {
	ca := newTestCA(t)
	kp, err := ca.Issue(t.TempDir(), WithSubject("admin", "system"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	cert, err := kp.TLSCertificate()
	if err != nil {
		t.Fatalf("TLSCertificate: %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Error("tls.Certificate carries no DER chain")
	}

	broken := &KeyPair{CertPath: kp.CertPath, CertPEM: []byte("not a certificate"), KeyPEM: kp.KeyPEM}
	if _, err := broken.TLSCertificate(); err == nil {
		t.Fatal("TLSCertificate accepted a malformed certificate")
	} else {
		var certErr *CertError
		if !errors.As(err, &certErr) {
			t.Fatalf("err = %v, want *CertError", err)
		}
	}
}

func TestCATLSFlags(t *testing.T) {
	ca := newTestCA(t)
	kp, err := ca.Issue(t.TempDir(), WithSubject("test-node", ""), WithServerAuth())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	flags := ca.TLSFlags(kp)
	want := []string{
		"--tls-ca-file", ca.CertPath,
		"--tls-cert-file", kp.CertPath,
		"--tls-key-file", kp.KeyPath,
	}
	if len(flags) != len(want) {
		t.Fatalf("TLSFlags = %v, want %v", flags, want)
	}
	for i := range want {
		if flags[i] != want[i] {
			t.Errorf("TLSFlags[%d] = %q, want %q", i, flags[i], want[i])
		}
	}
}

func TestLocalSANs(t *testing.T) {
	dnsNames, ips := localSANs()
	if len(dnsNames) == 0 || dnsNames[0] != "localhost" {
		t.Errorf("dnsNames = %v, want localhost first", dnsNames)
	}
	if len(ips) < 4 {
		t.Errorf("ips = %v, want at least the loopback and unspecified addresses", ips)
	}
}
