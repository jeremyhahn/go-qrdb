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

func (m *mockCredentialService) CredentialStrategy() string {
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
