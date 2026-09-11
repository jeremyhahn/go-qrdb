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
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
)

// stubPINBackend is a minimal pin.PINBackend used by the gRPC barrier
// PIN tests. It records every Set/Verify call and lets each test drive
// the desired path with canned errors.
type stubPINBackend struct {
	strategy        pin.StrategyID
	soPINSet        bool
	userPINSet      bool
	storedSOPIN     string
	storedUserPIN   string
	verifyErr       error
	setSOPINErr     error
	setSOPINCalls   int
	setUserPINCalls int
	verifyCalls     int
}

func newStubPINBackend() *stubPINBackend {
	return &stubPINBackend{strategy: pin.StrategySoftware}
}

func (b *stubPINBackend) Strategy() pin.StrategyID { return b.strategy }

func (b *stubPINBackend) SetSOPIN(_, newSOPIN string) error {
	b.setSOPINCalls++
	if b.setSOPINErr != nil {
		return b.setSOPINErr
	}
	b.storedSOPIN = newSOPIN
	b.soPINSet = true
	return nil
}

func (b *stubPINBackend) SetUserPIN(_, newUserPIN string) error {
	b.setUserPINCalls++
	b.storedUserPIN = newUserPIN
	b.userPINSet = true
	return nil
}

func (b *stubPINBackend) ChangeSOPIN(_, _ string) error   { return nil }
func (b *stubPINBackend) ChangeUserPIN(_, _ string) error { return nil }

func (b *stubPINBackend) VerifySOPIN(p string) error {
	b.verifyCalls++
	if b.verifyErr != nil {
		return b.verifyErr
	}
	if p != b.storedSOPIN {
		return pin.ErrPINInvalid
	}
	return nil
}

func (b *stubPINBackend) VerifyUserPIN(_ string) error         { return nil }
func (b *stubPINBackend) IsInitialized() bool                  { return b.soPINSet }
func (b *stubPINBackend) SOPINSet() bool                       { return b.soPINSet }
func (b *stubPINBackend) UserPINSet() bool                     { return b.userPINSet }
func (b *stubPINBackend) GetLockoutStatus() *pin.LockoutStatus { return nil }
func (b *stubPINBackend) ResetLockout(_ string) error          { return nil }

var _ pin.PINBackend = (*stubPINBackend)(nil)

// TestGRPC_BarrierInitialize_PersistsPINs verifies that the BarrierInitialize
// gRPC handler forwards SoPin and UserPin to the configured pin.PINBackend
// before invoking the barrier service.
func TestGRPC_BarrierInitialize_PersistsPINs(t *testing.T) {
	barrierSvc := newMockBarrierService()
	backend := newStubPINBackend()

	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithBarrierService(barrierSvc),
		WithPINBackend(backend))
	defer cleanup()

	_, err := client.BarrierInitialize(context.Background(), &pb.BarrierInitializeRequest{
		Secret:  []byte("s"),
		SoPin:   "so-1234",
		UserPin: "u-5678",
	})
	if err != nil {
		t.Fatalf("BarrierInitialize: %v", err)
	}
	if !backend.SOPINSet() || backend.storedSOPIN != "so-1234" {
		t.Fatalf("SO PIN not persisted: set=%v stored=%q", backend.SOPINSet(), backend.storedSOPIN)
	}
	if !backend.UserPINSet() || backend.storedUserPIN != "u-5678" {
		t.Fatalf("User PIN not persisted: set=%v stored=%q", backend.UserPINSet(), backend.storedUserPIN)
	}
}

// TestGRPC_BarrierRekey_BadSOPIN verifies that the BarrierRekey gRPC
// handler returns Unauthenticated when the supplied SO PIN does not
// match the PIN backend's record.
func TestGRPC_BarrierRekey_BadSOPIN(t *testing.T) {
	barrierSvc := newMockBarrierService()
	backend := newStubPINBackend()
	if err := backend.SetSOPIN("", "correct-pin"); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}

	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithBarrierService(barrierSvc),
		WithPINBackend(backend))
	defer cleanup()

	_, err := client.BarrierRekey(context.Background(), &pb.BarrierRekeyRequest{
		Threshold: 2, Total: 3, SoPin: "wrong-pin",
	})
	if err == nil {
		t.Fatal("expected error for bad SO PIN, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %s", st.Code())
	}
	if backend.verifyCalls == 0 {
		t.Fatal("VerifySOPIN was not invoked")
	}
}

// TestGRPC_BarrierRekey_GoodSOPIN verifies that the BarrierRekey gRPC
// handler proceeds to the barrier service when the SO PIN matches.
func TestGRPC_BarrierRekey_GoodSOPIN(t *testing.T) {
	barrierSvc := newMockBarrierService()
	backend := newStubPINBackend()
	if err := backend.SetSOPIN("", "correct-pin"); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}

	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithBarrierService(barrierSvc),
		WithPINBackend(backend))
	defer cleanup()

	resp, err := client.BarrierRekey(context.Background(), &pb.BarrierRekeyRequest{
		Threshold: 2, Total: 3, SoPin: "correct-pin",
	})
	if err != nil {
		t.Fatalf("BarrierRekey: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response after successful rekey")
	}
}
