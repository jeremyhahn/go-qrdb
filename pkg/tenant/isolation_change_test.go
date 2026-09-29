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

package tenant

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// fakeDataProbe answers a fixed data state and counts the questions.
type fakeDataProbe struct {
	hasData bool
	err     error
	calls   int
}

func (p *fakeDataProbe) TenantHasData(context.Context, string) (bool, error) {
	p.calls++
	return p.hasData, p.err
}

var _ TenantDataProbe = (*fakeDataProbe)(nil)

// isolationServices returns both shipped tenant services, each holding tenant
// "acme" on prefix isolation, so every case runs against both.
func isolationServices(t *testing.T) map[string]interface {
	TenantServicer
	IsolationChangeWiring
} {
	t.Helper()
	barrierSvc, _ := newTestService(t)
	kvSvc, err := NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	out := map[string]interface {
		TenantServicer
		IsolationChangeWiring
	}{"barrier": barrierSvc, "kv": kvSvc.(*kvTenantService)}
	for name, svc := range out {
		if _, err := svc.CreateTenant(context.Background(), "acme", "Acme", IsolationPrefix); err != nil {
			t.Fatalf("%s CreateTenant: %v", name, err)
		}
	}
	return out
}

// An isolation change on a tenant that holds data is refused in the SERVICE,
// so every transport inherits it -- not only REST.
func TestUpdateTenant_IsolationChangeRefusedWhenTenantHasData(t *testing.T) {
	for name, svc := range isolationServices(t) {
		t.Run(name, func(t *testing.T) {
			svc.SetTenantDataProbe(&fakeDataProbe{hasData: true})
			_, err := svc.UpdateTenant(context.Background(), "acme", TenantPatch{IsolationType: IsolationVolume})
			if !errors.Is(err, ErrTenantHasData) {
				t.Fatalf("err = %v, want ErrTenantHasData", err)
			}
			info, _ := svc.GetTenant(context.Background(), "acme")
			if info.EffectiveIsolationType() != IsolationPrefix {
				t.Fatalf("a refused change moved acme to %s", info.EffectiveIsolationType())
			}
		})
	}
}

func TestUpdateTenant_IsolationChangeFailsClosedWithoutAProbe(t *testing.T) {
	for name, svc := range isolationServices(t) {
		t.Run(name, func(t *testing.T) {
			_, err := svc.UpdateTenant(context.Background(), "acme", TenantPatch{IsolationType: IsolationVolume})
			if !errors.Is(err, ErrTenantDataUnverifiable) {
				t.Fatalf("err = %v, want ErrTenantDataUnverifiable", err)
			}
		})
	}
}

func TestUpdateTenant_IsolationChangeFailsClosedOnProbeError(t *testing.T) {
	cause := errors.New("leader unavailable")
	for name, svc := range isolationServices(t) {
		t.Run(name, func(t *testing.T) {
			svc.SetTenantDataProbe(&fakeDataProbe{err: cause})
			_, err := svc.UpdateTenant(context.Background(), "acme", TenantPatch{IsolationType: IsolationVolume})
			var probeErr *TenantDataProbeError
			if !errors.As(err, &probeErr) || probeErr.TenantID != "acme" || !errors.Is(err, cause) {
				t.Fatalf("err = %v, want *TenantDataProbeError wrapping the cause", err)
			}
		})
	}
}

func TestUpdateTenant_IsolationChangeAllowedOnAnEmptyTenant(t *testing.T) {
	for name, svc := range isolationServices(t) {
		t.Run(name, func(t *testing.T) {
			probe := &fakeDataProbe{}
			svc.SetTenantDataProbe(probe)
			info, err := svc.UpdateTenant(context.Background(), "acme", TenantPatch{IsolationType: IsolationVolume})
			if err != nil {
				t.Fatalf("UpdateTenant: %v", err)
			}
			if info.IsolationType != IsolationVolume || probe.calls != 1 {
				t.Fatalf("isolation = %s after %d probes, want volume after 1", info.IsolationType, probe.calls)
			}
		})
	}
}

// Naming the CURRENT isolation type, or none, changes nothing and consults no
// probe -- a rename works on a node with no probe wired.
func TestUpdateTenant_NoOpIsolationNeedsNoProbe(t *testing.T) {
	for name, svc := range isolationServices(t) {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.UpdateTenant(context.Background(), "acme", TenantPatch{IsolationType: IsolationPrefix}); err != nil {
				t.Fatalf("same isolation type without a probe = %v, want nil", err)
			}
			if _, err := svc.UpdateTenant(context.Background(), "acme", TenantPatch{Name: "Acme Corp"}); err != nil {
				t.Fatalf("rename without a probe = %v, want nil", err)
			}
		})
	}
}

func TestWithTenantDataProbe_SetsTheProbe(t *testing.T) {
	s := &TenantService{}
	p := &fakeDataProbe{}
	WithTenantDataProbe(p)(s)
	if ref := s.dataProbe.Load(); ref == nil || ref.probe != p {
		t.Fatal("WithTenantDataProbe did not set the probe")
	}
	s.SetTenantDataProbe(nil)
	if s.dataProbe.Load() != nil {
		t.Fatal("a nil probe must unwire the probe")
	}
}

func TestTenantDataProbeError_Message(t *testing.T) {
	err := &TenantDataProbeError{TenantID: "acme", Err: errors.New("down")}
	if err.Error() != "tenant: cannot verify tenant acme holds no data: down" {
		t.Fatalf("Error() = %q", err.Error())
	}
}
