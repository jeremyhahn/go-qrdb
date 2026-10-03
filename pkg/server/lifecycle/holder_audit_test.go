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
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// sealCeremonyCore builds the seal-only service graph over infra and returns
// its core ceremony service.
func sealCeremonyCore(t *testing.T, infra lifecycle.Infra) *ceremony.CeremonyService {
	t.Helper()
	node, err := lifecycle.BuildSealServicesOnly(context.Background(), infra)
	if err != nil {
		t.Fatalf("BuildSealServicesOnly: %v", err)
	}
	t.Cleanup(func() { _ = node.Stop(context.Background()) })
	core, ok := service.CeremonyServiceCore(node.Ceremony)
	if !ok {
		t.Fatal("the seal-only graph has no core ceremony service")
	}
	return core
}

// The sealed-window graph hands the composer's compliance trail to the
// ceremony service, whose HolderDeps carry it to every tenant and database
// holder authority.
func TestBuildSealServicesOnly_HandsTheTrailToTheHolderAuthority(t *testing.T) {
	barrier, registry, credSvc := newTestBarrier(t)
	trail := audit.NewRingBuffer(8)
	core := sealCeremonyCore(t, lifecycle.Infra{
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		AuditLogger:       trail,
		Logger:            slog.Default(),
	})
	got, ok := core.HolderDeps().Audit.(*audit.RingBuffer)
	if !ok || got != trail {
		t.Fatalf("HolderDeps().Audit = %T %p, want the composer's trail %p", core.HolderDeps().Audit, got, trail)
	}
}

// A threaded ceremony service -- the sealed window's, carried into the
// post-unseal graph or across an in-process restart -- records on the trail
// the graph that adopts it is given.
func TestBuildSealServicesOnly_ThreadedCeremonyAdoptsTheTrail(t *testing.T) {
	barrier, registry, credSvc := newTestBarrier(t)
	first := audit.NewRingBuffer(8)
	infra := lifecycle.Infra{
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		AuditLogger:       first,
		Logger:            slog.Default(),
	}
	core := sealCeremonyCore(t, infra)

	second := audit.NewRingBuffer(8)
	infra.CeremonyCoreSvc = core
	infra.AuditLogger = second
	if again := sealCeremonyCore(t, infra); again != core {
		t.Fatal("the threaded ceremony service was replaced")
	}
	if got, _ := core.HolderDeps().Audit.(*audit.RingBuffer); got != second {
		t.Fatalf("HolderDeps().Audit = %p, want the adopting graph's trail %p", got, second)
	}
}

// The sealed-window graph hands the node's audit failure posture to the
// holder authority with the trail.
func TestBuildSealServicesOnly_HandsThePostureToTheHolderAuthority(t *testing.T) {
	for _, policy := range []audit.FailurePolicy{audit.FailClosed, audit.FailOpen} {
		barrier, registry, credSvc := newTestBarrier(t)
		core := sealCeremonyCore(t, lifecycle.Infra{
			DataDir:            t.TempDir(),
			SealBarrier:        barrier,
			BarrierRegistry:    registry,
			CredentialCoreSvc:  credSvc,
			AuditLogger:        audit.NewRingBuffer(4),
			AuditFailurePolicy: policy,
			Logger:             slog.Default(),
		})
		if got := core.HolderDeps().AuditPolicy; got != policy {
			t.Fatalf("HolderDeps().AuditPolicy = %v, want %v", got, policy)
		}
	}
}
