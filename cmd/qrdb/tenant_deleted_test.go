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

package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// retiredTenantService returns a REAL seal-disabled tenant service over an
// in-memory store, with the named tenants created and deleted through it.
func retiredTenantService(t *testing.T, ids ...string) tenant.TenantServicer {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range ids {
		if _, err := svc.CreateTenant(ctx, id, "Tenant "+id, tenant.IsolationPrefix); err != nil {
			t.Fatal(err)
		}
		if err := svc.DeleteTenant(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	return svc
}

func TestTenantListDeletedCmd_ListsRetiredIds(t *testing.T) {
	out, err := executeTenantCmd(t, retiredTenantService(t, "bravo", "alpha"), "list-deleted")
	if err != nil {
		t.Fatalf("tenant list-deleted: %v", err)
	}
	a, b := strings.Index(out, "alpha"), strings.Index(out, "bravo")
	if a < 0 || b < 0 || a > b {
		t.Fatalf("want alpha then bravo listed, got:\n%s", out)
	}
	if !strings.Contains(out, "DELETED_AT") || !strings.Contains(out, time.Now().UTC().Format("2006-01-02")) {
		t.Fatalf("want the deletion time listed, got:\n%s", out)
	}
	if strings.Contains(out, "Next page token") {
		t.Fatalf("a single page printed a next page token:\n%s", out)
	}
}

func TestTenantListDeletedCmd_PagesWithToken(t *testing.T) {
	svc := retiredTenantService(t, "alpha", "bravo", "charlie")
	out, err := executeTenantCmd(t, svc, "list-deleted", "--page-size", "2")
	if err != nil {
		t.Fatalf("tenant list-deleted: %v", err)
	}
	idx := strings.Index(out, "Next page token: ")
	if idx < 0 || strings.Contains(out, "charlie") {
		t.Fatalf("want a first page of two with a next page token, got:\n%s", out)
	}
	token := strings.TrimSpace(out[idx+len("Next page token: "):])

	out, err = executeTenantCmd(t, svc, "list-deleted", "--page-size", "2", "--page-token", token)
	if err != nil {
		t.Fatalf("tenant list-deleted (page 2): %v", err)
	}
	if !strings.Contains(out, "charlie") || strings.Contains(out, "alpha") {
		t.Fatalf("want only charlie on page 2, got:\n%s", out)
	}
}

func TestTenantListDeletedCmd_RejectsNegativePageSize(t *testing.T) {
	if _, err := executeTenantCmd(t, retiredTenantService(t), "list-deleted", "--page-size", "-1"); err == nil {
		t.Fatal("a negative page size must be refused")
	}
}

func TestTenantReconcileDeletedCmd_ReportsWhatItInspected(t *testing.T) {
	out, err := executeTenantCmd(t, retiredTenantService(t, "alpha", "bravo"), "reconcile-deleted")
	if err != nil {
		t.Fatalf("tenant reconcile-deleted: %v", err)
	}
	if !strings.Contains(out, "Inspected: 2 retired tenant ids") {
		t.Fatalf("want the inspected count, got:\n%s", out)
	}
}

func TestFormatDeletedAt(t *testing.T) {
	if got := formatDeletedAt(time.Time{}); got != "unknown" {
		t.Errorf("formatDeletedAt(zero) = %q; want unknown", got)
	}
	ts := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	if got := formatDeletedAt(ts); got != "2026-09-25T12:00:00Z" {
		t.Errorf("formatDeletedAt = %q", got)
	}
}

// partialReconcileCLISvc answers ReconcileDeletedTenants as the real service
// does when one retired id failed and another was reclaimed.
type partialReconcileCLISvc struct {
	tenant.TenantServicer
}

func (partialReconcileCLISvc) ReconcileDeletedTenants(context.Context) (*tenant.ReconcileReport, error) {
	boom := errors.New("device busy")
	return &tenant.ReconcileReport{
			Inspected: 2,
			Reclaimed: []tenant.ReclaimedPath{{TenantID: "vb", Path: "/srv/vb"}},
			Failed:    []tenant.FailedReclaim{{TenantID: "va", Op: "remove", Path: "/srv/va", Error: boom.Error()}},
		}, &tenant.ResidueReconcileErrors{Errors: []*tenant.ResidueReconcileError{
			{TenantID: "va", Op: "remove", Path: "/srv/va", Err: boom},
		}}
}

// TestTenantReconcileDeletedCmd_PartialFailurePrintsTheReportAndFails: what
// was reclaimed is printed with the failed row, and the command still exits
// with the error.
func TestTenantReconcileDeletedCmd_PartialFailurePrintsTheReportAndFails(t *testing.T) {
	out, err := executeTenantCmd(t, partialReconcileCLISvc{TenantServicer: retiredTenantService(t)}, "reconcile-deleted")
	if err == nil {
		t.Fatal("a partly failed pass exited successfully")
	}
	for _, want := range []string{"Inspected: 2", "reclaimed", "vb", "failed", "va", "remove: device busy"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
}
