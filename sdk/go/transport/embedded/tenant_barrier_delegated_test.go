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

package embedded

import (
	"context"
	"crypto/x509"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

const barrierDelegatedPrincipal = "embedded-barrier-deleg-test"

func init() {
	permission.RegisterRoleGrant(barrierDelegatedPrincipal, permission.TenantBarrierSeal, permission.TenantBarrierOfficerList)
}

// delegatedBarrierCalls runs TenantBarrierSeal and TenantBarrierOfficerList
// for tenantID on an embedded transport whose gate verifies a #873
// delegation for acme, as that delegation, and returns each call's error by
// operation name and whether the service was reached.
func delegatedBarrierCalls(t *testing.T, tenantID string) (map[string]error, bool) {
	t.Helper()
	d := authtest.NewDelegation(t, delegation.Grant{Tenant: "acme", Principal: barrierDelegatedPrincipal,
		Scope: []string{string(permission.TenantBarrierSeal), string(permission.TenantBarrierOfficerList)}})
	roles := authtest.BuiltInRoles(t, "acme", "bob-corp")
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles),
		authmw.WithDelegationVerifier(d.Authenticator),
		authmw.WithAuditLogger(audit.NewDecisionLogger(audit.NewRingBuffer(8))))
	ctx, _, err := mw.AuthenticateDelegation(t.Context(), []*x509.Certificate{d.Certificate})
	if err != nil {
		t.Fatalf("AuthenticateDelegation: %v", err)
	}

	svc := &fullTenantMock{}
	tr := newExtTransport(t, WithTenantService(svc), WithAuthMiddleware(mw))
	errs := map[string]error{}
	for op, call := range map[string]func(context.Context) error{
		"seal": func(ctx context.Context) error {
			return tr.TenantBarrierSeal(ctx, &transport.TenantBarrierSealRequest{TenantID: tenantID})
		},
		"officer_list": func(ctx context.Context) error {
			_, err := tr.TenantBarrierOfficerList(ctx, tenantID)
			return err
		},
	} {
		errs[op] = call(ctx)
	}
	return errs, svc.called
}

// TestTenantBarrierGate_Delegated_SealAndOfficerList proves a #873
// delegation for acme passes the seal and officer_list gates in acme.
func TestTenantBarrierGate_Delegated_SealAndOfficerList(t *testing.T) {
	errs, called := delegatedBarrierCalls(t, "acme")
	for op, err := range errs {
		if err != nil {
			t.Errorf("delegated %s in acme: %v; want the gate to pass", op, err)
		}
	}
	if !called {
		t.Fatal("service was never reached")
	}
}

// TestTenantBarrierGate_Delegated_CrossTenantDenied proves the same
// delegation is denied on another tenant's barrier, before the service.
func TestTenantBarrierGate_Delegated_CrossTenantDenied(t *testing.T) {
	errs, called := delegatedBarrierCalls(t, "bob-corp")
	for op, err := range errs {
		if !errors.Is(err, auth.ErrUnauthorized) {
			t.Errorf("delegated %s in bob-corp: %v; want an authorization denial", op, err)
		}
	}
	if called {
		t.Fatal("service was reached despite a denial")
	}
}
