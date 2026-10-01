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
	"crypto/tls"
	"crypto/x509"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	grpcpeer "google.golang.org/grpc/peer"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
)

// noHolderDatabaseSvc is a database service without share-holder custody.
// The handlers only type-assert it; none of its methods is called.
type noHolderDatabaseSvc struct{ service.DatabaseServicer }

// dbHolderPeer is a context whose connection presents h's certificate.
func dbHolderPeer(h *servicetest.Holder) context.Context {
	return grpcpeer.NewContext(context.Background(), &grpcpeer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{h.Cert}}},
	})
}

// submitDBShare answers h's mtls challenge for op and submits its share.
func submitDBShare(t *testing.T, s *qrdbServiceServer, op string, h *servicetest.Holder) *pb.BarrierQuorumOutcome {
	t.Helper()
	ctx := context.Background()
	ch, err := s.DatabaseBarrierQuorumBegin(ctx, &pb.DatabaseBarrierQuorumBeginRequest{
		Database: "vault", Begin: &pb.BarrierQuorumBeginRequest{Op: op, Username: h.Name},
	})
	if err != nil {
		t.Fatalf("%s begin %s: %v", op, h.Name, err)
	}
	out, err := s.DatabaseBarrierQuorumSubmit(ctx, &pb.DatabaseBarrierQuorumSubmitRequest{
		Database: "vault",
		Submit: &pb.BarrierQuorumSubmitRequest{
			Op: op, Username: h.Name, ChallengeId: ch.GetChallengeId(), Factor: ch.GetFactor(),
			Response: h.Sign(t, ch.GetNonce()), Share: h.Share(t),
		},
	})
	if err != nil {
		t.Fatalf("%s submit %s: %v", op, h.Name, err)
	}
	return out
}

// TestGRPCDatabaseHolders_Lifecycle drives a Shamir database's custody
// through the RPC handlers over a real database service: enrolment, listing,
// initialization, claims presenting each holder's certificate on the
// connection, a quorum seal and unseal, a rekey round and a revocation the
// remaining holders cannot satisfy.
func TestGRPCDatabaseHolders_Lifecycle(t *testing.T) {
	fx := servicetest.NewShamirDatabase(t, "vault", 2, 2)
	s := &qrdbServiceServer{databaseSvc: fx.Service}
	ctx := context.Background()
	holders := []*servicetest.Holder{servicetest.NewHolder(t, "alice"), servicetest.NewHolder(t, "bob")}

	for _, h := range holders {
		resp, err := s.DatabaseBarrierOfficerRegister(ctx, &pb.DatabaseBarrierOfficerRegisterRequest{
			Database: "vault", Officer: &pb.BarrierOfficerRegisterRequest{Username: h.Name, CsrPem: []byte(h.CSRPEM)},
		})
		if err != nil {
			t.Fatalf("register %s: %v", h.Name, err)
		}
		h.Enrolled(t, string(resp.GetCertPem()))
	}
	list, err := s.DatabaseBarrierOfficerList(ctx, &pb.DatabaseBarrierOfficerListRequest{Database: "vault"})
	if err != nil || len(list.GetOfficers()) != 2 {
		t.Fatalf("list = %v, %v", list, err)
	}
	init, err := s.DatabaseBarrierShamirInitialize(ctx, &pb.DatabaseBarrierShamirInitializeRequest{Database: "vault"})
	if err != nil || init.GetThreshold() != 2 || init.GetTotalShares() != 2 || init.GetShareCount() != 2 {
		t.Fatalf("initialize = %v, %v", init, err)
	}

	for _, h := range holders {
		_, err := s.DatabaseBarrierShareClaimBegin(ctx, &pb.DatabaseBarrierShareClaimBeginRequest{
			Database: "vault", Claim: &pb.BarrierShareClaimBeginRequest{Username: h.Name},
		})
		requireCode(t, err, codes.Unauthenticated)
		peerCtx := dbHolderPeer(h)
		begin, err := s.DatabaseBarrierShareClaimBegin(peerCtx, &pb.DatabaseBarrierShareClaimBeginRequest{
			Database: "vault", Claim: &pb.BarrierShareClaimBeginRequest{Username: h.Name},
		})
		if err != nil {
			t.Fatalf("claim begin %s: %v", h.Name, err)
		}
		done, err := s.DatabaseBarrierShareClaimComplete(peerCtx, &pb.DatabaseBarrierShareClaimCompleteRequest{
			Database: "vault",
			Claim:    &pb.BarrierShareClaimCompleteRequest{Username: h.Name, NonceHex: begin.GetNonceHex(), Signature: h.SignClaimHex(t, "vault", begin.GetNonceHex())},
		})
		if err != nil {
			t.Fatalf("claim complete %s: %v", h.Name, err)
		}
		h.Envelope = done.GetEnvelope()
	}

	for _, op := range []string{"seal", "unseal"} {
		submitDBShare(t, s, op, holders[0])
		if out := submitDBShare(t, s, op, holders[1]); !out.GetComplete() {
			t.Fatalf("%s quorum = %v", op, out)
		}
	}

	round, err := s.DatabaseBarrierRekeyStart(ctx, &pb.DatabaseBarrierRekeyStartRequest{Database: "vault", Threshold: 2})
	if err != nil || round.GetOp() != "rekey" {
		t.Fatalf("rekey start = %v, %v", round, err)
	}
	_, err = s.DatabaseBarrierOfficerRevoke(ctx, &pb.DatabaseBarrierOfficerRevokeRequest{Database: "vault", Username: "bob", Threshold: 2})
	requireCode(t, err, codes.FailedPrecondition)
}

// TestGRPCDatabaseHolders_Refusals covers the handlers' own refusals: an
// absent database, a request the conversion rejects, and a database service
// without share-holder custody.
func TestGRPCDatabaseHolders_Refusals(t *testing.T) {
	fx := servicetest.NewShamirDatabase(t, "vault", 2, 2)
	s := &qrdbServiceServer{databaseSvc: fx.Service}
	ctx := context.Background()

	// A system caller is not ownership-checked, so the database barrier
	// manager answers, as it does for db.Seal: there is no Shamir barrier.
	_, err := s.DatabaseBarrierOfficerList(ctx, &pb.DatabaseBarrierOfficerListRequest{Database: "absent"})
	requireCode(t, err, codes.FailedPrecondition)
	_, err = s.DatabaseBarrierOfficerRegister(ctx, &pb.DatabaseBarrierOfficerRegisterRequest{
		Database: "vault", Officer: &pb.BarrierOfficerRegisterRequest{Username: "a", Factors: []*pb.HolderFactorEnrolment{{Name: "fido2", ParamsJson: []byte("{")}}},
	})
	if err == nil {
		t.Fatal("registering malformed factor parameters succeeded")
	}

	bare := &qrdbServiceServer{databaseSvc: noHolderDatabaseSvc{}}
	calls := map[string]func() error{
		"OfficerRegister": func() error {
			_, err := bare.DatabaseBarrierOfficerRegister(ctx, &pb.DatabaseBarrierOfficerRegisterRequest{Database: "vault"})
			return err
		},
		"OfficerList": func() error {
			_, err := bare.DatabaseBarrierOfficerList(ctx, &pb.DatabaseBarrierOfficerListRequest{Database: "vault"})
			return err
		},
		"ShamirInitialize": func() error {
			_, err := bare.DatabaseBarrierShamirInitialize(ctx, &pb.DatabaseBarrierShamirInitializeRequest{Database: "vault"})
			return err
		},
		"ShareClaimBegin": func() error {
			_, err := bare.DatabaseBarrierShareClaimBegin(ctx, &pb.DatabaseBarrierShareClaimBeginRequest{Database: "vault"})
			return err
		},
		"ShareClaimComplete": func() error {
			_, err := bare.DatabaseBarrierShareClaimComplete(ctx, &pb.DatabaseBarrierShareClaimCompleteRequest{Database: "vault"})
			return err
		},
		"QuorumBegin": func() error {
			_, err := bare.DatabaseBarrierQuorumBegin(ctx, &pb.DatabaseBarrierQuorumBeginRequest{Database: "vault"})
			return err
		},
		"QuorumSubmit": func() error {
			_, err := bare.DatabaseBarrierQuorumSubmit(ctx, &pb.DatabaseBarrierQuorumSubmitRequest{Database: "vault"})
			return err
		},
		"RekeyStart": func() error {
			_, err := bare.DatabaseBarrierRekeyStart(ctx, &pb.DatabaseBarrierRekeyStartRequest{Database: "vault"})
			return err
		},
		"OfficerRevoke": func() error {
			_, err := bare.DatabaseBarrierOfficerRevoke(ctx, &pb.DatabaseBarrierOfficerRevokeRequest{Database: "vault"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) { requireCode(t, call(), codes.Unimplemented) })
	}
}
