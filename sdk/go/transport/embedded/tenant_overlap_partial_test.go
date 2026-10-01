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
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// partialReconcileSvc answers ReconcileDeletedTenants as the real service does
// when one retired id failed and another was reclaimed: the report, whose
// Failed list copies the aggregate error returned with it (pinned in
// pkg/tenant). CreateTenant answers with createErr when it is set.
type partialReconcileSvc struct {
	tenant.TenantServicer
	createErr error
}

func (p partialReconcileSvc) ReconcileDeletedTenants(context.Context) (*tenant.ReconcileReport, error) {
	boom := errors.New("device busy")
	// Bound to names, not returned as two literals: go1.27's gofmt and
	// golangci-lint's gofmt indent a multi-value return of composite
	// literals differently, so no layout of that form satisfies both.
	report := &tenant.ReconcileReport{
		Inspected: 2,
		Reclaimed: []tenant.ReclaimedPath{{TenantID: "vb", Path: "/srv/vb"}},
		Failed:    []tenant.FailedReclaim{{TenantID: "va", Op: "remove", Path: "/srv/va", Error: boom.Error()}},
	}
	failures := &tenant.ResidueReconcileFailuresError{Errors: []*tenant.ResidueReconcileError{
		{TenantID: "va", Op: "remove", Path: "/srv/va", Err: boom},
	}}
	return report, failures
}

func (p partialReconcileSvc) CreateTenant(ctx context.Context, id, name string, iso tenant.IsolationType, opts ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	if p.createErr != nil {
		return nil, p.createErr
	}
	return p.TenantServicer.CreateTenant(ctx, id, name, iso, opts...)
}

// retiredOverlap is the refusal CreateTenant returns for a DataRoot a retired
// tenant's unreclaimed record still names.
func retiredOverlap() *tenant.DataRootOverlapError {
	return &tenant.DataRootOverlapError{
		DataRoot: "/srv/disk", Conflict: "/srv/disk", Kind: "retired tenant vgone",
		Remedy: "it is retired-tenant residue on this node; run `qrdb tenant reconcile-deleted` on this node to reclaim it",
	}
}

// assertPartial checks a client saw both halves of a partly failed pass.
func assertPartial(t *testing.T, resp *transport.ReconcileDeletedTenantsResponse, err error) *transport.ReconcileDeletedTenantsError {
	t.Helper()
	if resp == nil || resp.Inspected != 2 || len(resp.Reclaimed) != 1 || resp.Reclaimed[0].TenantID != "vb" {
		t.Fatalf("resp = %+v; want vb reclaimed out of 2", resp)
	}
	if len(resp.Failed) != 1 || resp.Failed[0].TenantID != "va" || resp.Failed[0].Op != "remove" ||
		resp.Failed[0].Path != "/srv/va" || resp.Failed[0].Error != "device busy" {
		t.Fatalf("failed = %+v; want va's removal failure", resp.Failed)
	}
	var re *transport.ReconcileDeletedTenantsError
	if !errors.As(err, &re) || len(re.Failed) != 1 || re.Failed[0].TenantID != "va" {
		t.Fatalf("err = %v; want *transport.ReconcileDeletedTenantsError naming va", err)
	}
	return re
}

// TestEmbedded_CreateTenant_DataRootOverlapIsTyped: in-process, the caller
// gets the service's own typed refusal, Remedy included.
func TestEmbedded_CreateTenant_DataRootOverlapIsTyped(t *testing.T) {
	tr := newExtTransport(t, WithTenantService(partialReconcileSvc{TenantServicer: retiredEmbeddedTenants(t), createErr: retiredOverlap()}))
	_, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{ID: "vnew", Name: "New", IsolationType: "volume"})
	var oe *tenant.DataRootOverlapError
	if !errors.As(err, &oe) || oe.Kind != "retired tenant vgone" || !strings.Contains(oe.Remedy, "qrdb tenant reconcile-deleted") {
		t.Fatalf("CreateTenant = %v; want the typed *tenant.DataRootOverlapError with its remedy", err)
	}
}

// TestEmbedded_ReconcileDeletedTenants_PartialFailureCarriesBoth: the
// response and the error both come back, and in-process the error still
// reaches the service's own *tenant.ResidueReconcileFailuresError.
func TestEmbedded_ReconcileDeletedTenants_PartialFailureCarriesBoth(t *testing.T) {
	tr := newExtTransport(t, WithTenantService(partialReconcileSvc{TenantServicer: retiredEmbeddedTenants(t)}))
	resp, err := tr.ReconcileDeletedTenants(context.Background())
	assertPartial(t, resp, err)
	var agg *tenant.ResidueReconcileFailuresError
	if !errors.As(err, &agg) || len(agg.Errors) != 1 {
		t.Fatalf("err = %v; want the service's *tenant.ResidueReconcileFailuresError reachable", err)
	}
}

// TestEmbedded_ReconcileDeletedTenants_GuardFailureIsOnlyAnError: a pass that
// could not read its guards returns no response.
func TestEmbedded_ReconcileDeletedTenants_GuardFailureIsOnlyAnError(t *testing.T) {
	guard := &tenant.ResidueReconcileError{Op: "list-tenants", Err: errors.New("corrupt")}
	tr := newExtTransport(t, WithTenantService(guardFailSvc{TenantServicer: retiredEmbeddedTenants(t), err: guard}))
	resp, err := tr.ReconcileDeletedTenants(context.Background())
	if resp != nil || !errors.Is(err, guard) {
		t.Fatalf("resp, err = %+v, %v; want only the guard error", resp, err)
	}
}

type guardFailSvc struct {
	tenant.TenantServicer
	err error
}

func (g guardFailSvc) ReconcileDeletedTenants(context.Context) (*tenant.ReconcileReport, error) {
	return nil, g.err
}
