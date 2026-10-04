// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

//go:build integration

// tenant_deleted_cli_test.go drives `qrdb tenant list-deleted` and `qrdb
// tenant reconcile-deleted` through the real CLI binary against every
// transport, closing the CLI-coverage-audit finding that this pair had NO
// test/integration coverage of any kind.
//
// `tenant delete` (tenant_cli_test.go) is itself the only public path to the
// retired state these two commands read: per docs/multi-tenancy/cli.md,
// delete is "permanent in both directions ... retires the id", so this file
// never needs a backdoor into the retired-tenant fixture the unit test
// (cmd/qrdb/tenant_deleted_test.go) reaches via retiredTenantService --
// create then delete is the real, public sequence that produces it.
package integration_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// reconcileDeletedTenants runs `tenant reconcile-deleted` over proto using
// the harness's system-admin identity.
func reconcileDeletedTenants(t *testing.T, node *multiProtoNode, proto multiProtocol) *transport.ReconcileDeletedTenantsResponse {
	t.Helper()
	out, err := runCLIProto(t, node, proto, "--output", "json", "tenant", "reconcile-deleted")
	if err != nil {
		t.Fatalf("[%s] tenant reconcile-deleted: %v\noutput: %s", proto.name, err, out)
	}
	var resp transport.ReconcileDeletedTenantsResponse
	decodeJSON(t, out, &resp)
	return &resp
}

// listDeletedTenants runs `tenant list-deleted` with no paging flags.
func listDeletedTenants(t *testing.T, node *multiProtoNode, proto multiProtocol) *transport.ListDeletedTenantsResponse {
	t.Helper()
	return listDeletedTenantsPaged(t, node, proto, 0, "")
}

// listDeletedTenantsPaged runs `tenant list-deleted` with explicit paging
// flags, omitting a flag whose value is the zero value.
func listDeletedTenantsPaged(t *testing.T, node *multiProtoNode, proto multiProtocol, pageSize int, pageToken string) *transport.ListDeletedTenantsResponse {
	t.Helper()
	args := []string{"--output", "json", "tenant", "list-deleted"}
	if pageSize > 0 {
		args = append(args, "--page-size", fmt.Sprintf("%d", pageSize))
	}
	if pageToken != "" {
		args = append(args, "--page-token", pageToken)
	}
	out, err := runCLIProto(t, node, proto, args...)
	if err != nil {
		t.Fatalf("[%s] tenant list-deleted: %v\noutput: %s", proto.name, err, out)
	}
	var resp transport.ListDeletedTenantsResponse
	decodeJSON(t, out, &resp)
	return &resp
}

// findDeletedTenant returns the entry for id in tenants, and whether it was
// found.
func findDeletedTenant(tenants []transport.DeletedTenant, id string) (transport.DeletedTenant, bool) {
	for _, dt := range tenants {
		if dt.ID == id {
			return dt, true
		}
	}
	return transport.DeletedTenant{}, false
}

// TestTenantDeleted_CLI_ListAndReconcile_PerProtocol drives `tenant
// list-deleted` and `tenant reconcile-deleted` through the CLI against every
// transport: delete two tenants, confirm both show up in list-deleted (with
// their name/isolation/deleted-at), confirm a retired id can never be
// recreated, exercise --page-size/--page-token pagination, and confirm
// reconcile-deleted's own Inspected count actually grew by the two ids this
// subtest retired.
func TestTenantDeleted_CLI_ListAndReconcile_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testTenantDeletedOverProtocol(t, node, proto)
		})
	}
}

func testTenantDeletedOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	// Baseline: how many retired ids reconcile-deleted inspects before this
	// subtest retires any of its own, so the delta asserted below is
	// attributable only to what this subtest does (other subtests share
	// this node and run sequentially before/after it).
	before := reconcileDeletedTenants(t, node, proto)

	id1 := fmt.Sprintf("tdel-%s-1-%d", proto.name, time.Now().UnixNano())
	id2 := fmt.Sprintf("tdel-%s-2-%d", proto.name, time.Now().UnixNano())
	createTenant(t, node, proto, id1, "Deleted Tenant One", "prefix", false)
	createTenant(t, node, proto, id2, "Deleted Tenant Two", "prefix", false)

	if out, err := runCLIProto(t, node, proto, "tenant", "delete", id1); err != nil {
		t.Fatalf("[%s] tenant delete %s: %v\noutput: %s", proto.name, id1, err, out)
	}
	if out, err := runCLIProto(t, node, proto, "tenant", "delete", id2); err != nil {
		t.Fatalf("[%s] tenant delete %s: %v\noutput: %s", proto.name, id2, err, out)
	}

	// A deleted id can never be recreated.
	if out, err := runCLIProto(t, node, proto, "tenant", "create", "--id", id1, "--name", "Reused", "--isolation-type", "prefix"); err == nil {
		t.Fatalf("[%s] tenant create with a retired id should fail, got success: %s", proto.name, out)
	}

	deleted := listDeletedTenants(t, node, proto)
	got1, ok1 := findDeletedTenant(deleted.Tenants, id1)
	got2, ok2 := findDeletedTenant(deleted.Tenants, id2)
	if !ok1 || !ok2 {
		t.Fatalf("[%s] tenant list-deleted does not contain both retired ids %s/%s: %+v", proto.name, id1, id2, deleted.Tenants)
	}
	if got1.Name != "Deleted Tenant One" || got1.IsolationType != "prefix" {
		t.Fatalf("[%s] list-deleted entry for %s = %+v, want name=%q isolation=prefix", proto.name, id1, got1, "Deleted Tenant One")
	}
	if got2.Name != "Deleted Tenant Two" || got2.IsolationType != "prefix" {
		t.Fatalf("[%s] list-deleted entry for %s = %+v, want name=%q isolation=prefix", proto.name, id2, got2, "Deleted Tenant Two")
	}
	if got1.DeletedAt.IsZero() || got2.DeletedAt.IsZero() {
		t.Fatalf("[%s] list-deleted entries should carry a non-zero deletion time, got %+v / %+v", proto.name, got1, got2)
	}

	// Pagination: --page-size 1 must return exactly one record plus a
	// non-empty next-page token (there are at least 2 retired ids by now).
	paged := listDeletedTenantsPaged(t, node, proto, 1, "")
	if len(paged.Tenants) != 1 || paged.NextPageToken == "" {
		t.Fatalf("[%s] list-deleted --page-size 1 = %+v, want exactly 1 record and a next page token", proto.name, paged)
	}

	// reconcile-deleted really walked the retired set: Inspected grew by
	// exactly the 2 ids this subtest just retired.
	after := reconcileDeletedTenants(t, node, proto)
	if after.Inspected != before.Inspected+2 {
		t.Fatalf("[%s] reconcile-deleted Inspected = %d, want %d (before=%d plus the 2 ids this subtest retired)",
			proto.name, after.Inspected, before.Inspected+2, before.Inspected)
	}
	for _, f := range after.Failed {
		t.Errorf("[%s] reconcile-deleted reported a failure for %s: %s: %s", proto.name, f.TenantID, f.Op, f.Error)
	}
}
