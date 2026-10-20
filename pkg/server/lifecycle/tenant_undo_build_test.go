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

package lifecycle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// On a standalone node the composed tenant service undoes a create so that
// the same id can be created again: the gate is active and the roster is this
// node, which acknowledged its cleanup at the undo.
func TestBuildServices_UndoneTenantIDIsReusableOnAStandaloneNode(t *testing.T) {
	host := newLocalHost()
	t.Cleanup(func() { _ = host.Close() })
	node, _ := buildRoutedIAMNodeOn(t, host)

	ctx := context.Background()
	const id = "undone-tenant"
	if _, err := node.Tenant.CreateTenant(ctx, id, "Undone Tenant", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	undoer, ok := node.Tenant.(tenant.CreateUndoer)
	if !ok {
		t.Fatalf("tenant service %T does not undo a create", node.Tenant)
	}
	if err := undoer.UndoCreateTenant(ctx, id); err != nil {
		t.Fatalf("UndoCreateTenant: %v", err)
	}
	if _, err := node.Tenant.CreateTenant(ctx, id, "Undone Tenant", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant after the undo = %v, want the id reused", err)
	}
}

// A deletion still retires the id for good with the feature active.
func TestBuildServices_DeletedTenantIDStaysRetiredOnAStandaloneNode(t *testing.T) {
	host := newLocalHost()
	t.Cleanup(func() { _ = host.Close() })
	node, _ := buildRoutedIAMNodeOn(t, host)

	ctx := context.Background()
	const id = "deleted-tenant"
	if _, err := node.Tenant.CreateTenant(ctx, id, "Deleted Tenant", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := node.Tenant.DeleteTenant(ctx, id); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	var deleted *tenant.TenantDeletedError
	if _, err := node.Tenant.CreateTenant(ctx, id, "Deleted Tenant", tenant.IsolationPrefix); !errors.As(err, &deleted) {
		t.Fatalf("CreateTenant after a deletion = %v, want *tenant.TenantDeletedError", err)
	}
}
