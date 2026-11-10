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

package embedded

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Mock CredentialServicer
// ---------------------------------------------------------------------------

// mockCredentialService implements service.CredentialServicer with
// function-field injection so each test can control behavior precisely.
type mockCredentialService struct {
	storeFn           func(ctx context.Context, name string, value []byte) error
	retrieveFn        func(ctx context.Context, name string) ([]byte, error)
	submitFn          func(ctx context.Context, name string, value []byte) error
	deleteFn          func(ctx context.Context, name string) error
	strategyFn        func() string
	listFn            func(ctx context.Context) ([]string, error)
	rotateFn          func(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error)
	retrieveVersionFn func(ctx context.Context, name string, version uint64) ([]byte, error)
	listVersionsFn    func(ctx context.Context, name string) ([]credentials.CredentialVersion, error)
	gcExpiredFn       func(ctx context.Context, name string) (int, error)
	setPolicyFn       func(ctx context.Context, name string, policy credentials.RotationPolicy) error
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
	return nil, nil
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

func (m *mockCredentialService) CredentialStrategy(context.Context) (string, error) {
	return m.credentialStrategy(), nil
}

func (m *mockCredentialService) credentialStrategy() string {
	if m.strategyFn != nil {
		return m.strategyFn()
	}
	return ""
}

func (m *mockCredentialService) CredentialList(ctx context.Context) ([]string, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return nil, nil
}

func (m *mockCredentialService) CredentialRotate(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
	if m.rotateFn != nil {
		return m.rotateFn(ctx, name, opts)
	}
	return &credentials.RotateResult{Name: name}, nil
}

func (m *mockCredentialService) retrieveVersionValue(ctx context.Context, name string, version uint64) ([]byte, error) {
	if m.retrieveVersionFn != nil {
		return m.retrieveVersionFn(ctx, name, version)
	}
	return nil, nil
}

func (m *mockCredentialService) CredentialListVersions(ctx context.Context, name string) ([]credentials.CredentialVersion, error) {
	if m.listVersionsFn != nil {
		return m.listVersionsFn(ctx, name)
	}
	return nil, nil
}

func (m *mockCredentialService) CredentialGCExpired(ctx context.Context, name string) (int, error) {
	if m.gcExpiredFn != nil {
		return m.gcExpiredFn(ctx, name)
	}
	return 0, nil
}

func (m *mockCredentialService) CredentialSetRotationPolicy(ctx context.Context, name string, policy credentials.RotationPolicy) error {
	if m.setPolicyFn != nil {
		return m.setPolicyFn(ctx, name, policy)
	}
	if name == "missing" {
		return credentials.ErrCredentialNotFound
	}
	return nil
}

func (m *mockCredentialService) CredentialRotationPolicy(_ context.Context, name string) (*service.CredentialRotationPolicyStatus, error) {
	if name == "missing" {
		return nil, credentials.ErrCredentialNotFound
	}
	return &service.CredentialRotationPolicyStatus{Policy: credentials.RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 2}}, nil
}

func (m *mockCredentialService) CredentialMigrationConflicts(_ context.Context) ([]credentials.CredentialMigrationConflictError, error) {
	return []credentials.CredentialMigrationConflictError{{Name: "db", Value: true, Policy: true}}, nil
}

func (m *mockCredentialService) CredentialResolveMigrationConflict(_ context.Context, name string, _ credentials.MigrationResolution) error {
	if name == "missing" {
		return credentials.ErrMigrationConflictNotFound
	}
	return nil
}

// newCredentialTransport creates a transport with the given credential
// service injected via WithCredentialService. Pass nil to omit injection
// (used to verify NilServiceError handling).
func newCredentialTransport(t *testing.T, svc service.CredentialServicer) *Transport {
	t.Helper()
	opts := []Option{}
	if svc != nil {
		opts = append(opts, WithCredentialService(svc))
	}
	tr, err := New(newMockKVService(), &mockAdminService{}, opts...)
	if err != nil {
		t.Fatalf("failed to create transport: %v", err)
	}
	return tr
}

// closedCredentialTransport returns a transport that has already been closed.
func closedCredentialTransport(t *testing.T) *Transport {
	t.Helper()
	tr := newCredentialTransport(t, &mockCredentialService{})
	if err := tr.Close(); err != nil {
		t.Fatalf("failed to close transport: %v", err)
	}
	return tr
}

// errCredential is a sentinel error returned by mock credential methods.
var errCredential = errors.New("credential: test error")

// ---------------------------------------------------------------------------
// CredentialList
// ---------------------------------------------------------------------------

func TestEmbeddedCredential_List(t *testing.T) {
	want := []string{"db-password", "smtp-password", "tls-key"}
	svc := &mockCredentialService{
		listFn: func(_ context.Context) ([]string, error) {
			return want, nil
		},
	}
	tr := newCredentialTransport(t, svc)

	resp, err := tr.CredentialList(context.Background(), &transport.CredentialListRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.Names) != len(want) {
		t.Fatalf("expected %d names, got %d", len(want), len(resp.Names))
	}
	for i, n := range want {
		if resp.Names[i] != n {
			t.Errorf("Names[%d] = %q, want %q", i, resp.Names[i], n)
		}
	}
}

func TestEmbeddedCredential_List_NilService(t *testing.T) {
	tr := newCredentialTransport(t, nil)

	_, err := tr.CredentialList(context.Background(), &transport.CredentialListRequest{})
	if err == nil {
		t.Fatal("expected NilServiceError")
	}
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
	if nilErr.Service != "CredentialServicer" {
		t.Errorf("expected service 'CredentialServicer', got %q", nilErr.Service)
	}
}

func TestEmbeddedCredential_List_Closed(t *testing.T) {
	tr := closedCredentialTransport(t)

	_, err := tr.CredentialList(context.Background(), &transport.CredentialListRequest{})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_List_ServiceError(t *testing.T) {
	svc := &mockCredentialService{
		listFn: func(_ context.Context) ([]string, error) {
			return nil, errCredential
		},
	}
	tr := newCredentialTransport(t, svc)

	_, err := tr.CredentialList(context.Background(), &transport.CredentialListRequest{})
	if !errors.Is(err, errCredential) {
		t.Errorf("expected errCredential, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// CredentialRotate
// ---------------------------------------------------------------------------

func TestEmbeddedCredential_Rotate(t *testing.T) {
	graceUntil := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	var capturedName string
	var capturedOpts credentials.RotateOptions
	svc := &mockCredentialService{
		rotateFn: func(_ context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
			capturedName = name
			capturedOpts = opts
			return &credentials.RotateResult{
				Name:            name,
				PreviousVersion: 3,
				NewVersion:      4,
				GraceUntil:      graceUntil,
			}, nil
		},
	}
	tr := newCredentialTransport(t, svc)

	resp, err := tr.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{
		Name:               "db-password",
		NewValue:           []byte("new-secret"),
		GracePeriodSeconds: 7200,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedName != "db-password" {
		t.Errorf("captured name = %q, want %q", capturedName, "db-password")
	}
	if string(capturedOpts.NewValue) != "new-secret" {
		t.Errorf("captured NewValue = %q, want %q", string(capturedOpts.NewValue), "new-secret")
	}
	if capturedOpts.GracePeriod != 2*time.Hour {
		t.Errorf("captured GracePeriod = %v, want %v", capturedOpts.GracePeriod, 2*time.Hour)
	}
	if resp.Name != "db-password" {
		t.Errorf("Name = %q, want %q", resp.Name, "db-password")
	}
	if resp.PreviousVersion != 3 {
		t.Errorf("PreviousVersion = %d, want 3", resp.PreviousVersion)
	}
	if resp.NewVersion != 4 {
		t.Errorf("NewVersion = %d, want 4", resp.NewVersion)
	}
	if resp.GraceUntilUnix != graceUntil.Unix() {
		t.Errorf("GraceUntilUnix = %d, want %d", resp.GraceUntilUnix, graceUntil.Unix())
	}
}

func TestEmbeddedCredential_Rotate_NoGrace(t *testing.T) {
	svc := &mockCredentialService{
		rotateFn: func(_ context.Context, name string, _ credentials.RotateOptions) (*credentials.RotateResult, error) {
			return &credentials.RotateResult{
				Name:            name,
				PreviousVersion: 1,
				NewVersion:      2,
				// GraceUntil is the zero value
			}, nil
		},
	}
	tr := newCredentialTransport(t, svc)

	resp, err := tr.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{
		Name:     "api-token",
		NewValue: []byte("v2"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GraceUntilUnix != 0 {
		t.Errorf("GraceUntilUnix = %d, want 0 for zero-time result", resp.GraceUntilUnix)
	}
}

func TestEmbeddedCredential_Rotate_NilService(t *testing.T) {
	tr := newCredentialTransport(t, nil)

	_, err := tr.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{Name: "x"})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_Rotate_NilRequest(t *testing.T) {
	tr := newCredentialTransport(t, &mockCredentialService{})

	_, err := tr.CredentialRotate(context.Background(), nil)
	var nilReqErr *NilRequestError
	if !errors.As(err, &nilReqErr) {
		t.Fatalf("expected NilRequestError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_Rotate_Closed(t *testing.T) {
	tr := closedCredentialTransport(t)

	_, err := tr.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{Name: "x"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_Rotate_ServiceError(t *testing.T) {
	svc := &mockCredentialService{
		rotateFn: func(_ context.Context, _ string, _ credentials.RotateOptions) (*credentials.RotateResult, error) {
			return nil, errCredential
		},
	}
	tr := newCredentialTransport(t, svc)

	_, err := tr.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{Name: "x", NewValue: []byte("y")})
	if !errors.Is(err, errCredential) {
		t.Errorf("expected errCredential, got %v", err)
	}
}

func TestEmbeddedCredential_SetRotationPolicy_Success(t *testing.T) {
	var gotName string
	var gotPolicy credentials.RotationPolicy
	svc := &mockCredentialService{
		setPolicyFn: func(_ context.Context, name string, policy credentials.RotationPolicy) error {
			gotName, gotPolicy = name, policy
			return nil
		},
	}
	tr := newCredentialTransport(t, svc)

	if err := tr.CredentialSetRotationPolicy(context.Background(), &transport.CredentialSetRotationPolicyRequest{
		Name: "db", Enabled: true, Interval: "24h", RetainVersions: 3,
	}); err != nil {
		t.Fatalf("CredentialSetRotationPolicy: %v", err)
	}
	want := credentials.RotationPolicy{Enabled: true, Interval: 24 * time.Hour, RetainVersions: 3}
	if gotName != "db" || gotPolicy != want {
		t.Errorf("service saw %q %+v, want db %+v", gotName, gotPolicy, want)
	}
}

func TestEmbeddedCredential_SetRotationPolicy_InvalidPolicy(t *testing.T) {
	svc := &mockCredentialService{
		setPolicyFn: func(context.Context, string, credentials.RotationPolicy) error {
			return &credentials.InvalidRotationPolicyError{Reason: "interval must be positive"}
		},
	}
	tr := newCredentialTransport(t, svc)

	var invalid *credentials.InvalidRotationPolicyError
	err := tr.CredentialSetRotationPolicy(context.Background(), &transport.CredentialSetRotationPolicyRequest{
		Name: "db", Enabled: true, Interval: "0s", RetainVersions: 1,
	})
	if !errors.As(err, &invalid) {
		t.Fatalf("service refusal: expected InvalidRotationPolicyError, got %T: %v", err, err)
	}
	// An interval that does not parse is refused before the service sees it.
	err = tr.CredentialSetRotationPolicy(context.Background(), &transport.CredentialSetRotationPolicyRequest{
		Name: "db", Enabled: true, Interval: "weekly", RetainVersions: 1,
	})
	if !errors.As(err, &invalid) {
		t.Fatalf("unparsable interval: expected InvalidRotationPolicyError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_SetRotationPolicy_NilServiceAndRequest(t *testing.T) {
	var nilSvc *NilServiceError
	if err := newCredentialTransport(t, nil).CredentialSetRotationPolicy(context.Background(), &transport.CredentialSetRotationPolicyRequest{Name: "x"}); !errors.As(err, &nilSvc) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
	var nilReq *NilRequestError
	if err := newCredentialTransport(t, &mockCredentialService{}).CredentialSetRotationPolicy(context.Background(), nil); !errors.As(err, &nilReq) {
		t.Fatalf("expected NilRequestError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_RotationPolicy_Success(t *testing.T) {
	tr := newCredentialTransport(t, &mockCredentialService{})

	resp, err := tr.CredentialRotationPolicy(context.Background(), "db")
	if err != nil {
		t.Fatalf("CredentialRotationPolicy: %v", err)
	}
	if resp.Name != "db" || !resp.Enabled || resp.Interval != time.Hour.String() || resp.RetainVersions != 2 {
		t.Errorf("unexpected resp: %+v", resp)
	}
}

func TestEmbeddedCredential_RotationPolicy_NotFoundAndClosed(t *testing.T) {
	tr := newCredentialTransport(t, &mockCredentialService{})
	if _, err := tr.CredentialRotationPolicy(context.Background(), "missing"); !errors.Is(err, credentials.ErrCredentialNotFound) {
		t.Fatalf("expected ErrCredentialNotFound, got %v", err)
	}
	var closed *transport.ClosedError
	if _, err := closedCredentialTransport(t).CredentialRotationPolicy(context.Background(), "db"); !errors.As(err, &closed) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_MigrationConflicts_Success(t *testing.T) {
	tr := newCredentialTransport(t, &mockCredentialService{})

	resp, err := tr.CredentialMigrationConflicts(context.Background())
	if err != nil {
		t.Fatalf("CredentialMigrationConflicts: %v", err)
	}
	want := transport.CredentialMigrationConflict{Name: "db", Value: true, Policy: true}
	if len(resp.Conflicts) != 1 || resp.Conflicts[0] != want {
		t.Errorf("unexpected resp: %+v", resp)
	}

	var out *transport.CredentialMigrationConflictsResponse
	if err := handleCredentialMigrationConflicts(context.Background(), tr, nil, &out); err != nil {
		t.Fatalf("handleCredentialMigrationConflicts: %v", err)
	}
	if out == nil || len(out.Conflicts) != 1 {
		t.Errorf("handler response: %+v", out)
	}
}

func TestEmbeddedCredential_MigrationConflicts_NilServiceAndClosed(t *testing.T) {
	var nilSvc *NilServiceError
	if _, err := newCredentialTransport(t, nil).CredentialMigrationConflicts(context.Background()); !errors.As(err, &nilSvc) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
	var closed *transport.ClosedError
	if _, err := closedCredentialTransport(t).CredentialMigrationConflicts(context.Background()); !errors.As(err, &closed) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
	if err := handleCredentialMigrationConflicts(context.Background(), closedCredentialTransport(t), nil, nil); !errors.As(err, &closed) {
		t.Fatalf("handler: expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_ResolveMigrationConflict_SuccessAndNotFound(t *testing.T) {
	tr := newCredentialTransport(t, &mockCredentialService{})

	if err := tr.CredentialResolveMigrationConflict(context.Background(),
		&transport.CredentialResolveMigrationConflictRequest{Name: "db", Resolution: "import"}); err != nil {
		t.Fatalf("CredentialResolveMigrationConflict: %v", err)
	}
	if err := handleCredentialResolveMigrationConflict(context.Background(), tr,
		&transport.CredentialResolveMigrationConflictRequest{Name: "missing", Resolution: "discard"}, nil); !errors.Is(err, credentials.ErrMigrationConflictNotFound) {
		t.Fatalf("expected ErrMigrationConflictNotFound, got %v", err)
	}
}

func TestEmbeddedCredential_ResolveMigrationConflict_NilServiceRequestAndClosed(t *testing.T) {
	req := &transport.CredentialResolveMigrationConflictRequest{Name: "db", Resolution: "import"}
	var nilSvc *NilServiceError
	if err := newCredentialTransport(t, nil).CredentialResolveMigrationConflict(context.Background(), req); !errors.As(err, &nilSvc) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
	tr := newCredentialTransport(t, &mockCredentialService{})
	var invalid *InvalidRequestError
	if err := tr.CredentialResolveMigrationConflict(context.Background(), nil); !errors.As(err, &invalid) {
		t.Fatalf("expected InvalidRequestError, got %T: %v", err, err)
	}
	if err := handleCredentialResolveMigrationConflict(context.Background(), tr, "not a request", nil); !errors.As(err, &invalid) {
		t.Fatalf("handler: expected InvalidRequestError, got %T: %v", err, err)
	}
	var closed *transport.ClosedError
	if err := closedCredentialTransport(t).CredentialResolveMigrationConflict(context.Background(), req); !errors.As(err, &closed) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// CredentialRetrieveVersion
// ---------------------------------------------------------------------------

func TestEmbeddedCredential_RetrieveVersion(t *testing.T) {
	var capturedName string
	var capturedVersion uint64
	svc := &mockCredentialService{
		retrieveVersionFn: func(_ context.Context, name string, version uint64) ([]byte, error) {
			capturedName = name
			capturedVersion = version
			return []byte("secret-v2"), nil
		},
	}
	tr := newCredentialTransport(t, svc)

	resp, err := tr.retrieveVersionResponse(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name:    "db-password",
		Version: 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedName != "db-password" {
		t.Errorf("captured name = %q, want %q", capturedName, "db-password")
	}
	if capturedVersion != 2 {
		t.Errorf("captured version = %d, want 2", capturedVersion)
	}
	if string(resp.Value) != "secret-v2" {
		t.Errorf("Value = %q, want %q", resp.Value, "secret-v2")
	}
}

func TestEmbeddedCredential_RetrieveVersion_Current(t *testing.T) {
	svc := &mockCredentialService{
		retrieveVersionFn: func(_ context.Context, _ string, version uint64) ([]byte, error) {
			if version != 0 {
				t.Errorf("expected version 0 for current, got %d", version)
			}
			return []byte("current-secret"), nil
		},
	}
	tr := newCredentialTransport(t, svc)

	resp, err := tr.retrieveVersionResponse(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name: "db-password",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.Value) != "current-secret" {
		t.Errorf("Value = %q, want %q", resp.Value, "current-secret")
	}
}

func TestEmbeddedCredential_RetrieveVersion_NilService(t *testing.T) {
	tr := newCredentialTransport(t, nil)

	_, err := tr.retrieveVersionResponse(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "x"})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_RetrieveVersion_NilRequest(t *testing.T) {
	tr := newCredentialTransport(t, &mockCredentialService{})

	_, err := tr.retrieveVersionResponse(context.Background(), nil)
	var nilReqErr *NilRequestError
	if !errors.As(err, &nilReqErr) {
		t.Fatalf("expected NilRequestError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_RetrieveVersion_Closed(t *testing.T) {
	tr := closedCredentialTransport(t)

	_, err := tr.retrieveVersionResponse(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "x"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_RetrieveVersion_ServiceError(t *testing.T) {
	svc := &mockCredentialService{
		retrieveVersionFn: func(_ context.Context, _ string, _ uint64) ([]byte, error) {
			return nil, errCredential
		},
	}
	tr := newCredentialTransport(t, svc)

	_, err := tr.retrieveVersionResponse(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "x", Version: 1})
	if !errors.Is(err, errCredential) {
		t.Errorf("expected errCredential, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// CredentialListVersions
// ---------------------------------------------------------------------------

func TestEmbeddedCredential_ListVersions(t *testing.T) {
	created := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)
	grace := time.Now().UTC().Add(1 * time.Hour).Truncate(time.Second)
	svc := &mockCredentialService{
		listVersionsFn: func(_ context.Context, name string) ([]credentials.CredentialVersion, error) {
			return []credentials.CredentialVersion{
				{Name: name, Version: 2, IsCurrent: true},
				{Name: name, Version: 1, CreatedAt: created, GraceUntil: grace},
			}, nil
		},
	}
	tr := newCredentialTransport(t, svc)

	resp, err := tr.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{Name: "db-password"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Name != "db-password" {
		t.Errorf("Name = %q, want %q", resp.Name, "db-password")
	}
	if len(resp.Versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(resp.Versions))
	}
	if resp.Versions[0].Version != 2 || !resp.Versions[0].IsCurrent {
		t.Errorf("Versions[0] = %+v, want v2 current", resp.Versions[0])
	}
	if resp.Versions[0].CreatedAtUnix != 0 {
		t.Errorf("Versions[0].CreatedAtUnix = %d, want 0 (zero time)", resp.Versions[0].CreatedAtUnix)
	}
	if resp.Versions[0].GraceUntilUnix != 0 {
		t.Errorf("Versions[0].GraceUntilUnix = %d, want 0 (zero time)", resp.Versions[0].GraceUntilUnix)
	}
	if resp.Versions[1].Version != 1 || resp.Versions[1].IsCurrent {
		t.Errorf("Versions[1] = %+v, want v1 non-current", resp.Versions[1])
	}
	if resp.Versions[1].CreatedAtUnix != created.Unix() {
		t.Errorf("Versions[1].CreatedAtUnix = %d, want %d", resp.Versions[1].CreatedAtUnix, created.Unix())
	}
	if resp.Versions[1].GraceUntilUnix != grace.Unix() {
		t.Errorf("Versions[1].GraceUntilUnix = %d, want %d", resp.Versions[1].GraceUntilUnix, grace.Unix())
	}
}

func TestEmbeddedCredential_ListVersions_Empty(t *testing.T) {
	svc := &mockCredentialService{
		listVersionsFn: func(_ context.Context, _ string) ([]credentials.CredentialVersion, error) {
			return []credentials.CredentialVersion{}, nil
		},
	}
	tr := newCredentialTransport(t, svc)

	resp, err := tr.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{Name: "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Versions) != 0 {
		t.Errorf("expected 0 versions, got %d", len(resp.Versions))
	}
}

func TestEmbeddedCredential_ListVersions_NilService(t *testing.T) {
	tr := newCredentialTransport(t, nil)

	_, err := tr.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{Name: "x"})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_ListVersions_NilRequest(t *testing.T) {
	tr := newCredentialTransport(t, &mockCredentialService{})

	_, err := tr.CredentialListVersions(context.Background(), nil)
	var nilReqErr *NilRequestError
	if !errors.As(err, &nilReqErr) {
		t.Fatalf("expected NilRequestError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_ListVersions_Closed(t *testing.T) {
	tr := closedCredentialTransport(t)

	_, err := tr.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{Name: "x"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_ListVersions_ServiceError(t *testing.T) {
	svc := &mockCredentialService{
		listVersionsFn: func(_ context.Context, _ string) ([]credentials.CredentialVersion, error) {
			return nil, errCredential
		},
	}
	tr := newCredentialTransport(t, svc)

	_, err := tr.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{Name: "x"})
	if !errors.Is(err, errCredential) {
		t.Errorf("expected errCredential, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// CredentialGCExpired
// ---------------------------------------------------------------------------

func TestEmbeddedCredential_GCExpired(t *testing.T) {
	var capturedName string
	svc := &mockCredentialService{
		gcExpiredFn: func(_ context.Context, name string) (int, error) {
			capturedName = name
			return 4, nil
		},
	}
	tr := newCredentialTransport(t, svc)

	resp, err := tr.CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{Name: "db-password"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedName != "db-password" {
		t.Errorf("captured name = %q, want %q", capturedName, "db-password")
	}
	if resp.Reaped != 4 {
		t.Errorf("Reaped = %d, want 4", resp.Reaped)
	}
}

func TestEmbeddedCredential_GCExpired_NilService(t *testing.T) {
	tr := newCredentialTransport(t, nil)

	_, err := tr.CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{Name: "x"})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_GCExpired_NilRequest(t *testing.T) {
	tr := newCredentialTransport(t, &mockCredentialService{})

	_, err := tr.CredentialGCExpired(context.Background(), nil)
	var nilReqErr *NilRequestError
	if !errors.As(err, &nilReqErr) {
		t.Fatalf("expected NilRequestError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_GCExpired_Closed(t *testing.T) {
	tr := closedCredentialTransport(t)

	_, err := tr.CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{Name: "x"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_GCExpired_ServiceError(t *testing.T) {
	svc := &mockCredentialService{
		gcExpiredFn: func(_ context.Context, _ string) (int, error) {
			return 0, errCredential
		},
	}
	tr := newCredentialTransport(t, svc)

	_, err := tr.CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{Name: "x"})
	if !errors.Is(err, errCredential) {
		t.Errorf("expected errCredential, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Dispatch handler coverage via Request()
// ---------------------------------------------------------------------------

func TestEmbeddedCredential_Dispatch_List(t *testing.T) {
	svc := &mockCredentialService{
		listFn: func(_ context.Context) ([]string, error) {
			return []string{"a", "b"}, nil
		},
	}
	tr := newCredentialTransport(t, svc)

	var resp *transport.CredentialListResponse
	if err := tr.Request(context.Background(), "CredentialList", &transport.CredentialListRequest{}, &resp); err != nil {
		t.Fatalf("dispatch error: %v", err)
	}
	if resp == nil || len(resp.Names) != 2 {
		t.Fatalf("dispatch returned unexpected result: %+v", resp)
	}
}

func TestEmbeddedCredential_Dispatch_Rotate(t *testing.T) {
	svc := &mockCredentialService{
		rotateFn: func(_ context.Context, name string, _ credentials.RotateOptions) (*credentials.RotateResult, error) {
			return &credentials.RotateResult{Name: name, PreviousVersion: 1, NewVersion: 2}, nil
		},
	}
	tr := newCredentialTransport(t, svc)

	var resp *transport.CredentialRotateResponse
	err := tr.Request(context.Background(), "CredentialRotate", &transport.CredentialRotateRequest{
		Name:     "k",
		NewValue: []byte("v"),
	}, &resp)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}
	if resp == nil || resp.NewVersion != 2 {
		t.Fatalf("dispatch returned unexpected result: %+v", resp)
	}
}

func TestEmbeddedCredential_Dispatch_RetrieveVersion(t *testing.T) {
	svc := &mockCredentialService{
		retrieveVersionFn: func(_ context.Context, _ string, _ uint64) ([]byte, error) {
			return []byte("payload"), nil
		},
	}
	tr := newCredentialTransport(t, svc)

	buf, err := tr.CredentialRetrieveVersionBuffer(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name:    "k",
		Version: 1,
	})
	if err != nil {
		t.Fatalf("retrieve version buffer error: %v", err)
	}
	defer buf.Destroy()
	got, err := secure.CopyOut(buf)
	if err != nil {
		t.Fatalf("copy out: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("retrieve returned %q, want payload", got)
	}
}

func TestEmbeddedCredential_Dispatch_ListVersions(t *testing.T) {
	svc := &mockCredentialService{
		listVersionsFn: func(_ context.Context, name string) ([]credentials.CredentialVersion, error) {
			return []credentials.CredentialVersion{{Name: name, Version: 1, IsCurrent: true}}, nil
		},
	}
	tr := newCredentialTransport(t, svc)

	var resp *transport.CredentialListVersionsResponse
	err := tr.Request(context.Background(), "CredentialListVersions", &transport.CredentialListVersionsRequest{Name: "k"}, &resp)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}
	if resp == nil || len(resp.Versions) != 1 || !resp.Versions[0].IsCurrent {
		t.Fatalf("dispatch returned unexpected result: %+v", resp)
	}
}

func TestEmbeddedCredential_Dispatch_GCExpired(t *testing.T) {
	svc := &mockCredentialService{
		gcExpiredFn: func(_ context.Context, _ string) (int, error) {
			return 7, nil
		},
	}
	tr := newCredentialTransport(t, svc)

	var resp *transport.CredentialGCResponse
	err := tr.Request(context.Background(), "CredentialGCExpired", &transport.CredentialGCRequest{Name: "k"}, &resp)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}
	if resp == nil || resp.Reaped != 7 {
		t.Fatalf("dispatch returned unexpected result: %+v", resp)
	}
}

func TestEmbeddedCredential_Dispatch_InvalidRequest(t *testing.T) {
	tr := newCredentialTransport(t, &mockCredentialService{})

	cases := []struct {
		method string
	}{
		{"CredentialList"},
		{"CredentialRotate"},
		{"CredentialListVersions"},
		{"CredentialGCExpired"},
	}

	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			err := tr.Request(context.Background(), tc.method, "not the right type", nil)
			var invalidErr *InvalidRequestError
			if !errors.As(err, &invalidErr) {
				t.Fatalf("expected InvalidRequestError, got %T: %v", err, err)
			}
			if invalidErr.Method != tc.method {
				t.Errorf("Method = %q, want %q", invalidErr.Method, tc.method)
			}
		})
	}
}

func TestEmbeddedCredential_StoreRetrieve_NilServiceAndClosed(t *testing.T) {
	ctx := context.Background()
	calls := map[string]func(*Transport) error{
		"CredentialStore": func(tr *Transport) error { return tr.CredentialStore(ctx, "k", []byte("v")) },
		"retrieveBytes": func(tr *Transport) error {
			_, err := tr.retrieveBytes(ctx, "k")
			return err
		},
	}
	for name, call := range calls {
		var ne *NilServiceError
		if err := call(newExtTransport(t)); !errors.As(err, &ne) {
			t.Errorf("%s nil service: expected NilServiceError, got %T: %v", name, err, err)
		}
		var ce *transport.ClosedError
		if err := call(closedExtTransport(t)); !errors.As(err, &ce) {
			t.Errorf("%s closed: expected ClosedError, got %T: %v", name, err, err)
		}
	}
}
