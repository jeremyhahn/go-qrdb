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
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Ceremony
// ---------------------------------------------------------------------------

func TestE2E_CeremonyInitialize_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ceremony.Initialize", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.CeremonyInitializeResponse{
			SpkiPin: "pin1", CaCertPem: "ca", TlsCertPem: "tls",
			State: "complete", ShareCount: 3,
		})
	})
	resp, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{
		SOPin: "1234", UserPin: "5678", Threshold: 2,
		CredentialSealStrategy: "barrier",
		Officers: []transport.CeremonyOfficerRequest{
			{Username: "alice", CSRPEM: "csr1"},
		},
		Hostname: "h.example", SANs: []string{"alt"},
	})
	if err != nil {
		t.Fatalf("CeremonyInitialize: %v", err)
	}
	if resp.SPKIPin != "pin1" || resp.ShareCount != 3 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_CeremonyStatus_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ceremony.Status", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.CeremonyStatusResponse{State: "ready", SpkiPin: "p"})
	})
	resp, err := tr.CeremonyStatus(context.Background())
	if err != nil {
		t.Fatalf("CeremonyStatus: %v", err)
	}
	if resp.State != "ready" {
		t.Fatalf("State = %q", resp.State)
	}
}

func TestE2E_CeremonySignCSR_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ceremony.SignCSR", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.CeremonySignCSRResponse{CertPem: "signed-cert"})
	})
	resp, err := tr.CeremonySignCSR(context.Background(), &transport.SignCSRInitRequest{
		Username: "alice", SOPin: "1234", CSRPEM: "csr", Role: "officer",
	})
	if err != nil {
		t.Fatalf("CeremonySignCSR: %v", err)
	}
	if resp.CertPEM != "signed-cert" {
		t.Fatalf("resp = %+v", resp)
	}
}

// ---------------------------------------------------------------------------
// Credentials
// ---------------------------------------------------------------------------

func TestE2E_CredentialSubmit_RoundTrip(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.Submit", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.CredentialSubmit(context.Background(), &transport.CredentialSubmitRequest{
		Name: "db-cred", Value: []byte("p@ss"),
	}); err != nil {
		t.Fatalf("CredentialSubmit: %v", err)
	}
}

func TestE2E_CredentialStrategy_RoundTrip(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.Strategy", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.CredentialStrategyResponse{Strategy: "barrier"})
	})
	resp, err := tr.CredentialStrategy(context.Background())
	if err != nil {
		t.Fatalf("CredentialStrategy: %v", err)
	}
	if resp.Strategy != "barrier" {
		t.Fatalf("Strategy = %q", resp.Strategy)
	}
}

func TestE2E_CredentialDelete_RoundTrip(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.Delete", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.CredentialDelete(context.Background(), "db-cred"); err != nil {
		t.Fatalf("CredentialDelete: %v", err)
	}
}

func TestE2E_CredentialList_RoundTrip(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.CredentialListResponse{Names: []string{"a", "b"}})
	})
	resp, err := tr.CredentialList(context.Background(), &transport.CredentialListRequest{})
	if err != nil {
		t.Fatalf("CredentialList: %v", err)
	}
	if len(resp.Names) != 2 {
		t.Fatalf("Names = %v", resp.Names)
	}
}

func TestE2E_CredentialRotate_RoundTrip(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.Rotate", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.CredentialRotateResponse{
			Name: "db-cred", PreviousVersion: 1, NewVersion: 2, GraceUntilUnix: 12345,
		})
	})
	resp, err := tr.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{
		Name: "db-cred", NewValue: []byte("v2"), GracePeriodSeconds: 60,
	})
	if err != nil {
		t.Fatalf("CredentialRotate: %v", err)
	}
	if resp.NewVersion != 2 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_CredentialSetRotationPolicy_RoundTrip(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.SetRotationPolicy", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.CredentialSetRotationPolicy(context.Background(), &transport.CredentialSetRotationPolicyRequest{
		Name: "db-cred", Enabled: true, Interval: "24h", RetainVersions: 3,
	}); err != nil {
		t.Fatalf("CredentialSetRotationPolicy: %v", err)
	}
	rec := srv.LastRequest("credential.SetRotationPolicy")
	if rec == nil {
		t.Fatal(`no request reached "credential.SetRotationPolicy"`)
	}
	var sent pb.CredentialSetRotationPolicyRequest
	if err := proto.Unmarshal(rec.Payload, &sent); err != nil {
		t.Fatalf("request payload: %v", err)
	}
	if sent.GetName() != "db-cred" || !sent.GetEnabled() || sent.GetInterval() != "24h" || sent.GetRetainVersions() != 3 {
		t.Fatalf("sent = %v", &sent)
	}
}

func TestE2E_CredentialSetRotationPolicy_InvalidArgument(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.SetRotationPolicy", func(_ []byte) (uint16, []byte) {
		return wire.StatusInvalidArgument, nil
	})
	err := tr.CredentialSetRotationPolicy(context.Background(), &transport.CredentialSetRotationPolicyRequest{
		Name: "db-cred", Enabled: true, Interval: "0s", RetainVersions: 1,
	})
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != int(wire.StatusInvalidArgument) {
		t.Fatalf("expected an InvalidArgument RequestError, got %T: %v", err, err)
	}
}

func TestE2E_CredentialRotationPolicy_RoundTrip(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.RotationPolicy", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.CredentialRotationPolicyResponse{
			Name: "db-cred", Enabled: true, Interval: "1h0m0s", RetainVersions: 2, NextRotation: "2026-10-29T12:00:00Z",
		})
	})
	resp, err := tr.CredentialRotationPolicy(context.Background(), "db-cred")
	if err != nil {
		t.Fatalf("CredentialRotationPolicy: %v", err)
	}
	if resp.Name != "db-cred" || !resp.Enabled || resp.Interval != "1h0m0s" || resp.RetainVersions != 2 || resp.NextRotation != "2026-10-29T12:00:00Z" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_CredentialRotationPolicy_ServerError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.RotationPolicy", func(_ []byte) (uint16, []byte) {
		return wire.StatusNotFound, nil
	})
	if _, err := tr.CredentialRotationPolicy(context.Background(), "missing"); err == nil {
		t.Fatal("expected an error for an unknown credential")
	}
}

func TestE2E_CredentialMigrationConflicts_RoundTrip(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.MigrationConflicts", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.CredentialMigrationConflictsResponse{
			Conflicts: []*pb.CredentialMigrationConflict{{Name: "db-cred", Value: true, Policy: true}},
		})
	})
	resp, err := tr.CredentialMigrationConflicts(context.Background())
	if err != nil {
		t.Fatalf("CredentialMigrationConflicts: %v", err)
	}
	want := transport.CredentialMigrationConflict{Name: "db-cred", Value: true, Policy: true}
	if len(resp.Conflicts) != 1 || resp.Conflicts[0] != want {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_CredentialMigrationConflicts_ServerError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.MigrationConflicts", func(_ []byte) (uint16, []byte) {
		return wire.StatusInvalidArgument, nil
	})
	if _, err := tr.CredentialMigrationConflicts(context.Background()); err == nil {
		t.Fatal("expected an error from a refusing server")
	}
}

func TestE2E_CredentialResolveMigrationConflict_RoundTrip(t *testing.T) {
	srv, tr := startE2EServer(t)
	// The responder runs on the server's goroutine; the channel orders its
	// write before the test's read.
	seen := make(chan *pb.CredentialResolveMigrationConflictRequest, 1)
	srv.SetResponder("credential.ResolveMigrationConflict", func(payload []byte) (uint16, []byte) {
		got := &pb.CredentialResolveMigrationConflictRequest{}
		if err := proto.Unmarshal(payload, got); err != nil {
			return wire.StatusInvalidArgument, nil
		}
		seen <- got
		return wire.StatusOK, nil
	})
	if err := tr.CredentialResolveMigrationConflict(context.Background(),
		&transport.CredentialResolveMigrationConflictRequest{Name: "db-cred", Resolution: "import"}); err != nil {
		t.Fatalf("CredentialResolveMigrationConflict: %v", err)
	}
	got := <-seen
	if got.GetName() != "db-cred" || got.GetResolution() != "import" {
		t.Fatalf("server saw %+v", got)
	}
}

func TestE2E_CredentialResolveMigrationConflict_ServerError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.ResolveMigrationConflict", func(_ []byte) (uint16, []byte) {
		return wire.StatusNotFound, nil
	})
	if err := tr.CredentialResolveMigrationConflict(context.Background(),
		&transport.CredentialResolveMigrationConflictRequest{Name: "missing", Resolution: "discard"}); err == nil {
		t.Fatal("expected an error for a name with no conflict")
	}
}

func TestE2E_CredentialRetrieveVersion_RoundTrip(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.RetrieveVersion", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.CredentialRetrieveVersionResponse{Value: []byte("secret-v2")})
	})
	resp, err := tr.retrieveVersionResponse(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name: "db", Version: 2,
	})
	if err != nil {
		t.Fatalf("CredentialRetrieveVersion: %v", err)
	}
	if string(resp.Value) != "secret-v2" {
		t.Fatalf("Value = %q", resp.Value)
	}
}

func TestE2E_CredentialListVersions_RoundTrip(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.ListVersions", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.CredentialListVersionsResponse{
			Name: "db",
			Versions: []*pb.CredentialVersionInfo{
				{Version: 1, IsCurrent: false},
				{Version: 2, IsCurrent: true},
			},
		})
	})
	resp, err := tr.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{Name: "db"})
	if err != nil {
		t.Fatalf("CredentialListVersions: %v", err)
	}
	if len(resp.Versions) != 2 {
		t.Fatalf("Versions = %+v", resp.Versions)
	}
}

func TestE2E_CredentialGCExpired_RoundTrip(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.GCExpired", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.CredentialGCResponse{Reaped: 5})
	})
	resp, err := tr.CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{Name: "db"})
	if err != nil {
		t.Fatalf("CredentialGCExpired: %v", err)
	}
	if resp.Reaped != 5 {
		t.Fatalf("Reaped = %d", resp.Reaped)
	}
}
