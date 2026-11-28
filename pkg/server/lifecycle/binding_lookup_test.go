package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clock"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// lookupTenantSvc answers GetTenant with a fixed result; every other
// TenantServicer method is unused by the binding lookup.
type lookupTenantSvc struct {
	tenant.TenantServicer
	info *tenant.TenantInfo
	err  error
}

func (s lookupTenantSvc) GetTenant(context.Context, string) (*tenant.TenantInfo, error) {
	return s.info, s.err
}

func TestTenantServiceBindingLookup_LookupFailures(t *testing.T) {
	down := errors.New("tenant store down")
	failing := TenantServiceBindingLookup(func() tenant.TenantServicer { return lookupTenantSvc{err: down} })
	if _, err := failing("acme"); !errors.Is(err, down) {
		t.Fatalf("lookup with a failing tenant service err = %v, want %v", err, down)
	}

	var unavailable *BindingTenantUnavailableError
	missing := TenantServiceBindingLookup(func() tenant.TenantServicer { return lookupTenantSvc{} })
	if _, err := missing("acme"); !errors.As(err, &unavailable) || unavailable.TenantID != "acme" {
		t.Fatalf("lookup of an unknown tenant err = %v, want *BindingTenantUnavailableError for acme", err)
	}
}

func TestVolumeDataRoot_SkipsAbsentResolvers(t *testing.T) {
	var unavailable *BindingTenantUnavailableError
	if _, err := volumeDataRoot("acme", []func() TenantDataRootResolver{nil}); !errors.As(err, &unavailable) {
		t.Fatalf("volumeDataRoot(nil resolver) err = %v, want *BindingTenantUnavailableError", err)
	}
}

func TestLocalBindingFactory_NilTenantLookupIsPrefix(t *testing.T) {
	f := NewLocalBindingFactory(database.BindingNode{DataDir: t.TempDir()}, nil, qrdbconfig.StorageConfig{}, nil)
	got, err := f.tenant("acme")
	if err != nil || got.ID != "acme" || got.Isolation != "" {
		t.Fatalf("tenant(acme) without a lookup = %+v, %v; want a bare acme tenant", got, err)
	}
}

func TestInfraClockAndLogReclaimer_Defaults(t *testing.T) {
	c := clock.Real()
	if got := infraClock(Infra{Clock: c}); got != c {
		t.Fatal("infraClock ignored the configured clock")
	}
	// A node without a database service has nothing to wire.
	wireLogReclaimer(&Node{})
	if err := (&replicaHost{}).AdmitReplica(1, "n1:7000"); err != nil {
		t.Fatalf("AdmitReplica without a feature gate = %v, want nil", err)
	}
}
