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

package quic

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
)

// noHolderDatabaseSvc is a database service without share-holder custody.
// The handlers only type-assert it; none of its methods is called.
type noHolderDatabaseSvc struct{ service.DatabaseServicer }

// dbHolderCall invokes handler with in marshalled and decodes the reply
// into out.
func dbHolderCall(t *testing.T, ctx context.Context, handler func(context.Context, []byte) ([]byte, error), in, out proto.Message) error {
	t.Helper()
	payload, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := handler(ctx, payload)
	if err != nil {
		return err
	}
	if err := proto.Unmarshal(reply, out); err != nil {
		t.Fatal(err)
	}
	return nil
}

// dbHolderTLS is a context whose stream presents h's certificate.
func dbHolderTLS(h *servicetest.Holder) context.Context {
	return auth.WithTLSState(context.Background(), &tls.ConnectionState{PeerCertificates: []*x509.Certificate{h.Cert}})
}

// submitQUICDBShare answers h's mtls challenge for op and submits its share.
func submitQUICDBShare(t *testing.T, s *Server, op string, h *servicetest.Holder) *pb.BarrierQuorumOutcome {
	t.Helper()
	ctx := context.Background()
	var ch pb.BarrierQuorumChallenge
	if err := dbHolderCall(t, ctx, s.handleDatabaseBarrierQuorumBegin, &pb.DatabaseBarrierQuorumBeginRequest{
		Database: "vault", Begin: &pb.BarrierQuorumBeginRequest{Op: op, Username: h.Name},
	}, &ch); err != nil {
		t.Fatalf("%s begin %s: %v", op, h.Name, err)
	}
	var out pb.BarrierQuorumOutcome
	if err := dbHolderCall(t, ctx, s.handleDatabaseBarrierQuorumSubmit, &pb.DatabaseBarrierQuorumSubmitRequest{
		Database: "vault",
		Submit: &pb.BarrierQuorumSubmitRequest{
			Op: op, Username: h.Name, ChallengeId: ch.GetChallengeId(), Factor: ch.GetFactor(),
			Response: h.Sign(t, ch.GetChallengeId(), ch.GetMethodData()[mtls.MethodDataPurpose], ch.GetNonce()), Share: h.Share(t),
		},
	}, &out); err != nil {
		t.Fatalf("%s submit %s: %v", op, h.Name, err)
	}
	return &out
}

// TestQUICDatabaseHolders_Lifecycle drives a Shamir database's custody
// through the db.Barrier* handlers over a real database service: enrolment,
// listing, initialization, claims presenting each holder's certificate on
// the stream, a quorum seal and unseal, a rekey round and a revocation the
// remaining holders cannot satisfy.
func TestQUICDatabaseHolders_Lifecycle(t *testing.T) {
	fx := servicetest.NewShamirDatabase(t, "vault", 2, 2)
	s := NewServer(nil, nil, "127.0.0.1:0", WithDatabaseService(fx.Service))
	ctx := context.Background()
	holders := []*servicetest.Holder{servicetest.NewHolder(t, "alice"), servicetest.NewHolder(t, "bob")}

	for _, h := range holders {
		var resp pb.BarrierOfficerRegisterResponse
		if err := dbHolderCall(t, ctx, s.handleDatabaseBarrierOfficerRegister, &pb.DatabaseBarrierOfficerRegisterRequest{
			Database: "vault", Officer: &pb.BarrierOfficerRegisterRequest{Username: h.Name, CsrPem: []byte(h.CSRPEM)},
		}, &resp); err != nil {
			t.Fatalf("register %s: %v", h.Name, err)
		}
		h.Enrolled(t, string(resp.GetCertPem()))
	}
	// A third holder of a 2-of-2 database is refused before initialization;
	// one enrolled by mistake is removed by unenrolment, without a quorum.
	extra := servicetest.NewHolder(t, "carol")
	var limit *ceremony.HolderLimitReachedError
	if err := dbHolderCall(t, ctx, s.handleDatabaseBarrierOfficerRegister, &pb.DatabaseBarrierOfficerRegisterRequest{
		Database: "vault", Officer: &pb.BarrierOfficerRegisterRequest{Username: extra.Name, CsrPem: []byte(extra.CSRPEM)},
	}, &pb.BarrierOfficerRegisterResponse{}); !errors.As(err, &limit) {
		t.Fatalf("a third holder = %v, want HolderLimitReachedError", err)
	}
	if err := dbHolderCall(t, ctx, s.handleDatabaseBarrierOfficerUnenrol, &pb.DatabaseBarrierOfficerUnenrolRequest{Database: "vault", Username: "bob"}, &emptypb.Empty{}); err != nil {
		t.Fatalf("unenrol bob: %v", err)
	}
	if err := dbHolderCall(t, ctx, s.handleDatabaseBarrierOfficerUnenrol, &pb.DatabaseBarrierOfficerUnenrolRequest{Database: "vault", Username: "bob"}, &emptypb.Empty{}); !errors.Is(err, ceremony.ErrOfficerNotFound) {
		t.Fatalf("unenrol bob again = %v, want ErrOfficerNotFound", err)
	}
	var again pb.BarrierOfficerRegisterResponse
	if err := dbHolderCall(t, ctx, s.handleDatabaseBarrierOfficerRegister, &pb.DatabaseBarrierOfficerRegisterRequest{
		Database: "vault", Officer: &pb.BarrierOfficerRegisterRequest{Username: "bob", CsrPem: []byte(holders[1].CSRPEM)},
	}, &again); err != nil {
		t.Fatalf("re-register bob: %v", err)
	}
	holders[1].Enrolled(t, string(again.GetCertPem()))
	var list pb.BarrierOfficerListResponse
	if err := dbHolderCall(t, ctx, s.handleDatabaseBarrierOfficerList, &pb.DatabaseBarrierOfficerListRequest{Database: "vault"}, &list); err != nil || len(list.GetOfficers()) != 2 {
		t.Fatalf("list = %v, %v", list.GetOfficers(), err)
	}
	var init pb.BarrierShamirOperationResponse
	if err := dbHolderCall(t, ctx, s.handleDatabaseBarrierShamirInitialize, &pb.DatabaseBarrierShamirInitializeRequest{Database: "vault"}, &init); err != nil || init.GetShareCount() != 2 {
		t.Fatalf("initialize = %v, %v", init.GetShareCount(), err)
	}

	for _, h := range holders {
		var begin pb.BarrierShareClaimBeginResponse
		beginReq := &pb.DatabaseBarrierShareClaimBeginRequest{Database: "vault", Claim: &pb.BarrierShareClaimBeginRequest{Username: h.Name}}
		var noCert *service.ClientCertRequiredError
		if err := dbHolderCall(t, ctx, s.handleDatabaseBarrierShareClaimBegin, beginReq, &begin); !errors.As(err, &noCert) {
			t.Fatalf("claim begin without a certificate: got %v, want ClientCertRequiredError", err)
		}
		if err := dbHolderCall(t, ctx, s.handleDatabaseBarrierShareClaimComplete, &pb.DatabaseBarrierShareClaimCompleteRequest{Database: "vault"}, &pb.BarrierShareClaimCompleteResponse{}); !errors.As(err, &noCert) {
			t.Fatalf("claim complete without a certificate: got %v, want ClientCertRequiredError", err)
		}
		tlsCtx := dbHolderTLS(h)
		if err := dbHolderCall(t, tlsCtx, s.handleDatabaseBarrierShareClaimBegin, beginReq, &begin); err != nil {
			t.Fatalf("claim begin %s: %v", h.Name, err)
		}
		var done pb.BarrierShareClaimCompleteResponse
		if err := dbHolderCall(t, tlsCtx, s.handleDatabaseBarrierShareClaimComplete, &pb.DatabaseBarrierShareClaimCompleteRequest{
			Database: "vault",
			Claim:    &pb.BarrierShareClaimCompleteRequest{Username: h.Name, NonceHex: begin.GetNonceHex(), Signature: h.SignClaimHex(t, "vault", begin.GetNonceHex())},
		}, &done); err != nil {
			t.Fatalf("claim complete %s: %v", h.Name, err)
		}
		h.Envelope = done.GetEnvelope()
	}

	for _, op := range []string{"seal", "unseal"} {
		submitQUICDBShare(t, s, op, holders[0])
		if out := submitQUICDBShare(t, s, op, holders[1]); !out.GetComplete() {
			t.Fatalf("%s quorum = %v", op, out)
		}
	}

	var round pb.BarrierQuorumRound
	if err := dbHolderCall(t, ctx, s.handleDatabaseBarrierRekeyStart, &pb.DatabaseBarrierRekeyStartRequest{Database: "vault", Threshold: 2}, &round); err != nil || round.GetOp() != "rekey" {
		t.Fatalf("rekey start = %v, %v", round.GetOp(), err)
	}
	if err := dbHolderCall(t, ctx, s.handleDatabaseBarrierOfficerRevoke, &pb.DatabaseBarrierOfficerRevokeRequest{Database: "vault", Username: "bob", Threshold: 2}, &round); err == nil {
		t.Fatal("revoking a holder below the threshold succeeded")
	}
	// Initialized, a holder is removed by revocation only.
	if err := dbHolderCall(t, ctx, s.handleDatabaseBarrierOfficerUnenrol, &pb.DatabaseBarrierOfficerUnenrolRequest{Database: "vault", Username: "bob"}, &emptypb.Empty{}); !errors.Is(err, ceremony.ErrUnenrolInitialized) {
		t.Fatalf("unenrol after initialization = %v, want ErrUnenrolInitialized", err)
	}
}

// TestQUICDatabaseHolders_Refusals covers the handlers' own refusals: a
// payload that does not decode, a request the conversion rejects, and a
// database service without share-holder custody.
func TestQUICDatabaseHolders_Refusals(t *testing.T) {
	fx := servicetest.NewShamirDatabase(t, "vault", 2, 2)
	s := NewServer(nil, nil, "127.0.0.1:0", WithDatabaseService(fx.Service))
	ctx := context.Background()

	var unmarshal *UnmarshalError
	if _, err := s.handleDatabaseBarrierOfficerList(ctx, []byte{0xff}); !errors.As(err, &unmarshal) {
		t.Fatalf("garbage payload: got %v, want UnmarshalError", err)
	}
	if err := dbHolderCall(t, ctx, s.handleDatabaseBarrierOfficerRegister, &pb.DatabaseBarrierOfficerRegisterRequest{
		Database: "vault", Officer: &pb.BarrierOfficerRegisterRequest{Username: "a", Factors: []*pb.HolderFactorEnrolment{{Name: "fido2", ParamsJson: []byte("{")}}},
	}, &pb.BarrierOfficerRegisterResponse{}); err == nil {
		t.Fatal("registering malformed factor parameters succeeded")
	}
	for name, call := range map[string]func() error{
		"ShamirInitialize": func() error {
			return dbHolderCall(t, ctx, s.handleDatabaseBarrierShamirInitialize, &pb.DatabaseBarrierShamirInitializeRequest{Database: "absent"}, &pb.BarrierShamirOperationResponse{})
		},
		"QuorumBegin": func() error {
			return dbHolderCall(t, ctx, s.handleDatabaseBarrierQuorumBegin, &pb.DatabaseBarrierQuorumBeginRequest{Database: "absent", Begin: &pb.BarrierQuorumBeginRequest{Op: "unseal"}}, &pb.BarrierQuorumChallenge{})
		},
		"QuorumSubmit": func() error {
			return dbHolderCall(t, ctx, s.handleDatabaseBarrierQuorumSubmit, &pb.DatabaseBarrierQuorumSubmitRequest{Database: "absent", Submit: &pb.BarrierQuorumSubmitRequest{Op: "unseal"}}, &pb.BarrierQuorumOutcome{})
		},
		"RekeyStart": func() error {
			return dbHolderCall(t, ctx, s.handleDatabaseBarrierRekeyStart, &pb.DatabaseBarrierRekeyStartRequest{Database: "absent", Threshold: 2}, &pb.BarrierQuorumRound{})
		},
		"OfficerList": func() error {
			return dbHolderCall(t, ctx, s.handleDatabaseBarrierOfficerList, &pb.DatabaseBarrierOfficerListRequest{Database: "absent"}, &pb.BarrierOfficerListResponse{})
		},
		"ClaimBegin": func() error {
			return dbHolderCall(t, dbHolderTLS(&servicetest.Holder{Cert: &x509.Certificate{Raw: []byte("x")}}), s.handleDatabaseBarrierShareClaimBegin, &pb.DatabaseBarrierShareClaimBeginRequest{Database: "absent"}, &pb.BarrierShareClaimBeginResponse{})
		},
		"ClaimComplete": func() error {
			return dbHolderCall(t, dbHolderTLS(&servicetest.Holder{Cert: &x509.Certificate{Raw: []byte("x")}}), s.handleDatabaseBarrierShareClaimComplete, &pb.DatabaseBarrierShareClaimCompleteRequest{Database: "absent"}, &pb.BarrierShareClaimCompleteResponse{})
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s on a database without a Shamir barrier succeeded", name)
		}
	}

	bare := NewServer(nil, nil, "127.0.0.1:0", WithDatabaseService(noHolderDatabaseSvc{}))
	handlers := map[string]func(context.Context, []byte) ([]byte, error){
		"OfficerRegister":    bare.handleDatabaseBarrierOfficerRegister,
		"OfficerList":        bare.handleDatabaseBarrierOfficerList,
		"ShamirInitialize":   bare.handleDatabaseBarrierShamirInitialize,
		"ShareClaimBegin":    bare.handleDatabaseBarrierShareClaimBegin,
		"ShareClaimComplete": bare.handleDatabaseBarrierShareClaimComplete,
		"QuorumBegin":        bare.handleDatabaseBarrierQuorumBegin,
		"QuorumSubmit":       bare.handleDatabaseBarrierQuorumSubmit,
		"RekeyStart":         bare.handleDatabaseBarrierRekeyStart,
		"OfficerRevoke":      bare.handleDatabaseBarrierOfficerRevoke,
		"OfficerUnenrol":     bare.handleDatabaseBarrierOfficerUnenrol,
	}
	for name, h := range handlers {
		var notConfigured *ServiceNotConfiguredError
		if _, err := h(ctx, nil); !errors.As(err, &notConfigured) {
			t.Errorf("%s without holder custody: got %v, want ServiceNotConfiguredError", name, err)
		}
	}
}
