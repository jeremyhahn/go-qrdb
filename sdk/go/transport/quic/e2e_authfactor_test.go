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
	"encoding/json"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// AuthFactor (ceremony-style: Enroll/Challenge/Verify/List/Revoke) — JSON wire
// ---------------------------------------------------------------------------

func TestE2E_EnrollFactor_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("authfactor.Enroll", func(_ []byte) (uint16, []byte) {
		resp := quicEnrollFactorResponse{
			EnrollmentID: "e1", FactorName: "yubikey", UserID: "u1", TenantID: "t1",
			EnrolledAt: "2026-01-01T00:00:00Z",
		}
		b, _ := json.Marshal(resp)
		return wire.StatusOK, b
	})
	resp, err := tr.EnrollFactor(context.Background(), &transport.EnrollFactorRequest{
		FactorName: "yubikey", Subject: "alice",
	})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	if resp.EnrollmentID != "e1" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_ChallengeFactor_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("authfactor.Challenge", func(_ []byte) (uint16, []byte) {
		resp := quicChallengeFactorResponse{
			ChallengeID: "c1", EnrollmentID: "e1", Nonce: []byte{0x01, 0x02},
			ExpiresAt: "2026-01-01T01:00:00Z",
		}
		b, _ := json.Marshal(resp)
		return wire.StatusOK, b
	})
	resp, err := tr.ChallengeFactor(context.Background(), &transport.ChallengeFactorRequest{EnrollmentID: "e1"})
	if err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}
	if resp.ChallengeID != "c1" || len(resp.Nonce) != 2 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_VerifyFactor_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("authfactor.Verify", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.VerifyFactor(context.Background(), &transport.VerifyFactorRequest{
		FactorName: "yubikey", ChallengeID: "c1", Value: []byte{0x01},
	}); err != nil {
		t.Fatalf("VerifyFactor: %v", err)
	}
}

func TestE2E_ListFactors_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("authfactor.List", func(_ []byte) (uint16, []byte) {
		resp := quicListFactorsResponse{
			Enrollments: []quicEnrollmentEntry{
				{EnrollmentID: "e1", FactorName: "yubikey", UserID: "u1", EnrolledAt: "2026-01-01T00:00:00Z"},
			},
			Total: 1,
		}
		b, _ := json.Marshal(resp)
		return wire.StatusOK, b
	})
	resp, err := tr.ListFactors(context.Background(), &transport.ListFactorsRequest{Subject: "alice"})
	if err != nil {
		t.Fatalf("ListFactors: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_RevokeFactor_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("authfactor.Revoke", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.RevokeFactor(context.Background(), &transport.RevokeFactorRequest{EnrollmentID: "e1"}); err != nil {
		t.Fatalf("RevokeFactor: %v", err)
	}
}

// ---------------------------------------------------------------------------
// UserAuthFactor (M14) — JSON wire
// ---------------------------------------------------------------------------

func TestE2E_AddAuthFactor_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("userauthfactor.Add", func(_ []byte) (uint16, []byte) {
		out := struct {
			Factor quicUserFactorRecord `json:"factor"`
		}{
			Factor: quicUserFactorRecord{
				ID: "f1", UserID: 7, Type: "mtls", Label: "laptop",
				Credential: []byte("c"), RegisteredAt: "2026-01-01T00:00:00Z",
			},
		}
		b, _ := json.Marshal(out)
		return wire.StatusOK, b
	})
	resp, err := tr.AddAuthFactor(context.Background(), &transport.AuthFactorAddRequest{
		UserID: 7, Type: "mtls", Label: "laptop", Credential: []byte("c"),
	})
	if err != nil {
		t.Fatalf("AddAuthFactor: %v", err)
	}
	if resp.Factor.ID != "f1" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_ListAuthFactors_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("userauthfactor.List", func(_ []byte) (uint16, []byte) {
		out := struct {
			Factors []quicUserFactorRecord `json:"factors"`
			Total   int                    `json:"total"`
		}{
			Factors: []quicUserFactorRecord{
				{ID: "f1", UserID: 7, Type: "mtls", RegisteredAt: "2026-01-01T00:00:00Z"},
			},
			Total: 1,
		}
		b, _ := json.Marshal(out)
		return wire.StatusOK, b
	})
	resp, err := tr.ListAuthFactors(context.Background(), &transport.AuthFactorListRequest{UserID: 7})
	if err != nil {
		t.Fatalf("ListAuthFactors: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_GetAuthFactor_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("userauthfactor.Get", func(_ []byte) (uint16, []byte) {
		out := struct {
			Factor quicUserFactorRecord `json:"factor"`
		}{Factor: quicUserFactorRecord{ID: "f1", UserID: 7, Type: "mtls", RegisteredAt: "2026-01-01T00:00:00Z"}}
		b, _ := json.Marshal(out)
		return wire.StatusOK, b
	})
	resp, err := tr.GetAuthFactor(context.Background(), &transport.AuthFactorGetRequest{FactorID: "f1"})
	if err != nil {
		t.Fatalf("GetAuthFactor: %v", err)
	}
	if resp.Factor.ID != "f1" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_RemoveAuthFactor_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("userauthfactor.Remove", func(_ []byte) (uint16, []byte) {
		out := struct {
			Status string `json:"status"`
		}{Status: "revoked"}
		b, _ := json.Marshal(out)
		return wire.StatusOK, b
	})
	resp, err := tr.RemoveAuthFactor(context.Background(), &transport.AuthFactorRemoveRequest{FactorID: "f1"})
	if err != nil {
		t.Fatalf("RemoveAuthFactor: %v", err)
	}
	if resp.Status != "revoked" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_DeleteAuthFactor_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	// Handed off over a channel; see TestE2E_IssueCert_HappyPath.
	gotReq := make(chan transport.AuthFactorDeleteRequest, 1)
	srv.SetResponder("userauthfactor.Delete", func(payload []byte) (uint16, []byte) {
		var req transport.AuthFactorDeleteRequest
		_ = json.Unmarshal(payload, &req)
		gotReq <- req
		b, _ := json.Marshal(transport.AuthFactorDeleteResponse{Status: "deleted"})
		return wire.StatusOK, b
	})
	resp, err := tr.DeleteAuthFactor(context.Background(), &transport.AuthFactorDeleteRequest{FactorID: "f1", Force: true})
	if err != nil {
		t.Fatalf("DeleteAuthFactor: %v", err)
	}
	if resp.Status != "deleted" {
		t.Fatalf("resp = %+v", resp)
	}
	if got := <-gotReq; got.FactorID != "f1" || !got.Force {
		t.Fatalf("server saw %+v, want factor f1 with force", got)
	}
}

func TestE2E_RotateAuthFactor_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("userauthfactor.Rotate", func(_ []byte) (uint16, []byte) {
		out := struct {
			NewFactor quicUserFactorRecord `json:"new_factor"`
		}{NewFactor: quicUserFactorRecord{ID: "f2", UserID: 7, Type: "mtls", RegisteredAt: "2026-02-01T00:00:00Z"}}
		b, _ := json.Marshal(out)
		return wire.StatusOK, b
	})
	resp, err := tr.RotateAuthFactor(context.Background(), &transport.AuthFactorRotateRequest{
		OldFactorID: "f1",
		NewFactor:   transport.AuthFactorAddRequest{UserID: 7, Type: "mtls"},
	})
	if err != nil {
		t.Fatalf("RotateAuthFactor: %v", err)
	}
	if resp.NewFactor.ID != "f2" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_RelabelAuthFactor_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("userauthfactor.Relabel", func(_ []byte) (uint16, []byte) {
		out := struct {
			Factor quicUserFactorRecord `json:"factor"`
		}{Factor: quicUserFactorRecord{ID: "f1", Label: "new-label", RegisteredAt: "2026-01-01T00:00:00Z"}}
		b, _ := json.Marshal(out)
		return wire.StatusOK, b
	})
	resp, err := tr.RelabelAuthFactor(context.Background(), &transport.AuthFactorRelabelRequest{
		FactorID: "f1", Label: "new-label",
	})
	if err != nil {
		t.Fatalf("RelabelAuthFactor: %v", err)
	}
	if resp.Factor.Label != "new-label" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_LoginAuthFactor_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("userauthfactor.Login", func(_ []byte) (uint16, []byte) {
		out := struct {
			UserID   uint64               `json:"user_id"`
			Username string               `json:"username"`
			Email    string               `json:"email,omitempty"`
			TenantID string               `json:"tenant_id,omitempty"`
			Roles    []string             `json:"roles,omitempty"`
			Active   bool                 `json:"active"`
			Factor   quicUserFactorRecord `json:"factor"`
		}{
			UserID: 7, Username: "alice", Active: true,
			Factor: quicUserFactorRecord{ID: "f1", UserID: 7, Type: "mtls", RegisteredAt: "2026-01-01T00:00:00Z"},
		}
		b, _ := json.Marshal(out)
		return wire.StatusOK, b
	})
	resp, err := tr.LoginAuthFactor(context.Background(), &transport.AuthFactorLoginRequest{
		Type: "mtls", LookupKey: []byte("c"),
	})
	if err != nil {
		t.Fatalf("LoginAuthFactor: %v", err)
	}
	if resp.UserID != 7 || !resp.Active {
		t.Fatalf("resp = %+v", resp)
	}
}
