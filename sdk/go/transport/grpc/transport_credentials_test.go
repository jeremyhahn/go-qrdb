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

package grpc

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// fakeCredentialServicer is an in-memory service.CredentialServicer used to
// drive the Transport client through a real loopback gRPC connection.
// ---------------------------------------------------------------------------

type fakeCredentialServicer struct {
	submitFn       func(ctx context.Context, name string, value []byte) error
	deleteFn       func(ctx context.Context, name string) error
	strategyFn     func() string
	listFn         func(ctx context.Context) ([]string, error)
	rotateFn       func(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error)
	retrieveVersFn func(ctx context.Context, name string, version uint64) ([]byte, error)
	listVersionsFn func(ctx context.Context, name string) ([]credentials.CredentialVersion, error)
	gcExpiredFn    func(ctx context.Context, name string) (int, error)
	setPolicyFn    func(ctx context.Context, name string, policy credentials.RotationPolicy) error
}

func (f *fakeCredentialServicer) CredentialStore(_ context.Context, _ string, _ []byte) error {
	return nil
}
func (f *fakeCredentialServicer) retrieveValue(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}

func (f *fakeCredentialServicer) CredentialRetrieveBuffer(_ context.Context, _ string) (*secure.Buffer, error) {
	return secure.NewBufferFromBytes([]byte("fake-value"))
}

func (f *fakeCredentialServicer) CredentialRetrieveVersionBuffer(ctx context.Context, name string, version uint64) (*secure.Buffer, error) {
	if f.retrieveVersFn == nil {
		return secure.NewBufferFromBytes([]byte("fake-value"))
	}
	v, err := f.retrieveVersFn(ctx, name, version)
	if err != nil {
		return nil, err
	}
	return secure.NewBufferFromBytes(append([]byte(nil), v...))
}
func (f *fakeCredentialServicer) CredentialSubmit(ctx context.Context, name string, value []byte) error {
	if f.submitFn != nil {
		return f.submitFn(ctx, name, value)
	}
	return nil
}
func (f *fakeCredentialServicer) CredentialDelete(ctx context.Context, name string) error {
	if f.deleteFn != nil {
		return f.deleteFn(ctx, name)
	}
	return nil
}
func (f *fakeCredentialServicer) CredentialStrategy(context.Context) (string, error) {
	return f.credentialStrategy(), nil
}

func (f *fakeCredentialServicer) credentialStrategy() string {
	if f.strategyFn != nil {
		return f.strategyFn()
	}
	return "manual"
}
func (f *fakeCredentialServicer) CredentialList(ctx context.Context) ([]string, error) {
	if f.listFn != nil {
		return f.listFn(ctx)
	}
	return nil, nil
}
func (f *fakeCredentialServicer) CredentialRotate(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
	if f.rotateFn != nil {
		return f.rotateFn(ctx, name, opts)
	}
	return nil, nil
}
func (f *fakeCredentialServicer) retrieveVersionValue(ctx context.Context, name string, version uint64) ([]byte, error) {
	if f.retrieveVersFn != nil {
		return f.retrieveVersFn(ctx, name, version)
	}
	return nil, nil
}
func (f *fakeCredentialServicer) CredentialListVersions(ctx context.Context, name string) ([]credentials.CredentialVersion, error) {
	if f.listVersionsFn != nil {
		return f.listVersionsFn(ctx, name)
	}
	return nil, nil
}
func (f *fakeCredentialServicer) CredentialGCExpired(ctx context.Context, name string) (int, error) {
	if f.gcExpiredFn != nil {
		return f.gcExpiredFn(ctx, name)
	}
	return 0, nil
}

func (f *fakeCredentialServicer) CredentialSetRotationPolicy(ctx context.Context, name string, policy credentials.RotationPolicy) error {
	if f.setPolicyFn != nil {
		return f.setPolicyFn(ctx, name, policy)
	}
	if name == "missing" {
		return credentials.ErrCredentialNotFound
	}
	return nil
}

func (f *fakeCredentialServicer) CredentialRotationPolicy(_ context.Context, name string) (*service.CredentialRotationPolicyStatus, error) {
	if name == "missing" {
		return nil, credentials.ErrCredentialNotFound
	}
	return &service.CredentialRotationPolicyStatus{Policy: credentials.RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 2}}, nil
}

func (f *fakeCredentialServicer) CredentialMigrationConflicts(_ context.Context) ([]credentials.CredentialMigrationConflictError, error) {
	return []credentials.CredentialMigrationConflictError{{Name: "db", Value: true, Policy: true}}, nil
}

func (f *fakeCredentialServicer) CredentialResolveMigrationConflict(_ context.Context, name string, _ credentials.MigrationResolution) error {
	if name == "missing" {
		return credentials.ErrMigrationConflictNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// startCredentialServer spins up a bare gRPC server with the QRDB service
// registered. Only the credential servicer is wired in; KV/admin services
// are nil because the credential RPCs do not depend on them.
// ---------------------------------------------------------------------------

func startCredentialServer(t *testing.T, svc *fakeCredentialServicer) (string, func()) {
	t.Helper()
	srv := googlegrpc.NewServer()
	grpcpkg.RegisterService(srv, nil, nil, grpcpkg.WithRegisterCredentialService(svc))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() { srv.Stop() }
}

// connectCredentialTransport returns a Transport dialed against addr with the
// proto client wired up. It bypasses Transport.Connect so callers do not need
// to satisfy the health check.
func connectCredentialTransport(t *testing.T, addr string) (*Transport, func()) {
	t.Helper()
	conn, err := googlegrpc.NewClient(addr, googlegrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	tr := &Transport{
		config: &transport.Config{Address: addr},
		conn:   conn,
		client: pb.NewQRDBServiceClient(conn),
	}
	return tr, func() { _ = conn.Close() }
}

// ---------------------------------------------------------------------------
// CredentialSubmit
// ---------------------------------------------------------------------------

func TestTransportCredential_Submit_Success(t *testing.T) {
	var captured struct {
		name  string
		value []byte
	}
	svc := &fakeCredentialServicer{
		submitFn: func(_ context.Context, name string, value []byte) error {
			captured.name = name
			captured.value = value
			return nil
		},
	}
	addr, stop := startCredentialServer(t, svc)
	defer stop()
	tr, cleanup := connectCredentialTransport(t, addr)
	defer cleanup()

	err := tr.CredentialSubmit(context.Background(), &transport.CredentialSubmitRequest{
		Name: "secret-key", Value: []byte("abc123"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.name != "secret-key" || string(captured.value) != "abc123" {
		t.Errorf("unexpected captured args: %+v", captured)
	}
}

func TestTransportCredential_Submit_NilRequest(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	err := tr.CredentialSubmit(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
	var re *transport.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
}

func TestTransportCredential_Submit_AfterClose(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	tr.closed.Store(true)
	err := tr.CredentialSubmit(context.Background(), &transport.CredentialSubmitRequest{Name: "n"})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransportCredential_Submit_ServerError(t *testing.T) {
	svc := &fakeCredentialServicer{
		submitFn: func(_ context.Context, _ string, _ []byte) error {
			return credentials.ErrEmptyCredentialName
		},
	}
	addr, stop := startCredentialServer(t, svc)
	defer stop()
	tr, cleanup := connectCredentialTransport(t, addr)
	defer cleanup()

	err := tr.CredentialSubmit(context.Background(), &transport.CredentialSubmitRequest{Name: ""})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// CredentialStrategy
// ---------------------------------------------------------------------------

func TestTransportCredential_Strategy_Success(t *testing.T) {
	svc := &fakeCredentialServicer{strategyFn: func() string { return "barrier" }}
	addr, stop := startCredentialServer(t, svc)
	defer stop()
	tr, cleanup := connectCredentialTransport(t, addr)
	defer cleanup()

	resp, err := tr.CredentialStrategy(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Strategy != "barrier" {
		t.Errorf("expected 'barrier', got %q", resp.Strategy)
	}
}

func TestTransportCredential_Strategy_AfterClose(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	tr.closed.Store(true)
	_, err := tr.CredentialStrategy(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// CredentialDelete
// ---------------------------------------------------------------------------

func TestTransportCredential_Delete_Success(t *testing.T) {
	var capturedName string
	svc := &fakeCredentialServicer{
		deleteFn: func(_ context.Context, name string) error {
			capturedName = name
			return nil
		},
	}
	addr, stop := startCredentialServer(t, svc)
	defer stop()
	tr, cleanup := connectCredentialTransport(t, addr)
	defer cleanup()

	if err := tr.CredentialDelete(context.Background(), "to-delete"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedName != "to-delete" {
		t.Errorf("unexpected captured name: %q", capturedName)
	}
}

func TestTransportCredential_Delete_AfterClose(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	tr.closed.Store(true)
	err := tr.CredentialDelete(context.Background(), "n")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransportCredential_Delete_NotFound(t *testing.T) {
	svc := &fakeCredentialServicer{
		deleteFn: func(_ context.Context, _ string) error {
			return credentials.ErrCredentialNotFound
		},
	}
	addr, stop := startCredentialServer(t, svc)
	defer stop()
	tr, cleanup := connectCredentialTransport(t, addr)
	defer cleanup()

	err := tr.CredentialDelete(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// CredentialList
// ---------------------------------------------------------------------------

func TestTransportCredential_List_Success(t *testing.T) {
	svc := &fakeCredentialServicer{
		listFn: func(_ context.Context) ([]string, error) {
			return []string{"a", "b"}, nil
		},
	}
	addr, stop := startCredentialServer(t, svc)
	defer stop()
	tr, cleanup := connectCredentialTransport(t, addr)
	defer cleanup()

	resp, err := tr.CredentialList(context.Background(), &transport.CredentialListRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Names) != 2 || resp.Names[0] != "a" || resp.Names[1] != "b" {
		t.Errorf("unexpected names: %v", resp.Names)
	}
}

func TestTransportCredential_List_NilRequest(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	_, err := tr.CredentialList(context.Background(), nil)
	var re *transport.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
}

func TestTransportCredential_List_AfterClose(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	tr.closed.Store(true)
	_, err := tr.CredentialList(context.Background(), &transport.CredentialListRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// CredentialRotate
// ---------------------------------------------------------------------------

func TestTransportCredential_Rotate_Success(t *testing.T) {
	graceUntil := time.Unix(1_700_000_000, 0).UTC()
	var capturedOpts credentials.RotateOptions
	svc := &fakeCredentialServicer{
		rotateFn: func(_ context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
			capturedOpts = opts
			return &credentials.RotateResult{
				Name:            name,
				PreviousVersion: 4,
				NewVersion:      5,
				GraceUntil:      graceUntil,
			}, nil
		},
	}
	addr, stop := startCredentialServer(t, svc)
	defer stop()
	tr, cleanup := connectCredentialTransport(t, addr)
	defer cleanup()

	resp, err := tr.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{
		Name: "n", NewValue: []byte("v"), GracePeriodSeconds: 30,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.PreviousVersion != 4 || resp.NewVersion != 5 || resp.GraceUntilUnix != graceUntil.Unix() {
		t.Errorf("unexpected response: %+v", resp)
	}
	if capturedOpts.GracePeriod != 30*time.Second {
		t.Errorf("expected GracePeriod=30s, got %v", capturedOpts.GracePeriod)
	}
	if string(capturedOpts.NewValue) != "v" {
		t.Errorf("unexpected new value: %q", capturedOpts.NewValue)
	}
}

func TestTransportCredential_Rotate_NilRequest(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	_, err := tr.CredentialRotate(context.Background(), nil)
	var re *transport.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
}

func TestTransportCredential_Rotate_AfterClose(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	tr.closed.Store(true)
	_, err := tr.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{Name: "n"})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransportCredential_SetRotationPolicy_Success(t *testing.T) {
	var gotName string
	var gotPolicy credentials.RotationPolicy
	svc := &fakeCredentialServicer{
		setPolicyFn: func(_ context.Context, name string, policy credentials.RotationPolicy) error {
			gotName, gotPolicy = name, policy
			return nil
		},
	}
	addr, stop := startCredentialServer(t, svc)
	defer stop()
	tr, cleanup := connectCredentialTransport(t, addr)
	defer cleanup()

	err := tr.CredentialSetRotationPolicy(context.Background(), &transport.CredentialSetRotationPolicyRequest{
		Name: "n", Enabled: true, Interval: "24h", RetainVersions: 3,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := credentials.RotationPolicy{Enabled: true, Interval: 24 * time.Hour, RetainVersions: 3}
	if gotName != "n" || gotPolicy != want {
		t.Errorf("service saw %q %+v, want %q %+v", gotName, gotPolicy, "n", want)
	}
}

func TestTransportCredential_SetRotationPolicy_InvalidPolicy(t *testing.T) {
	svc := &fakeCredentialServicer{
		setPolicyFn: func(context.Context, string, credentials.RotationPolicy) error {
			return &credentials.InvalidRotationPolicyError{Reason: "interval must be positive"}
		},
	}
	addr, stop := startCredentialServer(t, svc)
	defer stop()
	tr, cleanup := connectCredentialTransport(t, addr)
	defer cleanup()

	err := tr.CredentialSetRotationPolicy(context.Background(), &transport.CredentialSetRotationPolicyRequest{
		Name: "n", Enabled: true, Interval: "0s", RetainVersions: 1,
	})
	var re *transport.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if re.StatusCode != int(codes.InvalidArgument) {
		t.Errorf("status = %d, want InvalidArgument (%d)", re.StatusCode, codes.InvalidArgument)
	}
}

func TestTransportCredential_SetRotationPolicy_NilRequestAndAfterClose(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	var re *transport.RequestError
	if err := tr.CredentialSetRotationPolicy(context.Background(), nil); !errors.As(err, &re) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	tr.closed.Store(true)
	var ce *transport.ClosedError
	if err := tr.CredentialSetRotationPolicy(context.Background(), &transport.CredentialSetRotationPolicyRequest{Name: "n"}); !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransportCredential_RotationPolicy_Success(t *testing.T) {
	addr, stop := startCredentialServer(t, &fakeCredentialServicer{})
	defer stop()
	tr, cleanup := connectCredentialTransport(t, addr)
	defer cleanup()

	resp, err := tr.CredentialRotationPolicy(context.Background(), "n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Name != "n" || !resp.Enabled || resp.Interval != time.Hour.String() || resp.RetainVersions != 2 {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestTransportCredential_RotationPolicy_NotFoundAndAfterClose(t *testing.T) {
	addr, stop := startCredentialServer(t, &fakeCredentialServicer{})
	defer stop()
	tr, cleanup := connectCredentialTransport(t, addr)
	defer cleanup()

	var nf *transport.KeyNotFoundError
	if _, err := tr.CredentialRotationPolicy(context.Background(), "missing"); !errors.As(err, &nf) {
		t.Fatalf("expected KeyNotFoundError, got %T: %v", err, err)
	}
	closed := &Transport{config: &transport.Config{}}
	closed.closed.Store(true)
	var ce *transport.ClosedError
	if _, err := closed.CredentialRotationPolicy(context.Background(), "n"); !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransportCredential_MigrationConflicts_Success(t *testing.T) {
	addr, stop := startCredentialServer(t, &fakeCredentialServicer{})
	defer stop()
	tr, cleanup := connectCredentialTransport(t, addr)
	defer cleanup()

	resp, err := tr.CredentialMigrationConflicts(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []transport.CredentialMigrationConflict{{Name: "db", Value: true, Policy: true}}
	if len(resp.Conflicts) != 1 || resp.Conflicts[0] != want[0] {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestTransportCredential_MigrationConflicts_AfterClose(t *testing.T) {
	closed := &Transport{config: &transport.Config{}}
	closed.closed.Store(true)
	var ce *transport.ClosedError
	if _, err := closed.CredentialMigrationConflicts(context.Background()); !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransportCredential_ResolveMigrationConflict_SuccessAndNotFound(t *testing.T) {
	addr, stop := startCredentialServer(t, &fakeCredentialServicer{})
	defer stop()
	tr, cleanup := connectCredentialTransport(t, addr)
	defer cleanup()

	if err := tr.CredentialResolveMigrationConflict(context.Background(),
		&transport.CredentialResolveMigrationConflictRequest{Name: "db", Resolution: "import"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var nf *transport.KeyNotFoundError
	if err := tr.CredentialResolveMigrationConflict(context.Background(),
		&transport.CredentialResolveMigrationConflictRequest{Name: "missing", Resolution: "discard"}); !errors.As(err, &nf) {
		t.Fatalf("expected KeyNotFoundError, got %T: %v", err, err)
	}
}

func TestTransportCredential_ResolveMigrationConflict_NilRequestAndAfterClose(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	var re *transport.RequestError
	if err := tr.CredentialResolveMigrationConflict(context.Background(), nil); !errors.As(err, &re) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	tr.closed.Store(true)
	var ce *transport.ClosedError
	if err := tr.CredentialResolveMigrationConflict(context.Background(),
		&transport.CredentialResolveMigrationConflictRequest{Name: "db", Resolution: "import"}); !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// CredentialRetrieveVersion
// ---------------------------------------------------------------------------

func TestTransportCredential_RetrieveVersion_Success(t *testing.T) {
	var capturedVersion uint64
	svc := &fakeCredentialServicer{
		retrieveVersFn: func(_ context.Context, _ string, version uint64) ([]byte, error) {
			capturedVersion = version
			return []byte("the-secret"), nil
		},
	}
	addr, stop := startCredentialServer(t, svc)
	defer stop()
	tr, cleanup := connectCredentialTransport(t, addr)
	defer cleanup()

	resp, err := tr.retrieveVersionResponse(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name: "n", Version: 9,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.Value) != "the-secret" {
		t.Errorf("unexpected value: %q", resp.Value)
	}
	if capturedVersion != 9 {
		t.Errorf("expected version=9, got %d", capturedVersion)
	}
}

func TestTransportCredential_RetrieveVersion_NilRequest(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	_, err := tr.retrieveVersionResponse(context.Background(), nil)
	var re *transport.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
}

func TestTransportCredential_RetrieveVersion_AfterClose(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	tr.closed.Store(true)
	_, err := tr.retrieveVersionResponse(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "n"})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// CredentialListVersions
// ---------------------------------------------------------------------------

func TestTransportCredential_ListVersions_Success(t *testing.T) {
	created := time.Unix(1_700_000_000, 0).UTC()
	grace := time.Unix(1_700_000_500, 0).UTC()
	svc := &fakeCredentialServicer{
		listVersionsFn: func(_ context.Context, _ string) ([]credentials.CredentialVersion, error) {
			return []credentials.CredentialVersion{
				{Name: "n", Version: 2, IsCurrent: true},
				{Name: "n", Version: 1, CreatedAt: created, GraceUntil: grace},
			}, nil
		},
	}
	addr, stop := startCredentialServer(t, svc)
	defer stop()
	tr, cleanup := connectCredentialTransport(t, addr)
	defer cleanup()

	resp, err := tr.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{Name: "n"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Name != "n" {
		t.Errorf("unexpected name: %q", resp.Name)
	}
	if len(resp.Versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(resp.Versions))
	}
	if resp.Versions[0].Version != 2 || !resp.Versions[0].IsCurrent {
		t.Errorf("unexpected current entry: %+v", resp.Versions[0])
	}
	if resp.Versions[1].Version != 1 || resp.Versions[1].CreatedAtUnix != created.Unix() || resp.Versions[1].GraceUntilUnix != grace.Unix() {
		t.Errorf("unexpected historical entry: %+v", resp.Versions[1])
	}
}

func TestTransportCredential_ListVersions_NilRequest(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	_, err := tr.CredentialListVersions(context.Background(), nil)
	var re *transport.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
}

func TestTransportCredential_ListVersions_AfterClose(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	tr.closed.Store(true)
	_, err := tr.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{Name: "n"})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// CredentialGCExpired
// ---------------------------------------------------------------------------

func TestTransportCredential_GCExpired_Success(t *testing.T) {
	svc := &fakeCredentialServicer{
		gcExpiredFn: func(_ context.Context, _ string) (int, error) {
			return 7, nil
		},
	}
	addr, stop := startCredentialServer(t, svc)
	defer stop()
	tr, cleanup := connectCredentialTransport(t, addr)
	defer cleanup()

	resp, err := tr.CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{Name: "n"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Reaped != 7 {
		t.Errorf("expected reaped=7, got %d", resp.Reaped)
	}
}

func TestTransportCredential_GCExpired_NilRequest(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	_, err := tr.CredentialGCExpired(context.Background(), nil)
	var re *transport.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
}

func TestTransportCredential_GCExpired_AfterClose(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	tr.closed.Store(true)
	_, err := tr.CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{Name: "n"})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// credentialNilRequestError
// ---------------------------------------------------------------------------

func TestErrCredentialNilRequest_Error(t *testing.T) {
	if got := (credentialNilRequestError{}).Error(); got != "credentials: nil request" {
		t.Errorf("unexpected error string: %q", got)
	}
}
