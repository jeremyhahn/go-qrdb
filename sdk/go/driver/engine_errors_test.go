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
package driver

import (
	"context"
	"errors"
	"testing"

	gmssql "github.com/dolthub/go-mysql-server/sql"
	gomysql "github.com/go-sql-driver/mysql"
)

// TestEngineError_ClassifiedIsBareMySQLError pins the contract
// gorm.io/driver/mysql's translator is written against: a failure the engine
// classified is a *mysql.MySQLError at the TOP of the error, not wrapped,
// with the number and SQLSTATE a wire client receives.
//
// wantState is go-mysql-server's CastSQLError, not the ANSI SQLSTATE a real
// MySQL server would assign for the same condition: only ErrLockDeadlock sets
// one explicitly ("40001"); every other error kind here falls through
// CastSQLError's switch with no SQLSTATE of its own, and vitess's
// mysql.NewSQLError defaults that to "HY000" (general error). A duplicate key
// or a foreign key violation is "23000" and a missing table is "42S02" on
// real MySQL, so a caller matching on SQLSTATE rather than Number for these
// will not see what a wire client would.
func TestEngineError_ClassifiedIsBareMySQLError(t *testing.T) {
	tests := []struct {
		name       string
		engineErr  error
		wantNumber uint16
		wantState  string
	}{
		{"duplicate primary key", gmssql.ErrPrimaryKeyViolation.New(), 1062, "HY000"},
		{"duplicate unique key", gmssql.ErrUniqueKeyViolation.New(), 1062, "HY000"},
		{"foreign key child", gmssql.ErrForeignKeyChildViolation.New("fk", "child", "parent", "row"), 1452, "HY000"},
		{"foreign key parent", gmssql.ErrForeignKeyParentViolation.New("fk", "parent", "child", "row"), 1451, "HY000"},
		{"lock deadlock", gmssql.ErrLockDeadlock.New("conflict"), 1213, "40001"},
		{"table not found", gmssql.ErrTableNotFound.New("missing"), 1146, "HY000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrapped := false
			err := engineError(tt.engineErr, func(err error) error { wrapped = true; return err })
			got, ok := err.(*gomysql.MySQLError)
			if !ok {
				t.Fatalf("engineError = %v (%T), want a bare *mysql.MySQLError", err, err)
			}
			if wrapped {
				t.Fatal("a classified engine error was handed to wrap")
			}
			if got.Number != tt.wantNumber {
				t.Errorf("Number = %d, want %d", got.Number, tt.wantNumber)
			}
			if state := string(got.SQLState[:]); state != tt.wantState {
				t.Errorf("SQLState = %q, want %q", state, tt.wantState)
			}
			if got.Message == "" {
				t.Error("Message is empty")
			}
		})
	}
}

// An error the engine could not classify keeps its typed chain inside wrap,
// so a caller can still match a context cancellation or one of qrdb's own
// error types.
func TestEngineError_UnclassifiedKeepsItsChain(t *testing.T) {
	err := engineError(context.Canceled, queryExecError("SELECT 1"))
	var execErr *QueryExecError
	if !errors.As(err, &execErr) || execErr.Query != "SELECT 1" {
		t.Fatalf("engineError = %v (%T), want a *QueryExecError naming the statement", err, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatal("the cancellation is no longer reachable with errors.Is")
	}
	if _, isMySQL := err.(*gomysql.MySQLError); isMySQL {
		t.Fatal("an unclassified error was given a MySQL number")
	}
}

func TestEngineError_NilIsNil(t *testing.T) {
	if err := engineError(nil, keepTyped); err != nil {
		t.Fatalf("engineError(nil) = %v, want nil", err)
	}
}
