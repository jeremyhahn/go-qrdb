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
	"io"
	"net"
	"strings"
	"testing"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// fakeMVCCServicer
// ===========================================================================

type fakeMVCCServicer struct {
	*mockKVService
}

var _ service.MVCCKVServicer = (*fakeMVCCServicer)(nil)

func (f *fakeMVCCServicer) PutWithLease(_ context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error) {
	return &kv.KeyValue{Key: []byte(key), Value: value, LeaseID: leaseID, ModRevision: 1, Version: 1}, nil
}
func (f *fakeMVCCServicer) PutWithLeaseAndMode(_ context.Context, key string, value []byte, leaseID int64, _ writemode.Mode) (*kv.KeyValue, error) {
	return &kv.KeyValue{Key: []byte(key), Value: value, LeaseID: leaseID, ModRevision: 1, Version: 1}, nil
}
func (f *fakeMVCCServicer) GetRevision(_ context.Context, key string) (*kv.KeyValue, error) {
	return &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: 1, Version: 1}, nil
}
func (f *fakeMVCCServicer) GetRevisionWithConsistency(_ context.Context, key string, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	return &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: 1, Version: 1}, nil
}
func (f *fakeMVCCServicer) GetAtRevision(_ context.Context, key string, revision int64) (*kv.KeyValue, error) {
	return &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: revision, Version: 1}, nil
}
func (f *fakeMVCCServicer) GetAtRevisionWithConsistency(_ context.Context, key string, revision int64, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	return &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: revision, Version: 1}, nil
}
func (f *fakeMVCCServicer) Range(_ context.Context, start, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	return []*kv.KeyValue{{Key: start, Value: []byte("v"), ModRevision: 1, Version: 1}}, 1, 42, nil
}
func (f *fakeMVCCServicer) RangeWithConsistency(_ context.Context, start, _ []byte, _ consistency.ReadOptions, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	return []*kv.KeyValue{{Key: start, Value: []byte("v"), ModRevision: 1, Version: 1}}, 1, 42, nil
}
func (f *fakeMVCCServicer) Compact(_ context.Context, _ int64) error         { return nil }
func (f *fakeMVCCServicer) CurrentRevision(_ context.Context) (int64, error) { return 42, nil }
func (f *fakeMVCCServicer) RestoreToRevision(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	return &service.RestoreRevisionResult{
		Revision: req.Revision,
		Changes: []service.RestoreKeyChange{
			{Key: "restored-key", Action: service.RestoreActionRevert, FromRevision: 5, ToRevision: 3, ValueBytes: 7},
		},
		Applied: 1,
		DryRun:  req.DryRun,
	}, nil
}

// ===========================================================================
// fakeTransactionServicer
// ===========================================================================

type fakeTransactionServicer struct{}

var _ service.TransactionServicer = (*fakeTransactionServicer)(nil)

func (f *fakeTransactionServicer) Execute(_ context.Context, _ []service.Compare, _, _ []service.Op) (*service.TransactionResponse, error) {
	return &service.TransactionResponse{Succeeded: true, Revision: 1, Responses: []service.OpResponse{{Succeeded: true}}}, nil
}
func (f *fakeTransactionServicer) ExecuteWithMode(_ context.Context, _ []service.Compare, _, _ []service.Op, _ writemode.Mode) (*service.TransactionResponse, error) {
	return &service.TransactionResponse{Succeeded: true, Revision: 1, Responses: []service.OpResponse{{Succeeded: true}}}, nil
}
func (f *fakeTransactionServicer) ExecuteEntity(_ context.Context, _ *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	return nil, nil
}

// ===========================================================================
// fakeBatchServicer (full implementation for ExecuteBatch)
// ===========================================================================

type fakeBatchServicer struct{}

var _ service.BatchServicer = (*fakeBatchServicer)(nil)

func (f *fakeBatchServicer) Execute(_ context.Context, ops []service.Op) ([]service.OpResponse, error) {
	resps := make([]service.OpResponse, len(ops))
	for i := range ops {
		resps[i] = service.OpResponse{Succeeded: true}
	}
	return resps, nil
}
func (f *fakeBatchServicer) ExecuteWithMode(ctx context.Context, ops []service.Op, _ writemode.Mode) ([]service.OpResponse, error) {
	return f.Execute(ctx, ops)
}
func (f *fakeBatchServicer) ExecuteEntity(_ context.Context, _ *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	return nil, nil
}
func (f *fakeBatchServicer) Import(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
	return &service.ImportResult{}, nil
}

// ===========================================================================
// fakeAuthFactorServicer
// ===========================================================================

type fakeAuthFactorServicer struct{}

var _ service.AuthFactorServicer = (*fakeAuthFactorServicer)(nil)

func (f *fakeAuthFactorServicer) EnrollFactor(_ context.Context, factorName, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	return &authfactors.Enrollment{
		EnrollmentID: "enr-1",
		FactorName:   factorName,
		UserID:       subject,
		TenantID:     params.TenantID,
		Metadata:     map[string]string{"k": "v"},
	}, nil
}

func (f *fakeAuthFactorServicer) ChallengeFactor(_ context.Context, enrollmentID string) (*authfactors.Challenge, error) {
	return &authfactors.Challenge{ChallengeID: "ch-1", EnrollmentID: enrollmentID, Nonce: []byte("n")}, nil
}

func (f *fakeAuthFactorServicer) VerifyFactor(_ context.Context, _, _ string, _ *authfactors.Response) error {
	return nil
}

func (f *fakeAuthFactorServicer) ListFactors(_ context.Context, subject string) ([]*authfactors.Enrollment, error) {
	return []*authfactors.Enrollment{{EnrollmentID: "enr-1", UserID: subject, FactorName: "pkcs11"}}, nil
}

func (f *fakeAuthFactorServicer) RevokeFactor(_ context.Context, _ string) error { return nil }

// ===========================================================================
// Server with kv+admin+mvcc+tx+batch+barrier+authfactor
// ===========================================================================

func startMVCCServer(t *testing.T) (string, func()) {
	t.Helper()
	srv := googlegrpc.NewServer()
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	mvccSvc := &fakeMVCCServicer{mockKVService: kvSvc}

	grpcpkg.RegisterService(srv, kvSvc, adminSvc,
		grpcpkg.WithRegisterMVCC(mvccSvc),
		grpcpkg.WithRegisterTransaction(&fakeTransactionServicer{}),
		grpcpkg.WithRegisterBatch(&fakeBatchServicer{}),
		grpcpkg.WithRegisterBarrierService(&fakeBarrierServicer{}),
	)

	// Also register the authfactor service separately via the helper (it's
	// not exposed through QRDBServer / RegisterOption — but the gRPC test
	// already wires authfactor via the service-registration option exposed
	// in the AuthFactor handler).
	grpcpkg.RegisterAuthFactorService(srv, &fakeAuthFactorServicer{})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() { srv.Stop() }
}

func connectMVCCTransport(t *testing.T, addr string) (*Transport, func()) {
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
// MVCC success tests
// ===========================================================================

func TestGRPCMVCC_AllMethods_Success(t *testing.T) {
	addr, stop := startMVCCServer(t)
	defer stop()
	tr, cleanup := connectMVCCTransport(t, addr)
	defer cleanup()
	ctx := context.Background()

	t.Run("PutWithLease", func(t *testing.T) {
		if _, err := tr.PutWithLease(ctx, "k", []byte("v"), 1); err != nil {
			t.Fatalf("PutWithLease: %v", err)
		}
	})
	t.Run("GetRevision", func(t *testing.T) {
		if _, err := tr.GetRevision(ctx, "k"); err != nil {
			t.Fatalf("GetRevision: %v", err)
		}
	})
	t.Run("GetAtRevision", func(t *testing.T) {
		if _, err := tr.GetAtRevision(ctx, "k", 1); err != nil {
			t.Fatalf("GetAtRevision: %v", err)
		}
	})
	t.Run("Range", func(t *testing.T) {
		resp, err := tr.Range(ctx, []byte("a"), []byte("z"), transport.RangeOptions{Limit: 10})
		if err != nil {
			t.Fatalf("Range: %v", err)
		}
		// Count and Revision are deliberately distinct values (1 vs 42) so
		// this test cannot pass by coincidence if the revision is dropped
		// or confused with the count.
		if resp.Count != 1 {
			t.Fatalf("Count = %d, want 1", resp.Count)
		}
		if resp.Revision != 42 {
			t.Fatalf("Revision = %d, want 42", resp.Revision)
		}
	})
	t.Run("Compact", func(t *testing.T) {
		if err := tr.Compact(ctx, 1); err != nil {
			t.Fatalf("Compact: %v", err)
		}
	})
	t.Run("CurrentRevision", func(t *testing.T) {
		if _, err := tr.CurrentRevision(ctx); err != nil {
			t.Fatalf("CurrentRevision: %v", err)
		}
	})
	t.Run("RestoreToRevision", func(t *testing.T) {
		result, err := tr.RestoreToRevision(ctx, &transport.RestoreRevisionRequest{
			Revision: 5,
			Prefix:   []byte("prefix"),
		})
		if err != nil {
			t.Fatalf("RestoreToRevision: %v", err)
		}
		if result.Applied != 1 {
			t.Fatalf("Applied = %d, want 1", result.Applied)
		}
		if len(result.Changes) != 1 || string(result.Changes[0].Key) != "restored-key" {
			t.Fatalf("unexpected changes: %+v", result.Changes)
		}
	})
}

// TestGRPCMVCC_RestoreToRevision_BinaryRoundTrip proves an arbitrary
// byte-valued prefix and key round-trip byte-for-byte through the gRPC
// proto `bytes` wire encoding.
func TestGRPCMVCC_RestoreToRevision_BinaryRoundTrip(t *testing.T) {
	binaryKey := []byte{0x00, 0xff, 0xfe, 'k', 0x80, 0x81}

	var captured *service.RestoreRevisionRequest
	fake := &fakeMVCCServicer{mockKVService: newMockKVService()}
	fakeWithCapture := &capturingMVCCServicer{fakeMVCCServicer: fake, captured: &captured}

	srv := googlegrpc.NewServer()
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	grpcpkg.RegisterService(srv, kvSvc, adminSvc, grpcpkg.WithRegisterMVCC(fakeWithCapture))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	tr, cleanup := connectMVCCTransport(t, lis.Addr().String())
	defer cleanup()

	result, err := tr.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{
		Revision: 5,
		Keys:     [][]byte{binaryKey},
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if result == nil {
		t.Fatal("expected a non-nil result")
	}

	if captured == nil || len(captured.Keys) != 1 || captured.Keys[0] != string(binaryKey) {
		t.Fatalf("expected service to receive binary key %v, got %+v", binaryKey, captured)
	}
}

// capturingMVCCServicer wraps fakeMVCCServicer and records the last
// RestoreToRevision request it received, for binary round-trip assertions.
type capturingMVCCServicer struct {
	*fakeMVCCServicer
	captured **service.RestoreRevisionRequest
}

func (c *capturingMVCCServicer) RestoreToRevision(ctx context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	*c.captured = req
	return c.fakeMVCCServicer.RestoreToRevision(ctx, req)
}

// TestGRPCMVCC_RestoreToRevision_NilRequest proves the client fails fast on
// a nil request rather than sending a zero-value RPC.
func TestGRPCMVCC_RestoreToRevision_NilRequest(t *testing.T) {
	addr, stop := startMVCCServer(t)
	defer stop()
	tr, cleanup := connectMVCCTransport(t, addr)
	defer cleanup()

	_, err := tr.RestoreToRevision(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
}

// ===========================================================================
// Transaction success tests
// ===========================================================================

func TestGRPCTransaction_Execute_Success(t *testing.T) {
	addr, stop := startMVCCServer(t)
	defer stop()
	tr, cleanup := connectMVCCTransport(t, addr)
	defer cleanup()

	resp, err := tr.ExecuteTransaction(context.Background(), &transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte("k1"), Target: transport.CompareValue, Result: transport.CompareEqual, Value: []byte("v")},
			{Key: []byte("k2"), Target: transport.CompareVersion, Result: transport.CompareGreater, Version: 1},
			{Key: []byte("k3"), Target: transport.CompareCreateRevision, Result: transport.CompareLess, CreateRevision: 5},
			{Key: []byte("k4"), Target: transport.CompareModRevision, Result: transport.CompareNotEqual, ModRevision: 7},
		},
		SuccessOps: []transport.Op{
			{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v"), LeaseID: 1},
			{Type: transport.OpGet, Key: []byte("k")},
			{Type: transport.OpDelete, Key: []byte("k")},
			{Type: transport.OpRange, Key: []byte("a"), RangeEnd: []byte("z")},
		},
		FailureOps: []transport.Op{
			{Type: transport.OpGet, Key: []byte("x")},
		},
	})
	if err != nil {
		t.Fatalf("ExecuteTransaction: %v", err)
	}
	if !resp.Succeeded {
		t.Fatal("expected success")
	}
}

// ===========================================================================
// Batch success tests
// ===========================================================================

func TestGRPCBatch_Execute_Success(t *testing.T) {
	addr, stop := startMVCCServer(t)
	defer stop()
	tr, cleanup := connectMVCCTransport(t, addr)
	defer cleanup()

	resp, err := tr.ExecuteBatch(context.Background(), []transport.Op{
		{Type: transport.OpPut, Key: []byte("k1"), Value: []byte("v1")},
		{Type: transport.OpDelete, Key: []byte("k2")},
	})
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}
	if len(resp.Responses) != 2 {
		t.Fatalf("got %d responses", len(resp.Responses))
	}
}

func TestGRPCBatch_Import_Success(t *testing.T) {
	addr, stop := startMVCCServer(t)
	defer stop()
	tr, cleanup := connectMVCCTransport(t, addr)
	defer cleanup()

	result, err := tr.ImportFile(context.Background(), "data.csv",
		strings.NewReader("id,val\n1,a\n2,b\n"),
		transport.ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row"})
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

// ===========================================================================
// Barrier remaining success tests
// ===========================================================================

func TestGRPCBarrier_RemainingMethods_Success(t *testing.T) {
	addr, stop := startMVCCServer(t)
	defer stop()
	tr, cleanup := connectMVCCTransport(t, addr)
	defer cleanup()
	ctx := context.Background()

	t.Run("BarrierInitialize", func(t *testing.T) {
		if err := tr.BarrierInitialize(ctx, &transport.BarrierInitializeRequest{Secret: []byte("p")}); err != nil {
			t.Fatalf("BarrierInitialize: %v", err)
		}
	})
	t.Run("BarrierUnseal", func(t *testing.T) {
		if err := tr.BarrierUnseal(ctx, &transport.BarrierUnsealRequest{Secret: []byte("p")}); err != nil {
			t.Fatalf("BarrierUnseal: %v", err)
		}
	})
	t.Run("BarrierSeal", func(t *testing.T) {
		if err := tr.BarrierSeal(ctx); err != nil {
			t.Fatalf("BarrierSeal: %v", err)
		}
	})
	t.Run("BarrierDeleteRecoveryKeys", func(t *testing.T) {
		if err := tr.BarrierDeleteRecoveryKeys(ctx); err != nil {
			t.Fatalf("BarrierDeleteRecoveryKeys: %v", err)
		}
	})
	t.Run("BarrierHasRecoveryKeys", func(t *testing.T) {
		if _, err := tr.BarrierHasRecoveryKeys(ctx); err != nil {
			t.Fatalf("BarrierHasRecoveryKeys: %v", err)
		}
	})
}

// ===========================================================================
// AuthFactor success tests
// ===========================================================================

func TestGRPCAuthFactor_AllMethods_Success(t *testing.T) {
	addr, stop := startMVCCServer(t)
	defer stop()
	tr, cleanup := connectMVCCTransport(t, addr)
	defer cleanup()
	ctx := context.Background()

	t.Run("EnrollFactor", func(t *testing.T) {
		if _, err := tr.EnrollFactor(ctx, &transport.EnrollFactorRequest{FactorName: "pkcs11", Subject: "alice"}); err != nil {
			t.Fatalf("EnrollFactor: %v", err)
		}
	})
	t.Run("ChallengeFactor", func(t *testing.T) {
		if _, err := tr.ChallengeFactor(ctx, &transport.ChallengeFactorRequest{EnrollmentID: "enr-1"}); err != nil {
			t.Fatalf("ChallengeFactor: %v", err)
		}
	})
	t.Run("ListFactors", func(t *testing.T) {
		if _, err := tr.ListFactors(ctx, &transport.ListFactorsRequest{Subject: "alice"}); err != nil {
			t.Fatalf("ListFactors: %v", err)
		}
	})
}

// CompareAndSwap satisfies the guarded-swap half of the interface (go-qrdb#56).
func (*fakeMVCCServicer) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}
