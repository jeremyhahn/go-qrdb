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

package service

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// otherScaleTenantID holds a database of the same name as scaleTenantID's.
const otherScaleTenantID = "scale-other-tenant"

// createSameNameForOtherTenant gives otherScaleTenantID its own database
// name, so the name alone no longer identifies scaleTenantID's database.
func createSameNameForOtherTenant(t *testing.T, svc *DatabaseService, name string, replicas int) {
	t.Helper()
	cfg := testDBConfig(name)
	cfg.Replicas = replicas
	ctx := tenant.WithTenantID(t.Context(), otherScaleTenantID)
	if _, err := svc.manager.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant(%s, %s): %v", otherScaleTenantID, name, err)
	}
}

// recordedReplicas returns the replica count the manager records for
// tenantID's database name.
func recordedReplicas(t *testing.T, svc *DatabaseService, tenantID, name string) int {
	t.Helper()
	db, err := svc.manager.GetOwned(name, tenantID)
	if err != nil {
		t.Fatalf("GetOwned(%s, %s): %v", name, tenantID, err)
	}
	return db.Info().Replicas
}

// A scale-up of a name two tenants share records the new replica count on
// the scaling tenant's database and leaves the other tenant's alone.
func TestScaleForTenant_ScaleUp_SharedNameUpdatesOnlyTheTenantsRecord(t *testing.T) {
	t.Parallel()

	svc, _, _ := newScaleDatabaseService(t, healthyNodes("n1", "n2", "n3"),
		&quota.Quotas{MaxReplicasPerGroup: 10, MaxTotalReplicas: 100}, nil)
	createScaleDB(t, svc, "db1", 2)
	createSameNameForOtherTenant(t, svc, "db1", 2)

	if _, err := svc.ScaleForTenant(scaleCtx(), "db1", 3); err != nil {
		t.Fatalf("ScaleForTenant: %v", err)
	}
	if got := recordedReplicas(t, svc, scaleTenantID, "db1"); got != 3 {
		t.Errorf("scaling tenant's recorded replicas = %d, want 3", got)
	}
	if got := recordedReplicas(t, svc, otherScaleTenantID, "db1"); got != 2 {
		t.Errorf("other tenant's recorded replicas = %d, want 2 (untouched)", got)
	}
}

// A scale-down does the same: only the scaling tenant's record changes.
func TestScaleForTenant_ScaleDown_SharedNameUpdatesOnlyTheTenantsRecord(t *testing.T) {
	t.Parallel()

	svc, _, _ := newScaleDatabaseService(t, healthyNodes("n1", "n2", "n3"),
		&quota.Quotas{MaxReplicasPerGroup: 10, MaxTotalReplicas: 100}, nil)
	createScaleDB(t, svc, "db1", 3)
	createSameNameForOtherTenant(t, svc, "db1", 3)

	if _, err := svc.ScaleForTenant(scaleCtx(), "db1", 1); err != nil {
		t.Fatalf("ScaleForTenant: %v", err)
	}
	if got := recordedReplicas(t, svc, scaleTenantID, "db1"); got != 1 {
		t.Errorf("scaling tenant's recorded replicas = %d, want 1", got)
	}
	if got := recordedReplicas(t, svc, otherScaleTenantID, "db1"); got != 3 {
		t.Errorf("other tenant's recorded replicas = %d, want 3 (untouched)", got)
	}
}
