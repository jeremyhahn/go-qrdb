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

package bootstrap

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
)

// errAnchorConflict is pinDouble's refusal of a second, different thumbprint.
var errAnchorConflict = errors.New("anchor already pinned to another admin")

// pinDouble keeps barrierinit.Pins' contract: each barrier's thumbprint is
// written once, pinning the same one again succeeds and any other is refused.
type pinDouble struct {
	mu   sync.Mutex
	pins map[clusterseal.Barrier]string
	err  error
}

func (p *pinDouble) PinFirstAdmin(_ context.Context, b clusterseal.Barrier, thumbprint string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	if p.pins == nil {
		p.pins = map[clusterseal.Barrier]string{}
	}
	if current, ok := p.pins[b]; ok && current != thumbprint {
		return errAnchorConflict
	}
	p.pins[b] = thumbprint
	return nil
}

func (p *pinDouble) pinned(b clusterseal.Barrier) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pins[b]
}

var _ FirstAdminPinner = (*pinDouble)(nil)

func TestProvisionPendingAdmin_PinsTheFirstAdminAsTheBarrierAnchor(t *testing.T) {
	ctx := context.Background()
	pins := &pinDouble{}
	var undo undoStack
	fail := func(cause error) (*TenantProvisionResult, error) { return nil, cause }
	plan := &adminPlan{csrPEM: validAdminCSR(t), signer: &fakeSigner{cert: realCertPEM(t)}}

	result, err := provisionPendingAdmin(ctx, TenantProvisionConfig{TenantID: "acme"}, Dependencies{
		TrustDomain:    onboardingTrustDomain,
		PendingAdmins:  tenantadmin.NewKVStore(kvstore.NewMemoryKVStore()),
		BarrierAnchors: pins,
		Logger:         discardLogger(),
	}, plan, "admin", &undo, fail)
	if err != nil {
		t.Fatalf("provisionPendingAdmin: %v", err)
	}
	if got := pins.pinned(clusterseal.TenantBarrier("acme")); got == "" || got != result.AdminCertThumbprint {
		t.Fatalf("pinned anchor = %q, want the admin's thumbprint %q", got, result.AdminCertThumbprint)
	}
}

// A re-run over an admin already pending pins that admin, completing a run
// that recorded the admin and stopped before its pin.
func TestProvisionPendingAdmin_ARerunPinsTheRecordedAdmin(t *testing.T) {
	ctx := context.Background()
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	var undo undoStack
	fail := func(cause error) (*TenantProvisionResult, error) { return nil, cause }
	plan := &adminPlan{csrPEM: validAdminCSR(t), signer: &fakeSigner{cert: realCertPEM(t)}}
	first, err := provisionPendingAdmin(ctx, TenantProvisionConfig{TenantID: "acme"}, Dependencies{
		TrustDomain:   onboardingTrustDomain,
		PendingAdmins: pending,
		Logger:        discardLogger(),
	}, plan, "admin", &undo, fail)
	if err != nil {
		t.Fatalf("provisionPendingAdmin: %v", err)
	}

	pins := &pinDouble{}
	again, err := provisionPendingAdmin(ctx, TenantProvisionConfig{TenantID: "acme"}, Dependencies{
		TrustDomain:    onboardingTrustDomain,
		PendingAdmins:  pending,
		BarrierAnchors: pins,
		Logger:         discardLogger(),
	}, plan, "admin", &undo, fail)
	if err != nil {
		t.Fatalf("provisionPendingAdmin re-run: %v", err)
	}
	if again.AdminCertThumbprint != first.AdminCertThumbprint {
		t.Fatalf("re-run rotated the admin: %q, want %q", again.AdminCertThumbprint, first.AdminCertThumbprint)
	}
	if got := pins.pinned(clusterseal.TenantBarrier("acme")); got != first.AdminCertThumbprint {
		t.Fatalf("pinned anchor = %q, want the recorded admin's %q", got, first.AdminCertThumbprint)
	}
}

// A provisioning whose anchor cannot be pinned fails, and an exclusive one
// takes the pending admin back with it: no node could verify that admin's
// init statement.
func TestProvisionPendingAdmin_APinFailureUndoesThePendingAdmin(t *testing.T) {
	ctx := context.Background()
	for name, pinErr := range map[string]error{
		"store failure": errors.New("system store unavailable"),
		"conflict":      errAnchorConflict,
	} {
		t.Run(name, func(t *testing.T) {
			pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
			var undo undoStack
			fail := func(cause error) (*TenantProvisionResult, error) {
				if failed := undo.run(ctx, discardLogger(), "acme"); len(failed) != 0 {
					t.Fatalf("undo.run failed: %v", failed)
				}
				return nil, cause
			}
			plan := &adminPlan{csrPEM: validAdminCSR(t), signer: &fakeSigner{cert: realCertPEM(t)}}
			_, err := provisionPendingAdmin(ctx, TenantProvisionConfig{TenantID: "acme", Exclusive: true}, Dependencies{
				TrustDomain:    onboardingTrustDomain,
				PendingAdmins:  pending,
				BarrierAnchors: &pinDouble{err: pinErr},
				Logger:         discardLogger(),
			}, plan, "admin", &undo, fail)
			var want *PendingTenantAdminError
			if !errors.As(err, &want) || want.Op != "pin barrier anchor" || !errors.Is(err, pinErr) {
				t.Fatalf("err = %v, want *PendingTenantAdminError pinning the barrier anchor", err)
			}
			if rec, _ := pending.Get(ctx, "acme"); rec != nil {
				t.Fatal("the pending admin outlived its failed pin")
			}
		})
	}
}

func TestPinFirstAdmin_WithoutASeamPinsNothing(t *testing.T) {
	if err := pinFirstAdmin(context.Background(), Dependencies{}, "acme", "thumb"); err != nil {
		t.Fatalf("pinFirstAdmin without BarrierAnchors = %v, want nil", err)
	}
}
