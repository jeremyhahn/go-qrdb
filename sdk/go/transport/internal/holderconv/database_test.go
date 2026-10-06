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

package holderconv

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// mappedError is the typed error mapErrForTest wraps an RPC failure in, so
// tests can assert MapErr actually ran (not just that SOME error came back)
// without reaching for fmt.Errorf.
type mappedError struct {
	method string
	err    error
}

func (e *mappedError) Error() string { return "mapped: " + e.method + ": " + e.err.Error() }
func (e *mappedError) Unwrap() error { return e.err }

func mapErrForTest(method string, err error) error { return &mappedError{method: method, err: err} }

// fakeDatabaseHolderRPC implements DatabaseHolderRPC. Each method returns
// the matching canned response/err pair and records its own request, so a
// test can drive the success path, the RPC-error path (asserting MapErr
// ran), and -- via DatabaseHolders itself -- the empty-database path that
// never reaches the client at all.
type fakeDatabaseHolderRPC struct {
	err error

	registerReq  *pb.DatabaseBarrierOfficerRegisterRequest
	registerResp *pb.BarrierOfficerRegisterResponse

	listReq  *pb.DatabaseBarrierOfficerListRequest
	listResp *pb.BarrierOfficerListResponse

	initReq  *pb.DatabaseBarrierShamirInitializeRequest
	initResp *pb.BarrierShamirOperationResponse

	claimBeginReq  *pb.DatabaseBarrierShareClaimBeginRequest
	claimBeginResp *pb.BarrierShareClaimBeginResponse

	claimCompleteReq  *pb.DatabaseBarrierShareClaimCompleteRequest
	claimCompleteResp *pb.BarrierShareClaimCompleteResponse

	quorumBeginReq  *pb.DatabaseBarrierQuorumBeginRequest
	quorumBeginResp *pb.BarrierQuorumChallenge

	quorumSubmitReq  *pb.DatabaseBarrierQuorumSubmitRequest
	quorumSubmitResp *pb.BarrierQuorumOutcome

	rekeyReq  *pb.DatabaseBarrierRekeyStartRequest
	rekeyResp *pb.BarrierQuorumRound

	revokeReq  *pb.DatabaseBarrierOfficerRevokeRequest
	revokeResp *pb.BarrierQuorumRound

	unenrolReq *pb.DatabaseBarrierOfficerUnenrolRequest
}

func (f *fakeDatabaseHolderRPC) DatabaseBarrierOfficerRegister(_ context.Context, in *pb.DatabaseBarrierOfficerRegisterRequest, _ ...grpc.CallOption) (*pb.BarrierOfficerRegisterResponse, error) {
	f.registerReq = in
	return f.registerResp, f.err
}

func (f *fakeDatabaseHolderRPC) DatabaseBarrierOfficerList(_ context.Context, in *pb.DatabaseBarrierOfficerListRequest, _ ...grpc.CallOption) (*pb.BarrierOfficerListResponse, error) {
	f.listReq = in
	return f.listResp, f.err
}

func (f *fakeDatabaseHolderRPC) DatabaseBarrierShamirInitialize(_ context.Context, in *pb.DatabaseBarrierShamirInitializeRequest, _ ...grpc.CallOption) (*pb.BarrierShamirOperationResponse, error) {
	f.initReq = in
	return f.initResp, f.err
}

func (f *fakeDatabaseHolderRPC) DatabaseBarrierShareClaimBegin(_ context.Context, in *pb.DatabaseBarrierShareClaimBeginRequest, _ ...grpc.CallOption) (*pb.BarrierShareClaimBeginResponse, error) {
	f.claimBeginReq = in
	return f.claimBeginResp, f.err
}

func (f *fakeDatabaseHolderRPC) DatabaseBarrierShareClaimComplete(_ context.Context, in *pb.DatabaseBarrierShareClaimCompleteRequest, _ ...grpc.CallOption) (*pb.BarrierShareClaimCompleteResponse, error) {
	f.claimCompleteReq = in
	return f.claimCompleteResp, f.err
}

func (f *fakeDatabaseHolderRPC) DatabaseBarrierQuorumBegin(_ context.Context, in *pb.DatabaseBarrierQuorumBeginRequest, _ ...grpc.CallOption) (*pb.BarrierQuorumChallenge, error) {
	f.quorumBeginReq = in
	return f.quorumBeginResp, f.err
}

func (f *fakeDatabaseHolderRPC) DatabaseBarrierQuorumSubmit(_ context.Context, in *pb.DatabaseBarrierQuorumSubmitRequest, _ ...grpc.CallOption) (*pb.BarrierQuorumOutcome, error) {
	f.quorumSubmitReq = in
	return f.quorumSubmitResp, f.err
}

func (f *fakeDatabaseHolderRPC) DatabaseBarrierRekeyStart(_ context.Context, in *pb.DatabaseBarrierRekeyStartRequest, _ ...grpc.CallOption) (*pb.BarrierQuorumRound, error) {
	f.rekeyReq = in
	return f.rekeyResp, f.err
}

func (f *fakeDatabaseHolderRPC) DatabaseBarrierOfficerRevoke(_ context.Context, in *pb.DatabaseBarrierOfficerRevokeRequest, _ ...grpc.CallOption) (*pb.BarrierQuorumRound, error) {
	f.revokeReq = in
	return f.revokeResp, f.err
}

func (f *fakeDatabaseHolderRPC) DatabaseBarrierOfficerUnenrol(_ context.Context, in *pb.DatabaseBarrierOfficerUnenrolRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	f.unenrolReq = in
	return &emptypb.Empty{}, f.err
}

// TestDatabaseHolders_Register covers the empty-database refusal, the
// mapped-error path, and a full success decode.
func TestDatabaseHolders_Register(t *testing.T) {
	fake := &fakeDatabaseHolderRPC{}
	d := DatabaseHolders{Client: fake, MapErr: mapErrForTest}

	if _, err := d.Register(context.Background(), nil); err == nil {
		t.Error("Register(nil) must refuse, not reach the client")
	}
	var nameErr *transport.DatabaseNameRequiredError
	if _, err := d.Register(context.Background(), &transport.DatabaseOfficerRegisterRequest{}); !errors.As(err, &nameErr) {
		t.Errorf("Register(no database) = %v, want *DatabaseNameRequiredError", err)
	}

	var fpErr *FactorParamsError
	badParams := transport.DatabaseOfficerRegisterRequest{
		Database: "db1",
		OfficerRegisterRequest: transport.OfficerRegisterRequest{
			Factors: []transport.HolderFactorEnrolment{{Name: "fido2", Params: map[string]any{"x": make(chan int)}}},
		},
	}
	if _, err := d.Register(context.Background(), &badParams); !errors.As(err, &fpErr) {
		t.Errorf("Register(unencodable factor params) = %v, want *FactorParamsError", err)
	}

	fake.err = errors.New("rpc failed")
	if _, err := d.Register(context.Background(), &transport.DatabaseOfficerRegisterRequest{Database: "db1"}); !isMappedFor(err, "DatabaseBarrierOfficerRegister") {
		t.Errorf("Register(rpc error) = %v, want mapped DatabaseBarrierOfficerRegister error", err)
	}
	fake.err = nil

	fake.registerResp = &pb.BarrierOfficerRegisterResponse{CertPem: []byte("cert"), Role: "so"}
	resp, err := d.Register(context.Background(), &transport.DatabaseOfficerRegisterRequest{
		Database: "db1", OfficerRegisterRequest: transport.OfficerRegisterRequest{Username: "alice", CSRPEM: "csr"},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if resp.CertPEM != "cert" || resp.Role != "so" {
		t.Errorf("Register() = %+v", resp)
	}
	if fake.registerReq.GetDatabase() != "db1" || fake.registerReq.GetOfficer().GetUsername() != "alice" {
		t.Errorf("Register() sent %+v", fake.registerReq)
	}
}

// TestDatabaseHolders_List covers the empty-database refusal, the
// mapped-error path, and a full success decode.
func TestDatabaseHolders_List(t *testing.T) {
	fake := &fakeDatabaseHolderRPC{}
	d := DatabaseHolders{Client: fake, MapErr: mapErrForTest}

	var nameErr *transport.DatabaseNameRequiredError
	if _, err := d.List(context.Background(), ""); !errors.As(err, &nameErr) {
		t.Errorf("List(\"\") = %v, want *DatabaseNameRequiredError", err)
	}

	fake.err = errors.New("rpc failed")
	if _, err := d.List(context.Background(), "db1"); !isMappedFor(err, "DatabaseBarrierOfficerList") {
		t.Errorf("List(rpc error) = %v, want mapped DatabaseBarrierOfficerList error", err)
	}
	fake.err = nil

	fake.listResp = &pb.BarrierOfficerListResponse{Officers: []*pb.OfficerStatus{{Username: "alice"}}}
	out, err := d.List(context.Background(), "db1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 1 || out[0].Username != "alice" {
		t.Errorf("List() = %+v", out)
	}
	if fake.listReq.GetDatabase() != "db1" {
		t.Errorf("List() sent %+v", fake.listReq)
	}
}

// TestDatabaseHolders_Initialize covers the empty-database refusal, the
// mapped-error path, and a full success decode.
func TestDatabaseHolders_Initialize(t *testing.T) {
	fake := &fakeDatabaseHolderRPC{}
	d := DatabaseHolders{Client: fake, MapErr: mapErrForTest}

	var nameErr *transport.DatabaseNameRequiredError
	if _, err := d.Initialize(context.Background(), nil); !errors.As(err, &nameErr) {
		t.Errorf("Initialize(nil) = %v, want *DatabaseNameRequiredError", err)
	}

	fake.err = errors.New("rpc failed")
	if _, err := d.Initialize(context.Background(), &transport.DatabaseShamirInitializeRequest{Database: "db1"}); !isMappedFor(err, "DatabaseBarrierShamirInitialize") {
		t.Errorf("Initialize(rpc error) = %v, want mapped DatabaseBarrierShamirInitialize error", err)
	}
	fake.err = nil

	fake.initResp = &pb.BarrierShamirOperationResponse{Threshold: 2, TotalShares: 3, ShareCount: 3}
	out, err := d.Initialize(context.Background(), &transport.DatabaseShamirInitializeRequest{Database: "db1", Policy: "designated"})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if out.Threshold != 2 || out.TotalShares != 3 || out.ShareCount != 3 {
		t.Errorf("Initialize() = %+v", out)
	}
	if fake.initReq.GetDatabase() != "db1" || fake.initReq.GetPolicy() != "designated" {
		t.Errorf("Initialize() sent %+v", fake.initReq)
	}
}

// TestDatabaseHolders_ClaimBegin covers the empty-database refusal, the
// mapped-error path, and a full success decode.
func TestDatabaseHolders_ClaimBegin(t *testing.T) {
	fake := &fakeDatabaseHolderRPC{}
	d := DatabaseHolders{Client: fake, MapErr: mapErrForTest}

	var nameErr *transport.DatabaseNameRequiredError
	if _, err := d.ClaimBegin(context.Background(), nil); !errors.As(err, &nameErr) {
		t.Errorf("ClaimBegin(nil) = %v, want *DatabaseNameRequiredError", err)
	}

	fake.err = errors.New("rpc failed")
	if _, err := d.ClaimBegin(context.Background(), &transport.DatabaseShareClaimBeginRequest{Database: "db1"}); !isMappedFor(err, "DatabaseBarrierShareClaimBegin") {
		t.Errorf("ClaimBegin(rpc error) = %v, want mapped DatabaseBarrierShareClaimBegin error", err)
	}
	fake.err = nil

	fake.claimBeginResp = &pb.BarrierShareClaimBeginResponse{NonceHex: "nonce"}
	out, err := d.ClaimBegin(context.Background(), &transport.DatabaseShareClaimBeginRequest{
		Database: "db1", ShareClaimBeginRequest: transport.ShareClaimBeginRequest{Username: "alice"},
	})
	if err != nil {
		t.Fatalf("ClaimBegin: %v", err)
	}
	if out.NonceHex != "nonce" {
		t.Errorf("ClaimBegin() = %+v", out)
	}
	if fake.claimBeginReq.GetDatabase() != "db1" || fake.claimBeginReq.GetClaim().GetUsername() != "alice" {
		t.Errorf("ClaimBegin() sent %+v", fake.claimBeginReq)
	}
}

// TestDatabaseHolders_ClaimComplete covers the empty-database refusal, the
// mapped-error path, and a full success decode.
func TestDatabaseHolders_ClaimComplete(t *testing.T) {
	fake := &fakeDatabaseHolderRPC{}
	d := DatabaseHolders{Client: fake, MapErr: mapErrForTest}

	var nameErr *transport.DatabaseNameRequiredError
	if _, err := d.ClaimComplete(context.Background(), nil); !errors.As(err, &nameErr) {
		t.Errorf("ClaimComplete(nil) = %v, want *DatabaseNameRequiredError", err)
	}

	fake.err = errors.New("rpc failed")
	if _, err := d.ClaimComplete(context.Background(), &transport.DatabaseShareClaimCompleteRequest{Database: "db1"}); !isMappedFor(err, "DatabaseBarrierShareClaimComplete") {
		t.Errorf("ClaimComplete(rpc error) = %v, want mapped DatabaseBarrierShareClaimComplete error", err)
	}
	fake.err = nil

	fake.claimCompleteResp = &pb.BarrierShareClaimCompleteResponse{ShareIndex: 1, Envelope: []byte("env")}
	out, err := d.ClaimComplete(context.Background(), &transport.DatabaseShareClaimCompleteRequest{
		Database: "db1",
		ShareClaimCompleteRequest: transport.ShareClaimCompleteRequest{
			Username: "alice", NonceHex: "nonce", Signature: []byte("sig"),
		},
	})
	if err != nil {
		t.Fatalf("ClaimComplete: %v", err)
	}
	if out.ShareIndex != 1 || string(out.Envelope) != "env" {
		t.Errorf("ClaimComplete() = %+v", out)
	}
	if fake.claimCompleteReq.GetDatabase() != "db1" || fake.claimCompleteReq.GetClaim().GetNonceHex() != "nonce" {
		t.Errorf("ClaimComplete() sent %+v", fake.claimCompleteReq)
	}
}

// TestDatabaseHolders_QuorumBegin covers the empty-database refusal, the
// mapped-error path, and a full success decode.
func TestDatabaseHolders_QuorumBegin(t *testing.T) {
	fake := &fakeDatabaseHolderRPC{}
	d := DatabaseHolders{Client: fake, MapErr: mapErrForTest}

	var nameErr *transport.DatabaseNameRequiredError
	if _, err := d.QuorumBegin(context.Background(), nil); !errors.As(err, &nameErr) {
		t.Errorf("QuorumBegin(nil) = %v, want *DatabaseNameRequiredError", err)
	}

	fake.err = errors.New("rpc failed")
	if _, err := d.QuorumBegin(context.Background(), &transport.DatabaseQuorumBeginRequest{Database: "db1"}); !isMappedFor(err, "DatabaseBarrierQuorumBegin") {
		t.Errorf("QuorumBegin(rpc error) = %v, want mapped DatabaseBarrierQuorumBegin error", err)
	}
	fake.err = nil

	fake.quorumBeginResp = &pb.BarrierQuorumChallenge{ChallengeId: "c1"}
	out, err := d.QuorumBegin(context.Background(), &transport.DatabaseQuorumBeginRequest{
		Database: "db1", QuorumBeginRequest: transport.QuorumBeginRequest{Domain: "unseal", Username: "alice"},
	})
	if err != nil {
		t.Fatalf("QuorumBegin: %v", err)
	}
	if out.ChallengeID != "c1" {
		t.Errorf("QuorumBegin() = %+v", out)
	}
	if fake.quorumBeginReq.GetDatabase() != "db1" || fake.quorumBeginReq.GetBegin().GetUsername() != "alice" {
		t.Errorf("QuorumBegin() sent %+v", fake.quorumBeginReq)
	}
}

// TestDatabaseHolders_QuorumSubmit covers the empty-database refusal, the
// mapped-error path, and a full success decode.
func TestDatabaseHolders_QuorumSubmit(t *testing.T) {
	fake := &fakeDatabaseHolderRPC{}
	d := DatabaseHolders{Client: fake, MapErr: mapErrForTest}

	var nameErr *transport.DatabaseNameRequiredError
	if _, err := d.QuorumSubmit(context.Background(), nil); !errors.As(err, &nameErr) {
		t.Errorf("QuorumSubmit(nil) = %v, want *DatabaseNameRequiredError", err)
	}

	fake.err = errors.New("rpc failed")
	if _, err := d.QuorumSubmit(context.Background(), &transport.DatabaseQuorumSubmitRequest{Database: "db1"}); !isMappedFor(err, "DatabaseBarrierQuorumSubmit") {
		t.Errorf("QuorumSubmit(rpc error) = %v, want mapped DatabaseBarrierQuorumSubmit error", err)
	}
	fake.err = nil

	fake.quorumSubmitResp = &pb.BarrierQuorumOutcome{Complete: true, Submitted: 2}
	out, err := d.QuorumSubmit(context.Background(), &transport.DatabaseQuorumSubmitRequest{
		Database: "db1", QuorumSubmitRequest: transport.QuorumSubmitRequest{Username: "alice", ChallengeID: "c1"},
	})
	if err != nil {
		t.Fatalf("QuorumSubmit: %v", err)
	}
	if !out.Complete || out.Submitted != 2 {
		t.Errorf("QuorumSubmit() = %+v", out)
	}
	if fake.quorumSubmitReq.GetDatabase() != "db1" || fake.quorumSubmitReq.GetSubmit().GetChallengeId() != "c1" {
		t.Errorf("QuorumSubmit() sent %+v", fake.quorumSubmitReq)
	}
}

// TestDatabaseHolders_RekeyStart covers the empty-database refusal, the
// mapped-error path, and a full success decode.
func TestDatabaseHolders_RekeyStart(t *testing.T) {
	fake := &fakeDatabaseHolderRPC{}
	d := DatabaseHolders{Client: fake, MapErr: mapErrForTest}

	var nameErr *transport.DatabaseNameRequiredError
	if _, err := d.RekeyStart(context.Background(), nil); !errors.As(err, &nameErr) {
		t.Errorf("RekeyStart(nil) = %v, want *DatabaseNameRequiredError", err)
	}

	fake.err = errors.New("rpc failed")
	if _, err := d.RekeyStart(context.Background(), &transport.DatabaseRekeyStartRequest{Database: "db1"}); !isMappedFor(err, "DatabaseBarrierRekeyStart") {
		t.Errorf("RekeyStart(rpc error) = %v, want mapped DatabaseBarrierRekeyStart error", err)
	}
	fake.err = nil

	fake.rekeyResp = &pb.BarrierQuorumRound{Required: 2, NewThreshold: 3}
	out, err := d.RekeyStart(context.Background(), &transport.DatabaseRekeyStartRequest{Database: "db1", Threshold: 3, Policy: "designated"})
	if err != nil {
		t.Fatalf("RekeyStart: %v", err)
	}
	if out.Required != 2 || out.NewThreshold != 3 {
		t.Errorf("RekeyStart() = %+v", out)
	}
	if fake.rekeyReq.GetDatabase() != "db1" || fake.rekeyReq.GetThreshold() != 3 || fake.rekeyReq.GetPolicy() != "designated" {
		t.Errorf("RekeyStart() sent %+v", fake.rekeyReq)
	}
}

// TestDatabaseHolders_Revoke covers the empty-database refusal, the
// mapped-error path, and a full success decode.
func TestDatabaseHolders_Revoke(t *testing.T) {
	fake := &fakeDatabaseHolderRPC{}
	d := DatabaseHolders{Client: fake, MapErr: mapErrForTest}

	var nameErr *transport.DatabaseNameRequiredError
	if _, err := d.Revoke(context.Background(), nil); !errors.As(err, &nameErr) {
		t.Errorf("Revoke(nil) = %v, want *DatabaseNameRequiredError", err)
	}

	fake.err = errors.New("rpc failed")
	if _, err := d.Revoke(context.Background(), &transport.DatabaseOfficerRevokeRequest{Database: "db1"}); !isMappedFor(err, "DatabaseBarrierOfficerRevoke") {
		t.Errorf("Revoke(rpc error) = %v, want mapped DatabaseBarrierOfficerRevoke error", err)
	}
	fake.err = nil

	fake.revokeResp = &pb.BarrierQuorumRound{Required: 1}
	out, err := d.Revoke(context.Background(), &transport.DatabaseOfficerRevokeRequest{Database: "db1", Username: "alice", Threshold: 2})
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if out.Required != 1 {
		t.Errorf("Revoke() = %+v", out)
	}
	if fake.revokeReq.GetDatabase() != "db1" || fake.revokeReq.GetUsername() != "alice" || fake.revokeReq.GetThreshold() != 2 {
		t.Errorf("Revoke() sent %+v", fake.revokeReq)
	}
}

// TestDatabaseHolders_Unenrol covers the empty-database refusal, the
// mapped-error path, and success (no response to decode, so success means
// a nil error and the right request sent).
func TestDatabaseHolders_Unenrol(t *testing.T) {
	fake := &fakeDatabaseHolderRPC{}
	d := DatabaseHolders{Client: fake, MapErr: mapErrForTest}

	var nameErr *transport.DatabaseNameRequiredError
	if err := d.Unenrol(context.Background(), nil); !errors.As(err, &nameErr) {
		t.Errorf("Unenrol(nil) = %v, want *DatabaseNameRequiredError", err)
	}

	fake.err = errors.New("rpc failed")
	if err := d.Unenrol(context.Background(), &transport.DatabaseOfficerUnenrolRequest{Database: "db1"}); !isMappedFor(err, "DatabaseBarrierOfficerUnenrol") {
		t.Errorf("Unenrol(rpc error) = %v, want mapped DatabaseBarrierOfficerUnenrol error", err)
	}
	fake.err = nil

	if err := d.Unenrol(context.Background(), &transport.DatabaseOfficerUnenrolRequest{Database: "db1", Username: "alice"}); err != nil {
		t.Fatalf("Unenrol: %v", err)
	}
	if fake.unenrolReq.GetDatabase() != "db1" || fake.unenrolReq.GetUsername() != "alice" {
		t.Errorf("Unenrol() sent %+v", fake.unenrolReq)
	}
}

// isMappedFor reports whether err is the *mappedError mapErrForTest
// produces for method, proving DatabaseHolders routed the RPC failure
// through MapErr rather than returning it (or something else) directly.
func isMappedFor(err error, method string) bool {
	var me *mappedError
	return errors.As(err, &me) && me.method == method
}
