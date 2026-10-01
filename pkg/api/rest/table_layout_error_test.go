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

// TestMapHTTPError_TableLayoutUnsupportedIsPreconditionFailed pins the REST
// classification of a table created by v0.20.0 through v0.21.0: 412, bare
// and wrapped in a SQL execution failure alike -- never a 500.
func TestMapHTTPError_TableLayoutUnsupportedIsPreconditionFailed(t *testing.T) {
	layout := &service.TableLayoutUnsupportedError{Database: "shop", Name: "users", TableID: 7}
	for name, err := range map[string]error{
		"sql engine": &service.SQLExecutionError{Err: layoutSQLError{}},
		"bare":       layout,
		"wrapped":    &service.SQLExecutionError{Err: layout},
		"read":       &service.ReadError{Err: layout},
	} {
		if got := mapHTTPError(err); got != http.StatusPreconditionFailed {
			t.Errorf("%s: mapHTTPError = %d, want %d", name, got, http.StatusPreconditionFailed)
		}
	}
}

// layoutSQLError is the SQL engine's rendering of the layout refusal: a
// numbered error carrying TableLayoutUnsupportedSQLCode, as the MySQL engine
// shapes it.
type layoutSQLError struct{}

func (layoutSQLError) Error() string { return "table shop/users: drop the table and re-create it" }
func (layoutSQLError) Number() int   { return service.TableLayoutUnsupportedSQLCode }
