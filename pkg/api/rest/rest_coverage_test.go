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

package rest

import (
	"context"
	"crypto/x509"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// mockCeremonyService (local to this file)
// ---------------------------------------------------------------------------

type coverageCeremonyService struct {
	initResult       *service.CeremonyInitResult
	initErr          error
	capturedInitOpts *service.CeremonyInitializeOptions
	statusResult     *service.CeremonyStatusInfo
	statusErr        error
	beginErr         error
	completeErr      error
	claimShareResult []byte
	claimShareErr    error
	signCSRResult    *service.CeremonySignCSRResult
	signCSRErr       error
}

var _ service.CeremonyServicer = (*coverageCeremonyService)(nil)

func newCoverageCeremonyService() *coverageCeremonyService {
	return &coverageCeremonyService{
		initResult: &service.CeremonyInitResult{
			SPKIPin:    "pin123",
			CACertPEM:  []byte("ca-cert"),
			TLSCertPEM: []byte("tls-cert"),
			State:      "initialized",
			ShareCount: 3,
		},
		statusResult:     &service.CeremonyStatusInfo{State: "initialized", SPKIPin: "pin123"},
		claimShareResult: []byte("shamir-share-bytes"),
		signCSRResult:    &service.CeremonySignCSRResult{CertPEM: []byte("signed-cert")},
	}
}

func (m *coverageCeremonyService) CeremonyInitialize(_ context.Context, _ ceremony.CeremonyCA, opts *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	m.capturedInitOpts = opts
	return m.initResult, m.initErr
}
func (m *coverageCeremonyService) CeremonyStatus(_ context.Context) (*service.CeremonyStatusInfo, error) {
	return m.statusResult, m.statusErr
}
func (m *coverageCeremonyService) CeremonySignCSR(_ context.Context, _, _, _, _ string) (*service.CeremonySignCSRResult, error) {
	return m.signCSRResult, m.signCSRErr
}

// ---------------------------------------------------------------------------
// coverageCeremonyCA implements ceremony.CeremonyCA for testing.
// ---------------------------------------------------------------------------

type coverageCeremonyCA struct{}

var _ ceremony.CeremonyCA = (*coverageCeremonyCA)(nil)

func (m *coverageCeremonyCA) SignCSR(_ context.Context, _ []byte, _ *ceremony.SignCSROptions) (*x509.Certificate, error) {
	return nil, nil
}
func (m *coverageCeremonyCA) CACertificate(context.Context) (*x509.Certificate, error) {
	return nil, nil
}
func (m *coverageCeremonyCA) CABundle() ([]byte, error) {
	return []byte("ca-bundle"), nil
}
func (m *coverageCeremonyCA) IssueCertificate(_ context.Context, _ *ceremony.IssueCertRequest) (*ceremony.IssuedCert, error) {
	return &ceremony.IssuedCert{
		CertPEM: []byte("cert-pem"),
	}, nil
}

// ---------------------------------------------------------------------------
// coverageCredentialService implements service.CredentialServicer for testing.
// ---------------------------------------------------------------------------

type coverageCredentialService struct {
	submitFn          func(ctx context.Context, name string, value []byte) error
	strategyFn        func() string
	deleteFn          func(ctx context.Context, name string) error
	storeFn           func(ctx context.Context, name string, value []byte) error
	retrieveFn        func(ctx context.Context, name string) ([]byte, error)
	listFn            func(ctx context.Context) ([]string, error)
	rotateFn          func(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error)
	retrieveVersionFn func(ctx context.Context, name string, version uint64) ([]byte, error)
	listVersionsFn    func(ctx context.Context, name string) ([]credentials.CredentialVersion, error)
	gcFn              func(ctx context.Context, name string) (int, error)
}

var _ service.CredentialServicer = (*coverageCredentialService)(nil)

func newCoverageCredentialService() *coverageCredentialService {
	return &coverageCredentialService{
		strategyFn: func() string { return "manual" },
	}
}

func (m *coverageCredentialService) CredentialSubmit(ctx context.Context, name string, value []byte) error {
	if m.submitFn != nil {
		return m.submitFn(ctx, name, value)
	}
	return nil
}
func (m *coverageCredentialService) CredentialStrategy(context.Context) (string, error) {
	return m.credentialStrategy(), nil
}

func (m *coverageCredentialService) credentialStrategy() string {
	if m.strategyFn != nil {
		return m.strategyFn()
	}
	return "manual"
}
func (m *coverageCredentialService) CredentialDelete(ctx context.Context, name string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, name)
	}
	return nil
}

func (*coverageCredentialService) CredentialSetRotationPolicy(context.Context, string, credentials.RotationPolicy) error {
	return nil
}

func (*coverageCredentialService) CredentialRotationPolicy(context.Context, string) (*service.CredentialRotationPolicyStatus, error) {
	return &service.CredentialRotationPolicyStatus{}, nil
}

func (*coverageCredentialService) CredentialMigrationConflicts(context.Context) ([]credentials.CredentialMigrationConflictError, error) {
	return nil, nil
}

func (*coverageCredentialService) CredentialResolveMigrationConflict(context.Context, string, credentials.MigrationResolution) error {
	return nil
}
func (m *coverageCredentialService) CredentialStore(ctx context.Context, name string, value []byte) error {
	if m.storeFn != nil {
		return m.storeFn(ctx, name, value)
	}
	return nil
}
func (m *coverageCredentialService) retrieveValue(ctx context.Context, name string) ([]byte, error) {
	if m.retrieveFn != nil {
		return m.retrieveFn(ctx, name)
	}
	return nil, nil
}
func (m *coverageCredentialService) CredentialList(ctx context.Context) ([]string, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return []string{}, nil
}
func (m *coverageCredentialService) CredentialRotate(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
	if m.rotateFn != nil {
		return m.rotateFn(ctx, name, opts)
	}
	return &credentials.RotateResult{Name: name, PreviousVersion: 0, NewVersion: 1}, nil
}
func (m *coverageCredentialService) retrieveVersionValue(ctx context.Context, name string, version uint64) ([]byte, error) {
	if m.retrieveVersionFn != nil {
		return m.retrieveVersionFn(ctx, name, version)
	}
	return nil, nil
}

func (m *coverageCredentialService) CredentialRetrieveBuffer(ctx context.Context, name string) (*secure.Buffer, error) {
	v, err := m.retrieveValue(ctx, name)
	if err != nil {
		return nil, err
	}
	return secure.NewBufferFromBytes(append([]byte(nil), v...))
}

func (m *coverageCredentialService) CredentialRetrieveVersionBuffer(ctx context.Context, name string, version uint64) (*secure.Buffer, error) {
	v, err := m.retrieveVersionValue(ctx, name, version)
	if err != nil {
		return nil, err
	}
	return secure.NewBufferFromBytes(append([]byte(nil), v...))
}
func (m *coverageCredentialService) CredentialListVersions(ctx context.Context, name string) ([]credentials.CredentialVersion, error) {
	if m.listVersionsFn != nil {
		return m.listVersionsFn(ctx, name)
	}
	return nil, nil
}
func (m *coverageCredentialService) CredentialGCExpired(ctx context.Context, name string) (int, error) {
	if m.gcFn != nil {
		return m.gcFn(ctx, name)
	}
	return 0, nil
}
