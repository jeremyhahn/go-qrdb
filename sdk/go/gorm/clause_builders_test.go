//go:build qrdb_sql

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
	"sync"
	"testing"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// newTestStatement returns a *gorm.Statement whose Dialector is dialector,
// ready to have a ClauseBuilder invoked on it and its emitted SQL inspected
// via stmt.SQL.String(). No connection is opened; clause builders only ever
// write to the statement's in-memory buffer.
func newTestStatement(dialector Dialector) *gorm.Statement {
	db := &gorm.DB{Config: &gorm.Config{Dialector: dialector}}
	return &gorm.Statement{DB: db, Clauses: map[string]clause.Clause{}}
}

// conflictSchema returns a parsed *schema.Schema for a model with a primary
// key, for tests of buildOnConflict's empty-DoUpdates self-assignment.
func conflictSchema(t *testing.T) *schema.Schema {
	t.Helper()
	type model struct {
		ID   uint `gorm:"primaryKey"`
		Name string
	}
	s, err := schema.Parse(&model{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("schema.Parse: %v", err)
	}
	return s
}

func TestClauseBuilders_Keys(t *testing.T) {
	t.Run("FOR present when DontSupportForShareClause is set", func(t *testing.T) {
		dialector := New(Config{DontSupportForShareClause: true}).(Dialector)
		builders := dialector.ClauseBuilders()
		for _, key := range []string{ClauseOnConflict, ClauseValues, ClauseFor} {
			if _, ok := builders[key]; !ok {
				t.Errorf("ClauseBuilders() missing key %q", key)
			}
		}
	})

	t.Run("FOR absent when DontSupportForShareClause is unset", func(t *testing.T) {
		dialector := New(Config{DontSupportForShareClause: false}).(Dialector)
		builders := dialector.ClauseBuilders()
		if _, ok := builders[ClauseFor]; ok {
			t.Error("ClauseBuilders() should not include FOR when DontSupportForShareClause is false")
		}
		for _, key := range []string{ClauseOnConflict, ClauseValues} {
			if _, ok := builders[key]; !ok {
				t.Errorf("ClauseBuilders() missing key %q", key)
			}
		}
	})
}

func TestBuildOnConflict_ExplicitAssignment(t *testing.T) {
	stmt := newTestStatement(New(Config{}).(Dialector))
	onConflict := clause.OnConflict{
		DoUpdates: clause.Set{{Column: clause.Column{Name: "name"}, Value: "bob"}},
	}

	buildOnConflict(clause.Clause{Expression: onConflict}, stmt)

	const want = "ON DUPLICATE KEY UPDATE `name`=?"
	if got := stmt.SQL.String(); got != want {
		t.Errorf("SQL = %q, want %q", got, want)
	}
}

func TestBuildOnConflict_ExcludedColumnRewrite(t *testing.T) {
	stmt := newTestStatement(New(Config{}).(Dialector))
	onConflict := clause.OnConflict{
		DoUpdates: clause.Set{{
			Column: clause.Column{Name: "name"},
			Value:  clause.Column{Name: "name", Table: "excluded"},
		}},
	}

	buildOnConflict(clause.Clause{Expression: onConflict}, stmt)

	const want = "ON DUPLICATE KEY UPDATE `name`=VALUES(`name`)"
	if got := stmt.SQL.String(); got != want {
		t.Errorf("SQL = %q, want %q", got, want)
	}
}

func TestBuildOnConflict_MultipleAssignmentsCommaSeparated(t *testing.T) {
	stmt := newTestStatement(New(Config{}).(Dialector))
	onConflict := clause.OnConflict{
		DoUpdates: clause.Set{
			{Column: clause.Column{Name: "name"}, Value: clause.Column{Name: "name", Table: "excluded"}},
			{Column: clause.Column{Name: "age"}, Value: 42},
		},
	}

	buildOnConflict(clause.Clause{Expression: onConflict}, stmt)

	const want = "ON DUPLICATE KEY UPDATE `name`=VALUES(`name`),`age`=?"
	if got := stmt.SQL.String(); got != want {
		t.Errorf("SQL = %q, want %q", got, want)
	}
}

func TestBuildOnConflict_EmptyDoUpdatesSynthesizesPrimaryKeySelfAssignment(t *testing.T) {
	stmt := newTestStatement(New(Config{}).(Dialector))
	stmt.Schema = conflictSchema(t)

	buildOnConflict(clause.Clause{Expression: clause.OnConflict{}}, stmt)

	const want = "ON DUPLICATE KEY UPDATE `id`=`id`"
	if got := stmt.SQL.String(); got != want {
		t.Errorf("SQL = %q, want %q", got, want)
	}
}

func TestBuildOnConflict_EmptyDoUpdatesFallsBackToFirstDBNameWithoutPrimaryKey(t *testing.T) {
	stmt := newTestStatement(New(Config{}).(Dialector))

	type model struct {
		Name string
		Age  int
	}
	s, err := schema.Parse(&model{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("schema.Parse: %v", err)
	}
	stmt.Schema = s

	buildOnConflict(clause.Clause{Expression: clause.OnConflict{}}, stmt)

	want := "ON DUPLICATE KEY UPDATE `" + s.DBNames[0] + "`=`" + s.DBNames[0] + "`"
	if got := stmt.SQL.String(); got != want {
		t.Errorf("SQL = %q, want %q", got, want)
	}
}

func TestBuildOnConflict_EmptyDoUpdatesNoSchemaEmitsBareKeyword(t *testing.T) {
	stmt := newTestStatement(New(Config{}).(Dialector))

	buildOnConflict(clause.Clause{Expression: clause.OnConflict{}}, stmt)

	const want = "ON DUPLICATE KEY UPDATE "
	if got := stmt.SQL.String(); got != want {
		t.Errorf("SQL = %q, want %q", got, want)
	}
}

func TestBuildOnConflict_NonOnConflictExpressionFallsThrough(t *testing.T) {
	stmt := newTestStatement(New(Config{}).(Dialector))

	buildOnConflict(clause.Clause{Expression: clause.Locking{Strength: "UPDATE"}}, stmt)

	const want = "UPDATE"
	if got := stmt.SQL.String(); got != want {
		t.Errorf("SQL = %q, want %q", got, want)
	}
}

func TestBuildValues_EmptyColumnsEmitsLiteralParens(t *testing.T) {
	stmt := newTestStatement(New(Config{}).(Dialector))

	buildValues(clause.Clause{Expression: clause.Values{}}, stmt)

	const want = "VALUES()"
	if got := stmt.SQL.String(); got != want {
		t.Errorf("SQL = %q, want %q", got, want)
	}
}

func TestBuildValues_NonEmptyColumnsDelegatesToDefaultBuild(t *testing.T) {
	stmt := newTestStatement(New(Config{}).(Dialector))
	values := clause.Values{
		Columns: []clause.Column{{Name: "name"}},
		Values:  [][]interface{}{{"bob"}},
	}

	buildValues(clause.Clause{Expression: values}, stmt)

	const want = "(`name`) VALUES (?)"
	if got := stmt.SQL.String(); got != want {
		t.Errorf("SQL = %q, want %q", got, want)
	}
}

func TestBuildFor_ShareStrengthRewritesToLockInShareMode(t *testing.T) {
	stmt := newTestStatement(New(Config{}).(Dialector))

	buildFor(clause.Clause{Expression: clause.Locking{Strength: "SHARE"}}, stmt)

	const want = "LOCK IN SHARE MODE"
	if got := stmt.SQL.String(); got != want {
		t.Errorf("SQL = %q, want %q", got, want)
	}
}

func TestBuildFor_ShareStrengthCaseInsensitive(t *testing.T) {
	stmt := newTestStatement(New(Config{}).(Dialector))

	buildFor(clause.Clause{Expression: clause.Locking{Strength: "share"}}, stmt)

	const want = "LOCK IN SHARE MODE"
	if got := stmt.SQL.String(); got != want {
		t.Errorf("SQL = %q, want %q", got, want)
	}
}

func TestBuildFor_UpdateStrengthFallsThrough(t *testing.T) {
	stmt := newTestStatement(New(Config{}).(Dialector))

	buildFor(clause.Clause{Expression: clause.Locking{Strength: "UPDATE"}}, stmt)

	const want = "UPDATE"
	if got := stmt.SQL.String(); got != want {
		t.Errorf("SQL = %q, want %q", got, want)
	}
}
