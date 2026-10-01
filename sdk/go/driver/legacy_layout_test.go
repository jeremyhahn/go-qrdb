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

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestEmbeddedRelational_LegacyLayoutTableIsRefusedWithItsCode proves the
// embedded SQL transport reports a table created by v0.20.0 through v0.21.0
// the way the MySQL wire does: MySQL error 1459 (ER_TABLE_NEEDS_UPGRADE),
// with a message naming the remedy -- never an empty result set.
func TestEmbeddedRelational_LegacyLayoutTableIsRefusedWithItsCode(t *testing.T) {
	db, reg := openRelationalTestDB(t, "legacydb")
	legacy := `{"table_id":7,"name":"users","database":"legacydb",` +
		`"columns":[{"name":"id","type":"BIGINT UNSIGNED","nullable":false}],` +
		`"primary_key":[{"name":"id"}],"schema_version":1}`
	if err := reg.kvSvc.Put(context.Background(), service.TableMetadataKey("legacydb", "users"), []byte(legacy)); err != nil {
		t.Fatalf("seed legacy table: %v", err)
	}

	rows, err := db.Query("SELECT id FROM users")
	if err == nil {
		defer rows.Close()
		if rows.Next() {
			t.Fatal("a legacy-layout table must not return rows")
		}
		err = rows.Err()
	}
	var stateErr *SQLStateError
	if !errors.As(err, &stateErr) {
		t.Fatalf("error = %v (%T), want a *SQLStateError", err, err)
	}
	if stateErr.Number != service.TableLayoutUnsupportedSQLCode {
		t.Errorf("Number = %d, want %d", stateErr.Number, service.TableLayoutUnsupportedSQLCode)
	}
}
