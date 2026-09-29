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

package quic

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
)

// stubPINBackend is a minimal pin.PINBackend used by the QUIC barrier
// PIN tests. It records every Set/Verify call so each test can drive
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

// TestQUIC_HandleBarrierInitialize_PersistsPINs verifies that the QUIC
// barrier initialize handler forwards so_pin and user_pin to the
// configured pin.PINBackend before invoking the barrier service.
func TestQUIC_HandleBarrierInitialize_PersistsPINs(t *testing.T) {
	called := false
	barrierSvc := &mockBarrierService{
		barrierInitializeFn: func(_ context.Context, secret []byte) error {
			called = true
			if string(secret) != "s" {
				t.Errorf("secret = %q, want %q", secret, "s")
			}
			return nil
		},
	}
	backend := newStubPINBackend()
	srv := newTestServer(&mockKVService{}, &mockAdminService{},
		WithBarrierService(barrierSvc),
		WithPINBackend(backend))

	payload, err := proto.Marshal(&pb.BarrierInitializeRequest{
		Secret:  []byte("s"),
		SoPin:   "so-1234",
		UserPin: "u-5678",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleBarrierInitialize(context.Background(), payload); err != nil {
		t.Fatalf("handleBarrierInitialize: %v", err)
	}
	if !called {
		t.Fatal("barrier initialize was not called")
	}
	if !backend.SOPINSet() || backend.storedSOPIN != "so-1234" {
		t.Fatalf("SO PIN not persisted: set=%v stored=%q", backend.SOPINSet(), backend.storedSOPIN)
	}
	if !backend.UserPINSet() || backend.storedUserPIN != "u-5678" {
		t.Fatalf("User PIN not persisted: set=%v stored=%q", backend.UserPINSet(), backend.storedUserPIN)
	}
}
