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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

func identityCtx(tenantID string) context.Context {
	return auth.WithIdentity(context.Background(), &auth.Identity{Subject: "u", TenantID: tenantID})
}

func TestCallerTenant(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"identity", identityCtx("acme"), "acme"},
		{"identity wins over a bound tenant", WithTenantID(identityCtx("acme"), "globex"), "acme"},
		{"identity with no tenant is the system tenant", identityCtx(""), tenantid.System},
		{"bound tenant without identity", WithTenantID(context.Background(), "globex"), "globex"},
		{"nothing bound is the system tenant", context.Background(), tenantid.System},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CallerTenant(tc.ctx); got != tc.want {
				t.Errorf("CallerTenant = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConfine(t *testing.T) {
	tests := []struct {
		name      string
		ctx       context.Context
		requested string
		want      string
		denied    bool
	}{
		{"empty is the caller's own", identityCtx("acme"), "", "acme", false},
		{"own tenant", identityCtx("acme"), "acme", "acme", false},
		{"another tenant", identityCtx("acme"), "globex", "", true},
		{"the system tenant from a tenant", identityCtx("acme"), tenantid.System, "", true},
		{"system to itself", identityCtx(tenantid.System), tenantid.System, tenantid.System, false},
		{"system to another tenant: no lift", identityCtx(tenantid.System), "acme", "", true},
		{"no tenant binding names the system tenant", context.Background(), tenantid.System, tenantid.System, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Confine(tc.ctx, tc.requested, "test op")
			if tc.denied {
				var denied *CrossTenantDeniedError
				if !errors.As(err, &denied) {
					t.Fatalf("err = %v; want *CrossTenantDeniedError", err)
				}
				if denied.RequestedTenantID != tc.requested || denied.Op != "test op" {
					t.Errorf("error = %+v; want it to name the requested tenant and the op", denied)
				}
				return
			}
			if err != nil {
				t.Fatalf("Confine: %v", err)
			}
			if got != tc.want {
				t.Errorf("Confine = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConfineAuditFilter(t *testing.T) {
	from := time.Unix(100, 0).UTC()
	in := audit.Filter{From: from, Actor: "a", Resource: "r", Action: "x", Outcome: "success", Limit: 5, Cursor: "c"}

	got, err := ConfineAuditFilter(identityCtx("acme"), in)
	if err != nil {
		t.Fatalf("ConfineAuditFilter: %v", err)
	}
	want := in
	want.TenantID = "acme"
	if got != want {
		t.Errorf("filter = %+v, want %+v (the other fields only narrow, and pass through)", got, want)
	}

	sys, err := ConfineAuditFilter(identityCtx(tenantid.System), audit.Filter{})
	if err != nil {
		t.Fatalf("ConfineAuditFilter(system): %v", err)
	}
	if sys.TenantID != tenantid.System || !sys.Untenanted {
		t.Errorf("system filter = %+v; want the system tenant plus untenanted platform events", sys)
	}

	// A request cannot set Untenanted for a tenant caller: it is recomputed.
	widened, err := ConfineAuditFilter(identityCtx("acme"), audit.Filter{Untenanted: true})
	if err != nil {
		t.Fatalf("ConfineAuditFilter: %v", err)
	}
	if widened.Untenanted {
		t.Error("a tenant caller's filter kept Untenanted, which would add platform events")
	}

	for _, foreign := range []string{"globex", tenantid.System} {
		_, err := ConfineAuditFilter(identityCtx("acme"), audit.Filter{TenantID: foreign})
		var denied *CrossTenantDeniedError
		if !errors.As(err, &denied) {
			t.Errorf("filter naming %s: err = %v; want *CrossTenantDeniedError", foreign, err)
		}
	}
	_, err = ConfineAuditFilter(identityCtx(tenantid.System), audit.Filter{TenantID: "acme"})
	var denied *CrossTenantDeniedError
	if !errors.As(err, &denied) {
		t.Errorf("system filter naming acme: err = %v; want *CrossTenantDeniedError", err)
	}
}
