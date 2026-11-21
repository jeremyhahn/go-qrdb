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
	"net"
	"os"
	"path/filepath"
	"sort"
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

func TestIssueURISAN(t *testing.T) {
	ca := newTestCA(t)
	tests := []struct {
		name string
		opts []CertOption
	}{
		{name: "replaces the OU-derived SAN", opts: []CertOption{WithSubject("alice", "acme")}},
		{name: "replaces WithSPIFFEID", opts: []CertOption{WithSubject("alice", "acme"), WithSPIFFEID("acme", "user/alice")}},
	}
	const want = "https://db.example.com/user/alice"
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kp, err := ca.Issue(t.TempDir(), append(tc.opts, WithURISAN(want))...)
			if err != nil {
				t.Fatalf("Issue: %v", err)
			}
			cert := parseLeaf(t, kp.CertPEM)
			if len(cert.URIs) != 1 || cert.URIs[0].String() != want {
				t.Fatalf("URIs = %v, want exactly %q", cert.URIs, want)
			}
		})
	}

	t.Run("unparseable", func(t *testing.T) {
		_, err := ca.Issue(t.TempDir(), WithSubject("alice", "acme"), WithURISAN("https://bad host\x7f%zz/"))
		var certErr *CertError
		if !errors.As(err, &certErr) {
			t.Fatalf("err = %v, want *CertError", err)
		}
		if !strings.Contains(certErr.Op, "parse uri san") {
			t.Errorf("Op = %q, want a uri san parse failure", certErr.Op)
		}
	})
}

// TestIssueTenantPrincipalSAN: a client certificate with an OU names the same
// tenant as the trust domain of its SPIFFE ID SAN, refused when the tenant is
// the cluster trust domain (WithClusterTrustDomain); WithSPIFFEID replaces it;
// a node leaf and a certificate with no OU carry none.
func TestIssueTenantPrincipalSAN(t *testing.T) {
	ca := newTestCA(t)
	tests := []struct {
		name string
		opts []CertOption
		want []string
	}{
		{name: "default trust domain", opts: []CertOption{WithSubject("alice", "acme")},
			want: []string{"spiffe://acme/user/alice"}},
		{name: "configured trust domain", opts: []CertOption{WithSubject("admin", "system"), WithClusterTrustDomain("db.example.com")},
			want: []string{"spiffe://system/user/admin"}},
		{name: "explicit SPIFFE ID replaces it", opts: []CertOption{WithSubject("alice", "acme"), WithSPIFFEID("globex", "user/alice")},
			want: []string{"spiffe://globex/user/alice"}},
		{name: "no OU", opts: []CertOption{WithSubject("alice", "")}},
		{name: "node leaf", opts: []CertOption{WithSubject("node-1", "raft"), WithServerAuth()}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kp, err := ca.Issue(t.TempDir(), tc.opts...)
			if err != nil {
				t.Fatalf("Issue: %v", err)
			}
			var got []string
			for _, u := range parseLeaf(t, kp.CertPEM).URIs {
				got = append(got, u.String())
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("URIs = %v, want %v", got, tc.want)
			}
		})
	}

	for name, ou := range map[string]string{
		"unrepresentable tenant":         "ac/me",
		"tenant not a trust domain name": "Acme",
		"tenant is cluster trust domain": "qrdb.cluster",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ca.Issue(t.TempDir(), WithSubject("alice", ou))
			var certErr *CertError
			if !errors.As(err, &certErr) {
				t.Fatalf("err = %v, want *CertError", err)
			}
			if certErr.Op != "tenant spiffe id" {
				t.Errorf("Op = %q, want the tenant SPIFFE ID failure", certErr.Op)
			}
		})
	}
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

// TestIssueServerCertificateSANSet pins the SAN set a harness node certificate
// carries, as a set equality in both directions rather than as a count.
//
// A count is a claim about how many NICs the host happens to have -- true on a
// workstation, false in a single-interface container. The version of this test
// that asserted `len(ips) >= 4` was copied verbatim into cmd/qrdbd, where the
// production function excludes the two unspecified addresses, and it was
// deterministically red in every container gate for three weeks (#297, #310).
//
// It asserts against the ISSUED CERTIFICATE rather than against a SAN helper,
// because the certificate is what a caller verifies. That also means the
// assertion survives the helper being replaced, which is what #304 did.
//
// Known limit, stated rather than papered over: the interface half of the
// expected set is derived the same way the implementation derives it, so that
// half MIRRORS the implementation rather than checking it independently. The
// loopback half and the exclusion half are genuine checks, and the exclusion
// half is the one that catches the divergence this test exists for. Making the
// interface half independent needs an enumerator seam in pkg/clustertls, which
// is an API change and not this fix (same reasoning as #310).
func TestIssueServerCertificateSANSet(t *testing.T) {
	ca := newTestCA(t)
	kp, err := ca.Issue(t.TempDir(), nodeSubject("test-node"), WithServerAuth())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	cert := parseLeaf(t, kp.CertPEM)

	wantDNS := map[string]bool{"localhost": true}
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		wantDNS[hostname] = true
	}
	gotDNS := make(map[string]bool, len(cert.DNSNames))
	for _, name := range cert.DNSNames {
		gotDNS[name] = true
	}
	if !equalStringSet(wantDNS, gotDNS) {
		t.Errorf("DNS SANs = %v, want %v", cert.DNSNames, keysOf(wantDNS))
	}

	// The contract: the two loopbacks plus every non-loopback interface
	// address, and nothing else. Missing entries catch a dropped loopback or a
	// dropped interface address; EXTRA entries catch 0.0.0.0 and ::, which
	// clustertls.LocalSANs excludes because nothing dials them.
	wantIP := map[string]bool{"127.0.0.1": true, "::1": true}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("InterfaceAddrs: %v", err)
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			wantIP[ipNet.IP.String()] = true
		}
	}
	gotIP := make(map[string]bool, len(cert.IPAddresses))
	for _, ip := range cert.IPAddresses {
		gotIP[ip.String()] = true
	}
	if !equalStringSet(wantIP, gotIP) {
		t.Errorf("IP SANs = %v, want the loopbacks plus every non-loopback interface address %v",
			cert.IPAddresses, keysOf(wantIP))
	}
}

// equalStringSet reports whether two string sets hold the same members.
func equalStringSet(want, got map[string]bool) bool {
	if len(want) != len(got) {
		return false
	}
	for k := range want {
		if !got[k] {
			return false
		}
	}
	return true
}

// keysOf returns a set's members, for a failure message that names them.
func keysOf(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
