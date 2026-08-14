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

package gorm

import (
	"testing"

	"gorm.io/gorm"

	_ "github.com/jeremyhahn/go-qrdb/sdk/go/driver/node"
)

// ---------------------------------------------------------------------------
// Migrator.DropTable over node://.
//
// These need a real relational catalog -- FOREIGN KEY is a relational-table
// capability -- so they use node:// rather than the embedded:// target the
// SQL-shape tests in migrator_test.go use: embedded:// has no
// service.Registry and therefore no CREATE TABLE at all (see
// sdk/go/driver/node's package doc).
//
// The defect they pin: qrdb's Migrator inherited gorm.io/gorm/migrator's
// base DropTable, which issues plain DROP TABLE statements on unpinned
// pooled connections. qrdb's engine refuses to drop a table another table
// still references, exactly as MySQL does, so dropping any set of
// FK-related tables failed -- and, because the base implementation stops at
// the first error, every table after the first failure was left behind too.
// ---------------------------------------------------------------------------

// dropParent and dropChild are a minimal parent/child pair joined by a
// foreign key, used to put the engine's drop-time referential check in the
// way of a DROP TABLE.
type dropParent struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

type dropChild struct {
	ID           uint `gorm:"primaryKey"`
	DropParentID uint
	DropParent   dropParent
}

// openNodeDB opens a *gorm.DB against a complete in-process qrdb node.
func openNodeDB(t *testing.T, database string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(New(Config{DSN: "node://memory?database=" + database}), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open(node://memory?database=%s): %v", database, err)
	}
	return db
}

// foreignKeyChecks reads the session's foreign_key_checks setting.
func foreignKeyChecks(t *testing.T, db *gorm.DB) int {
	t.Helper()
	var v int
	if err := db.Raw("SELECT @@foreign_key_checks").Row().Scan(&v); err != nil {
		t.Fatalf("SELECT @@foreign_key_checks: %v", err)
	}
	return v
}

// TestMigrator_DropTable_DropsReferencedTable covers the whole reason
// DropTable is overridden: a table another table still references must still
// be droppable, and every table in the request must be dropped rather than
// abandoned at the first refusal.
func TestMigrator_DropTable_DropsReferencedTable(t *testing.T) {
	db := openNodeDB(t, "migrator_droptable_referenced")

	if err := db.AutoMigrate(&dropParent{}, &dropChild{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	if !db.Migrator().HasTable(&dropParent{}) || !db.Migrator().HasTable(&dropChild{}) {
		t.Fatal("AutoMigrate did not create both tables")
	}

	// The parent is listed first, so the base migrator's reverse iteration
	// reaches the child first and the parent second -- which is the order
	// that succeeds. Listing the CHILD first is the order that fails without
	// foreign_key_checks disabled, since the parent is then dropped while
	// still referenced.
	if err := db.Migrator().DropTable(&dropChild{}, &dropParent{}); err != nil {
		t.Fatalf("DropTable(child, parent): %v", err)
	}

	if db.Migrator().HasTable(&dropParent{}) {
		t.Error("the referenced parent table was not dropped")
	}
	if db.Migrator().HasTable(&dropChild{}) {
		t.Error("the referencing child table was not dropped")
	}
}

// TestMigrator_DropTable_DropsOrphanedReferencedTable covers the case that
// no drop ORDER can rescue: the referencing table is not part of the request
// at all, so the referenced table can only be dropped with foreign key
// checking disabled.
func TestMigrator_DropTable_DropsOrphanedReferencedTable(t *testing.T) {
	db := openNodeDB(t, "migrator_droptable_orphaned")

	if err := db.AutoMigrate(&dropParent{}, &dropChild{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	if err := db.Migrator().DropTable(&dropParent{}); err != nil {
		t.Fatalf("DropTable(parent) while still referenced: %v", err)
	}
	if db.Migrator().HasTable(&dropParent{}) {
		t.Error("the referenced parent table was not dropped")
	}
}

// TestMigrator_DropTable_RestoresForeignKeyChecks covers the invariant that
// makes disabling the check safe: it is restored afterwards, so a caller's
// later statements are still checked.
func TestMigrator_DropTable_RestoresForeignKeyChecks(t *testing.T) {
	db := openNodeDB(t, "migrator_droptable_restores")

	if err := db.AutoMigrate(&dropParent{}, &dropChild{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	if got := foreignKeyChecks(t, db); got != 1 {
		t.Fatalf("foreign_key_checks = %d before DropTable, want 1", got)
	}

	if err := db.Migrator().DropTable(&dropParent{}); err != nil {
		t.Fatalf("DropTable: %v", err)
	}

	if got := foreignKeyChecks(t, db); got != 1 {
		t.Errorf("foreign_key_checks = %d after DropTable, want 1 (it must be restored)", got)
	}
}

// TestMigrator_DropTable_AcceptsBareTableNames covers DropTable's other
// caller shape: a literal table name string rather than a model, which is
// how a join table with no Go type of its own is dropped.
func TestMigrator_DropTable_AcceptsBareTableNames(t *testing.T) {
	db := openNodeDB(t, "migrator_droptable_barename")

	if err := db.Exec("CREATE TABLE loose_table (id BIGINT PRIMARY KEY)").Error; err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if !db.Migrator().HasTable("loose_table") {
		t.Fatal("CREATE TABLE did not create loose_table")
	}

	if err := db.Migrator().DropTable("loose_table"); err != nil {
		t.Fatalf("DropTable(%q): %v", "loose_table", err)
	}
	if db.Migrator().HasTable("loose_table") {
		t.Error("loose_table was not dropped")
	}
}

// TestMigrator_DropTable_NoValuesIsANoOp covers the empty request: it must
// neither fail nor leave foreign key checking off.
func TestMigrator_DropTable_NoValuesIsANoOp(t *testing.T) {
	db := openNodeDB(t, "migrator_droptable_empty")

	if err := db.Migrator().DropTable(); err != nil {
		t.Fatalf("DropTable() with no values: %v", err)
	}
	if got := foreignKeyChecks(t, db); got != 1 {
		t.Errorf("foreign_key_checks = %d, want 1", got)
	}
}
