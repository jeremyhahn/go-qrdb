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
	"net"
	"testing"
	"time"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// fakeBarrierServicer
// ===========================================================================

type fakeBarrierServicer struct{}

var _ service.BarrierServicer = (*fakeBarrierServicer)(nil)

func (f *fakeBarrierServicer) BarrierInitialize(_ context.Context, _ []byte) error { return nil }
func (f *fakeBarrierServicer) BarrierUnseal(_ context.Context, _ []byte) error     { return nil }
func (f *fakeBarrierServicer) BarrierSeal(_ context.Context) error                 { return nil }
func (f *fakeBarrierServicer) BarrierStatus(_ context.Context) (*service.BarrierStatusInfo, error) {
	return &service.BarrierStatusInfo{Sealed: false, Strategy: "passphrase", InitializedAt: time.Unix(1700000000, 0)}, nil
}
func (f *fakeBarrierServicer) BarrierInitializeShamir(_ context.Context, _ []byte, threshold, total int) (*service.ShamirInitInfo, error) {
	return &service.ShamirInitInfo{ShareCount: 2, Threshold: threshold, TotalShares: total}, nil
}
func (f *fakeBarrierServicer) BarrierUnsealWithShare(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
	return &service.UnsealShareProgress{Required: 2, Submitted: 1, Complete: false}, nil
}
func (f *fakeBarrierServicer) BarrierUnsealWithShares(_ context.Context, _ []string) error {
	return nil
}

func (f *fakeBarrierServicer) BarrierRekey(_ context.Context, threshold, total int) (*service.ShamirInitInfo, error) {
	return &service.ShamirInitInfo{ShareCount: 1, Threshold: threshold, TotalShares: total}, nil
}
func (f *fakeBarrierServicer) BarrierGenerateRecoveryKeys(_ context.Context, threshold, total int) (*service.RecoveryKeysInfo, error) {
	return &service.RecoveryKeysInfo{ShareCount: 1, Threshold: threshold, Total: total}, nil
}
func (f *fakeBarrierServicer) BarrierOfficerRegister(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	return &service.BarrierOfficerRegisterResult{}, nil
}
func (f *fakeBarrierServicer) BarrierOfficerList(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
	return nil, nil
}
func (f *fakeBarrierServicer) BarrierOfficerClaimStatus(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
	return nil, nil
}
func (f *fakeBarrierServicer) BarrierShareClaimBegin(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
	return &service.BarrierShareClaimChallenge{}, nil
}
func (f *fakeBarrierServicer) BarrierShareClaimComplete(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
	return &service.BarrierShareClaimResult{}, nil
}
func (f *fakeBarrierServicer) BarrierShamirSealVote(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
	return &service.UnsealShareProgress{}, nil
}
func (f *fakeBarrierServicer) BarrierRecoveryOfficerRegister(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	return &service.BarrierOfficerRegisterResult{}, nil
}
func (f *fakeBarrierServicer) BarrierRecoveryShareClaimBegin(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
	return &service.BarrierShareClaimChallenge{}, nil
}
func (f *fakeBarrierServicer) BarrierRecoveryShareClaimComplete(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
	return &service.BarrierShareClaimResult{}, nil
}
func (f *fakeBarrierServicer) BarrierRecoverWithKeys(_ context.Context, _ []string) error { return nil }
func (f *fakeBarrierServicer) BarrierDeleteRecoveryKeys(_ context.Context) error          { return nil }
func (f *fakeBarrierServicer) BarrierHasRecoveryKeys(_ context.Context) (bool, error) {
	return true, nil
}
func (f *fakeBarrierServicer) BarrierGenerateRootToken(_ context.Context, _ []string) (*service.RootTokenInfo, error) {
	return &service.RootTokenInfo{Token: "tok", CreatedAt: time.Unix(1700000000, 0)}, nil
}

// startBarrierServer starts a gRPC server with only the barrier servicer wired.
func startBarrierServer(t *testing.T) (string, func()) {
	t.Helper()
	srv := googlegrpc.NewServer()
	grpcpkg.RegisterService(srv, nil, nil, grpcpkg.WithRegisterBarrierService(&fakeBarrierServicer{}))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() { srv.Stop() }
}

func connectBarrierTransport(t *testing.T, addr string) (*Transport, func()) {
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

// ===========================================================================
// Barrier success-path tests
// ===========================================================================

func TestGRPCBarrier_Status_Success(t *testing.T) {
	addr, stop := startBarrierServer(t)
	defer stop()
	tr, cleanup := connectBarrierTransport(t, addr)
	defer cleanup()

	resp, err := tr.BarrierStatus(context.Background())
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if resp.Sealed {
		t.Fatalf("Sealed = %v", resp.Sealed)
	}
}

func TestGRPCBarrier_InitializeShamir_Success(t *testing.T) {
	addr, stop := startBarrierServer(t)
	defer stop()
	tr, cleanup := connectBarrierTransport(t, addr)
	defer cleanup()

	resp, err := tr.BarrierInitializeShamir(context.Background(), &transport.BarrierInitializeShamirRequest{Threshold: 2, TotalShares: 2})
	if err != nil {
		t.Fatalf("BarrierInitializeShamir: %v", err)
	}
	if resp.ShareCount != 2 {
		t.Fatalf("got ShareCount=%d, want 2", resp.ShareCount)
	}
}

func TestGRPCBarrier_UnsealWithShare_Success(t *testing.T) {
	addr, stop := startBarrierServer(t)
	defer stop()
	tr, cleanup := connectBarrierTransport(t, addr)
	defer cleanup()

	resp, err := tr.BarrierUnsealWithShare(context.Background(), &transport.BarrierUnsealShareRequest{Share: "s1"})
	if err != nil {
		t.Fatalf("BarrierUnsealWithShare: %v", err)
	}
	if resp.Required != 2 {
		t.Fatalf("Required = %d", resp.Required)
	}
}

func TestGRPCBarrier_UnsealWithShares_Success(t *testing.T) {
	addr, stop := startBarrierServer(t)
	defer stop()
	tr, cleanup := connectBarrierTransport(t, addr)
	defer cleanup()

	if err := tr.BarrierUnsealWithShares(context.Background(), &transport.BarrierUnsealSharesRequest{Shares: []string{"s1", "s2"}}); err != nil {
		t.Fatalf("BarrierUnsealWithShares: %v", err)
	}
}

func TestGRPCBarrier_Rekey_Success(t *testing.T) {
	addr, stop := startBarrierServer(t)
	defer stop()
	tr, cleanup := connectBarrierTransport(t, addr)
	defer cleanup()

	resp, err := tr.BarrierRekey(context.Background(), &transport.BarrierRekeyRequest{Threshold: 2, Total: 2})
	if err != nil {
		t.Fatalf("BarrierRekey: %v", err)
	}
	if resp == nil {
		t.Fatal("nil resp")
	}
}

func TestGRPCBarrier_GenerateRecoveryKeys_Success(t *testing.T) {
	addr, stop := startBarrierServer(t)
	defer stop()
	tr, cleanup := connectBarrierTransport(t, addr)
	defer cleanup()

	resp, err := tr.BarrierGenerateRecoveryKeys(context.Background(), &transport.BarrierGenerateRecoveryKeysRequest{Threshold: 2, Total: 2})
	if err != nil {
		t.Fatalf("BarrierGenerateRecoveryKeys: %v", err)
	}
	if resp.ShareCount == 0 && resp.Threshold == 0 {
		t.Fatal("expected non-empty recovery key response")
	}
}

func TestGRPCBarrier_RecoverWithKeys_Success(t *testing.T) {
	addr, stop := startBarrierServer(t)
	defer stop()
	tr, cleanup := connectBarrierTransport(t, addr)
	defer cleanup()

	if err := tr.BarrierRecoverWithKeys(context.Background(), &transport.BarrierRecoverWithKeysRequest{Keys: []string{"r1"}}); err != nil {
		t.Fatalf("BarrierRecoverWithKeys: %v", err)
	}
}

func TestGRPCBarrier_GenerateRootToken_Success(t *testing.T) {
	addr, stop := startBarrierServer(t)
	defer stop()
	tr, cleanup := connectBarrierTransport(t, addr)
	defer cleanup()

	resp, err := tr.BarrierGenerateRootToken(context.Background(), &transport.BarrierGenerateRootTokenRequest{Shares: []string{"s1"}})
	if err != nil {
		t.Fatalf("BarrierGenerateRootToken: %v", err)
	}
	if resp.Token != "tok" {
		t.Fatalf("Token = %q", resp.Token)
	}
}
