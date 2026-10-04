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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package quic

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
)

// TestHandleCeremonyInitialize_RealSOPINGateRefusesWrongPIN is the
// go-qrdb#560 review B QUIC equivalent of pkg/api/rest's
// TestHandleCeremonyInitialize_RealSOPINGateRefusesWrongPIN: unlike this
// file's own TestHandleCeremonyInitialize_* tests (a mockCeremonyService
// that never checks a PIN at all), this drives a REAL
// ceremony.CeremonyService, a REAL pin.SoftwareBackend and the REAL
// service.CeremonyServiceWithAdminRebind wrapper through the QUIC handler.
// Removing pin.RequireProvisionedSOPIN from ReissueAdminCredential (or
// reverting VerifyReissueSOPIN's ordering) turns this red; the mock-based
// siblings in this file cannot.
func TestHandleCeremonyInitialize_RealSOPINGateRefusesWrongPIN(t *testing.T) {
	wrapped, ca, _ := servicetest.NewRealReissueStack(t, "correct-horse-battery")
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(wrapped), WithCeremonyCA(ca))

	// Wrong SO PIN: refused, never bound.
	payload, err := proto.Marshal(&pb.CeremonyInitializeRequest{SoPin: "wrong-pin", AdminGenerate: true})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := srv.handleCeremonyInitialize(context.Background(), payload); !errors.Is(err, pin.ErrPINInvalid) {
		t.Fatalf("wrong SO PIN: got %v, want pin.ErrPINInvalid", err)
	}

	// Missing SO PIN entirely: also refused.
	payload, err = proto.Marshal(&pb.CeremonyInitializeRequest{AdminGenerate: true})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := srv.handleCeremonyInitialize(context.Background(), payload); !errors.Is(err, pin.ErrPINInvalid) {
		t.Fatalf("missing SO PIN: got %v, want pin.ErrPINInvalid", err)
	}

	// The correct SO PIN still reissues and binds cleanly -- proving the
	// refusals above were about the PIN, not a stack that always refuses.
	payload, err = proto.Marshal(&pb.CeremonyInitializeRequest{SoPin: "correct-horse-battery", AdminGenerate: true})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := srv.handleCeremonyInitialize(context.Background(), payload); err != nil {
		t.Fatalf("correct SO PIN: expected success, got %v", err)
	}
}

// TestHandleCeremonyInitialize_RealSOPINGateCheckedBeforeAdminAlreadyBound
// is the OTHER half of the go-qrdb#560 review B ordering check, over QUIC:
// against an admin that IS already bound, a wrong SO PIN must still answer
// the PIN failure (pin.ErrPINInvalid), never
// service.AdminAlreadyBoundError's own distinct signal -- which would leak
// the admin's binding state to a caller who never authenticated at all.
func TestHandleCeremonyInitialize_RealSOPINGateCheckedBeforeAdminAlreadyBound(t *testing.T) {
	wrapped, ca, _ := servicetest.NewRealReissueStack(t, "correct-horse-battery")
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(wrapped), WithCeremonyCA(ca))

	bindPayload, err := proto.Marshal(&pb.CeremonyInitializeRequest{SoPin: "correct-horse-battery", AdminGenerate: true})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := srv.handleCeremonyInitialize(context.Background(), bindPayload); err != nil {
		t.Fatalf("bind: expected success, got %v", err)
	}

	wrongPayload, err := proto.Marshal(&pb.CeremonyInitializeRequest{SoPin: "wrong-pin", AdminGenerate: true})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	_, err = srv.handleCeremonyInitialize(context.Background(), wrongPayload)
	var alreadyBound *service.AdminAlreadyBoundError
	if errors.As(err, &alreadyBound) {
		t.Fatal("wrong SO PIN against an already-bound admin leaked AdminAlreadyBoundError instead of the PIN failure")
	}
	if !errors.Is(err, pin.ErrPINInvalid) {
		t.Fatalf("got %v, want pin.ErrPINInvalid", err)
	}
}
