// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package engine provides a shared SQL execution engine backed by
// go-mysql-server. It is used by both the MySQL and PostgreSQL wire
// protocol implementations to parse, analyze, and execute SQL queries
// against the QRDB storage layer.
package engine

import (
	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/sql"
)

// Config holds configuration for the SQL engine.
type Config struct {
	// Provider is the QRDB database provider that supplies table and
	// schema metadata to go-mysql-server.
	Provider sql.DatabaseProvider
}

// New creates a new go-mysql-server Engine backed by the given database
// provider. The provider supplies catalog, schema, and table information
// so that go-mysql-server can parse, analyze, and execute SQL queries.
func New(cfg Config) *sqle.Engine {
	return sqle.NewDefault(cfg.Provider)
}
