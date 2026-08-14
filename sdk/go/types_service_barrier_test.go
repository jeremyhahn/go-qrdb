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

package sdk

import (
	"log/slog"
	"testing"
)

// newTestBarrier constructs a minimal in-memory StorageBarrier for the
// re-export tests.
func newTestBarrier(t *testing.T) *StorageBarrier {
	t.Helper()
	barrier, err := NewStorageBarrier(
		slog.Default(),
		NewMemoryBackend(),
		BarrierConfig{RootKeyPath: "test/root-key"},
		NewDeferredSoftwareStrategy(),
	)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	return barrier
}

// TestNewBarrierServiceWithCeremony_WiresDistributors proves the re-exported
// constructor installs the ceremony service as both the CeremonyDistributor
// and the RecoveryDistributor on the barrier — the wiring qrdbd relies on and
// that composing projects (quicbaas) consume through this re-export.
func TestNewBarrierServiceWithCeremony_WiresDistributors(t *testing.T) {
	barrier := newTestBarrier(t)

	cred, err := NewVersionedCredentialService(
		&CredentialConfig{Strategy: CredentialStrategyManual}, barrier, slog.Default())
	if err != nil {
		t.Fatalf("NewVersionedCredentialService: %v", err)
	}
	ceremonyCore, err := NewCeremonyService(
		&CeremonyConfig{DataDir: t.TempDir()}, barrier, cred, slog.Default())
	if err != nil {
		t.Fatalf("NewCeremonyService: %v", err)
	}

	svc := NewBarrierServiceWithCeremony(barrier, ceremonyCore)
	if svc == nil {
		t.Fatal("NewBarrierServiceWithCeremony returned nil")
	}
	if barrier.CeremonyDistributor() == nil {
		t.Error("ceremony distributor not installed on the barrier")
	}
	if barrier.RecoveryDistributor() == nil {
		t.Error("recovery distributor not installed on the barrier")
	}
}

// TestNewBarrierService_NoCeremony proves the plain constructor leaves the
// distributors unset, so the two re-exports remain distinguishable.
func TestNewBarrierService_NoCeremony(t *testing.T) {
	barrier := newTestBarrier(t)
	svc := NewBarrierService(barrier)
	if svc == nil {
		t.Fatal("NewBarrierService returned nil")
	}
	if barrier.CeremonyDistributor() != nil {
		t.Error("plain barrier service must not install a ceremony distributor")
	}
}

// TestCredentialStrategyBarrierReExport pins the constant's value to the
// canonical pkg/credentials identifier.
func TestCredentialStrategyBarrierReExport(t *testing.T) {
	if CredentialStrategyBarrier != "barrier" {
		t.Fatalf("CredentialStrategyBarrier = %q, want %q", CredentialStrategyBarrier, "barrier")
	}
}
