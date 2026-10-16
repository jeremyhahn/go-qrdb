// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Round-trip tests for the M14 user-side AuthFactor surface
// (transport.UserAuthFactorClient), closing the unix-vs-grpc SDK transport
// parity gap reported as: "auth factor: active transport does not
// implement the user-side AuthFactor service (M14)" when using the
// default (unix) transport, even though the server side already routed
// UserAuthFactorService over the Unix socket.

package unix

import (
	"context"
	"testing"
	"time"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockUserAuthFactorSvc
// ---------------------------------------------------------------------------

type mockUserAuthFactorSvc struct {
	addErr       error
	listResult   []*auth.AuthFactor
	listErr      error
	getResult    *auth.AuthFactor
	getErr       error
	removeErr    error
	deleteErr    error
	rotateErr    error
	relabelErr   error
	lookupResult *auth.AuthFactor
	lookupErr    error
}

var _ service.UserAuthFactorServicer = (*mockUserAuthFactorSvc)(nil)

func newMockUserAuthFactorSvc() *mockUserAuthFactorSvc {
	now := time.Unix(1700000000, 0)
	return &mockUserAuthFactorSvc{
		getResult: &auth.AuthFactor{
			ID:           "factor-1",
			UserID:       42,
			Type:         auth.AuthFactorType("totp"),
			Label:        "my-totp",
			Credential:   []byte("secret"),
			RegisteredAt: now,
		},
		lookupResult: &auth.AuthFactor{
			ID:           "factor-1",
			UserID:       42,
			Type:         auth.AuthFactorType("totp"),
			RegisteredAt: now,
		},
		listResult: []*auth.AuthFactor{
			{ID: "factor-1", UserID: 42, Type: "totp", RegisteredAt: now},
		},
	}
}

func (m *mockUserAuthFactorSvc) Add(_ context.Context, userID uint64, factor *auth.AuthFactor) error {
	if m.addErr != nil {
		return m.addErr
	}
	factor.ID = "factor-new"
	factor.UserID = userID
	factor.RegisteredAt = time.Unix(1700000000, 0)
	return nil
}
func (m *mockUserAuthFactorSvc) List(_ context.Context, _ uint64) ([]*auth.AuthFactor, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResult, nil
}
func (m *mockUserAuthFactorSvc) Get(_ context.Context, _ string) (*auth.AuthFactor, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.getResult, nil
}
func (m *mockUserAuthFactorSvc) LookupByCredentialKey(_ context.Context, _ auth.AuthFactorType, _ []byte) (*auth.AuthFactor, error) {
	if m.lookupErr != nil {
		return nil, m.lookupErr
	}
	return m.lookupResult, nil
}
func (m *mockUserAuthFactorSvc) Remove(_ context.Context, _ string, _ bool) error {
	return m.removeErr
}
func (m *mockUserAuthFactorSvc) Delete(_ context.Context, _ string, _ bool) error {
	return m.deleteErr
}
func (m *mockUserAuthFactorSvc) Rotate(_ context.Context, _ string, _ *auth.AuthFactor) error {
	return m.rotateErr
}
func (m *mockUserAuthFactorSvc) Relabel(_ context.Context, _ string, _ string) error {
	return m.relabelErr
}
func (m *mockUserAuthFactorSvc) MarkUsed(_ context.Context, _ string, _ time.Time) error {
	return nil
}

func (m *mockUserAuthFactorSvc) AddConditional(_ context.Context, _ uint64, _ *auth.AuthFactor, _ auth.FactorAddCondition) (uint64, error) {
	return 0, nil
}

func (m *mockUserAuthFactorSvc) FactorSetVersion(_ context.Context, _ uint64) (uint64, error) {
	return 0, nil
}

// ---------------------------------------------------------------------------
// AddAuthFactor
// ---------------------------------------------------------------------------

func TestUnixRT_AddAuthFactor(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithUserAuthFactorService(newMockUserAuthFactorSvc()))
	defer cleanup()

	resp, err := tr.AddAuthFactor(context.Background(), &transport.AuthFactorAddRequest{
		UserID: 1, Type: "mtls", Credential: []byte("c"),
	})
	if err != nil {
		t.Fatalf("AddAuthFactor: %v", err)
	}
	if resp.Factor.ID == "" {
		t.Errorf("got %+v, want non-empty factor ID", resp.Factor)
	}
}

func TestUnixRT_AddAuthFactor_Error(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	svc.addErr = errServiceFailed
	tr, cleanup := startAuthServer(t, unixserver.WithUserAuthFactorService(svc))
	defer cleanup()

	if _, err := tr.AddAuthFactor(context.Background(), &transport.AuthFactorAddRequest{UserID: 1, Type: "mtls"}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// ListAuthFactors
// ---------------------------------------------------------------------------

func TestUnixRT_ListAuthFactors(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithUserAuthFactorService(newMockUserAuthFactorSvc()))
	defer cleanup()

	resp, err := tr.ListAuthFactors(context.Background(), &transport.AuthFactorListRequest{UserID: 42})
	if err != nil {
		t.Fatalf("ListAuthFactors: %v", err)
	}
	if len(resp.Factors) != 1 || resp.Factors[0].ID != "factor-1" {
		t.Errorf("got %+v, want 1 factor factor-1", resp)
	}
}

func TestUnixRT_ListAuthFactors_Error(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	svc.listErr = errServiceFailed
	tr, cleanup := startAuthServer(t, unixserver.WithUserAuthFactorService(svc))
	defer cleanup()

	if _, err := tr.ListAuthFactors(context.Background(), &transport.AuthFactorListRequest{UserID: 42}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// GetAuthFactor
// ---------------------------------------------------------------------------

func TestUnixRT_GetAuthFactor(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithUserAuthFactorService(newMockUserAuthFactorSvc()))
	defer cleanup()

	resp, err := tr.GetAuthFactor(context.Background(), &transport.AuthFactorGetRequest{FactorID: "factor-1"})
	if err != nil {
		t.Fatalf("GetAuthFactor: %v", err)
	}
	if resp.Factor.ID != "factor-1" {
		t.Errorf("got %+v, want id=factor-1", resp.Factor)
	}
}

func TestUnixRT_GetAuthFactor_Error(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	svc.getErr = errServiceFailed
	tr, cleanup := startAuthServer(t, unixserver.WithUserAuthFactorService(svc))
	defer cleanup()

	if _, err := tr.GetAuthFactor(context.Background(), &transport.AuthFactorGetRequest{FactorID: "missing"}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// RemoveAuthFactor
// ---------------------------------------------------------------------------

func TestUnixRT_RemoveAuthFactor(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithUserAuthFactorService(newMockUserAuthFactorSvc()))
	defer cleanup()

	resp, err := tr.RemoveAuthFactor(context.Background(), &transport.AuthFactorRemoveRequest{FactorID: "factor-1"})
	if err != nil {
		t.Fatalf("RemoveAuthFactor: %v", err)
	}
	if resp.Status != "revoked" {
		t.Errorf("Status = %q, want revoked", resp.Status)
	}
}

func TestUnixRT_RemoveAuthFactor_Error(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	svc.removeErr = errServiceFailed
	tr, cleanup := startAuthServer(t, unixserver.WithUserAuthFactorService(svc))
	defer cleanup()

	if _, err := tr.RemoveAuthFactor(context.Background(), &transport.AuthFactorRemoveRequest{FactorID: "factor-1"}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// DeleteAuthFactor
// ---------------------------------------------------------------------------

func TestUnixRT_DeleteAuthFactor(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithUserAuthFactorService(newMockUserAuthFactorSvc()))
	defer cleanup()

	resp, err := tr.DeleteAuthFactor(context.Background(), &transport.AuthFactorDeleteRequest{FactorID: "factor-1", Force: true})
	if err != nil {
		t.Fatalf("DeleteAuthFactor: %v", err)
	}
	if resp.Status != "deleted" {
		t.Errorf("Status = %q, want deleted", resp.Status)
	}
}

func TestUnixRT_DeleteAuthFactor_Error(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	svc.deleteErr = errServiceFailed
	tr, cleanup := startAuthServer(t, unixserver.WithUserAuthFactorService(svc))
	defer cleanup()

	if _, err := tr.DeleteAuthFactor(context.Background(), &transport.AuthFactorDeleteRequest{FactorID: "factor-1"}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// RotateAuthFactor
// ---------------------------------------------------------------------------

func TestUnixRT_RotateAuthFactor(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithUserAuthFactorService(newMockUserAuthFactorSvc()))
	defer cleanup()

	resp, err := tr.RotateAuthFactor(context.Background(), &transport.AuthFactorRotateRequest{
		OldFactorID: "factor-1",
		NewFactor:   transport.AuthFactorAddRequest{UserID: 42, Type: "totp", Label: "new"},
	})
	if err != nil {
		t.Fatalf("RotateAuthFactor: %v", err)
	}
	if resp.NewFactor.UserID != 42 {
		t.Errorf("got %+v, want userID=42", resp.NewFactor)
	}
}

func TestUnixRT_RotateAuthFactor_Error(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	svc.rotateErr = errServiceFailed
	tr, cleanup := startAuthServer(t, unixserver.WithUserAuthFactorService(svc))
	defer cleanup()

	_, err := tr.RotateAuthFactor(context.Background(), &transport.AuthFactorRotateRequest{
		OldFactorID: "factor-1",
		NewFactor:   transport.AuthFactorAddRequest{},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// RelabelAuthFactor
// ---------------------------------------------------------------------------

func TestUnixRT_RelabelAuthFactor(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithUserAuthFactorService(newMockUserAuthFactorSvc()))
	defer cleanup()

	resp, err := tr.RelabelAuthFactor(context.Background(), &transport.AuthFactorRelabelRequest{FactorID: "factor-1", Label: "renamed"})
	if err != nil {
		t.Fatalf("RelabelAuthFactor: %v", err)
	}
	if resp.Factor.ID != "factor-1" {
		t.Errorf("got %+v, want id=factor-1", resp.Factor)
	}
}

func TestUnixRT_RelabelAuthFactor_Error(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	svc.relabelErr = errServiceFailed
	tr, cleanup := startAuthServer(t, unixserver.WithUserAuthFactorService(svc))
	defer cleanup()

	if _, err := tr.RelabelAuthFactor(context.Background(), &transport.AuthFactorRelabelRequest{FactorID: "factor-1", Label: "x"}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// LoginAuthFactor
// ---------------------------------------------------------------------------

func TestUnixRT_LoginAuthFactor(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithUserAuthFactorService(newMockUserAuthFactorSvc()))
	defer cleanup()

	resp, err := tr.LoginAuthFactor(context.Background(), &transport.AuthFactorLoginRequest{Type: "totp", LookupKey: []byte("key")})
	if err != nil {
		t.Fatalf("LoginAuthFactor: %v", err)
	}
	if resp.UserID != 42 || !resp.Active {
		t.Errorf("got %+v, want userID=42 active=true", resp)
	}
}

func TestUnixRT_LoginAuthFactor_Error(t *testing.T) {
	svc := newMockUserAuthFactorSvc()
	svc.lookupErr = errServiceFailed
	tr, cleanup := startAuthServer(t, unixserver.WithUserAuthFactorService(svc))
	defer cleanup()

	if _, err := tr.LoginAuthFactor(context.Background(), &transport.AuthFactorLoginRequest{Type: "totp", LookupKey: []byte("key")}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// pbUserFactorToTransport nil guard
// ---------------------------------------------------------------------------

func TestUnix_PbUserFactorToTransport_Nil(t *testing.T) {
	got := pbUserFactorToTransport(nil)
	if got.ID != "" || got.UserID != 0 {
		t.Errorf("pbUserFactorToTransport(nil) = %+v, want zero value", got)
	}
}
