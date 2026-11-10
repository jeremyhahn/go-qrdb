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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Helpers — extend mockBarrierService with configurable officer/claim/vote fns.
// These wrap the existing struct which returns zero-value defaults.
// ---------------------------------------------------------------------------

type extMockBarrierService struct {
	service.BarrierServicer
	*mockBarrierService
	officerRegisterFn         func(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error)
	officerListFn             func(ctx context.Context) ([]*service.BarrierOfficerStatus, error)
	officerClaimStatusFnExt   func(ctx context.Context) ([]*service.BarrierOfficerStatus, error)
	shareClaimBeginFn         func(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error)
	shareClaimCompleteFn      func(ctx context.Context, username, nonceHex string, sig []byte, thumbprint string) (*service.BarrierShareClaimResult, error)
	recoveryOfficerRegisterFn func(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error)
	recoveryClaimBeginFn      func(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error)
	recoveryClaimCompleteFn   func(ctx context.Context, username, nonceHex string, sig []byte, thumbprint string) (*service.BarrierShareClaimResult, error)
}

func newExtMockBarrierService() *extMockBarrierService {
	return &extMockBarrierService{mockBarrierService: newMockBarrierService()}
}

func (m *extMockBarrierService) BarrierOfficerList(ctx context.Context) ([]*service.BarrierOfficerStatus, error) {
	if m.officerListFn != nil {
		return m.officerListFn(ctx)
	}
	return nil, nil
}
func (m *extMockBarrierService) BarrierShareClaimBegin(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error) {
	if m.shareClaimBeginFn != nil {
		return m.shareClaimBeginFn(ctx, username, thumbprint)
	}
	return &service.BarrierShareClaimChallenge{NonceHex: "deadbeef", ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (m *extMockBarrierService) BarrierRecoveryShareClaimBegin(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error) {
	if m.recoveryClaimBeginFn != nil {
		return m.recoveryClaimBeginFn(ctx, username, thumbprint)
	}
	return &service.BarrierShareClaimChallenge{NonceHex: "cafebabe", ExpiresAt: time.Now().Add(time.Minute)}, nil
}

// startBarrierTestServer spins up a gRPC server with the full qrdb service
// wired against the supplied barrier service.
func startBarrierTestServer(t *testing.T, barrierSvc service.BarrierServicer) (*pb.QRDBServiceClient, func()) {
	t.Helper()
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(), WithBarrierService(barrierSvc))
	return &client, cleanup
}

// ---------------------------------------------------------------------------
// BarrierOfficerRegister
// ---------------------------------------------------------------------------

func TestBarrierOfficerRegister_NilService(t *testing.T) {
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.BarrierOfficerRegister(context.Background(), &pb.BarrierOfficerRegisterRequest{
		Username: "alice",
		CsrPem:   []byte("pem-data"),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// BarrierOfficerList
// ---------------------------------------------------------------------------

func TestBarrierOfficerList_NilService(t *testing.T) {
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.BarrierOfficerList(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// BarrierShareClaimBegin — requires a peer cert; without one we get
// InvalidArgument from peerCertThumbprint. We test via the direct server
// method to avoid needing a real TLS connection.
// ---------------------------------------------------------------------------

func TestBarrierShareClaimBegin_NilService(t *testing.T) {
	srv := &qrdbServiceServer{barrierSvc: nil}
	_, err := srv.BarrierShareClaimBegin(context.Background(), &pb.BarrierShareClaimBeginRequest{Username: "alice"})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// BarrierShareClaimComplete — also requires peer cert.
// ---------------------------------------------------------------------------

func TestBarrierShareClaimComplete_NilService(t *testing.T) {
	srv := &qrdbServiceServer{barrierSvc: nil}
	_, err := srv.BarrierShareClaimComplete(context.Background(), &pb.BarrierShareClaimCompleteRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// BarrierRecoveryOfficerRegister
// ---------------------------------------------------------------------------

func TestBarrierRecoveryOfficerRegister_NilService(t *testing.T) {
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.BarrierRecoveryOfficerRegister(context.Background(), &pb.BarrierOfficerRegisterRequest{
		Username: "recovery-alice",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// BarrierRecoveryShareClaimBegin — requires peer cert.
// ---------------------------------------------------------------------------

func TestBarrierRecoveryShareClaimBegin_NilService(t *testing.T) {
	srv := &qrdbServiceServer{barrierSvc: nil}
	_, err := srv.BarrierRecoveryShareClaimBegin(context.Background(), &pb.BarrierShareClaimBeginRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// BarrierRecoveryShareClaimComplete — requires peer cert.
// ---------------------------------------------------------------------------

func TestBarrierRecoveryShareClaimComplete_NilService(t *testing.T) {
	srv := &qrdbServiceServer{barrierSvc: nil}
	_, err := srv.BarrierRecoveryShareClaimComplete(context.Background(), &pb.BarrierShareClaimCompleteRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// mapServiceError — covers every sentinel.
// ---------------------------------------------------------------------------

func TestMapBarrierCeremonyError_AllBranches(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"nil", nil, codes.OK},
		{"ceremony not configured", service.ErrCeremonyNotConfigured, codes.FailedPrecondition},
		{"shamir requires quorum", seal.ErrShamirBarrierRequiresQuorum, codes.FailedPrecondition},
		{"shamir unseal requires quorum", seal.ErrShamirUnsealRequiresQuorum, codes.FailedPrecondition},
		{"unseal key mismatch", seal.ErrUnsealKeyMismatch, codes.PermissionDenied},
		{"wrapped root key corrupt", seal.ErrWrappedRootKeyCorrupt, codes.Internal},
		{"cert thumbprint mismatch", ceremony.ErrCertThumbprintMismatch, codes.PermissionDenied},
		{"officer not found", ceremony.ErrOfficerNotFound, codes.NotFound},
		{"share already claimed", ceremony.ErrShareAlreadyClaimed, codes.FailedPrecondition},
		{"no pending share", ceremony.ErrNoPendingShare, codes.NotFound},
		{"share expired", ceremony.ErrShareExpired, codes.FailedPrecondition},
		{"insufficient officers", ceremony.ErrInsufficientOfficers, codes.FailedPrecondition},
		{"generic error", errors.New("something went wrong"), codes.Internal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mapServiceError(tc.err)
			if tc.err == nil {
				if got != nil {
					t.Errorf("expected nil for nil input, got %v", got)
				}
				return
			}
			st, ok := status.FromError(got)
			if !ok {
				t.Fatalf("expected gRPC status error, got %T: %v", got, got)
			}
			if st.Code() != tc.want {
				t.Errorf("expected %s, got %s", tc.want, st.Code())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// INV-11 wire-contract guard: share material never leaves the server except
// sealed to its holder. No RPC response — nor any message nested in one — may
// carry a field named "share", "shares" or "share_bytes", and only the two
// claim-complete responses may carry an "envelope": the holder's share, HPKE-
// sealed to the holder's own key and delivered once over its mTLS-bound
// session. Requests may carry a share (a holder submits its share to a
// quorum).
// ---------------------------------------------------------------------------

func TestProto_OnlyClaimResponsesCarryShareMaterial(t *testing.T) {
	envelopeAllowed := map[protoreflect.FullName]bool{
		"qrdb.v1.BarrierShareClaimCompleteResponse":       true,
		"qrdb.v1.TenantBarrierShareClaimCompleteResponse": true,
	}
	shareFields := map[protoreflect.Name]bool{"share": true, "shares": true, "share_bytes": true}

	fd := pb.File_pkg_api_grpc_proto_qrdbv1_qrdb_proto

	seen := map[protoreflect.FullName]bool{}
	envelopes := map[protoreflect.FullName]bool{}
	var walk func(md protoreflect.MessageDescriptor)
	walk = func(md protoreflect.MessageDescriptor) {
		if seen[md.FullName()] {
			return
		}
		seen[md.FullName()] = true
		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			fld := fields.Get(i)
			if shareFields[fld.Name()] {
				t.Errorf("INV-11 violation: response message %s carries share field %q", md.FullName(), fld.Name())
			}
			if fld.Name() == "envelope" {
				if !envelopeAllowed[md.FullName()] {
					t.Errorf("INV-11 violation: response message %s carries a share envelope", md.FullName())
				}
				envelopes[md.FullName()] = true
			}
			if msg := fld.Message(); msg != nil {
				walk(msg)
			}
		}
	}

	services := fd.Services()
	outputs := 0
	for i := 0; i < services.Len(); i++ {
		methods := services.Get(i).Methods()
		for j := 0; j < methods.Len(); j++ {
			walk(methods.Get(j).Output())
			outputs++
		}
	}
	// Discovery floor: every assertion above is inside a loop, and an empty
	// loop reports success.
	if outputs < 100 {
		t.Fatalf("walked %d RPC outputs; the descriptor walk found too few to prove anything", outputs)
	}
	for name := range envelopeAllowed {
		if !envelopes[name] {
			t.Errorf("claim response %s no longer carries its envelope; update the INV-11 guard", name)
		}
	}
}

// TestBarrierStatusToProto_UninitializedHasNoInitializedAt pins the pre-init
// answer on gRPC (and the Unix socket and QUIC, which share the converter):
// a barrier never initialized carries an empty InitializedAt, as on REST and
// MCP, never the zero time formatted, which reads as initialized.
func TestBarrierStatusToProto_UninitializedHasNoInitializedAt(t *testing.T) {
	got := BarrierStatusToProto(&service.BarrierStatusInfo{Sealed: true, Strategy: "passphrase"})
	if got.GetInitializedAt() != "" {
		t.Fatalf("InitializedAt = %q, want empty for an uninitialized barrier", got.GetInitializedAt())
	}
	if !got.GetSealed() || got.GetStrategy() != "passphrase" || got.GetClusterSeal() == nil {
		t.Fatalf("BarrierStatusToProto = %+v, want sealed passphrase with a cluster seal state", got)
	}
}

// TestBarrierStatusToProto_CarriesInitialized: the initialized flag reaches
// the wire whichever way it reads, which is all a sealed node reports besides
// sealed.
func TestBarrierStatusToProto_CarriesInitialized(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		got := BarrierStatusToProto(&service.BarrierStatusInfo{Sealed: true, Initialized: initialized, ClusterSeal: &clusterseal.Report{}})
		if got.GetInitialized() != initialized || !got.GetSealed() {
			t.Errorf("BarrierStatusToProto(initialized %v) = %+v", initialized, got)
		}
		if got.GetStrategy() != "" || got.GetInitializedAt() != "" || got.GetShamirTotal() != 0 {
			t.Errorf("BarrierStatusToProto(initialized %v) = %+v, want nothing but sealed and initialized", initialized, got)
		}
	}
}

// TestBarrierStatusToProto_Initialized carries every field, InitializedAt in
// RFC 3339 with nanoseconds.
func TestBarrierStatusToProto_Initialized(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	got := BarrierStatusToProto(&service.BarrierStatusInfo{
		Strategy: "shamir", HardwareBacked: true, InitializedAt: at, ShamirThreshold: 2, ShamirTotal: 3,
	})
	if got.GetInitializedAt() != at.Format(time.RFC3339Nano) {
		t.Errorf("InitializedAt = %q, want %q", got.GetInitializedAt(), at.Format(time.RFC3339Nano))
	}
	if got.GetSealed() || !got.GetHardwareBacked() || got.GetShamirThreshold() != 2 || got.GetShamirTotal() != 3 {
		t.Errorf("BarrierStatusToProto = %+v, want unsealed hardware-backed 2-of-3", got)
	}
}

func TestBarrierStatusToProto_ClusterMigration(t *testing.T) {
	if got := BarrierStatusToProto(&service.BarrierStatusInfo{}); got.GetClusterMigration() != nil {
		t.Errorf("no migration in progress rendered %+v, want the field absent", got.GetClusterMigration())
	}
	got := BarrierStatusToProto(&service.BarrierStatusInfo{ClusterMigration: &clustermig.Report{
		Target: seal.StrategyPassphrase, Generation: 4, Acked: 1, Total: 2,
		Nodes: []clustermig.NodeReport{
			{NodeID: "n1", Step: clustermig.StepServing, Phase: seal.PhaseComplete},
			{NodeID: "n2", Step: clustermig.StepRecycling, Phase: seal.PhaseFailed, Attempts: 2, Detail: "credential missing"},
		},
	}}).GetClusterMigration()
	want := &pb.ClusterMigrationState{
		Target: "passphrase", Generation: 4, Acked: 1, Total: 2,
		Nodes: []*pb.ClusterMigrationNode{
			{NodeId: "n1", Step: "serving", Phase: "complete"},
			{NodeId: "n2", Step: "recycling", Phase: "failed", Attempts: 2, Detail: "credential missing"},
		},
	}
	if !proto.Equal(got, want) {
		t.Errorf("ClusterMigration = %+v, want %+v", got, want)
	}
	if got := ClusterMigrationToProto(&clustermig.Report{Unconfirmed: true}); !got.GetUnconfirmed() || len(got.GetNodes()) != 0 {
		t.Errorf("unconfirmed = %+v, want unconfirmed with no nodes", got)
	}
}

func TestGRPC_BarrierInitialize_Error(t *testing.T) {
	barrierSvc := newMockBarrierService()
	barrierSvc.initErr = &service.ProposeError{Err: errors.New("init failed")}
	client, cleanup := startTestServerFull(t, WithBarrierService(barrierSvc))
	defer cleanup()

	_, err := client.BarrierInitialize(context.Background(), &pb.BarrierInitializeRequest{Secret: []byte("s")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

// TestGRPC_BarrierSeal_NoPINBackendFailsClosed pins the fail-closed
// behaviour: with no PIN backend wired, BarrierSeal cannot verify the SO PIN
// and must refuse rather than seal unauthenticated.
func TestGRPC_BarrierSeal_NoPINBackendFailsClosed(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithBarrierService(newMockBarrierService()))
	defer cleanup()

	_, err := client.BarrierSeal(context.Background(), &pb.BarrierSealRequest{SoPin: "any"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("BarrierSeal without a PIN backend = %v, want FailedPrecondition", err)
	}
}

func TestGRPC_BarrierStatus_Success(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithBarrierService(newMockBarrierService()))
	defer cleanup()

	resp, err := client.BarrierStatus(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("BarrierStatus failed: %v", err)
	}
	if resp.GetStrategy() != "shamir" {
		t.Fatalf("expected strategy 'shamir', got %q", resp.GetStrategy())
	}
}

func TestGRPC_BarrierShamirInitialize_Success(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithBarrierService(newMockBarrierService()))
	defer cleanup()

	resp, err := client.BarrierShamirInitialize(context.Background(), &pb.BarrierShamirInitializeRequest{
		Threshold:   2,
		TotalShares: 3,
	})
	if err != nil {
		t.Fatalf("BarrierShamirInitialize failed: %v", err)
	}
	if resp.GetThreshold() != 2 {
		t.Fatalf("expected threshold 2, got %d", resp.GetThreshold())
	}
}

func TestGRPC_BarrierRecoveryGenerate_Success(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithBarrierService(newMockBarrierService()), WithPINBackend(newStubPINBackend()))
	defer cleanup()

	resp, err := client.BarrierRecoveryGenerate(context.Background(), &pb.BarrierRecoveryGenerateRequest{
		Threshold: 1,
	})
	if err != nil {
		t.Fatalf("BarrierRecoveryGenerate failed: %v", err)
	}
	// INV-1/INV-11: recovery key bytes never in response; assert share_count only.
	if resp.GetShareCount() != 1 {
		t.Fatalf("expected share_count=1, got %d", resp.GetShareCount())
	}
}

func TestGRPC_BarrierRecoveryDelete_Success(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithBarrierService(newMockBarrierService()))
	defer cleanup()

	_, err := client.BarrierRecoveryDelete(context.Background(), &pb.BarrierRecoveryDeleteRequest{})
	if err != nil {
		t.Fatalf("BarrierRecoveryDelete failed: %v", err)
	}
}

func TestGRPC_BarrierRecoveryStatus_Success(t *testing.T) {
	barrierSvc := newMockBarrierService()
	barrierSvc.recoveryHasResult = true
	client, cleanup := startTestServerFull(t, WithBarrierService(barrierSvc))
	defer cleanup()

	resp, err := client.BarrierRecoveryStatus(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("BarrierRecoveryStatus failed: %v", err)
	}
	if !resp.GetHasRecoveryKeys() {
		t.Fatal("expected HasRecoveryKeys=true")
	}
}

// TestGRPC_Barrier_NilService checks that every node-barrier RPC answers
// Unimplemented when the server has no barrier service.
func TestGRPC_Barrier_NilService(t *testing.T) {
	ctx := context.Background()
	calls := map[string]func(pb.QRDBServiceClient) error{
		"BarrierInitialize": func(c pb.QRDBServiceClient) error {
			_, err := c.BarrierInitialize(ctx, &pb.BarrierInitializeRequest{Secret: []byte("s")})
			return err
		},
		"BarrierUnseal": func(c pb.QRDBServiceClient) error {
			_, err := c.BarrierUnseal(ctx, &pb.BarrierUnsealRequest{Secret: []byte("s")})
			return err
		},
		"BarrierSeal": func(c pb.QRDBServiceClient) error {
			_, err := c.BarrierSeal(ctx, &pb.BarrierSealRequest{})
			return err
		},
		"BarrierStatus": func(c pb.QRDBServiceClient) error {
			_, err := c.BarrierStatus(ctx, &emptypb.Empty{})
			return err
		},
		"BarrierShamirInitialize": func(c pb.QRDBServiceClient) error {
			_, err := c.BarrierShamirInitialize(ctx, &pb.BarrierShamirInitializeRequest{})
			return err
		},
		"BarrierRecoveryGenerate": func(c pb.QRDBServiceClient) error {
			_, err := c.BarrierRecoveryGenerate(ctx, &pb.BarrierRecoveryGenerateRequest{})
			return err
		},
		"BarrierRecoveryDelete": func(c pb.QRDBServiceClient) error {
			_, err := c.BarrierRecoveryDelete(ctx, &pb.BarrierRecoveryDeleteRequest{})
			return err
		},
		"BarrierRecoveryStatus": func(c pb.QRDBServiceClient) error {
			_, err := c.BarrierRecoveryStatus(ctx, &emptypb.Empty{})
			return err
		},
	}
	client, cleanup := startTestServerFull(t)
	defer cleanup()
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			requireUnimplemented(t, call(client))
		})
	}
}
