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

package grpc

import (
	"context"
	"errors"
	"net"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	tenantpkg "github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// fakeTenantServicer is a minimal in-memory tenant.TenantServicer used to
// drive the per-tenant barrier round-trip tests over a real gRPC connection.
// Only the four barrier methods are exercised — the CRUD methods return zero
// values that satisfy the interface contract.
// ---------------------------------------------------------------------------

type fakeTenantServicer struct {
	tenant.TenantServicer
	initFn   func(ctx context.Context, id, passphrase string, threshold, shares int) error
	unsealFn func(ctx context.Context, id string, key []byte) error
	sealFn   func(ctx context.Context, id string) error
	statusFn func(ctx context.Context, id string) (*tenantpkg.TenantBarrierStatus, error)
	// initResults answers successive inits in order, as a Shamir init's
	// two requests; past its end an init answers with the tenant ID alone.
	initResults []*tenantpkg.TenantBarrierInitResult
	initCalls   atomic.Int32
}

var _ tenantpkg.TenantServicer = (*fakeTenantServicer)(nil)

func (f *fakeTenantServicer) CreateTenant(_ context.Context, id, name string, iso tenantpkg.IsolationType, _ ...tenantpkg.CreateTenantOption) (*tenantpkg.TenantInfo, error) {
	return &tenantpkg.TenantInfo{ID: id, Name: name, IsolationType: iso}, nil
}
func (f *fakeTenantServicer) GetTenant(_ context.Context, id string) (*tenantpkg.TenantInfo, error) {
	return &tenantpkg.TenantInfo{ID: id}, nil
}
func (f *fakeTenantServicer) ListDeletedTenants(_ context.Context, _ string, _ int) (*tenantpkg.DeletedTenantsPage, error) {
	return &tenantpkg.DeletedTenantsPage{}, nil
}

func (f *fakeTenantServicer) ReconcileDeletedTenants(_ context.Context) (*tenantpkg.ReconcileReport, error) {
	return &tenantpkg.ReconcileReport{}, nil
}

func (f *fakeTenantServicer) ListTenants(_ context.Context) ([]tenantpkg.TenantInfo, error) {
	return nil, nil
}

// UndoCreateTenant undoes a create the way this double deletes one.
func (f *fakeTenantServicer) UndoCreateTenant(ctx context.Context, tenantID string) error {
	return f.DeleteTenant(ctx, tenantID)
}

func (f *fakeTenantServicer) DeleteTenant(_ context.Context, _ string) error {
	return nil
}
func (f *fakeTenantServicer) UpdateTenant(_ context.Context, id string, _ tenantpkg.TenantPatch) (*tenantpkg.TenantInfo, error) {
	return &tenantpkg.TenantInfo{ID: id}, nil
}
func (f *fakeTenantServicer) UpdateQuotas(_ context.Context, id string, q *quota.Quotas) (*tenantpkg.TenantInfo, error) {
	return &tenantpkg.TenantInfo{ID: id, Quotas: q}, nil
}

func (f *fakeTenantServicer) UpdateEntitlements(ctx context.Context, id string, e *quota.Entitlements) (*tenantpkg.TenantInfo, error) {
	return &tenantpkg.TenantInfo{ID: id, Entitlements: e}, nil
}
func (f *fakeTenantServicer) TenantBarrierInitWithStrategy(ctx context.Context, id string, passphrase []byte, threshold, shares int, _ seal.StrategyID, _ *tenantpkg.TenantBarrierShamirRoster, _ ...tenantpkg.TenantBarrierInitOption) (*tenantpkg.TenantBarrierInitResult, error) {
	if err := f.callInitFn(ctx, id, string(passphrase), threshold, shares); err != nil {
		return nil, err
	}
	if n := int(f.initCalls.Add(1)) - 1; n < len(f.initResults) {
		return f.initResults[n], nil
	}
	return &tenantpkg.TenantBarrierInitResult{TenantID: id}, nil
}

func (f *fakeTenantServicer) TenantBarrierInit(ctx context.Context, id string, passphrase []byte, threshold, shares int) (*tenantpkg.TenantBarrierInitResult, error) {
	if err := f.callInitFn(ctx, id, string(passphrase), threshold, shares); err != nil {
		return nil, err
	}
	return &tenantpkg.TenantBarrierInitResult{TenantID: id}, nil
}

func (f *fakeTenantServicer) callInitFn(ctx context.Context, id, passphrase string, threshold, shares int) error {
	if f.initFn != nil {
		return f.initFn(ctx, id, passphrase, threshold, shares)
	}
	return nil
}
func (f *fakeTenantServicer) TenantBarrierUnseal(ctx context.Context, id string, key []byte) error {
	if f.unsealFn != nil {
		return f.unsealFn(ctx, id, key)
	}
	return nil
}
func (f *fakeTenantServicer) TenantBarrierSeal(ctx context.Context, id string) error {
	if f.sealFn != nil {
		return f.sealFn(ctx, id)
	}
	return nil
}
func (f *fakeTenantServicer) TenantBarrierStatus(ctx context.Context, id string) (*tenantpkg.TenantBarrierStatus, error) {
	if f.statusFn != nil {
		return f.statusFn(ctx, id)
	}
	return &tenantpkg.TenantBarrierStatus{TenantID: id, Sealed: true, Initialized: false}, nil
}
func (f *fakeTenantServicer) IncrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenantpkg.TenantInfo, error) {
	return nil, nil
}
func (f *fakeTenantServicer) DecrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenantpkg.TenantInfo, error) {
	return nil, nil
}
func (f *fakeTenantServicer) GetQuotaUsage(_ context.Context, _ string, _ quota.Resource) (int64, error) {
	return 0, nil
}

func (f *fakeTenantServicer) TenantBarrierOfficerRegister(_ context.Context, _ string, _ *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	return &ceremony.RegisterOfficerResult{}, nil
}
func (f *fakeTenantServicer) TenantBarrierOfficerList(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
	return nil, nil
}
func (f *fakeTenantServicer) TenantBarrierShareClaimBegin(_ context.Context, _, _, _ string) (*ceremony.ChallengeResponse, error) {
	return &ceremony.ChallengeResponse{}, nil
}
func (f *fakeTenantServicer) TenantBarrierShareClaimComplete(_ context.Context, _, _, _ string, _ []byte, _ string) (*ceremony.ShareClaimResult, error) {
	return &ceremony.ShareClaimResult{}, nil
}

// ---------------------------------------------------------------------------
// startTenantBarrierServer spins up a bare gRPC server that registers ONLY
// the per-tenant barrier service and returns the listener address plus a
// cleanup function. The transport is connected without a health check to
// avoid pulling in QRDBService.
// ---------------------------------------------------------------------------

func startTenantBarrierServer(t *testing.T, svc tenantpkg.TenantServicer) (string, func()) {
	t.Helper()
	srv := googlegrpc.NewServer()
	grpcpkg.RegisterTenantBarrierService(srv, svc, nil)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()

	return lis.Addr().String(), func() { srv.Stop() }
}

// connectBareTransport returns a Transport whose conn is dialed against the
// given address. It bypasses Connect() so we don't require the QRDBService
// health check to be registered on the test server.
func connectBareTransport(t *testing.T, addr string) (*Transport, func()) {
	t.Helper()
	conn, err := googlegrpc.NewClient(addr, googlegrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	tr := &Transport{
		config: &transport.Config{Address: addr},
		conn:   conn,
		// closed defaults to zero-value (false) which is the desired open state.
	}
	cleanup := func() {
		_ = conn.Close()
	}
	return tr, cleanup
}

// ---------------------------------------------------------------------------
// TenantBarrierInit
// ---------------------------------------------------------------------------

func TestTransport_TenantBarrierInit_Success(t *testing.T) {
	captured := struct {
		id        string
		threshold int
		shares    int
	}{}
	svc := &fakeTenantServicer{
		initFn: func(_ context.Context, id, _ string, th, sh int) error {
			captured.id, captured.threshold, captured.shares = id, th, sh
			return nil
		},
	}
	addr, stop := startTenantBarrierServer(t, svc)
	defer stop()

	tr, cleanup := connectBareTransport(t, addr)
	defer cleanup()

	_, err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{
		TenantID: "acme", Threshold: 3, Shares: 5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.id != "acme" || captured.threshold != 3 || captured.shares != 5 {
		t.Errorf("unexpected captured args: %+v", captured)
	}
}

// A Shamir init round-trips its split: the first response's roster and
// check value reach the SDK, which makes the second request and returns its
// response with the first's holders.
func TestTransport_TenantBarrierInit_ShamirRoundTrip(t *testing.T) {
	svc := &fakeTenantServicer{initResults: []*tenantpkg.TenantBarrierInitResult{
		{TenantID: "acme", Strategy: "shamir", Threshold: 2, TotalShares: 2,
			Holders: []tenantpkg.TenantBarrierHolderCert{{Username: "alice"}},
			Roster:  []byte("roster"), UnsealKeyCheck: []byte("check")},
		{TenantID: "acme", Strategy: "shamir", Threshold: 2, TotalShares: 2, ShareCount: 2,
			Roster: []byte("roster"), UnsealKeyCheck: []byte("check")},
	}}
	addr, stop := startTenantBarrierServer(t, svc)
	defer stop()
	tr, cleanup := connectBareTransport(t, addr)
	defer cleanup()

	resp, err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{TenantID: "acme", Strategy: "shamir"})
	if err != nil {
		t.Fatalf("TenantBarrierInit: %v", err)
	}
	if got := svc.initCalls.Load(); got != 2 {
		t.Fatalf("init requests = %d, want 2", got)
	}
	if resp.ShareCount != 2 || string(resp.Roster) != "roster" || string(resp.UnsealKeyCheck) != "check" ||
		len(resp.Holders) != 1 || resp.Holders[0].Username != "alice" {
		t.Fatalf("resp = %+v, want the completed split with the first response's holders", resp)
	}
}

// A first response without the split refuses the init before a second request.
func TestTransport_TenantBarrierInit_ShamirWithoutSplitRefused(t *testing.T) {
	svc := &fakeTenantServicer{initResults: []*tenantpkg.TenantBarrierInitResult{{TenantID: "acme", Strategy: "shamir"}}}
	addr, stop := startTenantBarrierServer(t, svc)
	defer stop()
	tr, cleanup := connectBareTransport(t, addr)
	defer cleanup()

	_, err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{TenantID: "acme", Strategy: "shamir"})
	var missing *transport.ShamirSplitMissingError
	if !errors.As(err, &missing) || svc.initCalls.Load() != 1 {
		t.Fatalf("TenantBarrierInit = %v after %d requests, want ShamirSplitMissingError after one", err, svc.initCalls.Load())
	}
}

func TestTransport_TenantBarrierInit_NilRequest(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	_, err := tr.TenantBarrierInit(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestTransport_TenantBarrierInit_AfterClose(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	tr.closed.Store(true)
	_, err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{TenantID: "acme"})
	if err == nil {
		t.Fatal("expected ClosedError")
	}
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_TenantBarrierInit_ServerError(t *testing.T) {
	svc := &fakeTenantServicer{
		initFn: func(_ context.Context, _, _ string, _, _ int) error {
			return tenantpkg.ErrTenantNotFound
		},
	}
	addr, stop := startTenantBarrierServer(t, svc)
	defer stop()

	tr, cleanup := connectBareTransport(t, addr)
	defer cleanup()

	_, err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{TenantID: "missing"})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// TenantBarrierUnseal
// ---------------------------------------------------------------------------

func TestTransport_TenantBarrierUnseal_Success_Key(t *testing.T) {
	var seen atomic.Pointer[[]byte]
	svc := &fakeTenantServicer{
		unsealFn: func(_ context.Context, _ string, key []byte) error {
			b := append([]byte(nil), key...)
			seen.Store(&b)
			return nil
		},
	}
	addr, stop := startTenantBarrierServer(t, svc)
	defer stop()
	tr, cleanup := connectBareTransport(t, addr)
	defer cleanup()

	err := tr.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{
		TenantID: "acme", Key: []byte("the-key"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := seen.Load()
	if got == nil || string(*got) != "the-key" {
		t.Errorf("expected server to receive 'the-key', got %v", got)
	}
}

func TestTransport_TenantBarrierUnseal_NilRequest(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	err := tr.TenantBarrierUnseal(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestTransport_TenantBarrierUnseal_AfterClose(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	tr.closed.Store(true)
	err := tr.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{TenantID: "acme"})
	if err == nil {
		t.Fatal("expected ClosedError")
	}
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestTransport_TenantBarrierUnseal_ServerError(t *testing.T) {
	svc := &fakeTenantServicer{
		unsealFn: func(_ context.Context, _ string, _ []byte) error {
			return &tenantpkg.BarrierError{TenantID: "acme", Op: "unseal", Err: errors.New("boom")}
		},
	}
	addr, stop := startTenantBarrierServer(t, svc)
	defer stop()
	tr, cleanup := connectBareTransport(t, addr)
	defer cleanup()

	err := tr.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{TenantID: "acme", Key: []byte("k")})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// TenantBarrierSeal
// ---------------------------------------------------------------------------

func TestTransport_TenantBarrierSeal_Success(t *testing.T) {
	called := atomic.Bool{}
	svc := &fakeTenantServicer{
		sealFn: func(_ context.Context, id string) error {
			called.Store(true)
			if id != "acme" {
				t.Errorf("expected id 'acme', got %q", id)
			}
			return nil
		},
	}
	addr, stop := startTenantBarrierServer(t, svc)
	defer stop()
	tr, cleanup := connectBareTransport(t, addr)
	defer cleanup()

	if err := tr.TenantBarrierSeal(context.Background(), &transport.TenantBarrierSealRequest{TenantID: "acme"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called.Load() {
		t.Error("expected sealFn to be called")
	}
}

func TestTransport_TenantBarrierSeal_NilRequest(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	err := tr.TenantBarrierSeal(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestTransport_TenantBarrierSeal_AfterClose(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	tr.closed.Store(true)
	err := tr.TenantBarrierSeal(context.Background(), &transport.TenantBarrierSealRequest{TenantID: "acme"})
	if err == nil {
		t.Fatal("expected ClosedError")
	}
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestTransport_TenantBarrierSeal_ServerError(t *testing.T) {
	svc := &fakeTenantServicer{
		sealFn: func(_ context.Context, _ string) error {
			return tenantpkg.ErrTenantNotFound
		},
	}
	addr, stop := startTenantBarrierServer(t, svc)
	defer stop()
	tr, cleanup := connectBareTransport(t, addr)
	defer cleanup()

	err := tr.TenantBarrierSeal(context.Background(), &transport.TenantBarrierSealRequest{TenantID: "missing"})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// TenantBarrierStatus
// ---------------------------------------------------------------------------

// servingNodes is a status's serving-node list, and wantServingNodes its
// conversion.
var (
	servingNodes     = []tenant.BarrierNode{{Node: "n1:7000", Endpoints: map[string]string{"rest": "n1:8443"}}, {Node: "n2:7000"}}
	wantServingNodes = []transport.TenantBarrierNode{{Node: "n1:7000", Endpoints: map[string]string{"rest": "n1:8443"}}, {Node: "n2:7000"}}
)

func TestTransport_TenantBarrierStatus_Success(t *testing.T) {
	svc := &fakeTenantServicer{
		statusFn: func(_ context.Context, id string) (*tenantpkg.TenantBarrierStatus, error) {
			return &tenantpkg.TenantBarrierStatus{
				TenantID:       id,
				Sealed:         false,
				Initialized:    true,
				Strategy:       "shamir",
				HardwareBacked: true,
				KeyFingerprint: "0a1b2c",
				Nodes:          servingNodes,
			}, nil
		},
	}
	addr, stop := startTenantBarrierServer(t, svc)
	defer stop()
	tr, cleanup := connectBareTransport(t, addr)
	defer cleanup()

	resp, err := tr.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.TenantID != "acme" || resp.Sealed || !resp.Initialized || resp.Strategy != "shamir" || !resp.HardwareBacked || resp.KeyFingerprint != "0a1b2c" || !reflect.DeepEqual(resp.Nodes, wantServingNodes) {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestTransport_TenantBarrierStatus_NilRequest(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	_, err := tr.TenantBarrierStatus(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestTransport_TenantBarrierStatus_AfterClose(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	tr.closed.Store(true)
	_, err := tr.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "acme"})
	if err == nil {
		t.Fatal("expected ClosedError")
	}
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestTransport_TenantBarrierStatus_ServerError(t *testing.T) {
	svc := &fakeTenantServicer{
		statusFn: func(_ context.Context, _ string) (*tenantpkg.TenantBarrierStatus, error) {
			return nil, &tenantpkg.StoreError{Op: "get", Err: errors.New("io")}
		},
	}
	addr, stop := startTenantBarrierServer(t, svc)
	defer stop()
	tr, cleanup := connectBareTransport(t, addr)
	defer cleanup()

	_, err := tr.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "acme"})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// Nil-request guard
// ---------------------------------------------------------------------------

func TestTenantBarrier_NilRequest(t *testing.T) {
	addr, stop := startTenantBarrierServer(t, &fakeTenantServicer{})
	defer stop()
	tr, cleanup := connectBareTransport(t, addr)
	defer cleanup()

	_, err := tr.TenantBarrierInit(context.Background(), nil)
	var nre *transport.NilRequestError
	if !errors.As(err, &nre) {
		t.Fatalf("expected *transport.NilRequestError, got %T: %v", err, err)
	}
	var re *transport.RequestError
	if !errors.As(err, &re) || re.Method != "TenantBarrierInit" {
		t.Errorf("expected RequestError carrying method TenantBarrierInit, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Round-trip with deadline propagation
// ---------------------------------------------------------------------------

func TestTransport_TenantBarrier_DeadlineRespected(t *testing.T) {
	// Server blocks until ctx is done, returning ctx.Err on cancellation.
	svc := &fakeTenantServicer{
		sealFn: func(ctx context.Context, _ string) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	addr, stop := startTenantBarrierServer(t, svc)
	defer stop()
	tr, cleanup := connectBareTransport(t, addr)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := tr.TenantBarrierSeal(ctx, &transport.TenantBarrierSealRequest{TenantID: "acme"})
	if err == nil {
		t.Fatal("expected deadline-exceeded error")
	}
}

func (m *fakeTenantServicer) CheckBarrierMode(_ context.Context, _ string, _ seal.StrategyID) error {
	return nil
}
