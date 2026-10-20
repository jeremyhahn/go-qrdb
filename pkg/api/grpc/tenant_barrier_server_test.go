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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package grpc

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	tenantpkg "github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// startTenantBarrierTestServer brings up a bare grpc.Server with the tenant
// barrier service registered against the supplied servicer and returns a
// raw client connection plus a cleanup function.
// ---------------------------------------------------------------------------

func startTenantBarrierTestServer(t *testing.T, svc tenantpkg.TenantServicer) (*grpc.ClientConn, func()) {
	t.Helper()
	srv := grpc.NewServer()
	RegisterTenantBarrierService(srv, svc, nil)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		srv.Stop()
		t.Fatalf("grpc.NewClient: %v", err)
	}
	return conn, func() {
		_ = conn.Close()
		srv.Stop()
	}
}

// ---------------------------------------------------------------------------
// Init handler tests
// ---------------------------------------------------------------------------

func TestTenantBarrier_Init_Success(t *testing.T) {
	svc := newMockTenantService()
	captured := struct {
		id         string
		passphrase string
		threshold  int
		shares     int
	}{}
	svc.barrierInitFn = func(_ context.Context, id string, passphrase []byte, th, sh int) error {
		captured.id, captured.passphrase, captured.threshold, captured.shares = id, string(passphrase), th, sh
		return nil
	}

	conn := connect(t, svc)
	_, err := pb.NewTenantBarrierServiceClient(conn).InitComplete(context.Background(), &pb.TenantBarrierInitRequest{
		TenantId: "acme", Passphrase: []byte("supplied-secret"), Threshold: 3, Shares: 5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.id != "acme" || captured.passphrase != "supplied-secret" || captured.threshold != 3 || captured.shares != 5 {
		t.Errorf("unexpected captured args: %+v", captured)
	}
}

func TestTenantBarrier_Init_NilService(t *testing.T) {
	srv := &tenantBarrierServer{svc: nil}
	_, err := srv.InitComplete(context.Background(), &pb.TenantBarrierInitRequest{TenantId: "acme"})
	if err == nil {
		t.Fatal("expected error")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestTenantBarrier_Init_EmptyTenantID(t *testing.T) {
	svc := newMockTenantService()
	conn, cleanup := startTenantBarrierTestServer(t, svc)
	defer cleanup()

	_, err := pb.NewTenantBarrierServiceClient(conn).InitComplete(context.Background(), &pb.TenantBarrierInitRequest{TenantId: ""})
	if err == nil {
		t.Fatal("expected error")
	}
	if st, _ := status.FromError(err); st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestTenantBarrier_Init_NotFound(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierInitFn = func(_ context.Context, _ string, _ []byte, _, _ int) error {
		return tenantpkg.ErrTenantNotFound
	}
	conn, cleanup := startTenantBarrierTestServer(t, svc)
	defer cleanup()

	_, err := pb.NewTenantBarrierServiceClient(conn).InitComplete(context.Background(), &pb.TenantBarrierInitRequest{TenantId: "missing"})
	if err == nil {
		t.Fatal("expected error")
	}
	if st, _ := status.FromError(err); st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestTenantBarrier_Init_BarrierError(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierInitFn = func(_ context.Context, _ string, _ []byte, _, _ int) error {
		return &tenantpkg.BarrierError{TenantID: "acme", Op: "initialize", Err: errors.New("boom")}
	}
	conn, cleanup := startTenantBarrierTestServer(t, svc)
	defer cleanup()

	_, err := pb.NewTenantBarrierServiceClient(conn).InitComplete(context.Background(), &pb.TenantBarrierInitRequest{TenantId: "acme"})
	if err == nil {
		t.Fatal("expected error")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// InitBegin handler tests
// ---------------------------------------------------------------------------

// TestTenantBarrier_InitBegin_Unauthenticated_NoProofAsked proves a node
// without an auth middleware asks no proof: begin answers an empty nonce and
// no expiry, and a complete carrying no proof then succeeds.
func TestTenantBarrier_InitBegin_Unauthenticated_NoProofAsked(t *testing.T) {
	conn, cleanup := startTenantBarrierTestServer(t, newMockTenantService())
	defer cleanup()

	c := pb.NewTenantBarrierServiceClient(conn)
	resp, err := c.InitBegin(context.Background(), &pb.TenantBarrierInitBeginRequest{TenantId: "acme"})
	if err != nil {
		t.Fatalf("InitBegin: %v", err)
	}
	if len(resp.GetNonce()) != 0 || resp.GetExpires() != 0 {
		t.Fatalf("InitBegin = %+v; want no nonce and no expiry", resp)
	}
	if _, err := c.InitComplete(context.Background(), &pb.TenantBarrierInitRequest{TenantId: "acme"}); err != nil {
		t.Fatalf("InitComplete: %v", err)
	}
}

func TestTenantBarrier_InitBegin_NilService(t *testing.T) {
	srv := &tenantBarrierServer{svc: nil}
	_, err := srv.InitBegin(context.Background(), &pb.TenantBarrierInitBeginRequest{TenantId: "acme"})
	requireCode(t, err, codes.Unimplemented)
}

func TestTenantBarrier_InitBegin_EmptyTenantID(t *testing.T) {
	conn, cleanup := startTenantBarrierTestServer(t, newMockTenantService())
	defer cleanup()

	_, err := pb.NewTenantBarrierServiceClient(conn).InitBegin(context.Background(), &pb.TenantBarrierInitBeginRequest{})
	requireCode(t, err, codes.InvalidArgument)
}

// ---------------------------------------------------------------------------
// Unseal handler tests
// ---------------------------------------------------------------------------

func TestTenantBarrier_Unseal_Success_Key(t *testing.T) {
	svc := newMockTenantService()
	var receivedKey []byte
	svc.barrierUnsealFn = func(_ context.Context, _ string, key []byte) error {
		receivedKey = key
		return nil
	}
	conn, cleanup := startTenantBarrierTestServer(t, svc)
	defer cleanup()

	_, err := pb.NewTenantBarrierServiceClient(conn).Unseal(context.Background(), &pb.TenantBarrierUnsealRequest{
		TenantId: "acme", Key: []byte("super-secret"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(receivedKey) != "super-secret" {
		t.Errorf("expected key 'super-secret', got %q", receivedKey)
	}
}

func TestTenantBarrier_Unseal_Success_FallbackToShare(t *testing.T) {
	svc := newMockTenantService()
	var receivedKey []byte
	svc.barrierUnsealFn = func(_ context.Context, _ string, key []byte) error {
		receivedKey = key
		return nil
	}
	conn, cleanup := startTenantBarrierTestServer(t, svc)
	defer cleanup()

	// The new proto Unseal takes Key []byte; pass the share value as the key.
	_, err := pb.NewTenantBarrierServiceClient(conn).Unseal(context.Background(), &pb.TenantBarrierUnsealRequest{
		TenantId: "acme", Key: []byte("share-bytes"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(receivedKey) != "share-bytes" {
		t.Errorf("expected key 'share-bytes', got %q", receivedKey)
	}
}

func TestTenantBarrier_Unseal_NilService(t *testing.T) {
	srv := &tenantBarrierServer{svc: nil}
	_, err := srv.Unseal(context.Background(), &pb.TenantBarrierUnsealRequest{TenantId: "acme"})
	if err == nil {
		t.Fatal("expected error")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestTenantBarrier_Unseal_EmptyTenantID(t *testing.T) {
	svc := newMockTenantService()
	conn, cleanup := startTenantBarrierTestServer(t, svc)
	defer cleanup()

	_, err := pb.NewTenantBarrierServiceClient(conn).Unseal(context.Background(), &pb.TenantBarrierUnsealRequest{TenantId: ""})
	if err == nil {
		t.Fatal("expected error")
	}
	if st, _ := status.FromError(err); st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestTenantBarrier_Unseal_NotFound(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierUnsealFn = func(_ context.Context, _ string, _ []byte) error {
		return tenantpkg.ErrTenantNotFound
	}
	conn, cleanup := startTenantBarrierTestServer(t, svc)
	defer cleanup()

	_, err := pb.NewTenantBarrierServiceClient(conn).Unseal(context.Background(), &pb.TenantBarrierUnsealRequest{TenantId: "missing"})
	if err == nil {
		t.Fatal("expected error")
	}
	if st, _ := status.FromError(err); st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// Seal handler tests
// ---------------------------------------------------------------------------

func TestTenantBarrier_Seal_Success(t *testing.T) {
	svc := newMockTenantService()
	called := false
	svc.barrierSealFn = func(_ context.Context, id string) error {
		called = true
		if id != "acme" {
			t.Errorf("expected id 'acme', got %q", id)
		}
		return nil
	}
	conn, cleanup := startTenantBarrierTestServer(t, svc)
	defer cleanup()

	if _, err := pb.NewTenantBarrierServiceClient(conn).Seal(context.Background(), &pb.TenantBarrierSealRequest{TenantId: "acme"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected TenantBarrierSeal to be called")
	}
}

func TestTenantBarrier_Seal_NilService(t *testing.T) {
	srv := &tenantBarrierServer{svc: nil}
	_, err := srv.Seal(context.Background(), &pb.TenantBarrierSealRequest{TenantId: "acme"})
	if err == nil {
		t.Fatal("expected error")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestTenantBarrier_Seal_EmptyTenantID(t *testing.T) {
	svc := newMockTenantService()
	conn, cleanup := startTenantBarrierTestServer(t, svc)
	defer cleanup()

	_, err := pb.NewTenantBarrierServiceClient(conn).Seal(context.Background(), &pb.TenantBarrierSealRequest{TenantId: ""})
	if err == nil {
		t.Fatal("expected error")
	}
	if st, _ := status.FromError(err); st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestTenantBarrier_Seal_NotFound(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierSealFn = func(_ context.Context, _ string) error { return tenantpkg.ErrTenantNotFound }
	conn, cleanup := startTenantBarrierTestServer(t, svc)
	defer cleanup()

	_, err := pb.NewTenantBarrierServiceClient(conn).Seal(context.Background(), &pb.TenantBarrierSealRequest{TenantId: "missing"})
	if err == nil {
		t.Fatal("expected error")
	}
	if st, _ := status.FromError(err); st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// Status handler tests
// ---------------------------------------------------------------------------

func TestTenantBarrier_Status_Success(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierStatusFn = func(_ context.Context, id string) (*tenantpkg.TenantBarrierStatus, error) {
		return &tenantpkg.TenantBarrierStatus{
			TenantID:       id,
			Sealed:         false,
			Initialized:    true,
			Strategy:       "passphrase",
			HardwareBacked: false,
		}, nil
	}
	conn, cleanup := startTenantBarrierTestServer(t, svc)
	defer cleanup()

	resp, err := pb.NewTenantBarrierServiceClient(conn).Status(context.Background(), &pb.TenantBarrierStatusRequest{TenantId: "acme"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.GetTenantId() != "acme" || resp.GetSealed() || !resp.GetInitialized() || resp.GetStrategy() != "passphrase" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

// TestTenantBarrier_Status_ServingNodes pins that the nodes serving the
// barrier reach the client with their endpoints, and that a status listing
// none sends none.
func TestTenantBarrier_Status_ServingNodes(t *testing.T) {
	var nodes []tenantpkg.BarrierNode
	svc := newMockTenantService()
	svc.barrierStatusFn = func(_ context.Context, id string) (*tenantpkg.TenantBarrierStatus, error) {
		return &tenantpkg.TenantBarrierStatus{TenantID: id, Sealed: true, Nodes: nodes}, nil
	}
	conn, cleanup := startTenantBarrierTestServer(t, svc)
	defer cleanup()
	client := pb.NewTenantBarrierServiceClient(conn)

	nodes = []tenantpkg.BarrierNode{
		{Node: "n1:7000", Endpoints: map[string]string{"rest": "n1:8443", "grpc": "n1:9443"}},
		{Node: "n2:7000"},
	}
	resp, err := client.Status(context.Background(), &pb.TenantBarrierStatusRequest{TenantId: "acme"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := resp.GetNodes()
	if len(got) != 2 || got[0].GetNode() != "n1:7000" || got[0].GetEndpoints()["grpc"] != "n1:9443" ||
		got[1].GetNode() != "n2:7000" || len(got[1].GetEndpoints()) != 0 {
		t.Fatalf("nodes = %v, want n1:7000 with its endpoints and n2:7000 with none", got)
	}

	nodes = nil
	resp, err = client.Status(context.Background(), &pb.TenantBarrierStatusRequest{TenantId: "acme"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(resp.GetNodes()) != 0 {
		t.Fatalf("nodes = %v, want none", resp.GetNodes())
	}
}

func TestTenantBarrier_Status_DefaultUninitialized(t *testing.T) {
	svc := newMockTenantService()
	conn, cleanup := startTenantBarrierTestServer(t, svc)
	defer cleanup()

	resp, err := pb.NewTenantBarrierServiceClient(conn).Status(context.Background(), &pb.TenantBarrierStatusRequest{TenantId: "fresh"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetSealed() || resp.GetInitialized() {
		t.Errorf("expected Sealed=true, Initialized=false; got Sealed=%v, Initialized=%v",
			resp.GetSealed(), resp.GetInitialized())
	}
}

func TestTenantBarrier_Status_NilService(t *testing.T) {
	srv := &tenantBarrierServer{svc: nil}
	_, err := srv.Status(context.Background(), &pb.TenantBarrierStatusRequest{TenantId: "acme"})
	if err == nil {
		t.Fatal("expected error")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestTenantBarrier_Status_EmptyTenantID(t *testing.T) {
	svc := newMockTenantService()
	conn, cleanup := startTenantBarrierTestServer(t, svc)
	defer cleanup()

	_, err := pb.NewTenantBarrierServiceClient(conn).Status(context.Background(), &pb.TenantBarrierStatusRequest{TenantId: ""})
	if err == nil {
		t.Fatal("expected error")
	}
	if st, _ := status.FromError(err); st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestTenantBarrier_Status_ServiceError(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierStatusFn = func(_ context.Context, _ string) (*tenantpkg.TenantBarrierStatus, error) {
		return nil, &tenantpkg.StoreError{Op: "get", Err: errors.New("io")}
	}
	conn, cleanup := startTenantBarrierTestServer(t, svc)
	defer cleanup()

	_, err := pb.NewTenantBarrierServiceClient(conn).Status(context.Background(), &pb.TenantBarrierStatusRequest{TenantId: "acme"})
	if err == nil {
		t.Fatal("expected error")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestTenantBarrier_Status_NilStatus(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierStatusFn = func(_ context.Context, _ string) (*tenantpkg.TenantBarrierStatus, error) {
		return nil, nil
	}
	conn, cleanup := startTenantBarrierTestServer(t, svc)
	defer cleanup()

	_, err := pb.NewTenantBarrierServiceClient(conn).Status(context.Background(), &pb.TenantBarrierStatusRequest{TenantId: "acme"})
	if err == nil {
		t.Fatal("expected error for nil status response")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// Service registration tests
// ---------------------------------------------------------------------------

func TestRegisterTenantBarrierService_NilSvcSkipsRegistration(t *testing.T) {
	srv := grpc.NewServer()
	RegisterTenantBarrierService(srv, nil, nil)
	// The descriptor must NOT be present when nil is passed.
	if info, ok := srv.GetServiceInfo()["qrdb.v1.TenantBarrierService"]; ok {
		t.Fatalf("expected service not to be registered with nil svc, got %+v", info)
	}
}

func TestRegisterTenantBarrierService_RegistersWhenConfigured(t *testing.T) {
	srv := grpc.NewServer()
	RegisterTenantBarrierService(srv, newMockTenantService(), nil)
	if _, ok := srv.GetServiceInfo()["qrdb.v1.TenantBarrierService"]; !ok {
		t.Fatal("expected qrdb.v1.TenantBarrierService to be registered")
	}
}

// ---------------------------------------------------------------------------
// Proto response shape sanity test (replaces the old JSON round-trip test).
// ---------------------------------------------------------------------------

func TestTenantBarrier_StatusResponse_ProtoShape(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierStatusFn = func(_ context.Context, id string) (*tenantpkg.TenantBarrierStatus, error) {
		return &tenantpkg.TenantBarrierStatus{
			TenantID:        id,
			Sealed:          true,
			Initialized:     true,
			Strategy:        "shamir",
			HardwareBacked:  true,
			ShamirThreshold: 2,
			ShamirTotal:     3,
		}, nil
	}
	conn, cleanup := startTenantBarrierTestServer(t, svc)
	defer cleanup()

	resp, err := pb.NewTenantBarrierServiceClient(conn).Status(context.Background(), &pb.TenantBarrierStatusRequest{TenantId: "acme"})
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if resp.GetTenantId() != "acme" {
		t.Errorf("expected tenant_id 'acme', got %q", resp.GetTenantId())
	}
	if !resp.GetSealed() {
		t.Error("expected sealed=true")
	}
	if !resp.GetInitialized() {
		t.Error("expected initialized=true")
	}
	if resp.GetStrategy() != "shamir" {
		t.Errorf("expected strategy 'shamir', got %q", resp.GetStrategy())
	}
	if !resp.GetHardwareBacked() {
		t.Error("expected hardware_backed=true")
	}
	if resp.GetShamirThreshold() != 2 || resp.GetShamirTotal() != 3 {
		t.Errorf("expected threshold=2 total=3, got threshold=%d total=%d",
			resp.GetShamirThreshold(), resp.GetShamirTotal())
	}
}

// ---------------------------------------------------------------------------
// Helper: connect spins up an in-process server and returns the conn. Cleanup
// is registered with t.Cleanup so callers can simply use the returned conn.
// ---------------------------------------------------------------------------

func connect(t *testing.T, svc tenantpkg.TenantServicer) *grpc.ClientConn {
	t.Helper()
	conn, cleanup := startTenantBarrierTestServer(t, svc)
	t.Cleanup(cleanup)
	return conn
}

// ---------------------------------------------------------------------------
// Interceptor branch coverage: when grpc.NewServer is given a UnaryInterceptor,
// the per-method handlers must invoke it and forward the response. This
// covers the otherwise-cold interceptor path inside each handler.
// ---------------------------------------------------------------------------

func startTenantBarrierTestServerWithInterceptor(t *testing.T, svc tenantpkg.TenantServicer, interceptor grpc.UnaryServerInterceptor) (*grpc.ClientConn, func()) {
	t.Helper()
	srv := grpc.NewServer(grpc.UnaryInterceptor(interceptor))
	RegisterTenantBarrierService(srv, svc, nil)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		srv.Stop()
		t.Fatalf("grpc.NewClient: %v", err)
	}
	return conn, func() {
		_ = conn.Close()
		srv.Stop()
	}
}

func TestTenantBarrier_AllHandlers_Interceptor(t *testing.T) {
	calls := map[string]int{}
	icpt := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		calls[info.FullMethod]++
		return handler(ctx, req)
	}

	svc := newMockTenantService()
	conn, cleanup := startTenantBarrierTestServerWithInterceptor(t, svc, icpt)
	defer cleanup()

	c := pb.NewTenantBarrierServiceClient(conn)
	if _, err := c.InitBegin(context.Background(), &pb.TenantBarrierInitBeginRequest{TenantId: "acme"}); err != nil {
		t.Fatalf("InitBegin: %v", err)
	}
	if _, err := c.InitComplete(context.Background(), &pb.TenantBarrierInitRequest{TenantId: "acme"}); err != nil {
		t.Fatalf("InitComplete: %v", err)
	}
	if _, err := c.Unseal(context.Background(), &pb.TenantBarrierUnsealRequest{TenantId: "acme", Key: []byte("k")}); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if _, err := c.Seal(context.Background(), &pb.TenantBarrierSealRequest{TenantId: "acme"}); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := c.Status(context.Background(), &pb.TenantBarrierStatusRequest{TenantId: "acme"}); err != nil {
		t.Fatalf("Status: %v", err)
	}

	for _, m := range []string{
		"/qrdb.v1.TenantBarrierService/InitBegin",
		"/qrdb.v1.TenantBarrierService/InitComplete",
		"/qrdb.v1.TenantBarrierService/Unseal",
		"/qrdb.v1.TenantBarrierService/Seal",
		"/qrdb.v1.TenantBarrierService/Status",
	} {
		if calls[m] != 1 {
			t.Errorf("interceptor: expected 1 call to %s, got %d", m, calls[m])
		}
	}
}

// ---------------------------------------------------------------------------
// Tenant barrier: OfficerRegister, OfficerList, ShareClaimBegin,
// ShareClaimComplete, OfficerClaimStatus, ShamirSealVote.
// These handlers are on tenantBarrierServer and require only a TenantServicer.
// ---------------------------------------------------------------------------

// extMockTenantService extends mockTenantService with configurable SO fns.
type extMockTenantService struct {
	tenant.TenantServicer
	*mockTenantService
	officerRegisterFn    func(ctx context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error)
	officerListFn        func(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error)
	claimBeginFn         func(ctx context.Context, tenantID, username, thumbprint string) (*ceremony.ChallengeResponse, error)
	claimCompleteFn      func(ctx context.Context, tenantID, username, nonceHex string, sig []byte, thumbprint string) (*ceremony.ShareClaimResult, error)
	officerClaimStatusFn func(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error)
}

func newExtMockTenantService() *extMockTenantService {
	return &extMockTenantService{mockTenantService: newMockTenantService()}
}

func (m *extMockTenantService) TenantBarrierOfficerList(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error) {
	if m.officerListFn != nil {
		return m.officerListFn(ctx, tenantID)
	}
	return nil, nil
}
func (m *extMockTenantService) TenantBarrierShareClaimBegin(ctx context.Context, tenantID, username, thumbprint string) (*ceremony.ChallengeResponse, error) {
	if m.claimBeginFn != nil {
		return m.claimBeginFn(ctx, tenantID, username, thumbprint)
	}
	return &ceremony.ChallengeResponse{Nonce: []byte("nonce-bytes"), ExpiresAt: time.Now().Add(time.Minute)}, nil
}

// ---------------------------------------------------------------------------
// OfficerRegister (tenant)
// ---------------------------------------------------------------------------

func TestTenantBarrier_OfficerRegister_NilService(t *testing.T) {
	srv := &tenantBarrierServer{svc: nil}
	_, err := srv.OfficerRegister(context.Background(), &pb.TenantBarrierOfficerRegisterRequest{TenantId: "acme"})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// OfficerList (tenant)
// ---------------------------------------------------------------------------

func TestTenantBarrier_OfficerList_NilService(t *testing.T) {
	srv := &tenantBarrierServer{svc: nil}
	_, err := srv.OfficerList(context.Background(), &pb.TenantBarrierOfficerListRequest{TenantId: "acme"})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// ShareClaimBegin (tenant) — requires peer cert; test via direct srv call.
// ---------------------------------------------------------------------------

func TestTenantBarrier_ShareClaimBegin_NilService(t *testing.T) {
	srv := &tenantBarrierServer{svc: nil}
	_, err := srv.ShareClaimBegin(context.Background(), &pb.TenantBarrierShareClaimBeginRequest{TenantId: "acme"})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// ShareClaimComplete (tenant) — requires peer cert.
// ---------------------------------------------------------------------------

func TestTenantBarrier_ShareClaimComplete_NilService(t *testing.T) {
	srv := &tenantBarrierServer{svc: nil}
	_, err := srv.ShareClaimComplete(context.Background(), &pb.TenantBarrierShareClaimCompleteRequest{TenantId: "acme"})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// mapServiceError — covers all sentinel paths.
// ---------------------------------------------------------------------------

func TestMapTenantBarrierCeremonyError_AllBranches(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"nil", nil, codes.OK},
		{"cert thumbprint mismatch", ceremony.ErrCertThumbprintMismatch, codes.PermissionDenied},
		{"officer not found", ceremony.ErrOfficerNotFound, codes.NotFound},
		{"share already claimed", ceremony.ErrShareAlreadyClaimed, codes.FailedPrecondition},
		{"no pending share", ceremony.ErrNoPendingShare, codes.NotFound},
		{"share expired", ceremony.ErrShareExpired, codes.FailedPrecondition},
		{"insufficient officers", ceremony.ErrInsufficientOfficers, codes.FailedPrecondition},
		{"shamir requires quorum", seal.ErrShamirBarrierRequiresQuorum, codes.FailedPrecondition},
		{"shamir unseal requires quorum", seal.ErrShamirUnsealRequiresQuorum, codes.FailedPrecondition},
		{"unseal key mismatch", seal.ErrUnsealKeyMismatch, codes.PermissionDenied},
		{"wrapped root key corrupt", seal.ErrWrappedRootKeyCorrupt, codes.Internal},
		{"generic error", errors.New("unknown"), codes.Internal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mapServiceError(tc.err)
			if tc.err == nil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			st, ok := status.FromError(got)
			if !ok {
				t.Fatalf("expected gRPC status error, got %T", got)
			}
			if st.Code() != tc.want {
				t.Errorf("expected %s, got %s", tc.want, st.Code())
			}
		})
	}
}
