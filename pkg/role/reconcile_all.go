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

package role

import (
	"context"
	"errors"
	"sort"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TenantLister enumerates the tenants a node knows. *tenant.TenantService
// satisfies it; ReconcileAllTenants needs nothing else from it.
type TenantLister interface {
	ListTenants(ctx context.Context) ([]tenant.TenantInfo, error)
}

// Reasons a tenant is skipped by ReconcileAllTenants. They are stable wire
// values: a transport carries them verbatim.
const (
	// SkipReasonSealed marks a dedicated-isolation tenant whose barrier is
	// sealed on this node, so its role store cannot be opened. Unsealing
	// the barrier and running the pass again converges it.
	SkipReasonSealed = "sealed"

	// SkipReasonRefused marks a tenant this node refuses to serve (its
	// volume pool or catalog assignment does not resolve here), whose data
	// is therefore never opened. See tenant.TenantInfo.Refusal.
	SkipReasonRefused = "refused"
)

// SkippedTenant names a tenant ReconcileAllTenants did not open, and why.
type SkippedTenant struct {
	// TenantID is the skipped tenant.
	TenantID string `json:"tenantID"`

	// Reason is SkipReasonSealed or SkipReasonRefused.
	Reason string `json:"reason"`

	// Detail is the human-readable cause, when there is one.
	Detail string `json:"detail,omitempty"`
}

// FailedTenant names a tenant whose reconciliation failed, and the error.
type FailedTenant struct {
	// TenantID is the tenant that failed.
	TenantID string `json:"tenantID"`

	// Error is the failure's message.
	Error string `json:"error"`
}

// AllTenantsReconcileReport is the outcome of ReconcileAllTenants: one
// per-tenant report for every tenant converged, changed or not, and the
// tenants it skipped or failed. One tenant's failure never stops the pass,
// so a caller decides on Failed, not on the returned error alone.
type AllTenantsReconcileReport struct {
	// Tenants holds one report per tenant reconciled, the system tenant
	// first and the rest ordered by id. A report with no roles is a tenant
	// that already agreed with the build.
	Tenants []ReconcileReport `json:"tenants,omitempty"`

	// Skipped lists the tenants whose role store was not opened.
	Skipped []SkippedTenant `json:"skipped,omitempty"`

	// Failed lists the tenants whose reconciliation returned an error.
	Failed []FailedTenant `json:"failed,omitempty"`
}

// Changed reports whether the pass wrote anything in any tenant.
func (r *AllTenantsReconcileReport) Changed() bool {
	if r == nil {
		return false
	}
	for i := range r.Tenants {
		if r.Tenants[i].Changed() {
			return true
		}
	}
	return false
}

// ReconcileAllTenants converges the stored built-in roles of EVERY tenant the
// node knows onto BuiltInRoles, through roles' per-tenant
// ReconcileBuiltInRoles: the same semantics, tenant by tenant, so custom roles
// and every non-built-in grant are left untouched.
//
// It exists because a permission rename strands every tenant's built-ins on
// the retired strings, and the per-tenant verb cannot repair a tenant other
// than the caller's own: the node's service principal is refused outside the
// system tenant, and nobody else holds roles:create in every tenant at boot.
//
// The caller must be bound to the system tenant (anything else is a
// *tenant.PlatformOperatorRequiredError); the transports also gate it on the
// cross-scope permission.RolesReconcileAll. Each tenant is then reconciled
// with the context re-bound to it, which is what lets the per-tenant verb's
// own tenant check admit the call; the authenticated identity, and so the
// audit record's actor, stays the caller's.
//
// A tenant this node refuses, or whose barrier is sealed, is reported in
// Skipped; any other per-tenant error is reported in Failed and the pass
// goes on. The returned error is non-nil only when the pass could not run:
// the caller is not the platform operator, or the tenants could not be
// listed.
//
// TIMING: as for ReconcileBuiltInRoles, call it after module permission
// registration. A system tenant whose built-ins predate RolesReconcileAll
// must have its own roles reconciled first (roles:create), which grants it.
func ReconcileAllTenants(ctx context.Context, roles RoleServicer, tenants TenantLister) (*AllTenantsReconcileReport, error) {
	if roles == nil {
		return nil, &StoreError{Op: "reconcile_all", Detail: "role service must not be nil"}
	}
	if tenants == nil {
		return nil, &StoreError{Op: "reconcile_all", Detail: "tenant lister must not be nil"}
	}
	if caller := tenant.FromContext(ctx); caller != tenantid.System {
		return nil, &tenant.PlatformOperatorRequiredError{
			IdentityTenantID: caller,
			Op:               "reconcile built-in roles in every tenant",
		}
	}

	infos, err := tenants.ListTenants(ctx)
	if err != nil {
		return nil, &StoreError{Op: "reconcile_all", Detail: "list tenants", Err: err}
	}

	report := &AllTenantsReconcileReport{}
	for _, info := range reconcileOrder(infos) {
		if info.Refusal != nil {
			report.Skipped = append(report.Skipped, SkippedTenant{
				TenantID: info.ID, Reason: SkipReasonRefused, Detail: info.Refusal.Error(),
			})
			continue
		}
		tr, rerr := roles.ReconcileBuiltInRoles(tenant.WithTenantID(ctx, info.ID), info.ID)
		switch {
		case errors.Is(rerr, tenant.ErrTenantSealed):
			report.Skipped = append(report.Skipped, SkippedTenant{
				TenantID: info.ID, Reason: SkipReasonSealed, Detail: rerr.Error(),
			})
		case rerr != nil:
			report.Failed = append(report.Failed, FailedTenant{TenantID: info.ID, Error: rerr.Error()})
		case tr == nil:
			report.Tenants = append(report.Tenants, ReconcileReport{TenantID: info.ID})
		default:
			report.Tenants = append(report.Tenants, *tr)
		}
	}
	return report, nil
}

// reconcileOrder returns the tenants to reconcile: the system tenant first,
// whether or not it is listed, then every other listed tenant once, by id.
// An entry with an empty id is dropped; it names no tenant.
func reconcileOrder(infos []tenant.TenantInfo) []tenant.TenantInfo {
	out := []tenant.TenantInfo{{ID: tenantid.System}}
	seen := map[string]struct{}{tenantid.System: {}}
	rest := make([]tenant.TenantInfo, 0, len(infos))
	for _, info := range infos {
		if info.ID == "" {
			continue
		}
		if info.ID == tenantid.System {
			out[0] = info
			continue
		}
		if _, dup := seen[info.ID]; dup {
			continue
		}
		seen[info.ID] = struct{}{}
		rest = append(rest, info)
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].ID < rest[j].ID })
	return append(out, rest...)
}
