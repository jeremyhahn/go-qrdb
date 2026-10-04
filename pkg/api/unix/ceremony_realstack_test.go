// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !integration

package unix

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
)

// TestUnix_CeremonyInitialize_RealSOPINGateRefusesWrongPIN is the
// go-qrdb#560 review B Unix-domain-socket equivalent of pkg/api/rest's
// TestHandleCeremonyInitialize_RealSOPINGateRefusesWrongPIN. The Unix
// transport shares gRPC's qrdbServiceServer handler, but that sharing is
// exactly what a mock-based test cannot distinguish from "gRPC's gate
// happens to also run here" -- this drives a REAL ceremony.CeremonyService,
// a REAL pin.SoftwareBackend and the REAL
// service.CeremonyServiceWithAdminRebind wrapper through a REAL Unix socket
// server and a REAL client dialed over that socket. Removing
// pin.RequireProvisionedSOPIN from ReissueAdminCredential (or reverting
// VerifyReissueSOPIN's ordering) turns this red; the mock-based sibling
// test cannot.
func TestUnix_CeremonyInitialize_RealSOPINGateRefusesWrongPIN(t *testing.T) {
	socketPath := tempSocketPath(t)
	tlsCfg := selfSignedTLSConfig(t)
	wrapped, ca, _ := servicetest.NewRealReissueStack(t, "correct-horse-battery")
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath, WithTLS(tlsCfg),
		WithCeremonyService(wrapped), WithCeremonyCA(ca))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn := dialUnixTLS(t, socketPath, tlsCfg)
	defer func() { _ = conn.Close() }()
	client := pb.NewQRDBServiceClient(conn)

	// Wrong SO PIN: refused, never bound.
	_, err := client.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{
		SoPin: "wrong-pin", AdminGenerate: true,
	})
	if err == nil {
		t.Fatal("expected the wrong SO PIN to be refused, got success")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Unauthenticated {
		t.Fatalf("wrong SO PIN: got code %s, want Unauthenticated", st.Code())
	}

	// Missing SO PIN entirely: also refused.
	_, err = client.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{
		AdminGenerate: true,
	})
	if err == nil {
		t.Fatal("expected the missing SO PIN to be refused, got success")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Unauthenticated {
		t.Fatalf("missing SO PIN: got code %s, want Unauthenticated", st.Code())
	}

	// The correct SO PIN still reissues and binds cleanly -- proving the
	// refusals above were about the PIN, not a stack that always refuses.
	_, err = client.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{
		SoPin: "correct-horse-battery", AdminGenerate: true,
	})
	if err != nil {
		t.Fatalf("correct SO PIN: expected success, got %v", err)
	}
}

// TestUnix_CeremonyInitialize_RealSOPINGateCheckedBeforeAdminAlreadyBound
// is the OTHER half of the go-qrdb#560 review B ordering check, over the
// Unix socket transport: against an admin that IS already bound, a wrong
// SO PIN must still answer the PIN failure (Unauthenticated), never
// service.AdminAlreadyBoundError's own distinct signal.
func TestUnix_CeremonyInitialize_RealSOPINGateCheckedBeforeAdminAlreadyBound(t *testing.T) {
	socketPath := tempSocketPath(t)
	tlsCfg := selfSignedTLSConfig(t)
	wrapped, ca, _ := servicetest.NewRealReissueStack(t, "correct-horse-battery")
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath, WithTLS(tlsCfg),
		WithCeremonyService(wrapped), WithCeremonyCA(ca))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn := dialUnixTLS(t, socketPath, tlsCfg)
	defer func() { _ = conn.Close() }()
	client := pb.NewQRDBServiceClient(conn)

	if _, err := client.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{
		SoPin: "correct-horse-battery", AdminGenerate: true,
	}); err != nil {
		t.Fatalf("bind: expected success, got %v", err)
	}

	_, err := client.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{
		SoPin: "wrong-pin", AdminGenerate: true,
	})
	if err == nil {
		t.Fatal("expected the wrong SO PIN to be refused even against an already-bound admin, got success")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unauthenticated {
		t.Fatalf("got code %s, want Unauthenticated (never a distinct already-bound signal)", st.Code())
	}
}
