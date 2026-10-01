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
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// fakeBarrierServicer
// ===========================================================================

type fakeBarrierServicer struct {
	service.BarrierServicer
}

var _ service.BarrierServicer = (*fakeBarrierServicer)(nil)

func (f *fakeBarrierServicer) BarrierInitialize(_ context.Context, _ []byte) error { return nil }
func (f *fakeBarrierServicer) BarrierUnseal(_ context.Context, _ []byte) error     { return nil }
func (f *fakeBarrierServicer) BarrierSeal(_ context.Context) error                 { return nil }
func (f *fakeBarrierServicer) BarrierInitializeShamir(_ context.Context, threshold, total int, _ seal.FactorPolicy) (*service.ShamirInitInfo, error) {
	return &service.ShamirInitInfo{ShareCount: total, Threshold: threshold, TotalShares: total}, nil
}
func (f *fakeBarrierServicer) BarrierGenerateRecoveryKeys(_ context.Context, threshold int, _ seal.FactorPolicy) (*service.RecoveryKeysInfo, error) {
	return &service.RecoveryKeysInfo{ShareCount: threshold + 1, Threshold: threshold, Total: threshold + 1}, nil
}
func (f *fakeBarrierServicer) BarrierStatus(_ context.Context) (*service.BarrierStatusInfo, error) {
	return &service.BarrierStatusInfo{Sealed: false, Strategy: "passphrase", InitializedAt: time.Unix(1700000000, 0)}, nil
}

func (f *fakeBarrierServicer) BarrierDeleteRecoveryKeys(_ context.Context) error { return nil }

// BarrierInitialized reports the double as never initialized: its
// initialize paths answer for themselves through their canned results.
func (f *fakeBarrierServicer) BarrierInitialized(_ context.Context) (bool, error) { return false, nil }

func (f *fakeBarrierServicer) BarrierHasRecoveryKeys(_ context.Context) (bool, error) {
	return true, nil
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
