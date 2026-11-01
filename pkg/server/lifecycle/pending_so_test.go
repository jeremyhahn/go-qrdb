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

package lifecycle

import (
	"context"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
)

// TestCompletePendingTenantAdmin_CompletesThePendingSO proves the hook
// creates the SO recorded under __sys:pending-so beside the admin, with the
// tenant's so role alone, and consumes both records.
func TestCompletePendingTenantAdmin_CompletesThePendingSO(t *testing.T) {
	ctx := context.Background()
	users, roles := newRealUserAndRole(t)
	store := kvstore.NewMemoryKVStore()
	admins, sos := tenantadmin.NewKVStore(store), tenantadmin.NewSOKVStore(store)
	putPending(t, admins, "acme", "admin", spkiThumbprint(t))
	putPending(t, sos, "acme", "officer", spkiThumbprint(t))

	n := &Node{PendingAdmins: admins, PendingSOs: sos, User: users, Role: roles}
	if err := n.completePendingTenantAdmin(discardLog())(ctx, "acme", tenant.AdminCompletionWithin); err != nil {
		t.Fatalf("hook = %v; want nil", err)
	}
	so, err := users.GetUserByUsername(ctx, "acme", "officer")
	if err != nil {
		t.Fatalf("GetUserByUsername(officer): %v", err)
	}
	if want := []string{role.SystemRoleID("acme", role.RoleNameSO)}; !slices.Equal(so.RoleIDs, want) {
		t.Fatalf("SO RoleIDs = %v, want %v", so.RoleIDs, want)
	}
	for name, s := range map[string]tenantadmin.Store{"admin": admins, "so": sos} {
		if rec, err := s.Get(ctx, "acme"); err != nil || rec != nil {
			t.Fatalf("pending %s = %+v, %v; want it consumed", name, rec, err)
		}
	}
}

// TestCompletePendingTenantAdmin_PendingSOsAloneRunsTheHook proves a node
// wired with only the SO store still runs the completion.
func TestCompletePendingTenantAdmin_PendingSOsAloneRunsTheHook(t *testing.T) {
	ctx := context.Background()
	users, roles := newRealUserAndRole(t)
	sos := tenantadmin.NewSOKVStore(kvstore.NewMemoryKVStore())
	putPending(t, sos, "acme", "officer", spkiThumbprint(t))

	n := &Node{PendingSOs: sos, User: users, Role: roles}
	if err := n.completePendingTenantAdmin(discardLog())(ctx, "acme", tenant.AdminCompletionWithin); err != nil {
		t.Fatalf("hook = %v; want nil", err)
	}
	if _, err := users.GetUserByUsername(ctx, "acme", "officer"); err != nil {
		t.Fatalf("GetUserByUsername(officer): %v", err)
	}
}

// TestNode_AttachSOPINsWithoutOnboardingIsANoOp proves a node with no tenant
// onboarding service, or a nil seam, attaches nothing and does not panic.
func TestNode_AttachSOPINsWithoutOnboardingIsANoOp(t *testing.T) {
	n := &Node{}
	n.AttachSOPINs(nil)
	n.AttachSOPINs(&noSOPINs{})
	if n.onboarding != nil {
		t.Fatal("AttachSOPINs created an onboarding service")
	}
}

// noSOPINs is an SO PIN seam that commits nothing.
type noSOPINs struct{}

func (*noSOPINs) SetSOPIN(context.Context, string, []byte) error { return nil }
