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

package lifecycle_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestNewSealStorePINBackend_PersistsAcrossRestarts pins that the PINs live
// in the node-local seal store: a second backend over the same store sees
// the SO PIN the first provisioned, and verifies it.
func TestNewSealStorePINBackend_PersistsAcrossRestarts(t *testing.T) {
	store := seal.NewMemoryBackend()
	first, err := lifecycle.NewSealStorePINBackend(store)
	if err != nil {
		t.Fatalf("NewSealStorePINBackend: %v", err)
	}
	if err := pin.Provision(context.Background(), first, "so-pin-1234", "user-pin-1234"); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	second, err := lifecycle.NewSealStorePINBackend(store)
	if err != nil {
		t.Fatalf("NewSealStorePINBackend (restart): %v", err)
	}
	if !second.SOPINSet() {
		t.Fatal("the SO PIN must survive a restart")
	}
	if err := second.VerifySOPIN("so-pin-1234"); err != nil {
		t.Errorf("VerifySOPIN: %v", err)
	}
	if err := second.VerifySOPIN("wrong"); err == nil {
		t.Error("a wrong SO PIN must be refused")
	}
	keys, err := store.List(context.Background(), "__pin/")
	if err != nil || len(keys) == 0 {
		t.Errorf("PIN records must be namespaced under __pin/: %v %v", keys, err)
	}
}

// TestNewSealStorePINBackend_NilStore pins the typed refusal.
func TestNewSealStorePINBackend_NilStore(t *testing.T) {
	_, err := lifecycle.NewSealStorePINBackend(nil)
	var nilStore *lifecycle.NilSealStoreError
	if !errors.As(err, &nilStore) {
		t.Fatalf("got %v, want NilSealStoreError", err)
	}
}

// TestBuildSealServicesOnly_WiresHolderDeps pins that Infra's PIN backend
// and holder trust domain reach the ceremony service.
func TestBuildSealServicesOnly_WiresHolderDeps(t *testing.T) {
	barrier, registry, credSvc := newTestBarrier(t)
	pins, err := lifecycle.NewSealStorePINBackend(seal.NewMemoryBackend())
	if err != nil {
		t.Fatal(err)
	}
	node, err := lifecycle.BuildSealServicesOnly(context.Background(), lifecycle.Infra{
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		PINBackend:        pins,
		HolderTrustDomain: "db.example.com",
		Logger:            slog.Default(),
	})
	if err != nil {
		t.Fatalf("BuildSealServicesOnly: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()
	core, ok := service.CeremonyServiceCore(node.Ceremony)
	if !ok {
		t.Fatal("core ceremony service not exposed")
	}
	if got := core.HolderDeps().TrustDomain; got != "db.example.com" {
		t.Errorf("holder trust domain = %q", got)
	}
}
