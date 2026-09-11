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

package tenant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestEnforceTenantScope_SystemCallerAllowed verifies a context with no
// tenant binding (resolving to the system tenant) and an explicit
// system-tenant binding both pass for any target.
func TestEnforceTenantScope_SystemCallerAllowed(t *testing.T) {
	if err := enforceTenantScope(context.Background(), "victim", "seal"); err != nil {
		t.Errorf("unbound ctx (system fallback): unexpected error %v", err)
	}
	ctx := WithTenantID(context.Background(), SystemTenantID)
	if err := enforceTenantScope(ctx, "victim", "seal"); err != nil {
		t.Errorf("explicit system binding: unexpected error %v", err)
	}
}

// TestEnforceTenantScope_OwnTenantAllowed verifies a caller bound to the
// target tenant passes.
func TestEnforceTenantScope_OwnTenantAllowed(t *testing.T) {
	ctx := WithTenantID(context.Background(), "acme")
	if err := enforceTenantScope(ctx, "acme", "unseal"); err != nil {
		t.Errorf("own-tenant caller: unexpected error %v", err)
	}
}

// TestEnforceTenantScope_CrossTenantDenied verifies a caller bound to one
// tenant is denied against a different target with a populated
// TenantScopeError.
func TestEnforceTenantScope_CrossTenantDenied(t *testing.T) {
	ctx := WithTenantID(context.Background(), "attacker")
	err := enforceTenantScope(ctx, "victim", "initialize")
	var tse *TenantScopeError
	if !errors.As(err, &tse) {
		t.Fatalf("expected *TenantScopeError, got %T: %v", err, err)
	}
	if tse.CallerTenantID != "attacker" || tse.TargetTenantID != "victim" || tse.Op != "initialize" {
		t.Errorf("TenantScopeError = %+v, want caller=attacker target=victim op=initialize", tse)
	}
}

// TestTenantScopeError_Error verifies the error message carries both tenant
// ids and the operation.
func TestTenantScopeError_Error(t *testing.T) {
	e := &TenantScopeError{CallerTenantID: "a", TargetTenantID: "b", Op: "seal"}
	msg := e.Error()
	for _, want := range []string{"a", "b", "seal"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, missing %q", msg, want)
		}
	}
}

// TestTenantBarrierOps_CrossTenantDenied verifies every per-tenant barrier
// service method fails closed with TenantScopeError when the caller's
// context is bound to a different tenant than the target — the service-layer
// defense behind transport RBAC, covering in-process surfaces and any
// transport that fails to bind the true target tenant.
func TestTenantBarrierOps_CrossTenantDenied(t *testing.T) {
	svc, _ := newTestService(t)

	if _, err := svc.CreateTenant(context.Background(), "victim", "Victim", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	attacker := WithTenantID(context.Background(), "attacker")

	cases := []struct {
		name string
		call func() error
	}{
		{"init", func() error {
			_, err := svc.TenantBarrierInit(attacker, "victim", []byte("secret"), 0, 0)
			return err
		}},
		{"init-with-strategy", func() error {
			_, err := svc.TenantBarrierInitWithStrategy(attacker, "victim", []byte(""), 0, 0, seal.StrategyNone)
			return err
		}},
		{"init-shamir", func() error {
			_, err := svc.TenantBarrierInitWithStrategy(attacker, "victim", []byte(""), 2, 3, seal.StrategyShamir)
			return err
		}},
		{"unseal", func() error {
			return svc.TenantBarrierUnseal(attacker, "victim", []byte("key"))
		}},
		{"unseal-share", func() error {
			_, err := svc.TenantBarrierUnsealShare(attacker, "victim", "share-1")
			return err
		}},
		{"seal", func() error {
			return svc.TenantBarrierSeal(attacker, "victim")
		}},
		{"status", func() error {
			_, err := svc.TenantBarrierStatus(attacker, "victim")
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			var tse *TenantScopeError
			if !errors.As(err, &tse) {
				t.Fatalf("expected *TenantScopeError, got %T: %v", err, err)
			}
			if tse.TargetTenantID != "victim" {
				t.Errorf("TargetTenantID = %q, want %q", tse.TargetTenantID, "victim")
			}
		})
	}
}

// TestTenantBarrierOps_OwnTenantPermitted verifies a caller bound to the
// target tenant passes the scope gate: a full init/status/seal round-trip
// succeeds with the caller's own tenant bound in the context.
func TestTenantBarrierOps_OwnTenantPermitted(t *testing.T) {
	svc, _ := newTestService(t)

	if _, err := svc.CreateTenant(context.Background(), "acme", "Acme", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	own := WithTenantID(context.Background(), "acme")

	if _, err := svc.TenantBarrierInitWithStrategy(own, "acme", []byte(""), 0, 0, seal.StrategyNone); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy(own tenant): %v", err)
	}
	st, err := svc.TenantBarrierStatus(own, "acme")
	if err != nil {
		t.Fatalf("TenantBarrierStatus(own tenant): %v", err)
	}
	if !st.Initialized {
		t.Error("expected initialized=true after init")
	}
	if err := svc.TenantBarrierSeal(own, "acme"); err != nil {
		t.Fatalf("TenantBarrierSeal(own tenant): %v", err)
	}
}
