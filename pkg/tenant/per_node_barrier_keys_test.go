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
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
)

// While the composer's per-node-barrier-keys check refuses, an isolated
// tenant is refused before anything is written -- a node that predates the
// feature would neither honour the tenant's init statement nor initialise its
// own barrier key from it. A shared tenant needs no per-node key and is not
// gated. Activation lifts the refusal; it is a toggle here, the node
// composition answering it from its featuregate.Gate.
func TestTenantService_CreateTenant_IsolatedBeforePerNodeBarrierKeysActivation(t *testing.T) {
	ctx := context.Background()
	var active atomic.Bool
	required := func() error {
		if active.Load() {
			return nil
		}
		return notActivePerNodeBarrierKeys()
	}
	svc, _ := newRaftService(t, WithPerNodeBarrierKeysGate(required))

	for _, isolation := range []IsolationType{IsolationBarrier, IsolationVolume, IsolationRaft} {
		_, err := svc.CreateTenant(ctx, "acme", "Acme", isolation, WithIsolationPolicy(TenantIsolationPolicyStrict))
		var notActive *featuregate.NotActiveError
		if !errors.As(err, &notActive) || notActive.Feature != featuregate.PerNodeBarrierKeys {
			t.Fatalf("CreateTenant(%s) before activation = %v, want NotActiveError(%s)", isolation, err, featuregate.PerNodeBarrierKeys)
		}
		if _, gerr := svc.GetTenant(ctx, "acme"); gerr == nil {
			t.Fatalf("a refused %s create left a record behind", isolation)
		}
	}
	if _, err := svc.CreateTenant(ctx, "shared", "Shared", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant(prefix) before activation: %v", err)
	}

	active.Store(true)
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationRaft, WithIsolationPolicy(TenantIsolationPolicyStrict)); err != nil {
		t.Fatalf("CreateTenant(raft) after activation: %v", err)
	}
}

// A prefix tenant cannot be changed into an isolated one before activation
// either: the change is the same new barrier a create would make.
func TestTenantService_UpdateTenant_ToIsolatedBeforePerNodeBarrierKeysActivation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newRaftService(t, WithPerNodeBarrierKeysGate(notActivePerNodeBarrierKeys))
	if _, err := svc.CreateTenant(ctx, "shared", "Shared", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant(prefix): %v", err)
	}
	var notActive *featuregate.NotActiveError
	if _, err := svc.UpdateTenant(ctx, "shared", TenantPatch{IsolationType: IsolationRaft}); !errors.As(err, &notActive) || notActive.Feature != featuregate.PerNodeBarrierKeys {
		t.Fatalf("UpdateTenant(prefix -> raft) before activation = %v, want NotActiveError(%s)", err, featuregate.PerNodeBarrierKeys)
	}
	if _, err := svc.UpdateTenant(ctx, "shared", TenantPatch{Name: "Renamed", IsolationType: IsolationPrefix}); err != nil {
		t.Fatalf("UpdateTenant keeping prefix isolation: %v", err)
	}
}

func TestWithPerNodeBarrierKeysGate_NilGatesNothing(t *testing.T) {
	ctx := context.Background()
	svc, _ := newRaftService(t, WithPerNodeBarrierKeysGate(nil))
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationRaft, WithIsolationPolicy(TenantIsolationPolicyStrict)); err != nil {
		t.Fatalf("CreateTenant(raft) on a node that gates nothing: %v", err)
	}
}

// notActivePerNodeBarrierKeys is the refusal the composer's check returns
// before activation (pkg/server/lifecycle featureRequired).
func notActivePerNodeBarrierKeys() error {
	return &featuregate.NotActiveError{Feature: featuregate.PerNodeBarrierKeys}
}
