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

package quic

import (
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestMapServiceError_TableResolutionStaleIsAborted pins the QUIC
// classification of a statement that outlived its table resolution:
// StatusAborted, retry the operation, never the generic error.
func TestMapServiceError_TableResolutionStaleIsAborted(t *testing.T) {
	for name, err := range staleResolutionErrors() {
		if got, _ := mapServiceError(err); got != wire.StatusAborted {
			t.Errorf("%s: mapServiceError = %v, want StatusAborted", name, got)
		}
	}
}

// staleResolutionErrors is every shape a stale table resolution reaches a
// transport in: bare, as the SQL engine's serialization failure (whose
// cause is reachable only through Cause()) inside a SQL execution failure,
// and wrapped by a read.
func staleResolutionErrors() map[string]error {
	generation := &service.TableGenerationStaleError{Database: "shop", Name: "orders", Expected: 0, Actual: 1}
	definition := &service.TableDefinitionStaleError{Database: "shop", Name: "orders", Reason: "index idx_sku was dropped"}
	return map[string]error{
		"generation":            generation,
		"definition":            definition,
		"sql engine generation": &service.SQLExecutionError{Err: sqle.ErrLockDeadlock.Wrap(generation, generation.Error())},
		"sql engine definition": &service.SQLExecutionError{Err: sqle.ErrLockDeadlock.Wrap(definition, definition.Error())},
		"read":                  &service.ReadError{Err: generation},
	}
}
