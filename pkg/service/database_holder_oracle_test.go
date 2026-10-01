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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// systemHolderCtx is a context authenticated as a share holder whose
// identity is in the system tenant -- a holder of a system-tenant database,
// say, who reaches the identity-gated holder routes.
func systemHolderCtx() context.Context {
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{
		Subject:  "sys-holder",
		TenantID: tenantid.System,
		Metadata: map[string]string{auth.MetaPrincipalKind: auth.PrincipalShareHolder},
	})
	return tenant.WithTenantID(ctx, tenantid.System)
}

// TestAuthorizeDatabaseTenant_SystemShareHolderIsNotLifted: the system
// tenant may target any database by name, because it is the store-global
// admin. A share holder whose identity happens to be in the system tenant is
// not that admin. The holder routes are identity-gated, so before this they
// let such a holder probe every tenant's database names: another tenant's
// database answered with a holder or round refusal, a missing one with
// NotFound. Such a holder is confined to the system tenant's own databases,
// like any tenant caller, and another tenant's database answers NotFound,
// exactly as a missing one does.
func TestAuthorizeDatabaseTenant_SystemShareHolderIsNotLifted(t *testing.T) {
	svc, _ := newHolderDatabaseService(t)
	holder := systemHolderCtx()

	var notFound *database.NotFoundError
	_, err := svc.DatabaseBarrierQuorumBegin(holder, "vault", &QuorumBeginRequest{Op: seal.OpUnseal, Username: "alice"})
	if !errors.As(err, &notFound) {
		t.Fatalf("a system-tenant holder's quorum begin on acme's database = %v, want NotFoundError", err)
	}
	_, missing := svc.DatabaseBarrierQuorumBegin(holder, "no-such-db", &QuorumBeginRequest{Op: seal.OpUnseal, Username: "alice"})
	if !errors.As(missing, &notFound) {
		t.Fatalf("existing %v and missing %v must be indistinguishable", err, missing)
	}
	if _, err := svc.DatabaseBarrierShareClaimBegin(holder, "vault", "alice", "aa"); !errors.As(err, &notFound) {
		t.Fatalf("a system-tenant holder's claim on acme's database = %v, want NotFoundError", err)
	}

	// The system tenant's administrator is still the store-global admin.
	if _, err := svc.DatabaseBarrierOfficerList(dbAdminCtx(tenantid.System), "vault"); err != nil {
		t.Fatalf("the system administrator listing acme's holders: %v", err)
	}
}
