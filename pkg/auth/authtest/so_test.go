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

package authtest_test

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestSOIdentity_HoldsThePersistedSORole pins SOIdentity to what the role
// store actually persists, so the SO gate tests built on it test the real
// role and not a lookalike.
func TestSOIdentity_HoldsThePersistedSORole(t *testing.T) {
	roles := authtest.BuiltInRoles(t, "t1")
	id := authtest.SOIdentity("t1", "officer")
	held, err := role.HoldsSystemRole(context.Background(), roles, id.TenantID, id.Roles, role.RoleNameSO)
	if err != nil || !held {
		t.Fatalf("HoldsSystemRole(SOIdentity) = %v, %v; want true", held, err)
	}
}

// TestGrantedNonSOIdentity_IsAdmittedWithoutTheSORole: RBAC admits it to the
// permission, and it does not hold the SO role -- the two facts the SO gate
// tests rely on.
func TestGrantedNonSOIdentity_IsAdmittedWithoutTheSORole(t *testing.T) {
	// seal:rekey is platform scope (ScopeCross), so the identity is the
	// system tenant's.
	roles := authtest.BuiltInRoles(t, tenant.SystemTenantID)
	id := authtest.GrantedNonSOIdentity(tenant.SystemTenantID, "token", permission.SealRekey)
	ctx := tenant.WithTenantID(context.Background(), tenant.SystemTenantID)
	if err := rbac.New(roles).Authorize(ctx, id, "seal", "rekey"); err != nil {
		t.Fatalf("RBAC refused the granted identity: %v", err)
	}
	held, err := role.HoldsSystemRole(ctx, roles, id.TenantID, id.Roles, role.RoleNameSO)
	if err != nil || held {
		t.Fatalf("HoldsSystemRole = %v, %v; want false", held, err)
	}
}

func TestSOVerifier_VerifiesTheCommittedPIN(t *testing.T) {
	v := authtest.SOVerifier(t, "1234", "t1")
	if err := v.VerifySOPIN(context.Background(), "t1", []byte("1234")); err != nil {
		t.Fatalf("committed PIN refused: %v", err)
	}
	if err := v.VerifySOPIN(context.Background(), "t1", []byte("nope")); err == nil {
		t.Fatal("wrong PIN accepted")
	}
}

func TestBarrier_CountsRekeys(t *testing.T) {
	b := &authtest.Barrier{}
	info, err := b.BarrierRekey(context.Background(), 2, 3)
	if err != nil || info.Threshold != 2 || info.TotalShares != 3 {
		t.Fatalf("BarrierRekey = %+v, %v", info, err)
	}
	if b.Rekeys() != 1 {
		t.Fatalf("Rekeys = %d, want 1", b.Rekeys())
	}
}

func TestBarrier_CountsRecoveryDeletes(t *testing.T) {
	b := &authtest.Barrier{}
	if err := b.BarrierDeleteRecoveryKeys(context.Background()); err != nil {
		t.Fatalf("BarrierDeleteRecoveryKeys: %v", err)
	}
	if b.RecoveryDeletes() != 1 {
		t.Fatalf("RecoveryDeletes = %d, want 1", b.RecoveryDeletes())
	}
}

func TestBarrier_CountsSeals(t *testing.T) {
	b := &authtest.Barrier{}
	if err := b.BarrierSeal(context.Background()); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}
	if b.Seals() != 1 {
		t.Fatalf("Seals = %d, want 1", b.Seals())
	}
}

// TestBarrierPINBackend_VerifiesTheCommittedPIN pins the double to the real
// verifier's behaviour: the committed PIN verifies and nothing else does.
func TestBarrierPINBackend_VerifiesTheCommittedPIN(t *testing.T) {
	b := authtest.BarrierPINBackend(t, "barrier-1234")
	if !b.SOPINSet() {
		t.Fatal("no SO PIN set")
	}
	if err := b.VerifySOPIN("barrier-1234"); err != nil {
		t.Fatalf("committed PIN refused: %v", err)
	}
	if err := b.VerifySOPIN("wrong"); err == nil {
		t.Fatal("wrong PIN accepted")
	}
	if err := b.VerifySOPIN(""); err == nil {
		t.Fatal("empty PIN accepted")
	}
}
