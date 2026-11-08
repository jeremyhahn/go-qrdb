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
	"bytes"
	"context"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// signedInitTenantService answers both halves of a signed init and records
// what the CLI sent.
type signedInitTenantService struct {
	*mockTenantService
	begin    *tenant.TenantBarrierInitBeginRequest
	complete *tenant.TenantBarrierInitCompleteRequest
}

func (m *signedInitTenantService) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	return tenant.ClusterIsolationPolicyOperatorAccess, nil
}

func (m *signedInitTenantService) SetClusterIsolationPolicy(context.Context, tenant.ClusterIsolationPolicy) error {
	return nil
}

func (s *signedInitTenantService) TenantBarrierInitBegin(_ context.Context, req *tenant.TenantBarrierInitBeginRequest) (*tenant.TenantBarrierInitBeginResult, error) {
	s.begin = req
	return &tenant.TenantBarrierInitBeginResult{
		TenantID:  req.TenantID,
		Strategy:  string(req.Strategy),
		Reference: []byte{0xde, 0xad},
		Statement: []byte("the statement"),
		ExpiresAt: time.Unix(1700000000, 0).UTC(),
	}, nil
}

func (s *signedInitTenantService) TenantBarrierInitComplete(_ context.Context, req *tenant.TenantBarrierInitCompleteRequest) (*tenant.TenantBarrierInitResult, error) {
	s.complete = req
	if !bytes.Equal(req.Reference, []byte{0xde, 0xad}) {
		return nil, &tenant.SignedInitError{TenantID: req.TenantID, Reason: tenant.SignedInitUnknown}
	}
	return &tenant.TenantBarrierInitResult{TenantID: req.TenantID, Strategy: "none"}, nil
}

// executeSignedInit runs `qrdb tenant <args>` against svc over the embedded
// transport and returns what it printed.
func executeSignedInit(t *testing.T, svc tenant.TenantServicer, format string, args ...string) (string, error) {
	t.Helper()
	tr, err := embedded.New(&noopKVService{}, newMockAdminService(), embedded.WithTenantService(svc))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	var out bytes.Buffer
	oldClient, oldWriter, oldFormat := client, outputWriter, outputFormat
	t.Cleanup(func() { client, outputWriter, outputFormat = oldClient, oldWriter, oldFormat })
	client, outputWriter, outputFormat = sdkClient, &out, format

	root := &cobra.Command{Use: "qrdb", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(newTenantCmd())
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs(append([]string{"tenant"}, args...))
	err = root.ExecuteContext(context.Background())
	return out.String(), err
}

func writePublicKeyPEM(t *testing.T, blockType string, der []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "signer.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTenantBarrierInitBegin_SendsTheCertificatesKeyAndPrintsTheStatement(t *testing.T) {
	certFile, _, pair := writeProofKeyPair(t)
	svc := &signedInitTenantService{mockTenantService: &mockTenantService{}}
	statementOut := filepath.Join(t.TempDir(), "statement.bin")

	out, err := executeSignedInit(t, svc, "json", "barrier-init-begin", "--id", "acme", "--strategy", "none",
		"--public-key", certFile, "--statement-out", statementOut)
	if err != nil {
		t.Fatalf("barrier-init-begin: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(svc.begin.SignerPublicKey, leaf.RawSubjectPublicKeyInfo) || svc.begin.TenantID != "acme" || svc.begin.Strategy != "none" {
		t.Fatalf("begin request = %+v", svc.begin)
	}
	var printed signedInitBeginOutput
	if err := json.Unmarshal([]byte(out), &printed); err != nil {
		t.Fatalf("json output %q: %v", out, err)
	}
	if printed.ReferenceHex != "dead" || printed.StatementHex != hex.EncodeToString([]byte("the statement")) {
		t.Fatalf("printed = %+v", printed)
	}
	if got, err := os.ReadFile(statementOut); err != nil || string(got) != "the statement" {
		t.Fatalf("--statement-out = %q, %v", got, err)
	}
}

func TestTenantBarrierInitBegin_PublicKeyBlockAndTableOutput(t *testing.T) {
	_, _, pair := writeProofKeyPair(t)
	spki, err := x509.MarshalPKIXPublicKey(pair.Leaf.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	svc := &signedInitTenantService{mockTenantService: &mockTenantService{}}
	out, err := executeSignedInit(t, svc, "table", "barrier-init-begin", "--id", "acme",
		"--public-key", writePublicKeyPEM(t, "PUBLIC KEY", spki))
	if err != nil {
		t.Fatalf("barrier-init-begin: %v", err)
	}
	if !bytes.Equal(svc.begin.SignerPublicKey, spki) {
		t.Fatal("a PUBLIC KEY block must be sent as its DER")
	}
	if !strings.Contains(out, "ReferenceHex: dead") || !strings.Contains(out, "StatementHex: ") {
		t.Fatalf("table output = %q", out)
	}
}

func TestTenantBarrierInitBegin_RefusesAnUnusablePublicKey(t *testing.T) {
	cases := map[string]string{
		"missing file":    filepath.Join(t.TempDir(), "absent.pem"),
		"not PEM":         writeRaw(t, "not pem"),
		"wrong block":     writePublicKeyPEM(t, "PRIVATE KEY", []byte{1}),
		"bad SPKI":        writePublicKeyPEM(t, "PUBLIC KEY", []byte{1}),
		"bad certificate": writePublicKeyPEM(t, "CERTIFICATE", []byte{1}),
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			svc := &signedInitTenantService{mockTenantService: &mockTenantService{}}
			if _, err := executeSignedInit(t, svc, "table", "barrier-init-begin", "--id", "acme", "--public-key", path); err == nil {
				t.Fatal("an unusable --public-key must be refused")
			}
			if svc.begin != nil {
				t.Fatal("a refused --public-key must not reach the node")
			}
		})
	}
}

func writeRaw(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "raw")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTenantBarrierInitComplete_SendsTheSignature(t *testing.T) {
	svc := &signedInitTenantService{mockTenantService: &mockTenantService{}}
	out, err := executeSignedInit(t, svc, "table", "barrier-init-complete", "--id", "acme",
		"--reference-hex", "dead", "--sig-file", writeRaw(t, "sig"), "--passphrase", "pw")
	if err != nil {
		t.Fatalf("barrier-init-complete: %v", err)
	}
	if string(svc.complete.Signature) != "sig" || svc.complete.TenantID != "acme" {
		t.Fatalf("complete request = %+v", svc.complete)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("output = %q; want OK", out)
	}
}

func TestTenantBarrierInitComplete_RefusesBadInput(t *testing.T) {
	svc := &signedInitTenantService{mockTenantService: &mockTenantService{}}
	if _, err := executeSignedInit(t, svc, "table", "barrier-init-complete", "--id", "acme",
		"--reference-hex", "zz", "--sig-file", writeRaw(t, "sig")); err == nil {
		t.Fatal("a non-hex reference must be refused")
	}
	var fre *FileReadError
	if _, err := executeSignedInit(t, svc, "table", "barrier-init-complete", "--id", "acme",
		"--reference-hex", "dead", "--sig-file", filepath.Join(t.TempDir(), "absent")); !errors.As(err, &fre) {
		t.Fatalf("a missing --sig-file err = %v; want *FileReadError", err)
	}
	if svc.complete != nil {
		t.Fatal("refused input must not reach the node")
	}
	var se *tenant.SignedInitError
	if _, err := executeSignedInit(t, svc, "table", "barrier-init-complete", "--id", "acme",
		"--reference-hex", "beef", "--sig-file", writeRaw(t, "sig")); !errors.As(err, &se) {
		t.Fatalf("an unknown reference err = %v; want *tenant.SignedInitError", err)
	}
}

func TestTenantBarrierRoster_SharesMustMatch(t *testing.T) {
	if _, err := tenantBarrierRoster(nil, nil, 3); err != nil {
		t.Fatalf("no roster: %v", err)
	}
	var iae *InvalidArgError
	if _, err := tenantBarrierRoster([]string{"alice"}, nil, 1); !errors.As(err, &iae) {
		t.Fatalf("a malformed --officers entry err = %v; want *InvalidArgError", err)
	}
}
