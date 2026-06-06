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
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockUserAuthFactorSvc implements service.UserAuthFactorServicer.
// ---------------------------------------------------------------------------

type mockUserAuthFactorSvc struct {
	mu sync.Mutex

	addErr      error
	listErr     error
	getErr      error
	lookupErr   error
	removeErr   error
	rotateErr   error
	relabelErr  error
	markUsedErr error

	lastUserID    uint64
	lastFactor    *auth.AuthFactor
	lastListUID   uint64
	lastGetID     string
	lastLookupT   auth.AuthFactorType
	lastLookupKey []byte
	lastRemoveID  string
	lastRemoveF   bool
	lastRotateOld string
	lastRotateNew *auth.AuthFactor
	lastRelabelID string
	lastRelabel   string

	getResp    *auth.AuthFactor
	listResp   []*auth.AuthFactor
	lookupResp *auth.AuthFactor
}

var _ service.UserAuthFactorServicer = (*mockUserAuthFactorSvc)(nil)

func newMockUserAuthFactorSvc() *mockUserAuthFactorSvc {
	now := time.Now().UTC()
	f := &auth.AuthFactor{
		ID:           "f-1",
		UserID:       42,
		Type:         "mtls",
		Label:        "primary",
		Credential:   []byte("hashed"),
		Metadata:     map[string]string{"serial": "ABCD"},
		RegisteredAt: now,
		CreatedBy:    "self",
	}
	return &mockUserAuthFactorSvc{
		getResp:    f,
		lookupResp: f,
		listResp:   []*auth.AuthFactor{f},
	}
}

func (m *mockUserAuthFactorSvc) Add(_ context.Context, userID uint64, factor *auth.AuthFactor) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastUserID = userID
	m.lastFactor = factor
	if m.addErr != nil {
		return m.addErr
	}
	// Mutate the factor with an ID like the real service does
	factor.ID = "f-new"
	factor.UserID = userID
	if factor.RegisteredAt.IsZero() {
		factor.RegisteredAt = time.Now().UTC()
	}
	return nil
}

func (m *mockUserAuthFactorSvc) List(_ context.Context, userID uint64) ([]*auth.AuthFactor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastListUID = userID
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResp, nil
}

func (m *mockUserAuthFactorSvc) Get(_ context.Context, factorID string) (*auth.AuthFactor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastGetID = factorID
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.getResp, nil
}

func (m *mockUserAuthFactorSvc) LookupByCredentialKey(_ context.Context, t auth.AuthFactorType, key []byte) (*auth.AuthFactor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastLookupT = t
	m.lastLookupKey = append([]byte(nil), key...)
	if m.lookupErr != nil {
		return nil, m.lookupErr
	}
	return m.lookupResp, nil
}

func (m *mockUserAuthFactorSvc) Remove(_ context.Context, factorID string, force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRemoveID = factorID
	m.lastRemoveF = force
	return m.removeErr
}

func (m *mockUserAuthFactorSvc) Rotate(_ context.Context, oldID string, newFactor *auth.AuthFactor) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRotateOld = oldID
	m.lastRotateNew = newFactor
	if m.rotateErr != nil {
		return m.rotateErr
	}
	newFactor.ID = "f-rotated"
	if newFactor.RegisteredAt.IsZero() {
		newFactor.RegisteredAt = time.Now().UTC()
	}
	return nil
}

func (m *mockUserAuthFactorSvc) Relabel(_ context.Context, factorID string, label string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRelabelID = factorID
	m.lastRelabel = label
	return m.relabelErr
}

func (m *mockUserAuthFactorSvc) MarkUsed(_ context.Context, _ string, _ time.Time) error {
	return m.markUsedErr
}

// connectUserAuthFactorInProcess builds an in-process MCP server with both
// user-auth-factor service and a user lookup helper.
func connectUserAuthFactorInProcess(t *testing.T, svc service.UserAuthFactorServicer) (*Transport, func()) {
	t.Helper()
	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
		mcpapi.WithUserAuthFactorService(svc),
	)
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if connErr := tr.Connect(context.Background()); connErr != nil {
		t.Fatalf("Connect: %v", connErr)
	}
	return tr, func() { _ = tr.Close() }
}

// ---------------------------------------------------------------------------
// AddAuthFactor
// ---------------------------------------------------------------------------

func TestAddAuthFactor_Success(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	tr, cleanup := connectUserAuthFactorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.AddAuthFactor(context.Background(), &transport.AuthFactorAddRequest{
		UserID:     42,
		Type:       "mtls",
		Label:      "primary",
		Credential: []byte("hash"),
		Metadata:   map[string]string{"serial": "AA"},
		CreatedBy:  "self",
	})
	if err != nil {
		t.Fatalf("AddAuthFactor: %v", err)
	}
	if resp.Factor.ID == "" {
		t.Error("expected non-empty factor id")
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastUserID != 42 {
		t.Errorf("user id: %d", svc.lastUserID)
	}
}

func TestAddAuthFactor_ServiceError(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	svc.addErr = errors.New("add failed")
	tr, cleanup := connectUserAuthFactorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.AddAuthFactor(context.Background(), &transport.AuthFactorAddRequest{
		UserID: 1, Type: "mtls", Credential: []byte("x"),
	}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// ListAuthFactors
// ---------------------------------------------------------------------------

func TestListAuthFactors_Success(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	tr, cleanup := connectUserAuthFactorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.ListAuthFactors(context.Background(), &transport.AuthFactorListRequest{UserID: 42})
	if err != nil {
		t.Fatalf("ListAuthFactors: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("total: %d", resp.Total)
	}
}

func TestListAuthFactors_ServiceError(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	svc.listErr = errors.New("list failed")
	tr, cleanup := connectUserAuthFactorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.ListAuthFactors(context.Background(), &transport.AuthFactorListRequest{UserID: 42}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// GetAuthFactor
// ---------------------------------------------------------------------------

func TestGetAuthFactor_Success(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	tr, cleanup := connectUserAuthFactorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.GetAuthFactor(context.Background(), &transport.AuthFactorGetRequest{FactorID: "f-1"})
	if err != nil {
		t.Fatalf("GetAuthFactor: %v", err)
	}
	if resp.Factor.ID != "f-1" {
		t.Errorf("id: %q", resp.Factor.ID)
	}
}

func TestGetAuthFactor_ServiceError(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	svc.getErr = errors.New("not found")
	tr, cleanup := connectUserAuthFactorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.GetAuthFactor(context.Background(), &transport.AuthFactorGetRequest{FactorID: "x"}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// RemoveAuthFactor
// ---------------------------------------------------------------------------

func TestRemoveAuthFactor_Success(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	tr, cleanup := connectUserAuthFactorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.RemoveAuthFactor(context.Background(), &transport.AuthFactorRemoveRequest{
		FactorID: "f-1",
		Force:    false,
	})
	if err != nil {
		t.Fatalf("RemoveAuthFactor: %v", err)
	}
	if resp.Status == "" {
		t.Error("expected non-empty status")
	}
}

func TestRemoveAuthFactor_ServiceError(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	svc.removeErr = errors.New("remove failed")
	tr, cleanup := connectUserAuthFactorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.RemoveAuthFactor(context.Background(), &transport.AuthFactorRemoveRequest{FactorID: "x"}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// RotateAuthFactor
// ---------------------------------------------------------------------------

func TestRotateAuthFactor_Success(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	tr, cleanup := connectUserAuthFactorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.RotateAuthFactor(context.Background(), &transport.AuthFactorRotateRequest{
		OldFactorID: "f-1",
		NewFactor: transport.AuthFactorAddRequest{
			UserID:     42,
			Type:       "fido2",
			Credential: []byte("new"),
		},
	})
	if err != nil {
		t.Fatalf("RotateAuthFactor: %v", err)
	}
	if resp.NewFactor.ID == "" {
		t.Error("expected non-empty new factor id")
	}
}

func TestRotateAuthFactor_ServiceError(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	svc.rotateErr = errors.New("rotate failed")
	tr, cleanup := connectUserAuthFactorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.RotateAuthFactor(context.Background(), &transport.AuthFactorRotateRequest{
		OldFactorID: "x", NewFactor: transport.AuthFactorAddRequest{Type: "mtls", Credential: []byte("x")},
	}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// RelabelAuthFactor
// ---------------------------------------------------------------------------

func TestRelabelAuthFactor_Success(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	tr, cleanup := connectUserAuthFactorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.RelabelAuthFactor(context.Background(), &transport.AuthFactorRelabelRequest{
		FactorID: "f-1",
		Label:    "renamed",
	})
	if err != nil {
		t.Fatalf("RelabelAuthFactor: %v", err)
	}
	if resp.Factor.ID == "" {
		t.Error("expected non-empty id")
	}
}

func TestRelabelAuthFactor_ServiceError(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	svc.relabelErr = errors.New("relabel failed")
	tr, cleanup := connectUserAuthFactorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.RelabelAuthFactor(context.Background(), &transport.AuthFactorRelabelRequest{FactorID: "x", Label: "y"}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// LoginAuthFactor
// ---------------------------------------------------------------------------

func TestLoginAuthFactor_Success(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	tr, cleanup := connectUserAuthFactorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.LoginAuthFactor(context.Background(), &transport.AuthFactorLoginRequest{
		Type:      "mtls",
		LookupKey: []byte("hashed"),
	})
	if err != nil {
		t.Fatalf("LoginAuthFactor: %v", err)
	}
	if resp.UserID == 0 {
		t.Error("expected non-zero user id")
	}
}

func TestLoginAuthFactor_ServiceError(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	svc.lookupErr = errors.New("not found")
	tr, cleanup := connectUserAuthFactorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.LoginAuthFactor(context.Background(), &transport.AuthFactorLoginRequest{
		Type: "mtls", LookupKey: []byte("x"),
	}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// mcpRecordToTransport
// ---------------------------------------------------------------------------

func TestMcpRecordToTransport(t *testing.T) {
	in := &mcpFactorRecord{
		ID:           "f-1",
		UserID:       42,
		Type:         "mtls",
		Label:        "primary",
		Credential:   "aGFzaA==", // base64 of "hash"
		Metadata:     map[string]string{"k": "v"},
		RegisteredAt: "2026-01-01T00:00:00Z",
		LastUsedAt:   "2026-02-01T00:00:00Z",
		RevokedAt:    "",
		CreatedBy:    "self",
	}
	out := mcpRecordToTransport(in)
	if out.ID != "f-1" || out.UserID != 42 || out.Type != "mtls" {
		t.Errorf("identity: %+v", out)
	}
	if string(out.Credential) != "hash" {
		t.Errorf("credential: %q", string(out.Credential))
	}
	if out.LastUsedAt == nil {
		t.Error("expected non-nil LastUsedAt")
	}
}
