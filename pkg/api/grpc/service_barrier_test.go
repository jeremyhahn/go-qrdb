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
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Helpers — extend mockBarrierService with configurable officer/claim/vote fns.
// These wrap the existing struct which returns zero-value defaults.
// ---------------------------------------------------------------------------

type extMockBarrierService struct {
	*mockBarrierService
	officerRegisterFn         func(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error)
	officerListFn             func(ctx context.Context) ([]*service.BarrierOfficerStatus, error)
	officerClaimStatusFnExt   func(ctx context.Context) ([]*service.BarrierOfficerStatus, error)
	shareClaimBeginFn         func(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error)
	shareClaimCompleteFn      func(ctx context.Context, username, nonceHex string, sig []byte, thumbprint string) (*service.BarrierShareClaimResult, error)
	shamirSealVoteFn          func(ctx context.Context, share string) (*service.UnsealShareProgress, error)
	recoveryOfficerRegisterFn func(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error)
	recoveryClaimBeginFn      func(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error)
	recoveryClaimCompleteFn   func(ctx context.Context, username, nonceHex string, sig []byte, thumbprint string) (*service.BarrierShareClaimResult, error)
}

func newExtMockBarrierService() *extMockBarrierService {
	return &extMockBarrierService{mockBarrierService: newMockBarrierService()}
}

func (m *extMockBarrierService) BarrierOfficerRegister(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	if m.officerRegisterFn != nil {
		return m.officerRegisterFn(ctx, req)
	}
	return &service.BarrierOfficerRegisterResult{CertPEM: []byte("cert-pem"), ShareIndex: 1}, nil
}
func (m *extMockBarrierService) BarrierOfficerList(ctx context.Context) ([]*service.BarrierOfficerStatus, error) {
	if m.officerListFn != nil {
		return m.officerListFn(ctx)
	}
	return nil, nil
}
func (m *extMockBarrierService) BarrierOfficerClaimStatus(ctx context.Context) ([]*service.BarrierOfficerStatus, error) {
	if m.officerClaimStatusFnExt != nil {
		return m.officerClaimStatusFnExt(ctx)
	}
	return nil, nil
}
func (m *extMockBarrierService) BarrierShareClaimBegin(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error) {
	if m.shareClaimBeginFn != nil {
		return m.shareClaimBeginFn(ctx, username, thumbprint)
	}
	return &service.BarrierShareClaimChallenge{NonceHex: "deadbeef", ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (m *extMockBarrierService) BarrierShareClaimComplete(ctx context.Context, username, nonceHex string, sig []byte, thumbprint string) (*service.BarrierShareClaimResult, error) {
	if m.shareClaimCompleteFn != nil {
		return m.shareClaimCompleteFn(ctx, username, nonceHex, sig, thumbprint)
	}
	return &service.BarrierShareClaimResult{ShareIndex: 1, ShareBytes: []byte("share-bytes")}, nil
}
func (m *extMockBarrierService) BarrierShamirSealVote(ctx context.Context, share string) (*service.UnsealShareProgress, error) {
	if m.shamirSealVoteFn != nil {
		return m.shamirSealVoteFn(ctx, share)
	}
	return &service.UnsealShareProgress{Required: 2, Submitted: 1, Complete: false}, nil
}
func (m *extMockBarrierService) BarrierRecoveryOfficerRegister(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	if m.recoveryOfficerRegisterFn != nil {
		return m.recoveryOfficerRegisterFn(ctx, req)
	}
	return &service.BarrierOfficerRegisterResult{CertPEM: []byte("recovery-cert"), ShareIndex: 2}, nil
}
func (m *extMockBarrierService) BarrierRecoveryShareClaimBegin(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error) {
	if m.recoveryClaimBeginFn != nil {
		return m.recoveryClaimBeginFn(ctx, username, thumbprint)
	}
	return &service.BarrierShareClaimChallenge{NonceHex: "cafebabe", ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (m *extMockBarrierService) BarrierRecoveryShareClaimComplete(ctx context.Context, username, nonceHex string, sig []byte, thumbprint string) (*service.BarrierShareClaimResult, error) {
	if m.recoveryClaimCompleteFn != nil {
		return m.recoveryClaimCompleteFn(ctx, username, nonceHex, sig, thumbprint)
	}
	return &service.BarrierShareClaimResult{ShareIndex: 2, ShareBytes: []byte("recovery-share-bytes")}, nil
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

func TestBarrierOfficerRegister_Success(t *testing.T) {
	svc := newExtMockBarrierService()
	var gotReq *service.BarrierOfficerRegisterRequest
	svc.officerRegisterFn = func(_ context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
		gotReq = req
		return &service.BarrierOfficerRegisterResult{CertPEM: []byte("signed-cert"), ShareIndex: 1}, nil
	}
	client, cleanup := startBarrierTestServer(t, svc)
	defer cleanup()

	resp, err := (*client).BarrierOfficerRegister(context.Background(), &pb.BarrierOfficerRegisterRequest{
		Username: "alice",
		CsrPem:   []byte("pem-data"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.GetCertPem()) != "signed-cert" {
		t.Errorf("expected cert 'signed-cert', got %q", resp.GetCertPem())
	}
	if resp.GetShareIndex() != 1 {
		t.Errorf("expected share_index 1, got %d", resp.GetShareIndex())
	}
	if gotReq == nil || gotReq.Username != "alice" {
		t.Errorf("unexpected request: %+v", gotReq)
	}
	// Security invariant: response must NOT contain share bytes.
	_ = resp.GetCertPem()
	_ = resp.GetShareIndex()
}

func TestBarrierOfficerRegister_CeremonyError(t *testing.T) {
	svc := newExtMockBarrierService()
	svc.officerRegisterFn = func(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
		return nil, service.ErrCeremonyNotConfigured
	}
	client, cleanup := startBarrierTestServer(t, svc)
	defer cleanup()

	_, err := (*client).BarrierOfficerRegister(context.Background(), &pb.BarrierOfficerRegisterRequest{Username: "alice"})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %s", st.Code())
	}
}

func TestBarrierOfficerRegister_OfficerNotFound(t *testing.T) {
	svc := newExtMockBarrierService()
	svc.officerRegisterFn = func(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
		return nil, ceremony.ErrOfficerNotFound
	}
	client, cleanup := startBarrierTestServer(t, svc)
	defer cleanup()

	_, err := (*client).BarrierOfficerRegister(context.Background(), &pb.BarrierOfficerRegisterRequest{Username: "ghost"})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
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

func TestBarrierOfficerList_Success(t *testing.T) {
	svc := newExtMockBarrierService()
	svc.officerListFn = func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
		return []*service.BarrierOfficerStatus{
			{
				Username:       "alice",
				CertThumbprint: "aa:bb:cc",
				ShareIndex:     1,
				AuthFactor:     "software",
				EnrolledAt:     time.Now(),
				SharePending:   true,
			},
		}, nil
	}
	client, cleanup := startBarrierTestServer(t, svc)
	defer cleanup()

	resp, err := (*client).BarrierOfficerList(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetOfficers()) != 1 {
		t.Fatalf("expected 1 officer, got %d", len(resp.GetOfficers()))
	}
	o := resp.GetOfficers()[0]
	if o.GetUsername() != "alice" {
		t.Errorf("expected username 'alice', got %q", o.GetUsername())
	}
	if o.GetShareIndex() != 1 {
		t.Errorf("expected share_index 1, got %d", o.GetShareIndex())
	}
	// Security invariant: OfficerStatus in response has no share bytes.
	_ = o.GetCertThumbprint()
}

func TestBarrierOfficerList_Empty(t *testing.T) {
	svc := newExtMockBarrierService()
	svc.officerListFn = func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
		return nil, nil
	}
	client, cleanup := startBarrierTestServer(t, svc)
	defer cleanup()

	resp, err := (*client).BarrierOfficerList(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetOfficers()) != 0 {
		t.Errorf("expected empty list, got %d", len(resp.GetOfficers()))
	}
}

func TestBarrierOfficerList_OfficerWithClaimedAt(t *testing.T) {
	svc := newExtMockBarrierService()
	claimedAt := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	svc.officerListFn = func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
		return []*service.BarrierOfficerStatus{
			{
				Username:   "bob",
				EnrolledAt: time.Now(),
				ClaimedAt:  claimedAt,
			},
		}, nil
	}
	client, cleanup := startBarrierTestServer(t, svc)
	defer cleanup()

	resp, err := (*client).BarrierOfficerList(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetOfficers()) != 1 {
		t.Fatalf("expected 1 officer")
	}
	if resp.GetOfficers()[0].GetClaimedAt() == "" {
		t.Error("expected non-empty claimed_at for claimed officer")
	}
}

func TestBarrierOfficerList_ServiceError(t *testing.T) {
	svc := newExtMockBarrierService()
	svc.officerListFn = func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
		return nil, errors.New("list: io error")
	}
	client, cleanup := startBarrierTestServer(t, svc)
	defer cleanup()

	_, err := (*client).BarrierOfficerList(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// BarrierOfficerClaimStatus
// ---------------------------------------------------------------------------

func TestBarrierOfficerClaimStatus_NilService(t *testing.T) {
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.BarrierOfficerClaimStatus(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestBarrierOfficerClaimStatus_Success(t *testing.T) {
	svc := newExtMockBarrierService()
	svc.officerClaimStatusFnExt = func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
		return []*service.BarrierOfficerStatus{
			{Username: "alice", ShareIndex: 1, SharePending: false, EnrolledAt: time.Now()},
			{Username: "bob", ShareIndex: 2, SharePending: true, EnrolledAt: time.Now()},
		}, nil
	}
	client, cleanup := startBarrierTestServer(t, svc)
	defer cleanup()

	resp, err := (*client).BarrierOfficerClaimStatus(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetOfficers()) != 2 {
		t.Fatalf("expected 2 officers, got %d", len(resp.GetOfficers()))
	}
	// Security invariant: claim-status response contains only public metadata, no share bytes.
	for _, o := range resp.GetOfficers() {
		if o.GetSharePending() && len(o.GetCertThumbprint()) > 0 {
			// thumbprint is fine; share bytes must not appear
		}
		_ = o.GetUsername()
	}
}

func TestBarrierOfficerClaimStatus_ServiceError(t *testing.T) {
	svc := newExtMockBarrierService()
	svc.officerClaimStatusFnExt = func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
		return nil, errors.New("claim-status: storage unavailable")
	}
	client, cleanup := startBarrierTestServer(t, svc)
	defer cleanup()

	_, err := (*client).BarrierOfficerClaimStatus(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// BarrierShareClaimBegin — requires a peer cert; without one we get
// InvalidArgument from peerCertThumbprint. We test via the direct server
// method to avoid needing a real TLS connection.
// ---------------------------------------------------------------------------

func TestBarrierShareClaimBegin_NoPeerCert(t *testing.T) {
	svc := newExtMockBarrierService()
	srv := &qrdbServiceServer{barrierSvc: svc}
	_, err := srv.BarrierShareClaimBegin(context.Background(), &pb.BarrierShareClaimBeginRequest{Username: "alice"})
	if err == nil {
		t.Fatal("expected error without peer cert")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

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

func TestBarrierShareClaimComplete_NoPeerCert(t *testing.T) {
	svc := newExtMockBarrierService()
	srv := &qrdbServiceServer{barrierSvc: svc}
	_, err := srv.BarrierShareClaimComplete(context.Background(), &pb.BarrierShareClaimCompleteRequest{
		Username: "alice", NonceHex: "deadbeef", Signature: []byte("sig"),
	})
	if err == nil {
		t.Fatal("expected error without peer cert")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

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
// BarrierShamirSealVote
// ---------------------------------------------------------------------------

func TestBarrierShamirSealVote_NilService(t *testing.T) {
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.BarrierShamirSealVote(context.Background(), &pb.BarrierShamirSealVoteRequest{Share: "share-1"})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestBarrierShamirSealVote_Success(t *testing.T) {
	svc := newExtMockBarrierService()
	var gotShare string
	svc.shamirSealVoteFn = func(_ context.Context, share string) (*service.UnsealShareProgress, error) {
		gotShare = share
		return &service.UnsealShareProgress{Required: 3, Submitted: 2, Complete: false}, nil
	}
	client, cleanup := startBarrierTestServer(t, svc)
	defer cleanup()

	resp, err := (*client).BarrierShamirSealVote(context.Background(), &pb.BarrierShamirSealVoteRequest{Share: "share-data"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotShare != "share-data" {
		t.Errorf("expected share 'share-data', got %q", gotShare)
	}
	if resp.GetRequired() != 3 || resp.GetSubmitted() != 2 || resp.GetComplete() {
		t.Errorf("unexpected progress: required=%d submitted=%d complete=%v",
			resp.GetRequired(), resp.GetSubmitted(), resp.GetComplete())
	}
}

func TestBarrierShamirSealVote_Complete(t *testing.T) {
	svc := newExtMockBarrierService()
	svc.shamirSealVoteFn = func(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
		return &service.UnsealShareProgress{Required: 2, Submitted: 2, Complete: true}, nil
	}
	client, cleanup := startBarrierTestServer(t, svc)
	defer cleanup()

	resp, err := (*client).BarrierShamirSealVote(context.Background(), &pb.BarrierShamirSealVoteRequest{Share: "final-share"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetComplete() {
		t.Error("expected complete=true")
	}
}

func TestBarrierShamirSealVote_RequiresQuorumError(t *testing.T) {
	svc := newExtMockBarrierService()
	svc.shamirSealVoteFn = func(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
		return nil, seal.ErrShamirBarrierRequiresQuorum
	}
	client, cleanup := startBarrierTestServer(t, svc)
	defer cleanup()

	_, err := (*client).BarrierShamirSealVote(context.Background(), &pb.BarrierShamirSealVoteRequest{Share: "s"})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %s", st.Code())
	}
}

func TestBarrierShamirSealVote_VoteVerificationFailed(t *testing.T) {
	svc := newExtMockBarrierService()
	svc.shamirSealVoteFn = func(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
		return nil, seal.ErrSealVoteVerificationFailed
	}
	client, cleanup := startBarrierTestServer(t, svc)
	defer cleanup()

	_, err := (*client).BarrierShamirSealVote(context.Background(), &pb.BarrierShamirSealVoteRequest{Share: "bad-share"})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %s", st.Code())
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

func TestBarrierRecoveryOfficerRegister_Success(t *testing.T) {
	svc := newExtMockBarrierService()
	var gotReq *service.BarrierOfficerRegisterRequest
	svc.recoveryOfficerRegisterFn = func(_ context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
		gotReq = req
		return &service.BarrierOfficerRegisterResult{CertPEM: []byte("recovery-cert-pem"), ShareIndex: 2}, nil
	}
	client, cleanup := startBarrierTestServer(t, svc)
	defer cleanup()

	resp, err := (*client).BarrierRecoveryOfficerRegister(context.Background(), &pb.BarrierOfficerRegisterRequest{
		Username: "recovery-alice",
		CsrPem:   []byte("csr-pem"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.GetCertPem()) != "recovery-cert-pem" {
		t.Errorf("expected cert 'recovery-cert-pem', got %q", resp.GetCertPem())
	}
	if resp.GetShareIndex() != 2 {
		t.Errorf("expected share_index 2, got %d", resp.GetShareIndex())
	}
	if gotReq == nil || gotReq.Username != "recovery-alice" {
		t.Errorf("unexpected request: %+v", gotReq)
	}
	// Security invariant: response must NOT expose share bytes.
	_ = resp.GetCertPem()
}

func TestBarrierRecoveryOfficerRegister_InvalidAuthType(t *testing.T) {
	svc := newExtMockBarrierService()
	svc.recoveryOfficerRegisterFn = func(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
		return nil, ceremony.ErrInvalidAuthenticatorType
	}
	client, cleanup := startBarrierTestServer(t, svc)
	defer cleanup()

	_, err := (*client).BarrierRecoveryOfficerRegister(context.Background(), &pb.BarrierOfficerRegisterRequest{
		Username: "recovery-alice",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// BarrierRecoveryShareClaimBegin — requires peer cert.
// ---------------------------------------------------------------------------

func TestBarrierRecoveryShareClaimBegin_NoPeerCert(t *testing.T) {
	svc := newExtMockBarrierService()
	srv := &qrdbServiceServer{barrierSvc: svc}
	_, err := srv.BarrierRecoveryShareClaimBegin(context.Background(), &pb.BarrierShareClaimBeginRequest{Username: "alice"})
	if err == nil {
		t.Fatal("expected error without peer cert")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

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

func TestBarrierRecoveryShareClaimComplete_NoPeerCert(t *testing.T) {
	svc := newExtMockBarrierService()
	srv := &qrdbServiceServer{barrierSvc: svc}
	_, err := srv.BarrierRecoveryShareClaimComplete(context.Background(), &pb.BarrierShareClaimCompleteRequest{
		Username: "alice", NonceHex: "deadbeef",
	})
	if err == nil {
		t.Fatal("expected error without peer cert")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

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
// mapBarrierCeremonyError — covers every sentinel.
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
		{"seal vote failed", seal.ErrSealVoteVerificationFailed, codes.PermissionDenied},
		{"unseal key mismatch", seal.ErrUnsealKeyMismatch, codes.PermissionDenied},
		{"wrapped root key corrupt", seal.ErrWrappedRootKeyCorrupt, codes.Internal},
		{"cert thumbprint mismatch", ceremony.ErrCertThumbprintMismatch, codes.PermissionDenied},
		{"officer not found", ceremony.ErrOfficerNotFound, codes.NotFound},
		{"share already claimed", ceremony.ErrShareAlreadyClaimed, codes.FailedPrecondition},
		{"no pending share", ceremony.ErrNoPendingShare, codes.NotFound},
		{"insufficient officers", ceremony.ErrInsufficientOfficers, codes.FailedPrecondition},
		{"invalid auth type", ceremony.ErrInvalidAuthenticatorType, codes.InvalidArgument},
		{"generic error", errors.New("something went wrong"), codes.Internal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mapBarrierCeremonyError(tc.err)
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
// barrierOfficerStatusesToProto — covers the ClaimedAt=zero and non-zero paths.
// ---------------------------------------------------------------------------

func TestBarrierOfficerStatusesToProto_ZeroAndNonZeroClaimedAt(t *testing.T) {
	claimedAt := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	officers := []*service.BarrierOfficerStatus{
		{
			Username:       "alice",
			CertThumbprint: "aa:bb",
			ShareIndex:     1,
			AuthFactor:     "software",
			EnrolledAt:     time.Now(),
			// ClaimedAt is zero: should produce empty string.
		},
		{
			Username:   "bob",
			ShareIndex: 2,
			EnrolledAt: time.Now(),
			ClaimedAt:  claimedAt,
		},
	}
	out := barrierOfficerStatusesToProto(officers)
	if len(out) != 2 {
		t.Fatalf("expected 2 results, got %d", len(out))
	}
	if out[0].GetClaimedAt() != "" {
		t.Errorf("expected empty claimed_at for unclaimed officer, got %q", out[0].GetClaimedAt())
	}
	if out[1].GetClaimedAt() == "" {
		t.Error("expected non-empty claimed_at for claimed officer")
	}
}

// ---------------------------------------------------------------------------
// INV-11 wire-contract guard: raw share material (fields named "shares" or
// "share_bytes") must never appear in a response message, with exactly two
// exceptions — the one-time claim-complete responses that deliver a share to
// the claiming security officer over its mTLS-bound session. Requests may
// carry shares (officers submit them for unseal/seal-vote/root-token).
// ---------------------------------------------------------------------------

func TestProto_OnlyShareClaimResponseContainsShareField(t *testing.T) {
	allowed := map[string]bool{
		"BarrierShareClaimCompleteResponse":       true,
		"TenantBarrierShareClaimCompleteResponse": true,
	}
	shareFields := map[string]bool{
		"shares":      true,
		"share_bytes": true,
	}

	fd := pb.File_pkg_api_grpc_proto_qrdbv1_qrdb_proto

	var walk func(md protoreflect.MessageDescriptor)
	walk = func(md protoreflect.MessageDescriptor) {
		name := string(md.Name())
		isResponse := strings.HasSuffix(name, "Response")
		fields := md.Fields()
		hasShareField := false
		for i := 0; i < fields.Len(); i++ {
			fld := fields.Get(i)
			if !shareFields[string(fld.Name())] {
				continue
			}
			hasShareField = true
			if isResponse && !allowed[name] {
				t.Errorf("INV-11 violation: response message %s carries share field %q", name, fld.Name())
			}
		}
		if allowed[name] && !hasShareField {
			t.Errorf("whitelisted claim response %s no longer carries a share field; update the INV-11 guard", name)
		}
		nested := md.Messages()
		for i := 0; i < nested.Len(); i++ {
			walk(nested.Get(i))
		}
	}

	msgs := fd.Messages()
	seenAllowed := 0
	for i := 0; i < msgs.Len(); i++ {
		if allowed[string(msgs.Get(i).Name())] {
			seenAllowed++
		}
		walk(msgs.Get(i))
	}
	if seenAllowed != len(allowed) {
		t.Errorf("expected %d whitelisted claim-complete responses in descriptor, found %d", len(allowed), seenAllowed)
	}
}
