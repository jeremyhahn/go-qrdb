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

// mockCredentialService is shared by the credential tool tests across every
// build-tag combination, so it lives untagged (the ceremony suite above
// carries the full feature-tag constraint and cannot host shared helpers).

package mcp

import (
	"context"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock: CredentialServicer
// ---------------------------------------------------------------------------

type mockCredentialService struct {
	submitFn          func(ctx context.Context, name string, value []byte) error
	strategyFn        func() string
	deleteFn          func(ctx context.Context, name string) error
	storeFn           func(ctx context.Context, name string, value []byte) error
	retrieveFn        func(ctx context.Context, name string) ([]byte, error)
	listFn            func(ctx context.Context) ([]string, error)
	rotateFn          func(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error)
	retrieveVersionFn func(ctx context.Context, name string, version uint64) ([]byte, error)
	listVersionsFn    func(ctx context.Context, name string) ([]credentials.CredentialVersion, error)
	gcExpiredFn       func(ctx context.Context, name string) (int, error)
}

func newMockCredentialService() *mockCredentialService {
	return &mockCredentialService{}
}

func (m *mockCredentialService) CredentialStore(ctx context.Context, name string, value []byte) error {
	if m.storeFn != nil {
		return m.storeFn(ctx, name, value)
	}
	return nil
}

func (m *mockCredentialService) retrieveValue(ctx context.Context, name string) ([]byte, error) {
	if m.retrieveFn != nil {
		return m.retrieveFn(ctx, name)
	}
	return []byte("credential-value"), nil
}

func (m *mockCredentialService) CredentialSubmit(ctx context.Context, name string, value []byte) error {
	if m.submitFn != nil {
		return m.submitFn(ctx, name, value)
	}
	return nil
}

func (m *mockCredentialService) CredentialDelete(ctx context.Context, name string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, name)
	}
	return nil
}

func (*mockCredentialService) CredentialSetRotationPolicy(context.Context, string, credentials.RotationPolicy) error {
	return nil
}

func (*mockCredentialService) CredentialRotationPolicy(context.Context, string) (*service.CredentialRotationPolicyStatus, error) {
	return &service.CredentialRotationPolicyStatus{}, nil
}

func (*mockCredentialService) CredentialMigrationConflicts(context.Context) ([]credentials.CredentialMigrationConflictError, error) {
	return nil, nil
}

func (*mockCredentialService) CredentialResolveMigrationConflict(context.Context, string, credentials.MigrationResolution) error {
	return nil
}

func (m *mockCredentialService) CredentialStrategy(context.Context) (string, error) {
	return m.credentialStrategy(), nil
}

func (m *mockCredentialService) credentialStrategy() string {
	if m.strategyFn != nil {
		return m.strategyFn()
	}
	return "manual"
}

func (m *mockCredentialService) CredentialList(ctx context.Context) ([]string, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return []string{"alpha", "beta"}, nil
}

func (m *mockCredentialService) CredentialRotate(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
	if m.rotateFn != nil {
		return m.rotateFn(ctx, name, opts)
	}
	res := &credentials.RotateResult{
		Name:            name,
		PreviousVersion: 1,
		NewVersion:      2,
	}
	if opts.GracePeriod > 0 {
		res.GraceUntil = time.Unix(1_700_000_000, 0).Add(opts.GracePeriod)
	}
	return res, nil
}

func (m *mockCredentialService) retrieveVersionValue(ctx context.Context, name string, version uint64) ([]byte, error) {
	if m.retrieveVersionFn != nil {
		return m.retrieveVersionFn(ctx, name, version)
	}
	return []byte("version-value"), nil
}

func (m *mockCredentialService) CredentialRetrieveBuffer(ctx context.Context, name string) (*secure.Buffer, error) {
	v, err := m.retrieveValue(ctx, name)
	if err != nil {
		return nil, err
	}
	return secure.NewBufferFromBytes(append([]byte(nil), v...))
}

func (m *mockCredentialService) CredentialRetrieveVersionBuffer(ctx context.Context, name string, version uint64) (*secure.Buffer, error) {
	v, err := m.retrieveVersionValue(ctx, name, version)
	if err != nil {
		return nil, err
	}
	return secure.NewBufferFromBytes(append([]byte(nil), v...))
}

func (m *mockCredentialService) CredentialListVersions(ctx context.Context, name string) ([]credentials.CredentialVersion, error) {
	if m.listVersionsFn != nil {
		return m.listVersionsFn(ctx, name)
	}
	return []credentials.CredentialVersion{
		{Name: name, Version: 2, IsCurrent: true, CreatedAt: time.Unix(1_700_000_100, 0)},
		{Name: name, Version: 1, IsCurrent: false, CreatedAt: time.Unix(1_700_000_000, 0), GraceUntil: time.Unix(1_700_003_600, 0)},
	}, nil
}

func (m *mockCredentialService) CredentialGCExpired(ctx context.Context, name string) (int, error) {
	if m.gcExpiredFn != nil {
		return m.gcExpiredFn(ctx, name)
	}
	return 0, nil
}

// Compile-time assertion that the mock satisfies the full CredentialServicer
// interface, including the versioned-rotation surface.
var _ service.CredentialServicer = (*mockCredentialService)(nil)
