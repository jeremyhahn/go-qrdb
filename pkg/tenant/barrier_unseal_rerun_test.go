package tenant

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
)

// A rerun of a partial fan-out reaches replicas the first run opened: an
// unseal of an open barrier under the credential that opened it succeeds,
// and under any other credential refuses and leaves the barrier open.
func TestTenantBarrierUnseal_AnOpenBarrierVerifiesTheCredential(t *testing.T) {
	svc, reg := newTestService(t)
	ctx := WithTenantID(context.Background(), "rerun")

	if _, err := svc.CreateTenant(context.Background(), "rerun", "Rerun", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.TenantBarrierInit(context.Background(), "rerun", []byte("rerun-pw"), 0, 0); err != nil {
		t.Fatalf("TenantBarrierInit: %v", err)
	}
	if err := reg.SealTenant("rerun"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	if err := svc.TenantBarrierUnseal(ctx, "rerun", []byte("rerun-pw")); err != nil {
		t.Fatalf("first TenantBarrierUnseal: %v", err)
	}

	if err := svc.TenantBarrierUnseal(ctx, "rerun", []byte("rerun-pw")); err != nil {
		t.Fatalf("rerun TenantBarrierUnseal with the same credential: %v", err)
	}

	err := svc.TenantBarrierUnseal(ctx, "rerun", []byte("not-the-pw"))
	if !errors.Is(err, seal.ErrCredentialMismatch) {
		t.Fatalf("rerun TenantBarrierUnseal with a wrong credential = %v, want ErrCredentialMismatch", err)
	}
	var be *BarrierError
	if !errors.As(err, &be) || be.TenantID != "rerun" {
		t.Fatalf("wrong-credential error = %#v, want a *BarrierError for rerun", err)
	}
	status, err := reg.TenantStatus("rerun")
	if err != nil {
		t.Fatalf("TenantStatus: %v", err)
	}
	if status.Sealed {
		t.Fatal("a refused rerun sealed the open barrier")
	}
}

// TestTenantBarrierUnseal_AnOpenNoneBarrierRerunSucceeds pins the rerun of a
// plaintext tenant barrier: none holds no root key and takes no secret, so a
// second unseal of the already-open barrier succeeds instead of failing the
// credential check.
func TestTenantBarrierUnseal_AnOpenNoneBarrierRerunSucceeds(t *testing.T) {
	svc, reg := newTestService(t)
	ctx := WithTenantID(context.Background(), "plain")

	if _, err := svc.CreateTenant(context.Background(), "plain", "Plain", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, "plain", nil, 0, 0, seal.StrategyNone, nil); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy(none): %v", err)
	}
	if err := reg.SealTenant("plain"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	if err := svc.TenantBarrierUnseal(ctx, "plain", []byte("unused")); err != nil {
		t.Fatalf("first TenantBarrierUnseal: %v", err)
	}
	if err := svc.TenantBarrierUnseal(ctx, "plain", []byte("unused")); err != nil {
		t.Fatalf("rerun TenantBarrierUnseal of an open none barrier: %v", err)
	}
	status, err := reg.TenantStatus("plain")
	if err != nil {
		t.Fatalf("TenantStatus: %v", err)
	}
	if status.Sealed {
		t.Fatal("barrier sealed after the rerun")
	}
}

// The provider-backed path answers a rerun the same way.
func TestTenantBarrierUnseal_AnOpenProviderBackedBarrierRerunSucceeds(t *testing.T) {
	device, err := sealtest.NewDeviceStrategy("test-kms")
	if err != nil {
		t.Fatalf("NewDeviceStrategy: %v", err)
	}
	f := newProviderUnsealFixture(t, device)
	if err := f.unseal(); err != nil {
		t.Fatalf("first TenantBarrierUnseal: %v", err)
	}
	if err := f.unseal(); err != nil {
		t.Fatalf("rerun TenantBarrierUnseal: %v", err)
	}
	if f.barrier(t).IsSealed() {
		t.Fatal("barrier sealed after the rerun")
	}
}

// TestTenantBarrierUnseal_AnOpenProviderBackedBarrierRerunUsesTheResolvedStrategy
// pins that a rerun on an open provider-backed barrier is checked with the
// strategy the provider returns now, not the one cached at the first unseal:
// a same-ID strategy that cannot unwrap the root key is refused, and the
// original strategy restored passes again.
func TestTenantBarrierUnseal_AnOpenProviderBackedBarrierRerunUsesTheResolvedStrategy(t *testing.T) {
	device, err := sealtest.NewDeviceStrategy("test-kms")
	if err != nil {
		t.Fatalf("NewDeviceStrategy: %v", err)
	}
	other, err := sealtest.NewDeviceStrategy("test-kms")
	if err != nil {
		t.Fatalf("NewDeviceStrategy: %v", err)
	}
	f := newProviderUnsealFixture(t, device)
	if err := f.unseal(); err != nil {
		t.Fatalf("first TenantBarrierUnseal: %v", err)
	}
	resolve := func(s seal.SealingStrategy) {
		f.provider.mu.Lock()
		f.provider.result = &seal.BarrierKeyStrategy{
			Strategy:   s,
			Credential: seal.CredentialNone,
			Custody:    seal.CustodyOwner,
		}
		f.provider.mu.Unlock()
	}

	resolve(other)
	err = f.unseal()
	var be *BarrierError
	if !errors.As(err, &be) || !errors.Is(err, seal.ErrCredentialMismatch) {
		t.Fatalf("rerun with a same-ID strategy of another key = %v, want BarrierError wrapping ErrCredentialMismatch", err)
	}
	if f.barrier(t).IsSealed() {
		t.Fatal("barrier sealed after the refused rerun")
	}

	resolve(device)
	if err := f.unseal(); err != nil {
		t.Fatalf("rerun with the original strategy: %v", err)
	}
}
