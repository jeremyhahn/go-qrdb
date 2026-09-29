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

// ceremony_cli_helpers_test.go holds the shared ceremony CLI test doubles
// (mockCeremonyCA, mockCeremonyService) and the executeCeremonyCmd harness.
// They depend only on untagged symbols, so they live untagged and serve both
// the default-tag ceremony CLI tests and the qrdb_embedded coverage tests.
package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"io"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// mockCeremonyCA implements ceremony.CeremonyCA as a no-op for ceremony
// init tests. The embedded transport requires a non-nil CA to reach the
// service layer.
type mockCeremonyCA struct{}

func (m *mockCeremonyCA) SignCSR(_ context.Context, _ []byte, _ *ceremony.SignCSROptions) (*x509.Certificate, error) {
	return nil, nil
}

func (m *mockCeremonyCA) CACertificate() (*x509.Certificate, error) { return nil, nil }
func (m *mockCeremonyCA) CABundle() ([]byte, error)                 { return []byte("---CA---"), nil }
func (m *mockCeremonyCA) IssueCertificate(_ context.Context, _ *ceremony.IssueCertRequest) (*ceremony.IssuedCert, error) {
	return nil, nil
}

var _ ceremony.CeremonyCA = (*mockCeremonyCA)(nil)

// mockCeremonyService implements service.CeremonyServicer for testing.
type mockCeremonyService struct {
	initFn       func(ctx context.Context, ca ceremony.CeremonyCA) (*service.CeremonyInitResult, error)
	statusFn     func(ctx context.Context) (*service.CeremonyStatusInfo, error)
	claimShareFn func(ctx context.Context, username string) ([]byte, error)
	signCSRFn    func(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error)
}

func (m *mockCeremonyService) CeremonyInitialize(ctx context.Context, ca ceremony.CeremonyCA, _ *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	if m.initFn != nil {
		return m.initFn(ctx, ca)
	}
	return &service.CeremonyInitResult{
		SPKIPin:    "abc123",
		CACertPEM:  []byte("---CA---"),
		TLSCertPEM: []byte("---TLS---"),
		State:      "initialized",
		ShareCount: 3,
	}, nil
}

func (m *mockCeremonyService) CeremonyStatus(ctx context.Context) (*service.CeremonyStatusInfo, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx)
	}
	return &service.CeremonyStatusInfo{State: "initialized", SPKIPin: "abc123"}, nil
}

func (m *mockCeremonyService) CeremonySignCSR(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error) {
	if m.signCSRFn != nil {
		return m.signCSRFn(ctx, username, soPin, csrPEM, role)
	}
	return &service.CeremonySignCSRResult{CertPEM: []byte("---CERT---")}, nil
}

var _ service.CeremonyServicer = (*mockCeremonyService)(nil)

// executeCeremonyCmd builds a client backed by an embedded transport with
// the mock ceremony service, then executes the given ceremony subcommand args.
func executeCeremonyCmd(t *testing.T, svc service.CeremonyServicer, args ...string) (string, error) {
	t.Helper()

	tr, err := embedded.New(
		&noopKVService{},
		&dbTestAdminService{},
		embedded.WithCeremonyService(svc),
		embedded.WithCeremonyCA(&mockCeremonyCA{}),
	)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}

	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	oldClient := client
	oldWriter := outputWriter
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
	})

	client = sdkClient
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newCeremonyCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs(args)

	execErr := cmd.ExecuteContext(context.Background())
	return buf.String(), execErr
}
