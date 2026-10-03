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

package query

import (
	"errors"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestExecutionError_ShedsTheInsertWrapper proves a stale resolution raised
// by an INSERT -- which the engine wraps in sql.WrappedInsertError, opaque
// to both Unwrap and Cause() -- still classifies at the service boundary,
// exactly as the same failure raised by a SELECT does.
func TestExecutionError_ShedsTheInsertWrapper(t *testing.T) {
	stale := &service.TableGenerationStaleError{Database: "shop", Name: "orders", Expected: 0, Actual: 1}
	deadlock := sql.ErrLockDeadlock.Wrap(stale, stale.Error())

	for name, engineErr := range map[string]error{
		"select": deadlock,
		"insert": sql.NewWrappedInsertError(sql.Row{1}, deadlock),
	} {
		err := executionError("INSERT INTO orders VALUES (1)", engineErr)
		if !service.IsTableResolutionStale(err) {
			t.Errorf("%s: %v does not classify as a stale resolution", name, err)
		}
		if err.Query != "INSERT INTO orders VALUES (1)" {
			t.Errorf("%s: query = %q", name, err.Query)
		}
	}
}

// TestExecutionError_KeepsOtherErrors: anything but the insert wrapper is
// carried unchanged.
func TestExecutionError_KeepsOtherErrors(t *testing.T) {
	boom := errors.New("boom")
	if got := executionError("SELECT 1", boom); got.Err != boom { //nolint:errorlint // identity is the assertion
		t.Errorf("Err = %v, want the engine's own error", got.Err)
	}
}
