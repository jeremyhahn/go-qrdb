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

package database

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestManager_RecordedIncarnation_FollowsTheCatalog checks the incarnation is
// the catalog's: the live one while the record exists, none once a drop
// removed it, and the successor's once the name is created again.
func TestManager_RecordedIncarnation_FollowsTheCatalog(t *testing.T) {
	mgr := newCatalogManager(nil, t.TempDir())
	// A database gets an incarnation of its own only where a reclaimer marks
	// it; without one every incarnation is the empty one.
	mgr.SetKeyspaceReclaimer(&spyReclaimer{})
	ctx := tenant.WithTenantID(context.Background(), "acme")

	first, err := mgr.CreateForTenant(ctx, DatabaseConfig{Name: "billing", Backend: BackendMemory})
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	got, ok, err := mgr.RecordedIncarnation(ctx, "acme", "billing")
	if err != nil || !ok || got != first.Incarnation() {
		t.Fatalf("RecordedIncarnation() = (%q, %v, %v), want (%q, true, nil)", got, ok, err, first.Incarnation())
	}

	if err := mgr.DropForTenant(ctx, "billing"); err != nil {
		t.Fatalf("DropForTenant: %v", err)
	}
	if got, ok, err := mgr.RecordedIncarnation(ctx, "acme", "billing"); err != nil || ok || got != "" {
		t.Fatalf("RecordedIncarnation() after the drop = (%q, %v, %v), want (\"\", false, nil)", got, ok, err)
	}

	second, err := mgr.CreateForTenant(ctx, DatabaseConfig{Name: "billing", Backend: BackendMemory})
	if err != nil {
		t.Fatalf("CreateForTenant again: %v", err)
	}
	if second.Incarnation() == first.Incarnation() {
		t.Fatalf("the name was created again under the same incarnation %q", first.Incarnation())
	}
	if got, ok, err := mgr.RecordedIncarnation(ctx, "acme", "billing"); err != nil || !ok || got != second.Incarnation() {
		t.Fatalf("RecordedIncarnation() after the create = (%q, %v, %v), want (%q, true, nil)", got, ok, err, second.Incarnation())
	}
}

// TestManager_RecordedIncarnation_RequiresACatalog checks a Manager with no
// catalog reports that, never "no record": a caller fencing by incarnation
// reads "no record" as the database being gone.
func TestManager_RecordedIncarnation_RequiresACatalog(t *testing.T) {
	mgr := NewManager(nil, t.TempDir())
	_, ok, err := mgr.RecordedIncarnation(context.Background(), "acme", "billing")
	var required *CatalogRequiredError
	if !errors.As(err, &required) || ok {
		t.Fatalf("RecordedIncarnation() with no catalog = (ok %v, %v), want *CatalogRequiredError", ok, err)
	}
}
