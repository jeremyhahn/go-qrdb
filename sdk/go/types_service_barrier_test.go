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
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
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
// constructors install the ceremony service as the barrier's
// CeremonyDistributor — the wiring qrdbd relies on and that composing
// projects (quicbaas) consume through these re-exports. One distributor now
// serves both key domains (unseal and recovery).
func TestNewBarrierServiceWithCeremony_WiresDistributors(t *testing.T) {
	barrier := newTestBarrier(t)

	ceremonyCore, err := NewCeremonyService(&CeremonyConfig{DataDir: t.TempDir()}, barrier, slog.Default())
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

// TestAuthFactorTenantMismatch_IsAnAliasNotACopy is the compile-time half of
// the seam-error contract (docs/auth-factors/architecture.md#the-seams-error-contract):
// a type declared here that merely resembled service.AuthFactorTenantMismatchError,
// rather than aliasing it, would compile and still leave every transport's
// errors.As blind to a value built through this name.
func TestAuthFactorTenantMismatch_IsAnAliasNotACopy(t *testing.T) {
	var _ *service.AuthFactorTenantMismatchError = (*AuthFactorTenantMismatchError)(nil)
}

// TestAuthFactorTenantMismatch_ConstructibleThroughTheSDK is the runtime half:
// a value built through the SDK-visible type name, with no pkg/service import
// of its own, must still match pkg/service's type via errors.As — the same
// assertion form every transport's classifier is written against.
func TestAuthFactorTenantMismatch_ConstructibleThroughTheSDK(t *testing.T) {
	var err error = &AuthFactorTenantMismatchError{Requested: "globex", Context: "acme"}

	var target *service.AuthFactorTenantMismatchError
	if !errors.As(err, &target) {
		t.Fatalf("a value built through the SDK alias must match pkg/service's type, got %T: %v", err, err)
	}
	if target.Requested != "globex" || target.Context != "acme" {
		t.Errorf("fields did not survive the alias: got Requested=%q Context=%q", target.Requested, target.Context)
	}
}
