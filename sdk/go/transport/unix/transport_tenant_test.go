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

package unix

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	tenantpkg "github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Mock TenantServicer
// ---------------------------------------------------------------------------

// mockTenantBarrierSvc is an inspectable TenantServicer that lets tests
// assert on per-tenant barrier RPC invocations and inject errors. It is
// the SDK-side equivalent of the Unix server-side stub.
type mockTenantBarrierSvc struct {
	tenant.TenantServicer
	initFn   func(ctx context.Context, tenantID, passphrase string, threshold, shares int) error
	unsealFn func(ctx context.Context, tenantID string, key []byte) error
	sealFn   func(ctx context.Context, tenantID string) error
	statusFn func(ctx context.Context, tenantID string) (*tenantpkg.TenantBarrierStatus, error)
	// initResults answers successive inits in order, as a Shamir init's
	// two requests; past its end an init answers as TenantBarrierInit does.
	initResults []*tenantpkg.TenantBarrierInitResult
	initCalls   atomic.Int32
}

// CreateTenant is unused by the barrier transport tests.
func (m *mockTenantBarrierSvc) CreateTenant(_ context.Context, _, _ string, _ tenantpkg.IsolationType, _ ...tenantpkg.CreateTenantOption) (*tenantpkg.TenantInfo, error) {
	return nil, nil
}
func (m *mockTenantBarrierSvc) GetTenant(_ context.Context, _ string) (*tenantpkg.TenantInfo, error) {
	return nil, nil
}
func (m *mockTenantBarrierSvc) ListDeletedTenants(_ context.Context, _ string, _ int) (*tenantpkg.DeletedTenantsPage, error) {
	return &tenantpkg.DeletedTenantsPage{}, nil
}

func (m *mockTenantBarrierSvc) ReconcileDeletedTenants(_ context.Context) (*tenantpkg.ReconcileReport, error) {
	return &tenantpkg.ReconcileReport{}, nil
}

func (m *mockTenantBarrierSvc) ListTenants(_ context.Context) ([]tenantpkg.TenantInfo, error) {
	return nil, nil
}

// UndoCreateTenant undoes a create the way this double deletes one.
func (m *mockTenantBarrierSvc) UndoCreateTenant(ctx context.Context, tenantID string) error {
	return m.DeleteTenant(ctx, tenantID)
}

func (m *mockTenantBarrierSvc) DeleteTenant(_ context.Context, _ string) error { return nil }
func (m *mockTenantBarrierSvc) UpdateTenant(_ context.Context, _ string, _ tenantpkg.TenantPatch) (*tenantpkg.TenantInfo, error) {
	return nil, nil
}
func (m *mockTenantBarrierSvc) UpdateQuotas(_ context.Context, _ string, _ *quota.Quotas) (*tenantpkg.TenantInfo, error) {
	return nil, nil
}

func (m *mockTenantBarrierSvc) UpdateEntitlements(ctx context.Context, id string, e *quota.Entitlements) (*tenantpkg.TenantInfo, error) {
	return &tenantpkg.TenantInfo{ID: id, Entitlements: e}, nil
}

// TenantBarrierInit dispatches to initFn or returns nil.
func (m *mockTenantBarrierSvc) TenantBarrierInit(ctx context.Context, tenantID string, passphrase []byte, threshold, shares int) (*tenantpkg.TenantBarrierInitResult, error) {
	if m.initFn != nil {
		if err := m.initFn(ctx, tenantID, string(passphrase), threshold, shares); err != nil {
			return nil, err
		}
	}
	return &tenantpkg.TenantBarrierInitResult{TenantID: tenantID}, nil
}

// TenantBarrierInitWithStrategy delegates to TenantBarrierInit.
func (m *mockTenantBarrierSvc) TenantBarrierInitWithStrategy(ctx context.Context, tenantID string, passphrase []byte, threshold, shares int, _ seal.StrategyID, _ *tenantpkg.TenantBarrierShamirRoster, _ ...tenantpkg.TenantBarrierInitOption) (*tenantpkg.TenantBarrierInitResult, error) {
	if n := int(m.initCalls.Add(1)) - 1; n < len(m.initResults) {
		return m.initResults[n], nil
	}
	return m.TenantBarrierInit(ctx, tenantID, passphrase, threshold, shares)
}

// TenantBarrierUnseal dispatches to unsealFn or returns nil.
func (m *mockTenantBarrierSvc) TenantBarrierUnseal(ctx context.Context, tenantID string, key []byte) error {
	if m.unsealFn != nil {
		return m.unsealFn(ctx, tenantID, key)
	}
	return nil
}

// TenantBarrierSeal dispatches to sealFn or returns nil.
func (m *mockTenantBarrierSvc) TenantBarrierSeal(ctx context.Context, tenantID string) error {
	if m.sealFn != nil {
		return m.sealFn(ctx, tenantID)
	}
	return nil
}

// TenantBarrierStatus dispatches to statusFn or returns a default reply.
func (m *mockTenantBarrierSvc) TenantBarrierStatus(ctx context.Context, tenantID string) (*tenantpkg.TenantBarrierStatus, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx, tenantID)
	}
	return &tenantpkg.TenantBarrierStatus{
		TenantID:    tenantID,
		Sealed:      false,
		Initialized: true,
		Strategy:    "passphrase",
	}, nil
}
func (m *mockTenantBarrierSvc) IncrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenantpkg.TenantInfo, error) {
	return nil, nil
}
func (m *mockTenantBarrierSvc) DecrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenantpkg.TenantInfo, error) {
	return nil, nil
}
func (m *mockTenantBarrierSvc) GetQuotaUsage(_ context.Context, _ string, _ quota.Resource) (int64, error) {
	return 0, nil
}

func (m *mockTenantBarrierSvc) TenantBarrierOfficerRegister(_ context.Context, _ string, _ *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	return &ceremony.RegisterOfficerResult{}, nil
}
func (m *mockTenantBarrierSvc) TenantBarrierOfficerList(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
	return nil, nil
}
func (m *mockTenantBarrierSvc) TenantBarrierShareClaimBegin(_ context.Context, _, _, _ string) (*ceremony.ChallengeResponse, error) {
	return &ceremony.ChallengeResponse{}, nil
}
func (m *mockTenantBarrierSvc) TenantBarrierShareClaimComplete(_ context.Context, _, _, _ string, _ []byte, _ string) (*ceremony.ShareClaimResult, error) {
	return &ceremony.ShareClaimResult{}, nil
}

// Compile-time interface assertion.
var _ tenantpkg.TenantServicer = (*mockTenantBarrierSvc)(nil)

// ---------------------------------------------------------------------------
// Test server bootstrap with the per-tenant barrier service registered
// ---------------------------------------------------------------------------

// startUnixTestServerWithBarrier starts a Unix gRPC server with both the
// KV/admin services and the per-tenant barrier service registered, returning
// the socket path and a cleanup function.
func startUnixTestServerWithBarrier(t *testing.T, barrierSvc tenantpkg.TenantServicer) (string, func()) {
	t.Helper()
	socketPath := tempSocketPath(t)
	srv := unixserver.NewServer(newMockKVService(), newMockAdminService(), socketPath,
		unixserver.WithTenantBarrierService(barrierSvc),
		unixserver.WithTLS(testUnixServerTLS),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("failed to start Unix server: %v", err)
	}
	return socketPath, func() { srv.Stop() }
}

// connectTransportWithBarrier dials a fresh transport against a server with
// the per-tenant barrier service registered.
func connectTransportWithBarrier(t *testing.T, barrierSvc tenantpkg.TenantServicer) (*Transport, func()) {
	t.Helper()
	socketPath, stopSrv := startUnixTestServerWithBarrier(t, barrierSvc)

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
// Round-trip success cases
// ---------------------------------------------------------------------------

func TestTransport_TenantBarrierInit_RoundTrip(t *testing.T) {
	var gotID string
	var gotT, gotS int
	mock := &mockTenantBarrierSvc{
		initFn: func(_ context.Context, tenantID, _ string, threshold, shares int) error {
			gotID, gotT, gotS = tenantID, threshold, shares
			return nil
		},
	}
	tr, cleanup := connectTransportWithBarrier(t, mock)
	defer cleanup()

	_, err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{
		TenantID:  "alice",
		Threshold: 3,
		Shares:    5,
	})
	if err != nil {
		t.Fatalf("TenantBarrierInit: %v", err)
	}
	if gotID != "alice" || gotT != 3 || gotS != 5 {
		t.Fatalf("server received (%q,%d,%d), want (alice,3,5)", gotID, gotT, gotS)
	}
}

// A Shamir init round-trips its split: the first response's roster and
// check value reach the SDK, which makes the second request and returns its
// response with the first's holders.
func TestTransport_TenantBarrierInit_ShamirRoundTrip(t *testing.T) {
	mock := &mockTenantBarrierSvc{initResults: []*tenantpkg.TenantBarrierInitResult{
		{TenantID: "acme", Strategy: "shamir", Threshold: 2, TotalShares: 2,
			Holders: []tenantpkg.TenantBarrierHolderCert{{Username: "alice"}},
			Roster:  []byte("roster"), UnsealKeyCheck: []byte("check")},
		{TenantID: "acme", Strategy: "shamir", Threshold: 2, TotalShares: 2, ShareCount: 2,
			Roster: []byte("roster"), UnsealKeyCheck: []byte("check")},
	}}
	tr, cleanup := connectTransportWithBarrier(t, mock)
	defer cleanup()

	resp, err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{TenantID: "acme", Strategy: "shamir"})
	if err != nil {
		t.Fatalf("TenantBarrierInit: %v", err)
	}
	if got := mock.initCalls.Load(); got != 2 {
		t.Fatalf("init requests = %d, want 2", got)
	}
	if resp.ShareCount != 2 || string(resp.Roster) != "roster" || string(resp.UnsealKeyCheck) != "check" ||
		len(resp.Holders) != 1 || resp.Holders[0].Username != "alice" {
		t.Fatalf("resp = %+v, want the completed split with the first response's holders", resp)
	}
}

// A first response without the split refuses the init before a second request.
func TestTransport_TenantBarrierInit_ShamirWithoutSplitRefused(t *testing.T) {
	mock := &mockTenantBarrierSvc{initResults: []*tenantpkg.TenantBarrierInitResult{{TenantID: "acme", Strategy: "shamir"}}}
	tr, cleanup := connectTransportWithBarrier(t, mock)
	defer cleanup()

	_, err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{TenantID: "acme", Strategy: "shamir"})
	var missing *transport.ShamirSplitMissingError
	if !errors.As(err, &missing) || mock.initCalls.Load() != 1 {
		t.Fatalf("TenantBarrierInit = %v after %d requests, want ShamirSplitMissingError after one", err, mock.initCalls.Load())
	}
}

func TestTransport_TenantBarrierUnseal_RoundTrip_KeyTakesPrecedence(t *testing.T) {
	var gotID string
	var gotKey []byte
	mock := &mockTenantBarrierSvc{
		unsealFn: func(_ context.Context, tenantID string, key []byte) error {
			gotID = string(tenantID)
			gotKey = key
			return nil
		},
	}
	tr, cleanup := connectTransportWithBarrier(t, mock)
	defer cleanup()

	err := tr.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{
		TenantID: "bob",
		Key:      []byte("kkkk"),
	})
	if err != nil {
		t.Fatalf("TenantBarrierUnseal: %v", err)
	}
	if gotID != "bob" {
		t.Fatalf("got tenant %q, want bob", gotID)
	}
	if !bytes.Equal(gotKey, []byte("kkkk")) {
		t.Fatalf("got key %q, want kkkk", gotKey)
	}
}

func TestTransport_TenantBarrierSeal_RoundTrip(t *testing.T) {
	var gotID string
	mock := &mockTenantBarrierSvc{
		sealFn: func(_ context.Context, tenantID string) error {
			gotID = tenantID
			return nil
		},
	}
	tr, cleanup := connectTransportWithBarrier(t, mock)
	defer cleanup()

	if err := tr.TenantBarrierSeal(context.Background(), &transport.TenantBarrierSealRequest{
		TenantID: "dan",
	}); err != nil {
		t.Fatalf("TenantBarrierSeal: %v", err)
	}
	if gotID != "dan" {
		t.Fatalf("got tenant %q, want dan", gotID)
	}
}

// servingNodes is a status's serving-node list, and wantServingNodes its
// conversion.
var (
	servingNodes     = []tenant.BarrierNode{{Node: "n1:7000", Endpoints: map[string]string{"rest": "n1:8443"}}, {Node: "n2:7000"}}
	wantServingNodes = []transport.TenantBarrierNode{{Node: "n1:7000", Endpoints: map[string]string{"rest": "n1:8443"}}, {Node: "n2:7000"}}
)

func TestTransport_TenantBarrierStatus_RoundTrip(t *testing.T) {
	mock := &mockTenantBarrierSvc{
		statusFn: func(_ context.Context, tenantID string) (*tenantpkg.TenantBarrierStatus, error) {
			return &tenantpkg.TenantBarrierStatus{
				TenantID:       tenantID,
				Sealed:         true,
				Initialized:    true,
				Strategy:       "shamir",
				HardwareBacked: true,
				KeyFingerprint: "0a1b2c",
				Nodes:          servingNodes,
			}, nil
		},
	}
	tr, cleanup := connectTransportWithBarrier(t, mock)
	defer cleanup()

	resp, err := tr.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{
		TenantID: "eve",
	})
	if err != nil {
		t.Fatalf("TenantBarrierStatus: %v", err)
	}
	if resp.KeyFingerprint != "0a1b2c" {
		t.Fatalf("got KeyFingerprint=%q, want 0a1b2c", resp.KeyFingerprint)
	}
	if !reflect.DeepEqual(resp.Nodes, wantServingNodes) {
		t.Fatalf("Nodes = %+v, want %+v", resp.Nodes, wantServingNodes)
	}
	if resp.TenantID != "eve" {
		t.Fatalf("got tenant %q, want eve", resp.TenantID)
	}
	if !resp.Sealed || !resp.Initialized {
		t.Fatalf("got Sealed=%v Initialized=%v, want both true", resp.Sealed, resp.Initialized)
	}
	if resp.Strategy != "shamir" || !resp.HardwareBacked {
		t.Fatalf("got Strategy=%q HardwareBacked=%v, want shamir,true", resp.Strategy, resp.HardwareBacked)
	}
}

// ---------------------------------------------------------------------------
// Argument-validation cases
// ---------------------------------------------------------------------------

func TestTransport_TenantBarrierInit_NilRequest(t *testing.T) {
	tr, cleanup := connectTransportWithBarrier(t, &mockTenantBarrierSvc{})
	defer cleanup()
	if _, err := tr.TenantBarrierInit(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestTransport_TenantBarrierUnseal_NilRequest(t *testing.T) {
	tr, cleanup := connectTransportWithBarrier(t, &mockTenantBarrierSvc{})
	defer cleanup()
	if err := tr.TenantBarrierUnseal(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestTransport_TenantBarrierSeal_NilRequest(t *testing.T) {
	tr, cleanup := connectTransportWithBarrier(t, &mockTenantBarrierSvc{})
	defer cleanup()
	if err := tr.TenantBarrierSeal(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestTransport_TenantBarrierStatus_NilRequest(t *testing.T) {
	tr, cleanup := connectTransportWithBarrier(t, &mockTenantBarrierSvc{})
	defer cleanup()
	if _, err := tr.TenantBarrierStatus(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil request")
	}
}

// ---------------------------------------------------------------------------
// Server-side error propagation
// ---------------------------------------------------------------------------

func TestTransport_TenantBarrierInit_ServerError(t *testing.T) {
	mock := &mockTenantBarrierSvc{
		initFn: func(_ context.Context, _, _ string, _, _ int) error {
			return tenantpkg.ErrTenantNotFound
		},
	}
	tr, cleanup := connectTransportWithBarrier(t, mock)
	defer cleanup()

	_, err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{TenantID: "ghost"})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestTransport_TenantBarrierUnseal_ServerError(t *testing.T) {
	mock := &mockTenantBarrierSvc{
		unsealFn: func(_ context.Context, _ string, _ []byte) error {
			return tenantpkg.ErrTenantNotFound
		},
	}
	tr, cleanup := connectTransportWithBarrier(t, mock)
	defer cleanup()

	err := tr.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{TenantID: "ghost"})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestTransport_TenantBarrierSeal_ServerError(t *testing.T) {
	mock := &mockTenantBarrierSvc{
		sealFn: func(_ context.Context, _ string) error {
			return tenantpkg.ErrTenantNotFound
		},
	}
	tr, cleanup := connectTransportWithBarrier(t, mock)
	defer cleanup()

	err := tr.TenantBarrierSeal(context.Background(), &transport.TenantBarrierSealRequest{TenantID: "ghost"})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestTransport_TenantBarrierStatus_ServerError(t *testing.T) {
	mock := &mockTenantBarrierSvc{
		statusFn: func(_ context.Context, _ string) (*tenantpkg.TenantBarrierStatus, error) {
			return nil, tenantpkg.ErrTenantNotFound
		},
	}
	tr, cleanup := connectTransportWithBarrier(t, mock)
	defer cleanup()

	_, err := tr.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "ghost"})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

// ---------------------------------------------------------------------------
// Closed-transport guard: every barrier method must short-circuit
// ---------------------------------------------------------------------------

func TestTransport_TenantBarrier_ClosedTransport(t *testing.T) {
	tr, cleanup := connectTransportWithBarrier(t, &mockTenantBarrierSvc{})
	defer cleanup()

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	cases := []struct {
		name string
		fn   func() error
	}{
		{"Init", func() error {
			_, err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{TenantID: "x"})
			return err
		}},
		{"Unseal", func() error {
			return tr.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{TenantID: "x"})
		}},
		{"Seal", func() error {
			return tr.TenantBarrierSeal(context.Background(), &transport.TenantBarrierSealRequest{TenantID: "x"})
		}},
		{"Status", func() error {
			_, err := tr.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "x"})
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

func (m *mockTenantBarrierSvc) CheckBarrierMode(_ context.Context, _ string, _ seal.StrategyID) error {
	return nil
}
