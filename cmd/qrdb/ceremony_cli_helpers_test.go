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
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
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
	initFn              func(ctx context.Context, ca ceremony.CeremonyCA) (*service.CeremonyInitResult, error)
	statusFn            func(ctx context.Context) (*service.CeremonyStatusInfo, error)
	claimCertBeginFn    func(ctx context.Context, username string) (*service.CeremonyChallenge, error)
	claimCertCompleteFn func(ctx context.Context, username, nonceHex string, sig []byte) (*service.CeremonyClaimCertResult, error)
	claimShareFn        func(ctx context.Context, username string) ([]byte, error)
	signCSRFn           func(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error)
	shareBeginFn        func(ctx context.Context, custodianID string) (*service.CeremonyCustodianChallenge, error)
	shareCompleteFn     func(ctx context.Context, custodianID, challengeID string, factorResponse []byte) (*custodian.Custodian, error)
	submitUnsealFn      func(ctx context.Context, custodianID, challengeID string, factorResponse, shareBytes []byte) (*service.CeremonyUnsealShareResult, error)
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

func (m *mockCeremonyService) CeremonyBeginClaimCert(ctx context.Context, username string) (*service.CeremonyChallenge, error) {
	if m.claimCertBeginFn != nil {
		return m.claimCertBeginFn(ctx, username)
	}
	return &service.CeremonyChallenge{
		NonceHex:  "deadbeef",
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}, nil
}

func (m *mockCeremonyService) CeremonyCompleteClaimCert(ctx context.Context, username, nonceHex string, sig []byte) (*service.CeremonyClaimCertResult, error) {
	if m.claimCertCompleteFn != nil {
		return m.claimCertCompleteFn(ctx, username, nonceHex, sig)
	}
	return &service.CeremonyClaimCertResult{
		CertPEM:   []byte("---CERT---"),
		CACertPEM: []byte("---CA---"),
	}, nil
}

func (m *mockCeremonyService) CeremonyBeginClaimShare(ctx context.Context, username, _ string) (*service.CeremonyChallenge, error) {
	if m.claimShareFn != nil {
		if _, err := m.claimShareFn(ctx, username); err != nil {
			return nil, err
		}
	}
	return &service.CeremonyChallenge{NonceHex: "abcd", ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func (m *mockCeremonyService) CeremonyCompleteClaimShare(ctx context.Context, username, _ string, _ []byte, _ string) (*service.CeremonyShareClaimResult, error) {
	if m.claimShareFn != nil {
		share, err := m.claimShareFn(ctx, username)
		if err != nil {
			return nil, err
		}
		return &service.CeremonyShareClaimResult{ShareIndex: 1, ShareBytes: share}, nil
	}
	return &service.CeremonyShareClaimResult{ShareIndex: 1, ShareBytes: []byte(`{"index":1,"share":"abc"}`)}, nil
}

func (m *mockCeremonyService) CeremonySignCSR(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error) {
	if m.signCSRFn != nil {
		return m.signCSRFn(ctx, username, soPin, csrPEM, role)
	}
	return &service.CeremonySignCSRResult{CertPEM: []byte("---CERT---")}, nil
}

// CeremonyClaimShareBegin is a stub satisfying the new custodian-ceremony
// portion of service.CeremonyServicer. The CLI coverage tests do not exercise
// factor-based claim flows, so the default implementation returns an error.
func (m *mockCeremonyService) CeremonyClaimShareBegin(ctx context.Context, custodianID string) (*service.CeremonyCustodianChallenge, error) {
	if m.shareBeginFn != nil {
		return m.shareBeginFn(ctx, custodianID)
	}
	return nil, fmt.Errorf("not implemented in mock")
}

// CeremonyClaimShareComplete is a stub for the custodian claim-complete flow.
func (m *mockCeremonyService) CeremonyClaimShareComplete(ctx context.Context, custodianID, challengeID string, factorResponse []byte) (*custodian.Custodian, error) {
	if m.shareCompleteFn != nil {
		return m.shareCompleteFn(ctx, custodianID, challengeID, factorResponse)
	}
	return nil, fmt.Errorf("not implemented in mock")
}

// CeremonySubmitUnsealShare is a stub for custodian unseal-share submission.
func (m *mockCeremonyService) CeremonySubmitUnsealShare(ctx context.Context, custodianID, challengeID string, factorResponse, shareBytes []byte) (*service.CeremonyUnsealShareResult, error) {
	if m.submitUnsealFn != nil {
		return m.submitUnsealFn(ctx, custodianID, challengeID, factorResponse, shareBytes)
	}
	return nil, fmt.Errorf("not implemented in mock")
}

// CeremonyEnrollExternalOfficer is a stub for external-officer enrollment.
func (m *mockCeremonyService) CeremonyEnrollExternalOfficer(_ context.Context, _ *service.CeremonyEnrollExternalOfficerRequest) (*service.CeremonyEnrollExternalOfficerResponse, error) {
	return nil, fmt.Errorf("not implemented in mock")
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
