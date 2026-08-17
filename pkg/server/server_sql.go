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

package server

import (
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sqlquery "github.com/jeremyhahn/go-qrdb/pkg/sql/query"
)

func init() {
	newSQLServiceFn = newSQLService
}

// newSQLService constructs the shared ad-hoc SQL query service used by every
// transport (REST, gRPC, QUIC, Unix, MCP). It is read-only by construction —
// the RPC/route name ("SQLQuery" / POST /sql/query) and its permission
// mapping (permission.KVRange, a read permission — see
// pkg/auth/middleware/mapping.go) both express a read-only contract, and the
// full-DML surface is served separately by the MySQL/PostgreSQL wire
// protocol listeners (pkg/sql/mysql, pkg/sql/pgsql), which authenticate a
// dedicated per-connection session. When a service.Registry is configured
// (buildServiceRegistry), every query is additionally resolved per-(tenant,
// db) through it — exactly like the KV/MVCC/Batch data-plane paths — so
// barrier/quota/isolation are enforced and IsolationRaft/IsolationVolume
// tenants route to their dedicated backend instead of the shared system
// store; the tenant is the one authenticated on the request context by the
// transport's auth middleware, never a fixed system default.
func newSQLService(s *Server) service.SQLServicer {
	return sqlquery.NewService(
		s.kvSvc,
		s.opts.databaseSvc,
		sqlquery.WithReadOnly(),
		sqlquery.WithRegistry(s.buildServiceRegistry()),
	)
}
