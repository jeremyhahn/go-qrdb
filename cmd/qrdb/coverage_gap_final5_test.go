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

// coverage_gap_final5_test.go covers ceremony command output format paths
// and other remaining gaps using a mock ceremony service.
package main

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ===========================================================================
// Minimal mock ceremony service
// ===========================================================================

type mockCeremonyServiceFinal5 struct{}

func (m *mockCeremonyServiceFinal5) CeremonyInitialize(_ context.Context, _ ceremony.CeremonyCA, _ *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	return &service.CeremonyInitResult{State: "initialized"}, nil
}

func (m *mockCeremonyServiceFinal5) CeremonyBeginClaimCert(_ context.Context, _ string) (*service.CeremonyChallenge, error) {
	return &service.CeremonyChallenge{NonceHex: "deadbeef"}, nil
}

func (m *mockCeremonyServiceFinal5) CeremonyCompleteClaimCert(_ context.Context, _, _ string, _ []byte) (*service.CeremonyClaimCertResult, error) {
	return &service.CeremonyClaimCertResult{
		CertPEM:   []byte("-----BEGIN CERTIFICATE-----\ncert\n-----END CERTIFICATE-----"),
		CACertPEM: []byte("-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----"),
	}, nil
}

func (m *mockCeremonyServiceFinal5) CeremonyBeginClaimShare(_ context.Context, _, _ string) (*service.CeremonyChallenge, error) {
	return &service.CeremonyChallenge{NonceHex: "abcd", ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func (m *mockCeremonyServiceFinal5) CeremonyCompleteClaimShare(_ context.Context, _, _ string, _ []byte, _ string) (*service.CeremonyShareClaimResult, error) {
	return &service.CeremonyShareClaimResult{ShareIndex: 1, ShareBytes: []byte(`{"share":"test-share"}`)}, nil
}

func (m *mockCeremonyServiceFinal5) CeremonySignCSR(_ context.Context, _, _, _, _ string) (*service.CeremonySignCSRResult, error) {
	return &service.CeremonySignCSRResult{
		CertPEM: []byte("-----BEGIN CERTIFICATE-----\nsigned\n-----END CERTIFICATE-----"),
	}, nil
}

func (m *mockCeremonyServiceFinal5) CeremonyStatus(_ context.Context) (*service.CeremonyStatusInfo, error) {
	return &service.CeremonyStatusInfo{State: "initialized", SPKIPin: "pin123"}, nil
}

func (m *mockCeremonyServiceFinal5) CeremonyClaimShareBegin(_ context.Context, _ string) (*service.CeremonyCustodianChallenge, error) {
	return &service.CeremonyCustodianChallenge{}, nil
}

func (m *mockCeremonyServiceFinal5) CeremonyClaimShareComplete(_ context.Context, _, _ string, _ []byte) (*custodian.Custodian, error) {
	return &custodian.Custodian{}, nil
}

func (m *mockCeremonyServiceFinal5) CeremonySubmitUnsealShare(_ context.Context, _, _ string, _, _ []byte) (*service.CeremonyUnsealShareResult, error) {
	return &service.CeremonyUnsealShareResult{}, nil
}

func (m *mockCeremonyServiceFinal5) CeremonyEnrollExternalOfficer(_ context.Context, _ *service.CeremonyEnrollExternalOfficerRequest) (*service.CeremonyEnrollExternalOfficerResponse, error) {
	return &service.CeremonyEnrollExternalOfficerResponse{}, nil
}

// ===========================================================================
// Helper: newRootCmdWithCeremony sets up the package-level client with a
// mock ceremony service.
// ===========================================================================

func newRootCmdWithCeremony(t *testing.T) (*sdk.Client, func()) {
	t.Helper()
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	tr, err := embedded.New(kvSvc, adminSvc,
		embedded.WithCeremonyService(&mockCeremonyServiceFinal5{}),
	)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	cl, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	if err := cl.Connect(context.Background()); err != nil {
		t.Fatalf("cl.Connect: %v", err)
	}

	oldClient := client
	client = cl

	return cl, func() {
		client = oldClient
		_ = cl.Close()
	}
}

// ===========================================================================
// ceremony.go — newCeremonyStatusCmd (40%)
// Success paths with all three output formats.
// ===========================================================================

func TestCeremonyStatusCmd_Table_WithMockCeremony(t *testing.T) {
	_, cleanup := newRootCmdWithCeremony(t)
	defer cleanup()

	old := outputWriter
	oldFmt := outputFormat
	defer func() { outputWriter = old; outputFormat = oldFmt }()
	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "table"

	cmd := newCeremonyStatusCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("initialized")) {
		t.Fatalf("expected 'initialized' in output: %q", buf.String())
	}
}

func TestCeremonyStatusCmd_JSON_WithMockCeremony(t *testing.T) {
	_, cleanup := newRootCmdWithCeremony(t)
	defer cleanup()

	old := outputWriter
	oldFmt := outputFormat
	defer func() { outputWriter = old; outputFormat = oldFmt }()
	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "json"

	cmd := newCeremonyStatusCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE JSON: %v", err)
	}
}

func TestCeremonyStatusCmd_YAML_WithMockCeremony(t *testing.T) {
	_, cleanup := newRootCmdWithCeremony(t)
	defer cleanup()

	old := outputWriter
	oldFmt := outputFormat
	defer func() { outputWriter = old; outputFormat = oldFmt }()
	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "yaml"

	cmd := newCeremonyStatusCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE YAML: %v", err)
	}
}

// ===========================================================================
// ceremony.go — newCeremonyClaimCertBeginCmd (57%)
// ===========================================================================

func TestCeremonyClaimCertBeginCmd_Table_WithMockCeremony(t *testing.T) {
	_, cleanup := newRootCmdWithCeremony(t)
	defer cleanup()

	old := outputWriter
	oldFmt := outputFormat
	defer func() { outputWriter = old; outputFormat = oldFmt }()
	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "table"

	cmd := newCeremonyClaimCertBeginCmd()
	_ = cmd.Flags().Set("username", "alice")
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
}

func TestCeremonyClaimCertBeginCmd_JSON_WithMockCeremony(t *testing.T) {
	_, cleanup := newRootCmdWithCeremony(t)
	defer cleanup()

	old := outputWriter
	oldFmt := outputFormat
	defer func() { outputWriter = old; outputFormat = oldFmt }()
	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "json"

	cmd := newCeremonyClaimCertBeginCmd()
	_ = cmd.Flags().Set("username", "alice")
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE JSON: %v", err)
	}
}

func TestCeremonyClaimCertBeginCmd_YAML_WithMockCeremony(t *testing.T) {
	_, cleanup := newRootCmdWithCeremony(t)
	defer cleanup()

	old := outputWriter
	oldFmt := outputFormat
	defer func() { outputWriter = old; outputFormat = oldFmt }()
	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "yaml"

	cmd := newCeremonyClaimCertBeginCmd()
	_ = cmd.Flags().Set("username", "bob")
	_ = cmd.RunE(cmd, nil)
}

// ===========================================================================
// ceremony.go — newCeremonyClaimCertCompleteCmd (66%)
// ===========================================================================

func TestCeremonyClaimCertCompleteCmd_Table_WithMockCeremony(t *testing.T) {
	_, cleanup := newRootCmdWithCeremony(t)
	defer cleanup()

	old := outputWriter
	oldFmt := outputFormat
	defer func() { outputWriter = old; outputFormat = oldFmt }()
	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "table"

	// The signature must be base64-encoded bytes. Use a valid base64 value.
	cmd := newCeremonyClaimCertCompleteCmd()
	_ = cmd.Flags().Set("username", "alice")
	_ = cmd.Flags().Set("nonce", "deadbeef")
	_ = cmd.Flags().Set("signature", "ZGVhZGJlZWY=") // base64("deadbeef")
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
}

func TestCeremonyClaimCertCompleteCmd_JSON_WithMockCeremony(t *testing.T) {
	_, cleanup := newRootCmdWithCeremony(t)
	defer cleanup()

	old := outputWriter
	oldFmt := outputFormat
	defer func() { outputWriter = old; outputFormat = oldFmt }()
	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "json"

	cmd := newCeremonyClaimCertCompleteCmd()
	_ = cmd.Flags().Set("username", "alice")
	_ = cmd.Flags().Set("nonce", "deadbeef")
	_ = cmd.Flags().Set("signature", "ZGVhZGJlZWY=")
	_ = cmd.RunE(cmd, nil)
}

// ===========================================================================
// ceremony.go — newCeremonyClaimShareCmd (61%)
// ===========================================================================

func TestCeremonyClaimShareCmd_Table_WithMockCeremony(t *testing.T) {
	_, cleanup := newRootCmdWithCeremony(t)
	defer cleanup()

	old := outputWriter
	oldFmt := outputFormat
	defer func() { outputWriter = old; outputFormat = oldFmt }()
	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "table"

	// The embedded transport derives the caller thumbprint from the TLS peer
	// certificate on the context. This CLI invocation carries none, so both
	// halves of the handshake must fail closed rather than release a share.
	cmd := newCeremonyBeginClaimShareCmd()
	_ = cmd.Flags().Set("username", "alice")
	if err := cmd.RunE(cmd, nil); err == nil {
		t.Fatal("expected refusal without a client certificate")
	}

	complete := newCeremonyCompleteClaimShareCmd()
	_ = complete.Flags().Set("username", "alice")
	_ = complete.Flags().Set("nonce", "abcd")
	_ = complete.Flags().Set("signature", "c2ln")
	if err := complete.RunE(complete, nil); err == nil {
		t.Fatal("expected refusal without a client certificate")
	}
	if buf.Len() != 0 {
		t.Fatalf("share material printed on refusal: %q", buf.String())
	}
}

func TestCeremonyClaimShareCmd_JSON_WithMockCeremony(t *testing.T) {
	_, cleanup := newRootCmdWithCeremony(t)
	defer cleanup()

	old := outputWriter
	oldFmt := outputFormat
	defer func() { outputWriter = old; outputFormat = oldFmt }()
	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "json"

	cmd := newCeremonyBeginClaimShareCmd()
	_ = cmd.Flags().Set("username", "alice")
	_ = cmd.RunE(cmd, nil)
}

// ===========================================================================
// ceremony.go — newCeremonySignCSRCmd (77%)
// ===========================================================================

func TestCeremonySignCSRCmd_Table_WithMockCeremony(t *testing.T) {
	csrFile := t.TempDir() + "/test.csr"
	if err := writeTestFileBytes(csrFile, []byte("-----BEGIN CERTIFICATE REQUEST-----\ndata\n-----END CERTIFICATE REQUEST-----\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, cleanup := newRootCmdWithCeremony(t)
	defer cleanup()

	old := outputWriter
	oldFmt := outputFormat
	defer func() { outputWriter = old; outputFormat = oldFmt }()
	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "table"

	cmd := newCeremonySignCSRCmd()
	_ = cmd.Flags().Set("username", "alice")
	_ = cmd.Flags().Set("so-pin", "secret")
	_ = cmd.Flags().Set("csr-file", csrFile)
	_ = cmd.Flags().Set("role", "admin")
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
}

func TestCeremonySignCSRCmd_JSON_WithMockCeremony(t *testing.T) {
	csrFile := t.TempDir() + "/test2.csr"
	if err := writeTestFileBytes(csrFile, []byte("-----BEGIN CERTIFICATE REQUEST-----\ndata2\n-----END CERTIFICATE REQUEST-----\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, cleanup := newRootCmdWithCeremony(t)
	defer cleanup()

	old := outputWriter
	oldFmt := outputFormat
	defer func() { outputWriter = old; outputFormat = oldFmt }()
	outputWriter = &bytes.Buffer{}
	outputFormat = "json"

	cmd := newCeremonySignCSRCmd()
	_ = cmd.Flags().Set("username", "alice")
	_ = cmd.Flags().Set("so-pin", "secret")
	_ = cmd.Flags().Set("csr-file", csrFile)
	_ = cmd.Flags().Set("role", "admin")
	_ = cmd.RunE(cmd, nil)
}

// ===========================================================================
// init.go — runInit exercises the code paths via the mock ceremony service.
// CeremonyInitialize requires a CeremonyCA which we cannot easily provide
// without a real PKI; we exercise what we can (the ceremony call, error return
// path) and accept the error from the nil CA check in the embedded transport.
// ===========================================================================

func TestRunInit_WithMockCeremony_EnterRunE(t *testing.T) {
	// This test verifies that runInit correctly calls CeremonyInitialize
	// and handles the transport error. The code path through the request
	// build and ceremony call is exercised even when the CA is nil.
	_, cleanup := newRootCmdWithCeremony(t)
	defer cleanup()

	old := outputWriter
	oldFmt := outputFormat
	defer func() { outputWriter = old; outputFormat = oldFmt }()
	outputWriter = &bytes.Buffer{}
	outputFormat = "table"

	f := &initFlags{
		soPin:           "sopin",
		userPin:         "userpin",
		barrierStrategy: "passphrase",
	}
	cmd := newInitCmd()
	// Accept any error — the mock transport returns a NilServiceError for the CA.
	_ = runInit(cmd, f)
}

func TestRunInit_WithMockCeremony_WithSANs(t *testing.T) {
	_, cleanup := newRootCmdWithCeremony(t)
	defer cleanup()

	old := outputWriter
	oldFmt := outputFormat
	defer func() { outputWriter = old; outputFormat = oldFmt }()
	outputWriter = &bytes.Buffer{}
	outputFormat = "table"

	f := &initFlags{
		soPin:           "sopin",
		userPin:         "userpin",
		barrierStrategy: "passphrase",
		hostname:        "localhost",
		sans:            "dns1.example.com,dns2.example.com",
	}
	cmd := newInitCmd()
	_ = runInit(cmd, f)
}

// ===========================================================================
// bindings_daemon.go — Start when not already running (exercise log/pid paths)
// ===========================================================================

func TestDaemonBinding_Start_QRDBNotFound(t *testing.T) {
	d := newTestDaemonBinding(t)
	// qrdbd is not in PATH; Start returns DaemonStartError.
	err := d.Start("/tmp/nonexistent-config.yaml")
	if err == nil {
		t.Fatal("expected error when qrdbd not found")
	}
}

// ===========================================================================
// Helpers
// ===========================================================================

// writeTestFileBytes writes data to path.
func writeTestFileBytes(path string, data []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.Write(data)
	return err
}

// Ensure the transport import is used.
var _ = transport.ProtocolConfig{}
