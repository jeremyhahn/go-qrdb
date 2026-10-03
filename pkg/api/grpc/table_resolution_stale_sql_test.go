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

package grpc

import (
	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// sqlEngineStaleResolutionErrors adds the SQL engine's own rendering of a
// stale table resolution -- its serialization failure wrapping the typed
// refusal, reachable only through Cause() -- to staleResolutionErrors'
// parity set. Built only under qrdb_sql, since go-mysql-server's sql
// package is otherwise not part of this binary.
func sqlEngineStaleResolutionErrors(generation, definition error) map[string]error {
	return map[string]error{
		"sql engine generation": &service.SQLExecutionError{Err: sqle.ErrLockDeadlock.Wrap(generation, generation.Error())},
		"sql engine definition": &service.SQLExecutionError{Err: sqle.ErrLockDeadlock.Wrap(definition, definition.Error())},
	}
}
