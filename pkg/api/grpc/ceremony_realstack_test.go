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

package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
)

// TestGRPC_CeremonyInitialize_RealSOPINGateRefusesWrongPIN is the
// go-qrdb#560 review B gRPC equivalent of
// pkg/api/rest's TestHandleCeremonyInitialize_RealSOPINGateRefusesWrongPIN:
// unlike TestGRPC_CeremonyInitialize_SOPINRefusalPropagates (a mock
// CeremonyServicer that returns pin.ErrPINInvalid directly, proving only
// that gRPC renders that error as Unauthenticated, not that anything
// actually checks a PIN), this drives a REAL ceremony.CeremonyService, a
// REAL pin.SoftwareBackend and the REAL
// service.CeremonyServiceWithAdminRebind wrapper through a REAL gRPC server
// and client. Removing pin.RequireProvisionedSOPIN from
// ReissueAdminCredential (or reverting VerifyReissueSOPIN's ordering) turns
// this red; the mock-based sibling test cannot.
func TestGRPC_CeremonyInitialize_RealSOPINGateRefusesWrongPIN(t *testing.T) {
	wrapped, ca, _ := servicetest.NewRealReissueStack(t, "correct-horse-battery")
	client, cleanup := startTestServerFull(t, WithCeremonyService(wrapped), WithCeremonyCA(ca))
	defer cleanup()

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

// TestGRPC_CeremonyInitialize_RealSOPINGateCheckedBeforeAdminAlreadyBound
// is the OTHER half of the go-qrdb#560 review B ordering check: the SO PIN
// is verified BEFORE anything that would tell an unauthenticated caller
// whether the admin already holds a binding. Against an admin that IS
// already bound, a wrong SO PIN must still answer the PIN failure
// (Unauthenticated), never service.AdminAlreadyBoundError's own distinct
// signal -- which would leak the admin's binding state to a caller who
// never authenticated at all.
func TestGRPC_CeremonyInitialize_RealSOPINGateCheckedBeforeAdminAlreadyBound(t *testing.T) {
	wrapped, ca, _ := servicetest.NewRealReissueStack(t, "correct-horse-battery")
	client, cleanup := startTestServerFull(t, WithCeremonyService(wrapped), WithCeremonyCA(ca))
	defer cleanup()

	// Bind the admin first, with the correct PIN, so the "already bound"
	// branch becomes reachable.
	if _, err := client.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{
		SoPin: "correct-horse-battery", AdminGenerate: true,
	}); err != nil {
		t.Fatalf("bind: expected success, got %v", err)
	}

	// A wrong SO PIN against this now-bound admin must still answer the PIN
	// failure, not leak AdminAlreadyBoundError to an unauthenticated caller.
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
