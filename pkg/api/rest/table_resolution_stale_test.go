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

package rest

import (
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestMapHTTPError_TableResolutionStaleIsConflict pins the REST
// classification of a statement that outlived its table resolution: 409,
// a conflict re-issuing the request resolves -- never a 500.
func TestMapHTTPError_TableResolutionStaleIsConflict(t *testing.T) {
	for name, err := range staleResolutionErrors() {
		if got := mapHTTPError(err); got != http.StatusConflict {
			t.Errorf("%s: mapHTTPError = %d, want %d", name, got, http.StatusConflict)
		}
	}
}

// staleResolutionErrors is every shape a stale table resolution reaches a
// transport in: bare, wrapped by a read, and -- under a qrdb_sql build --
// as the SQL engine's serialization failure (whose cause is reachable only
// through Cause()) inside a SQL execution failure. sqlEngineStaleResolutionErrors
// supplies the latter two only when the SQL engine is compiled in; see
// table_resolution_stale_sql_test.go and its !qrdb_sql counterpart.
func staleResolutionErrors() map[string]error {
	generation := &service.TableGenerationStaleError{Database: "shop", Name: "orders", Expected: 0, Actual: 1}
	definition := &service.TableDefinitionStaleError{Database: "shop", Name: "orders", Reason: "index idx_sku was dropped"}
	errs := map[string]error{
		"generation": generation,
		"definition": definition,
		"read":       &service.ReadError{Err: generation},
	}
	for name, err := range sqlEngineStaleResolutionErrors(generation, definition) {
		errs[name] = err
	}
	return errs
}
