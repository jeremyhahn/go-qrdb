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

//go:build qrdb_sql

package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// TestDatabaseCreate_SQLConcurrencyFlag and TestDatabaseUpdateSQL_Command
// exercise "database create"'s --sql-concurrency flag and the "database
// update-sql" subcommand, both qrdb_sql-only (database_sql_cmd.go,
// database.go), so this file carries the same tag. See
// database_service_helper_test.go for the shared, untagged
// newRealCLIDatabaseService helper.
func TestDatabaseCreate_SQLConcurrencyFlag(t *testing.T) {
	svc := newRealCLIDatabaseService(t)
	output, err := executeDatabaseCmd(t, svc, "create", "shop", "--backend", "memory", "--sql-concurrency", "pessimistic")
	if err != nil {
		t.Fatalf("create error: %v", err)
	}
	if !strings.Contains(output, "Concurrency: pessimistic") {
		t.Fatalf("output missing the SQL concurrency: %q", output)
	}
	info, err := svc.Get("shop")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if info.SQL.EffectiveConcurrency() != database.ConcurrencyPessimistic {
		t.Fatalf("stored SQL = %+v, want pessimistic", info.SQL)
	}
}

func TestDatabaseUpdateSQL_Command(t *testing.T) {
	svc := newRealCLIDatabaseService(t)
	if _, err := executeDatabaseCmd(t, svc, "create", "shop", "--backend", "memory"); err != nil {
		t.Fatalf("create error: %v", err)
	}

	output, err := executeDatabaseCmd(t, svc, "update-sql", "shop", "--concurrency", "pessimistic")
	if err != nil {
		t.Fatalf("update-sql error: %v", err)
	}
	if !strings.Contains(output, "Concurrency: pessimistic") {
		t.Fatalf("output missing the SQL concurrency: %q", output)
	}

	_, err = executeDatabaseCmd(t, svc, "update-sql", "shop", "--concurrency", "eager")
	var invalid *database.InvalidConcurrencyError
	if !errors.As(err, &invalid) {
		t.Fatalf("eager = %v, want *InvalidConcurrencyError", err)
	}

	if _, err := executeDatabaseCmd(t, svc, "update-sql", "shop"); err == nil {
		t.Fatal("update-sql without --concurrency must fail")
	}
}
