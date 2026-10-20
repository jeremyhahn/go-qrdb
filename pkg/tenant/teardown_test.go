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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// barrierWitness records, at each teardown step it stands in for, whether the
// tenant's barrier was still registered -- destroying it unregisters it, and
// a replica must be stopped before that happens.
type barrierWitness struct {
	reg   *seal.BarrierRegistry
	steps []string
	live  []bool
	err   map[string]error
}

func (w *barrierWitness) observe(step, tenantID string) error {
	_, err := w.reg.GetTenant(tenantID)
	w.steps = append(w.steps, step)
	w.live = append(w.live, err == nil)
	return w.err[step]
}

// RetireTenantHome implements HomeRetirer.
func (w *barrierWitness) RetireTenantHome(_ context.Context, tenantID string) error {
	return w.observe("retire-home", tenantID)
}

// CloseTenantRuntime implements TenantRuntimeCloser.
func (w *barrierWitness) CloseTenantRuntime(_ context.Context, tenantID string) error {
	return w.observe("close-runtime", tenantID)
}

var (
	_ HomeRetirer         = (*barrierWitness)(nil)
	_ TenantRuntimeCloser = (*barrierWitness)(nil)
)

// newWitnessedTenant creates a barrier-isolated tenant "acme" on a service
// whose home retirer and runtime closer are one witness.
func newWitnessedTenant(t *testing.T) (*TenantService, *seal.BarrierRegistry, *barrierWitness) {
	t.Helper()
	svc, reg := newTestService(t)
	if _, err := svc.CreateTenant(context.Background(), "acme", "Acme", IsolationBarrier); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	w := &barrierWitness{reg: reg, err: map[string]error{}}
	svc.SetHomeRetirer(w)
	svc.SetTenantRuntimeCloser(w)
	return svc, reg, w
}

func TestDeleteTenant_StopsRuntimesBeforeTheBarrierIsDestroyed(t *testing.T) {
	svc, reg, w := newWitnessedTenant(t)

	if err := svc.DeleteTenant(context.Background(), "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	if len(w.steps) != 2 || w.steps[0] != "retire-home" || w.steps[1] != "close-runtime" {
		t.Fatalf("steps = %v, want the home retired and then the runtimes closed", w.steps)
	}
	for i, live := range w.live {
		if !live {
			t.Errorf("%s ran after the barrier was destroyed", w.steps[i])
		}
	}
	if _, err := reg.GetTenant("acme"); !errors.Is(err, seal.ErrTenantNotFound) {
		t.Errorf("the barrier was not destroyed after the runtimes stopped: %v", err)
	}
}

func TestDeleteTenant_FailedRuntimeStopKeepsTheBarrier(t *testing.T) {
	for _, step := range []string{"retire-home", "close-runtime"} {
		t.Run(step, func(t *testing.T) {
			svc, reg, w := newWitnessedTenant(t)
			reclaim := &recordingReclaimer{}
			svc.SetKeyspaceReclaimer(reclaim)
			w.err[step] = errStoreFault

			err := svc.DeleteTenant(context.Background(), "acme")
			var te *TeardownError
			var rs *RuntimeStopError
			if !errors.As(err, &te) || !errors.As(err, &rs) || rs.Op != step || !errors.Is(err, errStoreFault) {
				t.Fatalf("DeleteTenant = %v, want a TeardownError over RuntimeStopError{Op: %s}", err, step)
			}
			if rs.Error() != "tenant: "+step+" for acme: "+errStoreFault.Error() {
				t.Errorf("Error() = %q", rs.Error())
			}
			if _, gErr := reg.GetTenant("acme"); gErr != nil {
				t.Errorf("a runtime that did not stop must keep its barrier: %v", gErr)
			}
			if len(reclaim.calls) != 0 {
				t.Errorf("reclaim ran over a teardown that stopped early: %v", reclaim.calls)
			}

			// Re-issuing resumes from the step that failed.
			delete(w.err, step)
			if err := svc.DeleteTenant(context.Background(), "acme"); err != nil {
				t.Fatalf("re-issued DeleteTenant: %v", err)
			}
			if _, err := reg.GetTenant("acme"); !errors.Is(err, seal.ErrTenantNotFound) {
				t.Errorf("the resume did not destroy the barrier: %v", err)
			}
		})
	}
}
