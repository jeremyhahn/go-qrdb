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

package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockCredentialSvc is an in-memory service.CredentialServicer used by
// transport round-trip tests. It records the last call for each method so
// assertions can verify wire-level argument decoding.
// ---------------------------------------------------------------------------

type mockCredentialSvc struct {
	mu sync.Mutex

	// Configurable error injection.
	submitErr          error
	deleteErr          error
	listErr            error
	rotateErr          error
	retrieveVersionErr error
	listVersionsErr    error
	gcExpiredErr       error

	// Configurable strategy / responses.
	strategy       string
	listResp       []string
	rotateResp     *credentials.RotateResult
	retrievedValue []byte
	listedVersions []credentials.CredentialVersion
	reapCount      int

	// Recorded last-call state.
	lastSubmitName       string
	lastSubmitValue      []byte
	lastDeleteName       string
	lastRotateName       string
	lastRotateOptions    credentials.RotateOptions
	lastRetrieveName     string
	lastRetrieveVersion  uint64
	lastListVersionsName string
	lastGCName           string
}

var _ service.CredentialServicer = (*mockCredentialSvc)(nil)

func newMockCredentialSvc() *mockCredentialSvc {
	return &mockCredentialSvc{
		strategy: "manual",
		listResp: []string{"alpha", "beta"},
		rotateResp: &credentials.RotateResult{
			Name:            "k",
			PreviousVersion: 1,
			NewVersion:      2,
		},
		retrievedValue: []byte("retrieved-value"),
		listedVersions: []credentials.CredentialVersion{
			{Name: "k", Version: 2, IsCurrent: true, CreatedAt: time.Unix(1_700_000_100, 0)},
			{Name: "k", Version: 1, IsCurrent: false, CreatedAt: time.Unix(1_700_000_000, 0), GraceUntil: time.Unix(1_700_003_600, 0)},
		},
		reapCount: 0,
	}
}

func (m *mockCredentialSvc) CredentialStore(_ context.Context, _ string, _ []byte) error {
	return nil
}

func (m *mockCredentialSvc) retrieveValue(_ context.Context, _ string) ([]byte, error) {
	return []byte("value"), nil
}

func (m *mockCredentialSvc) CredentialRetrieveBuffer(_ context.Context, _ string) (*secure.Buffer, error) {
	return secure.NewBufferFromBytes([]byte("value"))
}

func (m *mockCredentialSvc) CredentialRetrieveVersionBuffer(ctx context.Context, name string, version uint64) (*secure.Buffer, error) {
	v, err := m.retrieveVersionValue(ctx, name, version)
	if err != nil {
		return nil, err
	}
	return secure.NewBufferFromBytes(append([]byte(nil), v...))
}

func (m *mockCredentialSvc) CredentialSubmit(_ context.Context, name string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastSubmitName = name
	m.lastSubmitValue = append([]byte(nil), value...)
	return m.submitErr
}

func (m *mockCredentialSvc) CredentialDelete(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastDeleteName = name
	return m.deleteErr
}

func (m *mockCredentialSvc) CredentialStrategy() string {
	return m.strategy
}

func (m *mockCredentialSvc) CredentialList(_ context.Context) ([]string, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResp, nil
}

func (m *mockCredentialSvc) CredentialRotate(_ context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRotateName = name
	m.lastRotateOptions = opts
	if m.rotateErr != nil {
		return nil, m.rotateErr
	}
	res := *m.rotateResp
	res.Name = name
	if opts.GracePeriod > 0 {
		res.GraceUntil = time.Unix(1_700_000_100, 0).Add(opts.GracePeriod)
	}
	return &res, nil
}

func (m *mockCredentialSvc) retrieveVersionValue(_ context.Context, name string, version uint64) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRetrieveName = name
	m.lastRetrieveVersion = version
	if m.retrieveVersionErr != nil {
		return nil, m.retrieveVersionErr
	}
	return m.retrievedValue, nil
}

func (m *mockCredentialSvc) CredentialListVersions(_ context.Context, name string) ([]credentials.CredentialVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastListVersionsName = name
	if m.listVersionsErr != nil {
		return nil, m.listVersionsErr
	}
	return m.listedVersions, nil
}

func (m *mockCredentialSvc) CredentialGCExpired(_ context.Context, name string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastGCName = name
	if m.gcExpiredErr != nil {
		return 0, m.gcExpiredErr
	}
	return m.reapCount, nil
}

// ---------------------------------------------------------------------------
// Test harness
// ---------------------------------------------------------------------------

// connectCredentialInProcess builds an in-process MCP server with the given
// credential service registered and returns a connected SDK transport.
func connectCredentialInProcess(t *testing.T, svc service.CredentialServicer) (*Transport, func()) {
	t.Helper()

	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
		mcpapi.WithCredentialService(svc),
	)

	mcpSrv := srv.MCPServer()
	if mcpSrv == nil {
		t.Fatal("MCPServer is nil")
	}

	tr, err := NewInProcess(mcpSrv)
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}

	if connErr := tr.Connect(context.Background()); connErr != nil {
		t.Fatalf("Connect: %v", connErr)
	}

	return tr, func() { _ = tr.Close() }
}

// ---------------------------------------------------------------------------
// CredentialSubmit
// ---------------------------------------------------------------------------

func TestCredentialSubmit_Success(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	err := tr.CredentialSubmit(context.Background(), &transport.CredentialSubmitRequest{
		Name:  "db-password",
		Value: []byte("secret123"),
	})
	if err != nil {
		t.Fatalf("CredentialSubmit: %v", err)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastSubmitName != "db-password" {
		t.Errorf("name: got %q, want %q", svc.lastSubmitName, "db-password")
	}
	if string(svc.lastSubmitValue) != "secret123" {
		t.Errorf("value: got %q, want %q", string(svc.lastSubmitValue), "secret123")
	}
}

func TestCredentialSubmit_ServiceError(t *testing.T) {
	svc := newMockCredentialSvc()
	svc.submitErr = errors.New("submit refused")
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	err := tr.CredentialSubmit(context.Background(), &transport.CredentialSubmitRequest{
		Name:  "n",
		Value: []byte("v"),
	})
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

func TestCredentialSubmit_NilRequest(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	err := tr.CredentialSubmit(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestCredentialSubmit_Closed(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	_ = tr.Close()
	err := tr.CredentialSubmit(context.Background(), &transport.CredentialSubmitRequest{Name: "n", Value: []byte("v")})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected *transport.ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// CredentialStrategy
// ---------------------------------------------------------------------------

func TestCredentialStrategy_Success(t *testing.T) {
	svc := newMockCredentialSvc()
	svc.strategy = "barrier"
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	resp, err := tr.CredentialStrategy(context.Background())
	if err != nil {
		t.Fatalf("CredentialStrategy: %v", err)
	}
	if resp.Strategy != "barrier" {
		t.Errorf("strategy: got %q, want %q", resp.Strategy, "barrier")
	}
}

func TestCredentialStrategy_Closed(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	_ = tr.Close()
	_, err := tr.CredentialStrategy(context.Background())
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected *transport.ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// CredentialDelete
// ---------------------------------------------------------------------------

func TestCredentialDelete_Success(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	err := tr.CredentialDelete(context.Background(), "db-password")
	if err != nil {
		t.Fatalf("CredentialDelete: %v", err)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastDeleteName != "db-password" {
		t.Errorf("name: got %q, want %q", svc.lastDeleteName, "db-password")
	}
}

func TestCredentialDelete_ServiceError(t *testing.T) {
	svc := newMockCredentialSvc()
	svc.deleteErr = errors.New("credential not found")
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	err := tr.CredentialDelete(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

func TestCredentialDelete_Closed(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	_ = tr.Close()
	err := tr.CredentialDelete(context.Background(), "n")
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected *transport.ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// CredentialList
// ---------------------------------------------------------------------------

func TestCredentialList_Success(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	resp, err := tr.CredentialList(context.Background(), &transport.CredentialListRequest{})
	if err != nil {
		t.Fatalf("CredentialList: %v", err)
	}
	if len(resp.Names) != 2 || resp.Names[0] != "alpha" || resp.Names[1] != "beta" {
		t.Errorf("names: got %v, want [alpha beta]", resp.Names)
	}
}

func TestCredentialList_NilRequest(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	// Nil request is permitted — it carries no fields today and is reserved
	// for forward-compatible filter additions.
	resp, err := tr.CredentialList(context.Background(), nil)
	if err != nil {
		t.Fatalf("CredentialList(nil): %v", err)
	}
	if len(resp.Names) != 2 {
		t.Errorf("expected 2 names, got %d", len(resp.Names))
	}
}

func TestCredentialList_ServiceError(t *testing.T) {
	svc := newMockCredentialSvc()
	svc.listErr = errors.New("enumeration unavailable")
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	_, err := tr.CredentialList(context.Background(), &transport.CredentialListRequest{})
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

func TestCredentialList_Closed(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	_ = tr.Close()
	_, err := tr.CredentialList(context.Background(), &transport.CredentialListRequest{})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected *transport.ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// CredentialRotate
// ---------------------------------------------------------------------------

func TestCredentialRotate_SuccessNoGrace(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	resp, err := tr.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{
		Name:     "db-password",
		NewValue: []byte("v2"),
	})
	if err != nil {
		t.Fatalf("CredentialRotate: %v", err)
	}
	if resp.Name != "db-password" {
		t.Errorf("name: got %q, want %q", resp.Name, "db-password")
	}
	if resp.PreviousVersion != 1 || resp.NewVersion != 2 {
		t.Errorf("versions: got prev=%d new=%d, want prev=1 new=2", resp.PreviousVersion, resp.NewVersion)
	}
	if resp.GraceUntilUnix != 0 {
		t.Errorf("grace_until_unix: got %d, want 0", resp.GraceUntilUnix)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastRotateName != "db-password" {
		t.Errorf("rotated name: got %q, want %q", svc.lastRotateName, "db-password")
	}
	if string(svc.lastRotateOptions.NewValue) != "v2" {
		t.Errorf("rotated new value: got %q, want %q", string(svc.lastRotateOptions.NewValue), "v2")
	}
	if svc.lastRotateOptions.GracePeriod != 0 {
		t.Errorf("grace period: got %v, want 0", svc.lastRotateOptions.GracePeriod)
	}
}

func TestCredentialRotate_SuccessWithGrace(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	resp, err := tr.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{
		Name:               "db-password",
		NewValue:           []byte("v2"),
		GracePeriodSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("CredentialRotate: %v", err)
	}
	if resp.GraceUntilUnix == 0 {
		t.Error("expected non-zero grace_until_unix")
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastRotateOptions.GracePeriod != time.Hour {
		t.Errorf("grace period: got %v, want 1h", svc.lastRotateOptions.GracePeriod)
	}
}

func TestCredentialRotate_ServiceError(t *testing.T) {
	svc := newMockCredentialSvc()
	svc.rotateErr = errors.New("rotation refused")
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	_, err := tr.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{
		Name:     "n",
		NewValue: []byte("v"),
	})
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

func TestCredentialRotate_NilRequest(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	_, err := tr.CredentialRotate(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestCredentialRotate_Closed(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	_ = tr.Close()
	_, err := tr.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{Name: "n", NewValue: []byte("v")})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected *transport.ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// CredentialRetrieveVersion
// ---------------------------------------------------------------------------

func TestCredentialRetrieveVersion_Success(t *testing.T) {
	svc := newMockCredentialSvc()
	svc.retrievedValue = []byte("specific-v2")
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	resp, err := tr.retrieveVersionResponse(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name:    "db-password",
		Version: 2,
	})
	if err != nil {
		t.Fatalf("CredentialRetrieveVersion: %v", err)
	}
	if string(resp.Value) != "specific-v2" {
		t.Errorf("value: got %q, want %q", resp.Value, "specific-v2")
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastRetrieveName != "db-password" {
		t.Errorf("name: got %q, want %q", svc.lastRetrieveName, "db-password")
	}
	if svc.lastRetrieveVersion != 2 {
		t.Errorf("version: got %d, want 2", svc.lastRetrieveVersion)
	}
}

func TestCredentialRetrieveVersion_ZeroAliasesCurrent(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	_, err := tr.retrieveVersionResponse(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name: "db-password",
		// Version = 0 (zero value) intentionally
	})
	if err != nil {
		t.Fatalf("CredentialRetrieveVersion: %v", err)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastRetrieveVersion != 0 {
		t.Errorf("version: got %d, want 0", svc.lastRetrieveVersion)
	}
}

func TestCredentialRetrieveVersion_ServiceError(t *testing.T) {
	svc := newMockCredentialSvc()
	svc.retrieveVersionErr = errors.New("version expired")
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	_, err := tr.retrieveVersionResponse(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name:    "n",
		Version: 1,
	})
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

func TestCredentialRetrieveVersion_NilRequest(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	_, err := tr.retrieveVersionResponse(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestCredentialRetrieveVersion_Closed(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	_ = tr.Close()
	_, err := tr.retrieveVersionResponse(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "n"})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected *transport.ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// CredentialListVersions
// ---------------------------------------------------------------------------

func TestCredentialListVersions_Success(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	resp, err := tr.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{
		Name: "db-password",
	})
	if err != nil {
		t.Fatalf("CredentialListVersions: %v", err)
	}
	if resp.Name != "db-password" {
		t.Errorf("name: got %q, want %q", resp.Name, "db-password")
	}
	if len(resp.Versions) != 2 {
		t.Fatalf("versions: got %d entries, want 2", len(resp.Versions))
	}
	if !resp.Versions[0].IsCurrent {
		t.Error("expected versions[0] to be current")
	}
	if resp.Versions[1].IsCurrent {
		t.Error("expected versions[1] not to be current")
	}
	if resp.Versions[1].GraceUntilUnix == 0 {
		t.Error("expected versions[1] to carry grace_until_unix")
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastListVersionsName != "db-password" {
		t.Errorf("name: got %q, want %q", svc.lastListVersionsName, "db-password")
	}
}

func TestCredentialListVersions_ServiceError(t *testing.T) {
	svc := newMockCredentialSvc()
	svc.listVersionsErr = errors.New("not found")
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	_, err := tr.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{Name: "missing"})
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

func TestCredentialListVersions_NilRequest(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	_, err := tr.CredentialListVersions(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestCredentialListVersions_Closed(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	_ = tr.Close()
	_, err := tr.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{Name: "n"})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected *transport.ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// CredentialGCExpired
// ---------------------------------------------------------------------------

func TestCredentialGCExpired_Success(t *testing.T) {
	svc := newMockCredentialSvc()
	svc.reapCount = 3
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	resp, err := tr.CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{
		Name: "db-password",
	})
	if err != nil {
		t.Fatalf("CredentialGCExpired: %v", err)
	}
	if resp.Reaped != 3 {
		t.Errorf("reaped: got %d, want 3", resp.Reaped)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastGCName != "db-password" {
		t.Errorf("name: got %q, want %q", svc.lastGCName, "db-password")
	}
}

func TestCredentialGCExpired_ServiceError(t *testing.T) {
	svc := newMockCredentialSvc()
	svc.gcExpiredErr = errors.New("gc failed")
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	_, err := tr.CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{Name: "n"})
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

func TestCredentialGCExpired_NilRequest(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	_, err := tr.CredentialGCExpired(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestCredentialGCExpired_Closed(t *testing.T) {
	svc := newMockCredentialSvc()
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	_ = tr.Close()
	_, err := tr.CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{Name: "n"})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected *transport.ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Binary (non-UTF-8) round-trip
// ---------------------------------------------------------------------------

// nonUTF8CredentialValue is a credential payload whose leading bytes
// (0x00 0xff 0xfe 0x01) are invalid UTF-8. The MCP transport carries the value
// as a base64 (RFC 4648) tool argument/result, so binary credential material
// round-trips byte-for-byte through the SDK client end-to-end against a real
// in-process MCP server.
var nonUTF8CredentialValue = []byte{0x00, 0xff, 0xfe, 0x01, 'k', 'e', 'y'}

// TestCredential_BinaryRoundTrip exercises both wire directions through the SDK
// MCP client against an in-process server: CredentialSubmit base64-encodes the
// value on send (the server base64-decodes it) and CredentialRetrieveVersion
// base64-decodes the value on receive (the server base64-encodes it).
func TestCredential_BinaryRoundTrip(t *testing.T) {
	svc := newMockCredentialSvc()
	svc.retrievedValue = nonUTF8CredentialValue
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	if err := tr.CredentialSubmit(context.Background(), &transport.CredentialSubmitRequest{
		Name:  "bin",
		Value: nonUTF8CredentialValue,
	}); err != nil {
		t.Fatalf("CredentialSubmit: %v", err)
	}
	svc.mu.Lock()
	got := append([]byte(nil), svc.lastSubmitValue...)
	svc.mu.Unlock()
	if string(got) != string(nonUTF8CredentialValue) {
		t.Fatalf("server received submit value %#v, want %#v", got, nonUTF8CredentialValue)
	}

	resp, err := tr.retrieveVersionResponse(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name:    "bin",
		Version: 0,
	})
	if err != nil {
		t.Fatalf("CredentialRetrieveVersion: %v", err)
	}
	if string(resp.Value) != string(nonUTF8CredentialValue) {
		t.Errorf("round-tripped value = %#v, want %#v", resp.Value, nonUTF8CredentialValue)
	}
}
