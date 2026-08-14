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
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Factor-verified custodian ceremony
// ---------------------------------------------------------------------------

func TestE2E_CeremonyClaimShareBegin_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ceremony.ClaimShareBegin", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.CeremonyClaimShareBeginResponse{
			CustodianId: "officer-1", Nonce: []byte{0x01, 0x02}, ExpiresAtUnix: 1900000000,
		})
	})
	resp, err := tr.CeremonyClaimShareBegin(context.Background(), &transport.CeremonyClaimShareBeginRequest{
		CustodianID: "officer-1",
	})
	if err != nil {
		t.Fatalf("CeremonyClaimShareBegin: %v", err)
	}
	if resp.CustodianID != "officer-1" || len(resp.Nonce) != 2 || resp.ExpiresAt.Unix() != 1900000000 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_CeremonyClaimShareComplete_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ceremony.ClaimShareComplete", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.CeremonyCustodianResponse{
			Id: "officer-1", Username: "alice", ShareIndex: 2, ShareClaimed: true,
			EnrolledAtUnix: 1800000000, RevokedAtUnix: 1850000000, ShareClaimedAtUnix: 1860000000,
		})
	})
	info, err := tr.CeremonyClaimShareComplete(context.Background(), &transport.CeremonyClaimShareCompleteRequest{
		CustodianID: "officer-1", ChallengeID: "chal-1", FactorResponse: []byte{0x09},
	})
	if err != nil {
		t.Fatalf("CeremonyClaimShareComplete: %v", err)
	}
	if info.ID != "officer-1" || info.Username != "alice" || info.ShareIndex != 2 || !info.ShareClaimed {
		t.Fatalf("info = %+v", info)
	}
	if info.RevokedAt == nil || info.ShareClaimedAt == nil {
		t.Fatalf("expected optional timestamps populated, got %+v", info)
	}
}

func TestE2E_CeremonySubmitUnsealShare_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ceremony.SubmitUnsealShare", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.CeremonySubmitUnsealShareResponse{
			Submitted: 3, Required: 3, Unsealed: true,
		})
	})
	resp, err := tr.CeremonySubmitUnsealShare(context.Background(), &transport.CeremonySubmitUnsealShareRequest{
		CustodianID: "officer-1", ChallengeID: "chal-1", ShareBytes: []byte{0x0a, 0x0b},
	})
	if err != nil {
		t.Fatalf("CeremonySubmitUnsealShare: %v", err)
	}
	if resp.Submitted != 3 || resp.Required != 3 || !resp.Unsealed {
		t.Fatalf("resp = %+v", resp)
	}
}

// ---------------------------------------------------------------------------
// Non-versioned credential store / retrieve
// ---------------------------------------------------------------------------

func TestE2E_CredentialStore_RoundTrip(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.Store", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.CredentialStore(context.Background(), "db-cred", []byte("p@ss")); err != nil {
		t.Fatalf("CredentialStore: %v", err)
	}
}

func TestE2E_CredentialRetrieve_RoundTrip(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.Retrieve", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.CredentialRetrieveResponse{Value: []byte("p@ss")})
	})
	val, err := tr.CredentialRetrieve(context.Background(), "db-cred")
	if err != nil {
		t.Fatalf("CredentialRetrieve: %v", err)
	}
	if string(val) != "p@ss" {
		t.Fatalf("val = %q", val)
	}
}
