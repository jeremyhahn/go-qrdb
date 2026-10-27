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
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// writeTestCertPEM writes a self-signed certificate expiring at notAfter and
// returns its path and the parsed certificate.
func writeTestCertPEM(t *testing.T, notAfter time.Time) (string, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "alice"},
		NotBefore:    notAfter.Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	path := filepath.Join(t.TempDir(), "cert.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	return path, cert
}

func decodeBindings(t *testing.T, out string) []transport.CertBinding {
	t.Helper()
	var got []transport.CertBinding
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode bindings JSON: %v\noutput: %s", err, out)
	}
	return got
}

func TestUsersCertBindingAddCmd_Thumbprint(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "--output", "json", "users", "cert-binding", "add", "1", "tp-1",
		"--not-after", "2030-01-02T03:04:05Z")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	got := decodeBindings(t, out)
	if len(got) != 1 || got[0].Thumbprint != "tp-1" || got[0].NotAfter != "2030-01-02T03:04:05Z" {
		t.Fatalf("unexpected bindings: %+v", got)
	}
}

func TestUsersCertBindingAddCmd_CertFile(t *testing.T) {
	notAfter := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)
	path, cert := writeTestCertPEM(t, notAfter)
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if want != user.ComputeSPKIThumbprint(cert) {
		t.Fatalf("CLI thumbprint encoding diverges from the server's")
	}

	root := newTestRootCmdAuth()
	out, err := executeCommand(root, "--output", "json", "users", "cert-binding", "add", "1", "--cert", path)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	got := decodeBindings(t, out)
	if len(got) != 1 || got[0].Thumbprint != want || got[0].NotAfter != notAfter.Format(time.RFC3339) {
		t.Fatalf("unexpected bindings: %+v, want thumbprint %s", got, want)
	}
}

// TestUsersCertBindingAddCmd_PreEpochCertOverridden proves --not-after still
// overrides a certificate whose own expiry is at or before the epoch: only
// the expiry actually sent must not become the no-expiry sentinel.
func TestUsersCertBindingAddCmd_PreEpochCertOverridden(t *testing.T) {
	path, _ := writeTestCertPEM(t, time.Date(1969, 12, 31, 0, 0, 0, 0, time.UTC))
	notAfter := "2032-01-02T03:04:05Z"

	root := newTestRootCmdAuth()
	out, err := executeCommand(root, "--output", "json", "users", "cert-binding", "add", "1", "--cert", path, "--not-after", notAfter)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if got := decodeBindings(t, out); len(got) != 1 || got[0].NotAfter != notAfter {
		t.Fatalf("unexpected bindings: %+v, want expiry %s", got, notAfter)
	}
}

func TestUsersCertBindingAddCmd_TableOutput(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "users", "cert-binding", "add", "1", "tp-table")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "THUMBPRINT") || !strings.Contains(out, "tp-table") {
		t.Fatalf("expected a binding table, got: %q", out)
	}
}

func TestUsersCertBindingAddCmd_ArgErrors(t *testing.T) {
	path, _ := writeTestCertPEM(t, time.Now().Add(time.Hour))
	preEpoch, _ := writeTestCertPEM(t, time.Date(1969, 12, 31, 0, 0, 0, 0, time.UTC))
	notPEM := filepath.Join(t.TempDir(), "junk.pem")
	if err := os.WriteFile(notPEM, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
		want any
	}{
		{"neither", []string{"1"}, new(*MissingArgError)},
		{"both", []string{"1", "tp", "--cert", path}, new(*InvalidArgError)},
		{"bad not-after", []string{"1", "tp", "--not-after", "tomorrow"}, new(*InvalidFlagValueError)},
		{"epoch not-after", []string{"1", "tp", "--not-after", "1970-01-01T00:00:00Z"}, new(*InvalidFlagValueError)},
		{"pre-epoch not-after", []string{"1", "tp", "--not-after", "1969-07-20T20:17:40Z"}, new(*InvalidFlagValueError)},
		{"epoch not-after over cert", []string{"1", "--cert", path, "--not-after", "1970-01-01T00:00:00Z"}, new(*InvalidFlagValueError)},
		{"pre-epoch cert", []string{"1", "--cert", preEpoch}, new(*InvalidFlagValueError)},
		{"missing cert file", []string{"1", "--cert", filepath.Join(t.TempDir(), "absent.pem")}, new(*FileReadError)},
		{"not a certificate", []string{"1", "--cert", notPEM}, new(*InvalidFlagValueError)},
		{"unknown user", []string{"999999", "tp"}, new(*transport.UserNotFoundError)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newTestRootCmdAuth()
			_, err := executeCommand(root, append([]string{"users", "cert-binding", "add"}, tc.args...)...)
			if err == nil || !errors.As(err, tc.want) {
				t.Fatalf("expected %T, got: %T: %v", tc.want, err, err)
			}
		})
	}
}

func TestUsersCertBindingListCmd_Empty(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "--output", "json", "users", "cert-binding", "list", "1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if got := decodeBindings(t, out); len(got) != 0 {
		t.Fatalf("expected no bindings, got: %+v", got)
	}
}

func TestUsersCertBindingListCmd_Errors(t *testing.T) {
	root := newTestRootCmdAuth()
	_, err := executeCommand(root, "users", "cert-binding", "list", "999999")
	var nfe *transport.UserNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected UserNotFoundError, got: %T: %v", err, err)
	}

	root = newTestRootCmdAuth()
	if _, err := executeCommand(root, "users", "cert-binding", "list"); err == nil {
		t.Fatal("expected an error without a user ID")
	}
}

func TestUsersCertBindingRemoveCmd_Errors(t *testing.T) {
	root := newTestRootCmdAuth()
	_, err := executeCommand(root, "users", "cert-binding", "remove", "1", "tp-absent")
	var bnf *user.CertBindingNotFoundError
	if !errors.As(err, &bnf) {
		t.Fatalf("expected CertBindingNotFoundError, got: %T: %v", err, err)
	}

	root = newTestRootCmdAuth()
	_, err = executeCommand(root, "users", "cert-binding", "remove", "abc", "tp")
	if err == nil {
		t.Fatal("expected an error for a non-numeric user ID")
	}

	root = newTestRootCmdAuth()
	if _, err := executeCommand(root, "users", "cert-binding", "remove", "1"); err == nil {
		t.Fatal("expected an error without a thumbprint")
	}
}

// dashThumbprint is a base64url SHA-256 SPKI thumbprint (RFC 8705
// x5t#S256) that begins with '-', as about one in 64 of them does.
const dashThumbprint = "-mfgDIAA4OnmhPXEgQ0d46rNYBLntyNnLUbId3NJ_eg"

func TestUsersCertBindingAddCmd_DashThumbprintAfterTerminator(t *testing.T) {
	root := newTestRootCmdAuth()
	out, err := executeCommand(root, "--output", "json", "users", "cert-binding", "add", "1",
		"--not-after", "2030-01-02T03:04:05Z", "--", dashThumbprint)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	got := decodeBindings(t, out)
	if len(got) != 1 || got[0].Thumbprint != dashThumbprint || got[0].NotAfter != "2030-01-02T03:04:05Z" {
		t.Fatalf("unexpected bindings: %+v", got)
	}

	root = newTestRootCmdAuth()
	if _, err := executeCommand(root, "users", "cert-binding", "add", "1", dashThumbprint); err == nil {
		t.Fatal("expected a flag parse error for a '-' thumbprint before '--'")
	}
}

func TestUsersCertBindingRemoveCmd_DashThumbprintAfterTerminator(t *testing.T) {
	root := newTestRootCmdAuth()
	_, err := executeCommand(root, "users", "cert-binding", "remove", "1", "--", dashThumbprint)
	var bnf *user.CertBindingNotFoundError
	if !errors.As(err, &bnf) || bnf.Thumbprint != dashThumbprint {
		t.Fatalf("expected CertBindingNotFoundError for %s, got: %T: %v", dashThumbprint, err, err)
	}

	root = newTestRootCmdAuth()
	_, err = executeCommand(root, "users", "cert-binding", "remove", "1", dashThumbprint)
	if err == nil || errors.As(err, &bnf) {
		t.Fatalf("expected a flag parse error for a '-' thumbprint before '--', got: %v", err)
	}
}

func TestPrintCertBindings_YAML(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "--output", "yaml", "users", "cert-binding", "add", "1", "tp-yaml")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "thumbprint: tp-yaml") {
		t.Fatalf("expected YAML bindings, got: %q", out)
	}
}

func TestSpkiThumbprint_MatchesServer(t *testing.T) {
	for i := 0; i < 2; i++ {
		_, cert := writeTestCertPEM(t, time.Now().Add(time.Hour))
		if got, want := spkiThumbprint(cert), user.ComputeSPKIThumbprint(cert); got != want {
			t.Fatalf("spkiThumbprint = %s, server computes %s", got, want)
		}
	}
}
