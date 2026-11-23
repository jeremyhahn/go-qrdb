package quota

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSQLiteVFSEntitlementDefaultsClosed(t *testing.T) {
	var e Entitlements
	if e.AllowsFeature(FeatureSQLiteVFS) {
		t.Error("zero Entitlements allows sqlite-vfs, want false (fail closed)")
	}
	e.AllowSQLiteVFS = true
	if !e.AllowsFeature(FeatureSQLiteVFS) {
		t.Error("AllowSQLiteVFS=true does not allow sqlite-vfs")
	}
	if e.AllowsFeature("sqlite_vfs") || e.AllowsFeature("unknown-feature") {
		t.Error("an unknown feature name is allowed, want false")
	}
	if FeatureSQLiteVFS != "sqlite-vfs" {
		t.Errorf("FeatureSQLiteVFS = %q, want %q", FeatureSQLiteVFS, "sqlite-vfs")
	}
}

func TestSQLiteVFSEntitlementJSON(t *testing.T) {
	off, err := json.Marshal(Entitlements{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(off), "allow_sqlite_vfs") {
		t.Errorf("false entitlement serialised: %s", off)
	}
	on, err := json.Marshal(Entitlements{AllowSQLiteVFS: true})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back Entitlements
	if err := json.Unmarshal(on, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !back.AllowSQLiteVFS || !strings.Contains(string(on), `"allow_sqlite_vfs":true`) {
		t.Errorf("round trip lost allow_sqlite_vfs: %s", on)
	}
}

func TestSQLiteDatabasesResource(t *testing.T) {
	if got := ResourceSQLiteDatabases.String(); got != "sqlite_databases" {
		t.Errorf("ResourceSQLiteDatabases.String() = %q, want %q", got, "sqlite_databases")
	}
	if ResourceSQLiteDatabases <= ResourceDedicatedWALGroups {
		t.Errorf("ResourceSQLiteDatabases = %d, want it appended after ResourceDedicatedWALGroups (%d)",
			ResourceSQLiteDatabases, ResourceDedicatedWALGroups)
	}
	q := &Quotas{MaxSQLiteDatabases: 7}
	limit, err := q.Limit(ResourceSQLiteDatabases)
	if err != nil || limit != 7 {
		t.Errorf("Limit(ResourceSQLiteDatabases) = %d, %v, want 7, nil", limit, err)
	}
	if err := q.SetCurrentUsage(ResourceSQLiteDatabases, 3); err != nil {
		t.Fatalf("SetCurrent: %v", err)
	}
	if q.CurrentSQLiteDatabases != 3 {
		t.Errorf("CurrentSQLiteDatabases = %d, want 3", q.CurrentSQLiteDatabases)
	}
	cur, err := q.CurrentUsage(ResourceSQLiteDatabases)
	if err != nil || cur != 3 {
		t.Errorf("Current(ResourceSQLiteDatabases) = %d, %v, want 3, nil", cur, err)
	}
}

func TestSQLiteDatabasesQuotaJSON(t *testing.T) {
	raw, err := json.Marshal(Quotas{MaxSQLiteDatabases: 4})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"max_sqlite_databases":4`) {
		t.Errorf("max_sqlite_databases missing: %s", raw)
	}
	var back Quotas
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.MaxSQLiteDatabases != 4 {
		t.Errorf("round trip MaxSQLiteDatabases = %d, want 4", back.MaxSQLiteDatabases)
	}
}
