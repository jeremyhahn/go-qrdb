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

// tenant_barrier_shamir_reinit_test.go covers the B1 review finding: an
// already-initialized Shamir tenant barrier must refuse a second
// `tenant barrier-init --strategy shamir` call BEFORE touching the
// registry (ReconfigureTenantShamir mutates the barrier's live
// strategy/config in place) or registering any new officer, leaving the
// threshold, config and holder set it refused to re-initialize AGAINST
// completely unchanged.
package tenant

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// shamirRosterOfficer builds one inline roster entry from a tenantHolder.
func shamirRosterOfficer(h *tenantHolder) ceremony.OfficerConfig {
	return ceremony.OfficerConfig{Username: h.name, Role: seal.RoleCustodian, CSRPEM: h.csr}
}

// TestTenantBarrierInitShamir_AlreadyInitialized_RefusesAndLeavesStateUnchanged
// is the B1 regression test: a 3-of-3 Shamir init, then a re-init attempt
// with a completely different 2-of-2 roster, must be refused -- and the
// first init's threshold, total and holder set must survive the refused
// attempt exactly as they were.
func TestTenantBarrierInitShamir_AlreadyInitialized_RefusesAndLeavesStateUnchanged(t *testing.T) {
	svc := newHolderTenantService(t)
	ctx := tenantAdminCtx("acme")
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	alice := newTenantHolder(t, "alice")
	bob := newTenantHolder(t, "bob")
	carol := newTenantHolder(t, "carol")
	firstRoster := &TenantBarrierShamirRoster{Officers: []ceremony.OfficerConfig{
		shamirRosterOfficer(alice), shamirRosterOfficer(bob), shamirRosterOfficer(carol),
	}}
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 3, 3, seal.StrategyShamir, firstRoster); err != nil {
		t.Fatalf("first init: %v", err)
	}

	before, err := svc.TenantBarrierStatus(ctx, "acme")
	if err != nil {
		t.Fatalf("status after first init: %v", err)
	}
	if before.ShamirThreshold != 3 || before.ShamirTotal != 3 {
		t.Fatalf("status after first init = %+v, want threshold=3 total=3", before)
	}
	officersBefore, err := svc.TenantBarrierOfficerList(ctx, "acme")
	if err != nil {
		t.Fatalf("officer list after first init: %v", err)
	}
	if len(officersBefore) != 3 {
		t.Fatalf("officer list after first init = %d entries, want 3", len(officersBefore))
	}

	dave := newTenantHolder(t, "dave")
	erin := newTenantHolder(t, "erin")
	secondRoster := &TenantBarrierShamirRoster{Officers: []ceremony.OfficerConfig{
		shamirRosterOfficer(dave), shamirRosterOfficer(erin),
	}}
	_, err = svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 2, 2, seal.StrategyShamir, secondRoster)
	if err == nil {
		t.Fatal("re-init over an already-initialized shamir barrier succeeded, want refusal")
	}
	if !errors.Is(err, seal.ErrAlreadyInitialized) {
		t.Fatalf("re-init error = %v, want errors.Is(err, seal.ErrAlreadyInitialized)", err)
	}

	after, err := svc.TenantBarrierStatus(ctx, "acme")
	if err != nil {
		t.Fatalf("status after refused re-init: %v", err)
	}
	if after.ShamirThreshold != before.ShamirThreshold || after.ShamirTotal != before.ShamirTotal {
		t.Fatalf("status after refused re-init = %+v, want unchanged from %+v", after, before)
	}
	officersAfter, err := svc.TenantBarrierOfficerList(ctx, "acme")
	if err != nil {
		t.Fatalf("officer list after refused re-init: %v", err)
	}
	if len(officersAfter) != 3 {
		t.Fatalf("officer list after refused re-init = %d entries, want the original 3, unchanged", len(officersAfter))
	}
	for _, name := range []string{"dave", "erin"} {
		for _, o := range officersAfter {
			if o.Username == name {
				t.Fatalf("officer list after refused re-init contains %q, which the refused attempt must never have registered", name)
			}
		}
	}
}

// TestTenantBarrierInitWithStrategy_RosterRequiresShamir proves a non-empty
// inline roster is refused outright on any strategy other than Shamir,
// instead of being silently ignored.
func TestTenantBarrierInitWithStrategy_RosterRequiresShamir(t *testing.T) {
	svc := newHolderTenantService(t)
	ctx := tenantAdminCtx("acme")
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	alice := newTenantHolder(t, "alice")
	roster := &TenantBarrierShamirRoster{Officers: []ceremony.OfficerConfig{shamirRosterOfficer(alice)}}

	_, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", []byte("a-passphrase"), 0, 0, seal.StrategyPassphrase, roster)
	if err == nil {
		t.Fatal("tenant barrier-init --strategy passphrase with a non-empty roster succeeded, want refusal")
	}
	var rosterErr *RosterRequiresShamirError
	if !errors.As(err, &rosterErr) {
		t.Fatalf("error = %v (%T), want *RosterRequiresShamirError", err, err)
	}
}
