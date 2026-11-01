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

package unix

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Mock CredentialServicer
// ---------------------------------------------------------------------------

// mockCredentialSvc is an inspectable service.CredentialServicer that lets
// tests assert on credential RPC invocations and inject domain errors.
// Each method honours an override func, falling back to a benign default.
type mockCredentialSvc struct {
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

func (m *mockCredentialSvc) CredentialStore(ctx context.Context, name string, value []byte) error {
	if m.storeFn != nil {
		return m.storeFn(ctx, name, value)
	}
	return nil
}

func (m *mockCredentialSvc) retrieveValue(ctx context.Context, name string) ([]byte, error) {
	if m.retrieveFn != nil {
		return m.retrieveFn(ctx, name)
	}
	return nil, nil
}

func (m *mockCredentialSvc) CredentialRetrieveBuffer(ctx context.Context, name string) (*secure.Buffer, error) {
	v, err := m.retrieveValue(ctx, name)
	if err != nil {
		return nil, err
	}
	return secure.NewBufferFromBytes(append([]byte(nil), v...))
}

func (m *mockCredentialSvc) CredentialRetrieveVersionBuffer(ctx context.Context, name string, version uint64) (*secure.Buffer, error) {
	v, err := m.retrieveVersionValue(ctx, name, version)
	if err != nil {
		return nil, err
	}
	return secure.NewBufferFromBytes(append([]byte(nil), v...))
}

func (m *mockCredentialSvc) CredentialSubmit(ctx context.Context, name string, value []byte) error {
	if m.submitFn != nil {
		return m.submitFn(ctx, name, value)
	}
	return nil
}

func (m *mockCredentialSvc) CredentialDelete(ctx context.Context, name string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, name)
	}
	return nil
}

func (m *mockCredentialSvc) CredentialStrategy(context.Context) (string, error) {
	return m.credentialStrategy(), nil
}

func (m *mockCredentialSvc) credentialStrategy() string {
	if m.strategyFn != nil {
		return m.strategyFn()
	}
	return "manual"
}

func (m *mockCredentialSvc) CredentialList(ctx context.Context) ([]string, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return []string{}, nil
}

func (m *mockCredentialSvc) CredentialRotate(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
	if m.rotateFn != nil {
		return m.rotateFn(ctx, name, opts)
	}
	return &credentials.RotateResult{Name: name, NewVersion: 1}, nil
}

func (m *mockCredentialSvc) retrieveVersionValue(ctx context.Context, name string, version uint64) ([]byte, error) {
	if m.retrieveVersionFn != nil {
		return m.retrieveVersionFn(ctx, name, version)
	}
	return nil, nil
}

func (m *mockCredentialSvc) CredentialListVersions(ctx context.Context, name string) ([]credentials.CredentialVersion, error) {
	if m.listVersionsFn != nil {
		return m.listVersionsFn(ctx, name)
	}
	return nil, nil
}

func (m *mockCredentialSvc) CredentialGCExpired(ctx context.Context, name string) (int, error) {
	if m.gcExpiredFn != nil {
		return m.gcExpiredFn(ctx, name)
	}
	return 0, nil
}

func (m *mockCredentialSvc) CredentialSetRotationPolicy(ctx context.Context, name string, policy credentials.RotationPolicy) error {
	if m.setPolicyFn != nil {
		return m.setPolicyFn(ctx, name, policy)
	}
	if name == "missing" {
		return credentials.ErrCredentialNotFound
	}
	return nil
}

func (m *mockCredentialSvc) CredentialRotationPolicy(_ context.Context, name string) (*service.CredentialRotationPolicyStatus, error) {
	if name == "missing" {
		return nil, credentials.ErrCredentialNotFound
	}
	return &service.CredentialRotationPolicyStatus{Policy: credentials.RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 2}}, nil
}

func (m *mockCredentialSvc) CredentialMigrationConflicts(_ context.Context) ([]credentials.CredentialMigrationConflictError, error) {
	return []credentials.CredentialMigrationConflictError{{Name: "db", Value: true, Policy: true}}, nil
}

func (m *mockCredentialSvc) CredentialResolveMigrationConflict(_ context.Context, name string, _ credentials.MigrationResolution) error {
	if name == "missing" {
		return credentials.ErrMigrationConflictNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Test server bootstrap with the credential service registered
// ---------------------------------------------------------------------------

// startUnixTestServerWithCredentials starts a Unix gRPC server with both the
// KV/admin services and the credential service registered, returning the
// socket path and a cleanup function.
func startUnixTestServerWithCredentials(t *testing.T, credSvc *mockCredentialSvc) (string, func()) {
	t.Helper()
	socketPath := tempSocketPath(t)
	srv := unixserver.NewServer(newMockKVService(), newMockAdminService(), socketPath,
		unixserver.WithCredentialService(credSvc),
		unixserver.WithTLS(testUnixServerTLS),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("failed to start Unix server: %v", err)
	}
	return socketPath, func() { srv.Stop() }
}

// connectTransportWithCredentials dials a fresh transport against a server
// with the credential service registered.
func connectTransportWithCredentials(t *testing.T, credSvc *mockCredentialSvc) (*Transport, func()) {
	t.Helper()
	socketPath, stopSrv := startUnixTestServerWithCredentials(t, credSvc)

	tr, err := New(transport.WithAddress(socketPath), testClientTLSOption())
	if err != nil {
		stopSrv()
		t.Fatalf("New: %v", err)
	}
	if connErr := tr.Connect(context.Background()); connErr != nil {
		stopSrv()
		t.Fatalf("Connect: %v", connErr)
	}

	cleanup := func() {
		_ = tr.Close()
		stopSrv()
	}
	return tr, cleanup
}

// ---------------------------------------------------------------------------
// CredentialSubmit
// ---------------------------------------------------------------------------

func TestTransport_CredentialSubmit_RoundTrip(t *testing.T) {
	var gotName string
	var gotValue []byte
	svc := &mockCredentialSvc{
		submitFn: func(_ context.Context, name string, value []byte) error {
			gotName = name
			gotValue = append([]byte(nil), value...)
			return nil
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	if err := tr.CredentialSubmit(context.Background(), &transport.CredentialSubmitRequest{
		Name: "api-token", Value: []byte("letmein"),
	}); err != nil {
		t.Fatalf("CredentialSubmit: %v", err)
	}
	if gotName != "api-token" || string(gotValue) != "letmein" {
		t.Errorf("server got name=%q value=%q", gotName, gotValue)
	}
}

func TestTransport_CredentialSubmit_NilRequest(t *testing.T) {
	tr, cleanup := connectTransportWithCredentials(t, &mockCredentialSvc{})
	defer cleanup()
	if err := tr.CredentialSubmit(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestTransport_CredentialSubmit_ServerError(t *testing.T) {
	svc := &mockCredentialSvc{
		submitFn: func(_ context.Context, _ string, _ []byte) error {
			return credentials.ErrCredentialAlreadySubmitted
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()
	if err := tr.CredentialSubmit(context.Background(), &transport.CredentialSubmitRequest{Name: "x", Value: []byte("v")}); err == nil {
		t.Fatal("expected error from server")
	}
}

// ---------------------------------------------------------------------------
// CredentialStrategy
// ---------------------------------------------------------------------------

func TestTransport_CredentialStrategy_RoundTrip(t *testing.T) {
	svc := &mockCredentialSvc{strategyFn: func() string { return "barrier" }}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	resp, err := tr.CredentialStrategy(context.Background())
	if err != nil {
		t.Fatalf("CredentialStrategy: %v", err)
	}
	if resp.Strategy != "barrier" {
		t.Errorf("got strategy %q, want barrier", resp.Strategy)
	}
}

// ---------------------------------------------------------------------------
// CredentialDelete
// ---------------------------------------------------------------------------

func TestTransport_CredentialDelete_RoundTrip(t *testing.T) {
	var gotName string
	svc := &mockCredentialSvc{
		deleteFn: func(_ context.Context, name string) error { gotName = name; return nil },
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	if err := tr.CredentialDelete(context.Background(), "stale"); err != nil {
		t.Fatalf("CredentialDelete: %v", err)
	}
	if gotName != "stale" {
		t.Errorf("server got name %q, want stale", gotName)
	}
}

func TestTransport_CredentialDelete_EmptyName(t *testing.T) {
	tr, cleanup := connectTransportWithCredentials(t, &mockCredentialSvc{})
	defer cleanup()
	if err := tr.CredentialDelete(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestTransport_CredentialDelete_ServerError(t *testing.T) {
	svc := &mockCredentialSvc{
		deleteFn: func(_ context.Context, _ string) error { return credentials.ErrCredentialNotFound },
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	if err := tr.CredentialDelete(context.Background(), "missing"); err == nil {
		t.Fatal("expected error from server")
	}
}

// ---------------------------------------------------------------------------
// CredentialList
// ---------------------------------------------------------------------------

func TestTransport_CredentialList_RoundTrip(t *testing.T) {
	svc := &mockCredentialSvc{
		listFn: func(_ context.Context) ([]string, error) {
			return []string{"a", "b", "c"}, nil
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	resp, err := tr.CredentialList(context.Background(), &transport.CredentialListRequest{})
	if err != nil {
		t.Fatalf("CredentialList: %v", err)
	}
	if len(resp.Names) != 3 || resp.Names[0] != "a" || resp.Names[1] != "b" || resp.Names[2] != "c" {
		t.Errorf("got names %+v, want [a b c]", resp.Names)
	}
}

func TestTransport_CredentialList_Empty(t *testing.T) {
	svc := &mockCredentialSvc{
		listFn: func(_ context.Context) ([]string, error) { return nil, nil },
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	resp, err := tr.CredentialList(context.Background(), &transport.CredentialListRequest{})
	if err != nil {
		t.Fatalf("CredentialList: %v", err)
	}
	if resp.Names == nil {
		t.Error("expected non-nil empty slice")
	}
	if len(resp.Names) != 0 {
		t.Errorf("expected zero names, got %+v", resp.Names)
	}
}

func TestTransport_CredentialList_NilRequest(t *testing.T) {
	tr, cleanup := connectTransportWithCredentials(t, &mockCredentialSvc{})
	defer cleanup()
	if _, err := tr.CredentialList(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestTransport_CredentialList_EnumerationNotSupported(t *testing.T) {
	svc := &mockCredentialSvc{
		listFn: func(_ context.Context) ([]string, error) {
			return nil, credentials.ErrEnumerationNotSupported
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()
	if _, err := tr.CredentialList(context.Background(), &transport.CredentialListRequest{}); err == nil {
		t.Fatal("expected error from server")
	}
}

// ---------------------------------------------------------------------------
// CredentialRotate
// ---------------------------------------------------------------------------

func TestTransport_CredentialRotate_RoundTrip(t *testing.T) {
	graceUntil := time.Unix(1_700_000_000, 0).UTC()
	var gotOpts credentials.RotateOptions
	svc := &mockCredentialSvc{
		rotateFn: func(_ context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
			gotOpts = opts
			return &credentials.RotateResult{
				Name:            name,
				PreviousVersion: 4,
				NewVersion:      5,
				GraceUntil:      graceUntil,
			}, nil
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	resp, err := tr.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{
		Name:               "db-password",
		NewValue:           []byte("v2-secret"),
		GracePeriodSeconds: 120,
	})
	if err != nil {
		t.Fatalf("CredentialRotate: %v", err)
	}
	if resp.Name != "db-password" || resp.PreviousVersion != 4 || resp.NewVersion != 5 {
		t.Errorf("unexpected resp: %+v", resp)
	}
	if resp.GraceUntilUnix != graceUntil.Unix() {
		t.Errorf("got grace %d, want %d", resp.GraceUntilUnix, graceUntil.Unix())
	}
	if string(gotOpts.NewValue) != "v2-secret" {
		t.Errorf("server got new value %q, want v2-secret", gotOpts.NewValue)
	}
	if gotOpts.GracePeriod != 120*time.Second {
		t.Errorf("server got grace %s, want 120s", gotOpts.GracePeriod)
	}
}

func TestTransport_CredentialRotate_NoGrace(t *testing.T) {
	svc := &mockCredentialSvc{
		rotateFn: func(_ context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
			if opts.GracePeriod != 0 {
				t.Errorf("expected zero grace, got %s", opts.GracePeriod)
			}
			return &credentials.RotateResult{Name: name, NewVersion: 1}, nil
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	resp, err := tr.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{
		Name: "k", NewValue: []byte("v"),
	})
	if err != nil {
		t.Fatalf("CredentialRotate: %v", err)
	}
	if resp.GraceUntilUnix != 0 {
		t.Errorf("expected zero grace, got %d", resp.GraceUntilUnix)
	}
}

func TestTransport_CredentialRotate_NilRequest(t *testing.T) {
	tr, cleanup := connectTransportWithCredentials(t, &mockCredentialSvc{})
	defer cleanup()
	if _, err := tr.CredentialRotate(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestTransport_CredentialRotate_ServerError(t *testing.T) {
	svc := &mockCredentialSvc{
		rotateFn: func(_ context.Context, _ string, _ credentials.RotateOptions) (*credentials.RotateResult, error) {
			return nil, credentials.ErrInvalidGracePeriod
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()
	if _, err := tr.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{
		Name: "x", NewValue: []byte("v"),
	}); err == nil {
		t.Fatal("expected error from server")
	}
}

func TestTransport_CredentialSetRotationPolicy_RoundTrip(t *testing.T) {
	var gotName string
	var gotPolicy credentials.RotationPolicy
	svc := &mockCredentialSvc{
		setPolicyFn: func(_ context.Context, name string, policy credentials.RotationPolicy) error {
			gotName, gotPolicy = name, policy
			return nil
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	if err := tr.CredentialSetRotationPolicy(context.Background(), &transport.CredentialSetRotationPolicyRequest{
		Name: "db-password", Enabled: true, Interval: "720h", RetainVersions: 4,
	}); err != nil {
		t.Fatalf("CredentialSetRotationPolicy: %v", err)
	}
	want := credentials.RotationPolicy{Enabled: true, Interval: 720 * time.Hour, RetainVersions: 4}
	if gotName != "db-password" || gotPolicy != want {
		t.Errorf("server saw %q %+v, want db-password %+v", gotName, gotPolicy, want)
	}
}

func TestTransport_CredentialSetRotationPolicy_InvalidPolicy(t *testing.T) {
	svc := &mockCredentialSvc{
		setPolicyFn: func(context.Context, string, credentials.RotationPolicy) error {
			return &credentials.InvalidRotationPolicyError{Reason: "retain versions must be at least 1"}
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	err := tr.CredentialSetRotationPolicy(context.Background(), &transport.CredentialSetRotationPolicyRequest{
		Name: "x", Enabled: true, Interval: "1h",
	})
	var re *transport.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if re.StatusCode != int(codes.InvalidArgument) {
		t.Errorf("status = %d, want InvalidArgument (%d)", re.StatusCode, codes.InvalidArgument)
	}
}

func TestTransport_CredentialSetRotationPolicy_NilRequest(t *testing.T) {
	tr, cleanup := connectTransportWithCredentials(t, &mockCredentialSvc{})
	defer cleanup()
	var re *transport.RequestError
	if err := tr.CredentialSetRotationPolicy(context.Background(), nil); !errors.As(err, &re) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
}

func TestTransport_CredentialRotationPolicy_RoundTrip(t *testing.T) {
	tr, cleanup := connectTransportWithCredentials(t, &mockCredentialSvc{})
	defer cleanup()

	resp, err := tr.CredentialRotationPolicy(context.Background(), "db-password")
	if err != nil {
		t.Fatalf("CredentialRotationPolicy: %v", err)
	}
	if resp.Name != "db-password" || !resp.Enabled || resp.Interval != time.Hour.String() || resp.RetainVersions != 2 {
		t.Errorf("unexpected resp: %+v", resp)
	}
}

func TestTransport_CredentialRotationPolicy_NotFound(t *testing.T) {
	tr, cleanup := connectTransportWithCredentials(t, &mockCredentialSvc{})
	defer cleanup()

	var nf *transport.KeyNotFoundError
	if _, err := tr.CredentialRotationPolicy(context.Background(), "missing"); !errors.As(err, &nf) {
		t.Fatalf("expected KeyNotFoundError, got %T: %v", err, err)
	}
}

func TestTransport_CredentialMigrationConflicts_RoundTrip(t *testing.T) {
	tr, cleanup := connectTransportWithCredentials(t, &mockCredentialSvc{})
	defer cleanup()

	resp, err := tr.CredentialMigrationConflicts(context.Background())
	if err != nil {
		t.Fatalf("CredentialMigrationConflicts: %v", err)
	}
	want := transport.CredentialMigrationConflict{Name: "db", Value: true, Policy: true}
	if len(resp.Conflicts) != 1 || resp.Conflicts[0] != want {
		t.Errorf("unexpected resp: %+v", resp)
	}
}

func TestTransport_CredentialMigrationConflicts_Closed(t *testing.T) {
	tr := &Transport{}
	tr.closed.Store(true)
	var ce *transport.ClosedError
	if _, err := tr.CredentialMigrationConflicts(context.Background()); !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_CredentialResolveMigrationConflict_RoundTrip(t *testing.T) {
	tr, cleanup := connectTransportWithCredentials(t, &mockCredentialSvc{})
	defer cleanup()

	if err := tr.CredentialResolveMigrationConflict(context.Background(),
		&transport.CredentialResolveMigrationConflictRequest{Name: "db", Resolution: "import"}); err != nil {
		t.Fatalf("CredentialResolveMigrationConflict: %v", err)
	}
}

func TestTransport_CredentialResolveMigrationConflict_NotFound(t *testing.T) {
	tr, cleanup := connectTransportWithCredentials(t, &mockCredentialSvc{})
	defer cleanup()

	var nf *transport.KeyNotFoundError
	if err := tr.CredentialResolveMigrationConflict(context.Background(),
		&transport.CredentialResolveMigrationConflictRequest{Name: "missing", Resolution: "discard"}); !errors.As(err, &nf) {
		t.Fatalf("expected KeyNotFoundError, got %T: %v", err, err)
	}
}

func TestTransport_CredentialResolveMigrationConflict_NilRequestAndClosed(t *testing.T) {
	tr, cleanup := connectTransportWithCredentials(t, &mockCredentialSvc{})
	defer cleanup()
	var re *transport.RequestError
	if err := tr.CredentialResolveMigrationConflict(context.Background(), nil); !errors.As(err, &re) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	closed := &Transport{}
	closed.closed.Store(true)
	var ce *transport.ClosedError
	if err := closed.CredentialResolveMigrationConflict(context.Background(),
		&transport.CredentialResolveMigrationConflictRequest{Name: "db", Resolution: "import"}); !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// CredentialRetrieveVersion
// ---------------------------------------------------------------------------

func TestTransport_CredentialRetrieveVersion_RoundTrip(t *testing.T) {
	var gotName string
	var gotVersion uint64
	svc := &mockCredentialSvc{
		retrieveVersionFn: func(_ context.Context, name string, version uint64) ([]byte, error) {
			gotName = name
			gotVersion = version
			return []byte("v7-data"), nil
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	resp, err := tr.retrieveVersionResponse(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name: "db", Version: 7,
	})
	if err != nil {
		t.Fatalf("CredentialRetrieveVersion: %v", err)
	}
	if string(resp.Value) != "v7-data" {
		t.Errorf("got value %q, want v7-data", resp.Value)
	}
	if gotName != "db" || gotVersion != 7 {
		t.Errorf("server got name=%q version=%d", gotName, gotVersion)
	}
}

func TestTransport_CredentialRetrieveVersion_NilRequest(t *testing.T) {
	tr, cleanup := connectTransportWithCredentials(t, &mockCredentialSvc{})
	defer cleanup()
	if _, err := tr.retrieveVersionResponse(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestTransport_CredentialRetrieveVersion_VersionExpired(t *testing.T) {
	svc := &mockCredentialSvc{
		retrieveVersionFn: func(_ context.Context, _ string, _ uint64) ([]byte, error) {
			return nil, credentials.ErrVersionExpired
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()
	if _, err := tr.retrieveVersionResponse(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name: "x", Version: 1,
	}); err == nil {
		t.Fatal("expected error from server")
	}
}

// ---------------------------------------------------------------------------
// CredentialListVersions
// ---------------------------------------------------------------------------

func TestTransport_CredentialListVersions_RoundTrip(t *testing.T) {
	created := time.Unix(1_700_000_000, 0).UTC()
	grace := time.Unix(1_700_000_500, 0).UTC()
	svc := &mockCredentialSvc{
		listVersionsFn: func(_ context.Context, name string) ([]credentials.CredentialVersion, error) {
			return []credentials.CredentialVersion{
				{Name: name, Version: 1, CreatedAt: created, GraceUntil: grace, IsCurrent: false},
				{Name: name, Version: 2, CreatedAt: created.Add(time.Minute), IsCurrent: true},
			}, nil
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	resp, err := tr.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{
		Name: "db",
	})
	if err != nil {
		t.Fatalf("CredentialListVersions: %v", err)
	}
	if resp.Name != "db" || len(resp.Versions) != 2 {
		t.Fatalf("unexpected resp: %+v", resp)
	}
	if resp.Versions[0].Version != 1 || resp.Versions[0].IsCurrent {
		t.Errorf("v0 unexpected: %+v", resp.Versions[0])
	}
	if resp.Versions[0].CreatedAtUnix != created.Unix() {
		t.Errorf("v0 created got %d, want %d", resp.Versions[0].CreatedAtUnix, created.Unix())
	}
	if resp.Versions[0].GraceUntilUnix != grace.Unix() {
		t.Errorf("v0 grace got %d, want %d", resp.Versions[0].GraceUntilUnix, grace.Unix())
	}
	if !resp.Versions[1].IsCurrent {
		t.Errorf("v1 expected IsCurrent=true; got %+v", resp.Versions[1])
	}
	if resp.Versions[1].GraceUntilUnix != 0 {
		t.Errorf("v1 grace should be zero, got %d", resp.Versions[1].GraceUntilUnix)
	}
}

func TestTransport_CredentialListVersions_NilRequest(t *testing.T) {
	tr, cleanup := connectTransportWithCredentials(t, &mockCredentialSvc{})
	defer cleanup()
	if _, err := tr.CredentialListVersions(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestTransport_CredentialListVersions_ServerError(t *testing.T) {
	svc := &mockCredentialSvc{
		listVersionsFn: func(_ context.Context, _ string) ([]credentials.CredentialVersion, error) {
			return nil, credentials.ErrCredentialNotFound
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()
	if _, err := tr.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{
		Name: "missing",
	}); err == nil {
		t.Fatal("expected error from server")
	}
}

// ---------------------------------------------------------------------------
// CredentialGCExpired
// ---------------------------------------------------------------------------

func TestTransport_CredentialGCExpired_RoundTrip(t *testing.T) {
	var gotName string
	svc := &mockCredentialSvc{
		gcExpiredFn: func(_ context.Context, name string) (int, error) {
			gotName = name
			return 9, nil
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	resp, err := tr.CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{Name: "db"})
	if err != nil {
		t.Fatalf("CredentialGCExpired: %v", err)
	}
	if resp.Reaped != 9 {
		t.Errorf("got reaped %d, want 9", resp.Reaped)
	}
	if gotName != "db" {
		t.Errorf("server got name %q, want db", gotName)
	}
}

func TestTransport_CredentialGCExpired_NilRequest(t *testing.T) {
	tr, cleanup := connectTransportWithCredentials(t, &mockCredentialSvc{})
	defer cleanup()
	if _, err := tr.CredentialGCExpired(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestTransport_CredentialGCExpired_ServerError(t *testing.T) {
	svc := &mockCredentialSvc{
		gcExpiredFn: func(_ context.Context, _ string) (int, error) {
			return 0, credentials.ErrInvalidStrategy
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()
	if _, err := tr.CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{Name: "x"}); err == nil {
		t.Fatal("expected error from server")
	}
}

// ---------------------------------------------------------------------------
// Closed-transport guard: every credential method must short-circuit before
// touching the wire.
// ---------------------------------------------------------------------------

func TestTransport_Credential_ClosedTransport(t *testing.T) {
	tr, cleanup := connectTransportWithCredentials(t, &mockCredentialSvc{})
	defer cleanup()

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	cases := []struct {
		name string
		fn   func() error
	}{
		{"Submit", func() error {
			return tr.CredentialSubmit(context.Background(), &transport.CredentialSubmitRequest{Name: "x", Value: []byte("y")})
		}},
		{"Strategy", func() error {
			_, err := tr.CredentialStrategy(context.Background())
			return err
		}},
		{"Delete", func() error {
			return tr.CredentialDelete(context.Background(), "x")
		}},
		{"List", func() error {
			_, err := tr.CredentialList(context.Background(), &transport.CredentialListRequest{})
			return err
		}},
		{"Rotate", func() error {
			_, err := tr.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{Name: "x", NewValue: []byte("y")})
			return err
		}},
		{"RetrieveVersion", func() error {
			_, err := tr.retrieveVersionResponse(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "x"})
			return err
		}},
		{"ListVersions", func() error {
			_, err := tr.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{Name: "x"})
			return err
		}},
		{"GCExpired", func() error {
			_, err := tr.CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{Name: "x"})
			return err
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.fn()
			var closed *transport.ClosedError
			if !errors.As(err, &closed) {
				t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
			}
		})
	}
}
