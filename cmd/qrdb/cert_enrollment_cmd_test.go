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
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
)

// ===========================================================================
// certs enroll / ca enroll
// ===========================================================================

func TestCertsEnrollCmd_Success(t *testing.T) {
	certFile := filepath.Join(t.TempDir(), "cert.pem")
	if err := os.WriteFile(certFile, []byte("-----BEGIN CERTIFICATE-----\nleaf\n-----END CERTIFICATE-----\n"), 0600); err != nil {
		t.Fatalf("write cert: %v", err)
	}

	out, err := executeCertEnrollCmd(t, &fakeCertEnrollment{}, "ca", "enroll", "--username", "alice", "--cert", certFile)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "alice") {
		t.Fatalf("expected username in output, got: %q", out)
	}
	if !strings.Contains(out, "Thumbprint:") {
		t.Fatalf("expected thumbprint in output, got: %q", out)
	}
}

func TestCertsEnrollCmd_WithChain(t *testing.T) {
	certFile := filepath.Join(t.TempDir(), "cert.pem")
	chainFile := filepath.Join(t.TempDir(), "chain.pem")
	if err := os.WriteFile(certFile, []byte("-----BEGIN CERTIFICATE-----\nleaf\n-----END CERTIFICATE-----\n"), 0600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(chainFile, []byte("-----BEGIN CERTIFICATE-----\nintermediate\n-----END CERTIFICATE-----\n"), 0600); err != nil {
		t.Fatalf("write chain: %v", err)
	}

	var gotChain []byte
	fe := &fakeCertEnrollment{
		enrollFn: func(_ context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
			gotChain = req.ChainPEM
			return &service.EnrolledCertificate{Username: req.Username, CertPEM: req.CertPEM, Thumbprint: "thumb"}, nil
		},
	}
	_, err := executeCertEnrollCmd(t, fe, "ca", "enroll", "--username", "alice", "--cert", certFile, "--chain", chainFile)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(string(gotChain), "intermediate") {
		t.Fatalf("expected chain PEM to reach the service, got: %q", gotChain)
	}
}

func TestCertsEnrollCmd_MissingCert(t *testing.T) {
	_, err := executeCertEnrollCmd(t, &fakeCertEnrollment{}, "ca", "enroll", "--username", "alice")
	if err == nil {
		t.Fatal("expected error for missing --cert flag")
	}
}

func TestCertsEnrollCmd_NonexistentCertFile(t *testing.T) {
	_, err := executeCertEnrollCmd(t, &fakeCertEnrollment{}, "ca", "enroll", "--username", "alice", "--cert", "/nonexistent/cert.pem")
	if err == nil {
		t.Fatal("expected error for nonexistent cert file")
	}
	var fre *FileReadError
	if !errors.As(err, &fre) {
		t.Fatalf("expected FileReadError, got %T: %v", err, err)
	}
}

func TestCertsEnrollCmd_ServiceError(t *testing.T) {
	certFile := filepath.Join(t.TempDir(), "cert.pem")
	if err := os.WriteFile(certFile, []byte("-----BEGIN CERTIFICATE-----\nleaf\n-----END CERTIFICATE-----\n"), 0600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	fe := &fakeCertEnrollment{
		enrollFn: func(_ context.Context, _ *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
			return nil, errors.New("does not chain to a registered trust anchor")
		},
	}
	_, err := executeCertEnrollCmd(t, fe, "ca", "enroll", "--username", "alice", "--cert", certFile)
	if err == nil {
		t.Fatal("expected error from the enrollment service")
	}
}

// "ca enroll" is the canonical alias for "certs enroll".
func TestCAEnrollCmd_IsAliasForCertsEnroll(t *testing.T) {
	certFile := filepath.Join(t.TempDir(), "cert.pem")
	if err := os.WriteFile(certFile, []byte("-----BEGIN CERTIFICATE-----\nleaf\n-----END CERTIFICATE-----\n"), 0600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	out, err := executeCertEnrollCmd(t, &fakeCertEnrollment{}, "ca", "enroll", "--username", "alice", "--cert", certFile)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "alice") {
		t.Fatalf("expected username in output, got: %q", out)
	}
}

// ===========================================================================
// certs csr / ca csr
// ===========================================================================

func TestCertsCSRCmd_Success(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "csrout")
	out, err := executeCertEnrollCmd(t, &fakeCertEnrollment{}, "ca", "csr", "--username", "alice", "--out", outDir)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "key.pem") || !strings.Contains(out, "csr.pem") {
		t.Fatalf("expected key.pem and csr.pem paths in output, got: %q", out)
	}

	keyPath := filepath.Join(outDir, "key.pem")
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("expected key.pem to exist: %v", err)
	}
	if !strings.Contains(string(keyData), "PRIVATE KEY") {
		t.Fatalf("expected a private key, got: %q", keyData)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key.pem: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("expected key.pem mode 0600, got %o", perm)
	}

	csrData, err := os.ReadFile(filepath.Join(outDir, "csr.pem"))
	if err != nil {
		t.Fatalf("expected csr.pem to exist: %v", err)
	}
	if !strings.Contains(string(csrData), "CERTIFICATE REQUEST") {
		t.Fatalf("expected a certificate request, got: %q", csrData)
	}
}

func TestCertsCSRCmd_MissingOut(t *testing.T) {
	_, err := executeCertEnrollCmd(t, &fakeCertEnrollment{}, "ca", "csr", "--username", "alice")
	if err == nil {
		t.Fatal("expected error for missing --out flag")
	}
}

func TestCertsCSRCmd_MissingUsername(t *testing.T) {
	_, err := executeCertEnrollCmd(t, &fakeCertEnrollment{}, "ca", "csr", "--out", t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing --username flag")
	}
}

// "ca csr" is the canonical alias for "certs csr".
func TestCACSRCmd_IsAliasForCertsCSR(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "csrout")
	out, err := executeCertEnrollCmd(t, &fakeCertEnrollment{}, "ca", "csr", "--username", "alice", "--out", outDir)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "csr.pem") {
		t.Fatalf("expected csr.pem path in output, got: %q", out)
	}
}

// "ca issue" is the canonical alias for "certs issue".
func TestCAIssueCmd_IsAliasForCertsIssue(t *testing.T) {
	_, csrPEM, err := newClientKeyAndCSR("alice")
	if err != nil {
		t.Fatalf("newClientKeyAndCSR: %v", err)
	}
	csrFile := filepath.Join(t.TempDir(), "csr.pem")
	if err := os.WriteFile(csrFile, csrPEM, 0600); err != nil {
		t.Fatalf("write csr: %v", err)
	}
	out, err := executeCertEnrollCmd(t, &fakeCertEnrollment{}, "ca", "issue", "--username", "alice", "--csr", csrFile)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "AABBCCDD") {
		t.Fatalf("expected the issued serial in output, got: %q", out)
	}
}

// ===========================================================================
// tenant trust-anchors add|list|remove
// ===========================================================================

func TestTenantTrustAnchorsAddCmd_Success(t *testing.T) {
	bundleFile := filepath.Join(t.TempDir(), "bundle.pem")
	if err := os.WriteFile(bundleFile, []byte("-----BEGIN CERTIFICATE-----\nanchor\n-----END CERTIFICATE-----\n"), 0600); err != nil {
		t.Fatalf("write bundle: %v", err)
	}

	fe := &fakeCertEnrollment{
		addAnchorsFn: func(_ context.Context, tenantID string, _ []byte) ([]trustanchor.Anchor, error) {
			return []trustanchor.Anchor{
				{Fingerprint: "abcd1234", Subject: "CN=anchor", NotAfter: time.Now().Add(365 * 24 * time.Hour)},
			}, nil
		},
	}
	out, err := executeCertEnrollCmd(t, fe, "tenant", "trust-anchors", "add", "--tenant-id", tenantid.System, "--file", bundleFile)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "abcd1234") {
		t.Fatalf("expected the new anchor's fingerprint in output, got: %q", out)
	}
}

func TestTenantTrustAnchorsAddCmd_MissingFile(t *testing.T) {
	_, err := executeCertEnrollCmd(t, &fakeCertEnrollment{}, "tenant", "trust-anchors", "add", "--tenant-id", tenantid.System)
	if err == nil {
		t.Fatal("expected error for missing --file flag")
	}
}

func TestTenantTrustAnchorsAddCmd_NonexistentFile(t *testing.T) {
	_, err := executeCertEnrollCmd(t, &fakeCertEnrollment{}, "tenant", "trust-anchors", "add",
		"--tenant-id", tenantid.System, "--file", "/nonexistent/bundle.pem")
	if err == nil {
		t.Fatal("expected error for nonexistent bundle file")
	}
	var fre *FileReadError
	if !errors.As(err, &fre) {
		t.Fatalf("expected FileReadError, got %T: %v", err, err)
	}
}

func TestTenantTrustAnchorsListCmd_Success(t *testing.T) {
	fe := &fakeCertEnrollment{
		listAnchorsFn: func(_ context.Context, tenantID string) ([]trustanchor.Anchor, error) {
			return []trustanchor.Anchor{
				{Fingerprint: "fp-1", Subject: "CN=anchor-1", NotAfter: time.Now().Add(365 * 24 * time.Hour)},
				{Fingerprint: "fp-2", Subject: "CN=anchor-2", NotAfter: time.Now().Add(730 * 24 * time.Hour)},
			}, nil
		},
	}
	out, err := executeCertEnrollCmd(t, fe, "tenant", "trust-anchors", "list", "--tenant-id", tenantid.System)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "fp-1") || !strings.Contains(out, "fp-2") {
		t.Fatalf("expected both anchors in output, got: %q", out)
	}
}

func TestTenantTrustAnchorsListCmd_MissingTenantID(t *testing.T) {
	_, err := executeCertEnrollCmd(t, &fakeCertEnrollment{}, "tenant", "trust-anchors", "list")
	if err == nil {
		t.Fatal("expected error for missing --tenant-id flag")
	}
}

func TestTenantTrustAnchorsRemoveCmd_Success(t *testing.T) {
	var removedFingerprint string
	fe := &fakeCertEnrollment{
		removeAnchorFn: func(_ context.Context, _, fingerprint string) error {
			removedFingerprint = fingerprint
			return nil
		},
	}
	out, err := executeCertEnrollCmd(t, fe, "tenant", "trust-anchors", "remove", "--tenant-id", tenantid.System, "--fingerprint", "fp-1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK in output, got: %q", out)
	}
	if removedFingerprint != "fp-1" {
		t.Fatalf("expected fp-1 to reach the service, got: %q", removedFingerprint)
	}
}

func TestTenantTrustAnchorsRemoveCmd_MissingFingerprint(t *testing.T) {
	_, err := executeCertEnrollCmd(t, &fakeCertEnrollment{}, "tenant", "trust-anchors", "remove", "--tenant-id", tenantid.System)
	if err == nil {
		t.Fatal("expected error for missing --fingerprint flag")
	}
}

func TestTenantTrustAnchorsRemoveCmd_ServiceError(t *testing.T) {
	fe := &fakeCertEnrollment{
		removeAnchorFn: func(_ context.Context, _, _ string) error {
			return errors.New("no such trust anchor")
		},
	}
	_, err := executeCertEnrollCmd(t, fe, "tenant", "trust-anchors", "remove", "--tenant-id", tenantid.System, "--fingerprint", "unknown")
	if err == nil {
		t.Fatal("expected error from the service")
	}
}
