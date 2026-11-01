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
	"errors"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
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

func (m *mockCeremonyServiceFinal5) CeremonySignCSR(_ context.Context, _, _, _, _ string) (*service.CeremonySignCSRResult, error) {
	return &service.CeremonySignCSRResult{
		CertPEM: []byte("-----BEGIN CERTIFICATE-----\nsigned\n-----END CERTIFICATE-----"),
	}, nil
}

func (m *mockCeremonyServiceFinal5) CeremonyStatus(_ context.Context) (*service.CeremonyStatusInfo, error) {
	return &service.CeremonyStatusInfo{State: "initialized", SPKIPin: "pin123"}, nil
}

// ===========================================================================
// Helper: newRootCmdWithCeremony sets up the package-level client with a
// mock ceremony service.
// ===========================================================================

func newRootCmdWithCeremony(t *testing.T) (*sdk.Client, func()) {
	t.Helper()
	return newRootCmdWithCeremonyService(t, &mockCeremonyServiceFinal5{})
}

// newRootCmdWithCeremonyService sets up the package-level client over svc.
func newRootCmdWithCeremonyService(t *testing.T, svc service.CeremonyServicer) (*sdk.Client, func()) {
	t.Helper()
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	tr, err := embedded.New(kvSvc, adminSvc,
		embedded.WithCeremonyService(svc),
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
	if !bytes.Contains(buf.Bytes(), []byte(`"state"`)) {
		t.Fatalf("JSON output missing the state field: %q", buf.String())
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
	if !bytes.Contains(buf.Bytes(), []byte("state:")) {
		t.Fatalf("YAML output missing the state field: %q", buf.String())
	}
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
	if !bytes.Contains(buf.Bytes(), []byte("CertPEM:")) {
		t.Fatalf("table output missing CertPEM: %q", buf.String())
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
	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "json"

	cmd := newCeremonySignCSRCmd()
	_ = cmd.Flags().Set("username", "alice")
	_ = cmd.Flags().Set("so-pin", "secret")
	_ = cmd.Flags().Set("csr-file", csrFile)
	_ = cmd.Flags().Set("role", "admin")
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE JSON: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"cert_pem"`)) {
		t.Fatalf("JSON output missing cert_pem: %q", buf.String())
	}
}

// failingCeremonyService fails every ceremony call with errCeremonyTest.
type failingCeremonyService struct{ mockCeremonyServiceFinal5 }

func (failingCeremonyService) CeremonySignCSR(_ context.Context, _, _, _, _ string) (*service.CeremonySignCSRResult, error) {
	return nil, errCeremonyTest
}

func (failingCeremonyService) CeremonyStatus(_ context.Context) (*service.CeremonyStatusInfo, error) {
	return nil, errCeremonyTest
}

func TestCeremonyStatusCmd_ServiceError(t *testing.T) {
	_, cleanup := newRootCmdWithCeremonyService(t, &failingCeremonyService{})
	defer cleanup()

	old := outputWriter
	defer func() { outputWriter = old }()
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newCeremonyStatusCmd()
	if err := cmd.RunE(cmd, nil); !errors.Is(err, errCeremonyTest) {
		t.Fatalf("expected errCeremonyTest, got %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("a failed status must print nothing: %q", buf.String())
	}
}

func TestCeremonySignCSRCmd_ServiceError(t *testing.T) {
	csrFile := t.TempDir() + "/test.csr"
	if err := writeTestFileBytes(csrFile, []byte("-----BEGIN CERTIFICATE REQUEST-----\ndata\n-----END CERTIFICATE REQUEST-----\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, cleanup := newRootCmdWithCeremonyService(t, &failingCeremonyService{})
	defer cleanup()

	old := outputWriter
	defer func() { outputWriter = old }()
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newCeremonySignCSRCmd()
	_ = cmd.Flags().Set("username", "alice")
	_ = cmd.Flags().Set("so-pin", "secret")
	_ = cmd.Flags().Set("csr-file", csrFile)
	_ = cmd.Flags().Set("role", "admin")
	if err := cmd.RunE(cmd, nil); !errors.Is(err, errCeremonyTest) {
		t.Fatalf("expected errCeremonyTest, got %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("a failed signing must print nothing: %q", buf.String())
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
