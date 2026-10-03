//go:build qrdb_sql && !integration

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
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sqlquery "github.com/jeremyhahn/go-qrdb/pkg/sql/query"
)

// sqlGuardOptions wires the SQL service into newFullyWiredGuardServer so the
// route-mapping guard also covers /sql/query and /v1/sql/query. Built only
// under qrdb_sql: pkg/sql/query -- and the go-mysql-server/vitess
// dependency it pulls in -- is not part of a build without the tag. See
// route_mapping_guard_sql_stub_test.go for the !qrdb_sql counterpart.
func sqlGuardOptions(kvSvc service.KVServicer, dbSvc service.DatabaseServicer) []ServerOption {
	return []ServerOption{WithSQLService(sqlquery.NewService(kvSvc, dbSvc))}
}
