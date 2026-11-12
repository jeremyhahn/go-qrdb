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
