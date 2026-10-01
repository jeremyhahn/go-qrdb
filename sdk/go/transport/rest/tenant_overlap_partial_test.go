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

package rest

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	restserver "github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
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

// newTenantRoundTrip starts the REAL pkg/api/rest.Server over svc on a
// loopback port (see newRoundTripServer for why a real listener) and returns
// the REAL SDK client connected to it.
func newTenantRoundTrip(t *testing.T, svc tenant.TenantServicer) *Transport {
	t.Helper()
	serverTLS, roots := generateRoundTripTLSConfig(t)
	srv := restserver.NewServer(nil, roundTripAdminStub{}, "127.0.0.1:0",
		restserver.WithTenantService(svc),
		restserver.WithTLS(serverTLS),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	return newRoundTripClient(t, "https://"+srv.Addr(), roots)
}

func realKVTenants(t *testing.T) tenant.TenantServicer {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// TestRESTRoundTrip_CreateTenant_DataRootOverlapIs409: the real server
// answers the refusal 409, and the client sees that status with the remedy.
func TestRESTRoundTrip_CreateTenant_DataRootOverlapIs409(t *testing.T) {
	tr := newTenantRoundTrip(t, partialReconcileSvc{TenantServicer: realKVTenants(t), createErr: retiredOverlap()})
	_, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{ID: "vnew", Name: "New"})
	var re *transport.RequestError
	if !errors.As(err, &re) || re.StatusCode != http.StatusConflict || !strings.Contains(err.Error(), "qrdb tenant reconcile-deleted") {
		t.Fatalf("CreateTenant = %v; want 409 carrying the remedy", err)
	}
}

// TestRESTRoundTrip_ReconcileDeletedTenants_PartialFailureCarriesBoth: the
// real server answers 200 with the report and its failures, and the client
// returns both.
func TestRESTRoundTrip_ReconcileDeletedTenants_PartialFailureCarriesBoth(t *testing.T) {
	tr := newTenantRoundTrip(t, partialReconcileSvc{TenantServicer: realKVTenants(t)})
	resp, err := tr.ReconcileDeletedTenants(context.Background())
	assertPartial(t, resp, err)
}
