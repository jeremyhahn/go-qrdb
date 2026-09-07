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

// Package driver implements a database/sql driver for qrdb. It supports
// both embedded (in-process) and network (REST/gRPC) modes. Users opt-in
// via a blank import:
//
//	import _ "github.com/jeremyhahn/go-qrdb/sdk/go/driver"
//
// Then open a connection with sql.Open:
//
//	db, err := sql.Open("qrdb", "embedded://memory?database=mydb")
//	db, err := sql.Open("qrdb", "rest://localhost:63001/mydb")
//
// The embedded:// scheme is a KV-only in-process database: it builds a
// storage backend directly and serves the KV/entity/collection tables over
// it. It has no service.Registry, and therefore no relational-table catalog,
// so CREATE TABLE fails with *pkg/sql/provider.RelationalCatalogUnavailableError
// unless a registry is supplied programmatically via WithRegistry.
//
// For the full in-process stack -- Raft, the service registry, relational
// tables, and interactive transactions -- use the node:// scheme, which is
// registered by the sdk/go/driver/node subpackage:
//
//	import _ "github.com/jeremyhahn/go-qrdb/sdk/go/driver/node"
//
//	db, err := sql.Open("qrdb", "node://memory?database=mydb")
//
// Additional schemes register themselves the same way; see RegisterScheme.
package driver

import (
	"context"
	"database/sql"
	"database/sql/driver"
)

// driverName is the name used for sql.Register.
const driverName = "qrdb"

func init() {
	sql.Register(driverName, &Driver{})
}

// Compile-time interface assertions.
var (
	_ driver.Driver        = (*Driver)(nil)
	_ driver.DriverContext = (*Driver)(nil)
)

// Driver implements the database/sql driver.Driver and driver.DriverContext
// interfaces for qrdb.
type Driver struct{}

// Open parses the DSN and returns a new connection. For production use,
// prefer OpenConnector which allows connection pooling via sql.OpenDB.
func (d *Driver) Open(dsn string) (driver.Conn, error) {
	connector, err := d.OpenConnector(dsn)
	if err != nil {
		return nil, err
	}
	// context.Background(), not nil: database/sql's own Open path calls
	// Connect with a real context, and a nil one is undefined behaviour for
	// any connector that consults it.
	return connector.Connect(context.Background())
}

// OpenConnector parses the DSN and returns a Connector that can be used
// with sql.OpenDB for connection pooling. The connector is built by the
// factory the DSN's scheme was registered with (see RegisterScheme).
func (d *Driver) OpenConnector(dsn string) (driver.Connector, error) {
	cfg, err := ParseDSN(dsn)
	if err != nil {
		return nil, err
	}

	// ParseDSN already rejected an unregistered scheme, so a miss here can
	// only mean a scheme was unregistered between the two lookups -- which
	// nothing can do today. It is still reported rather than dereferenced.
	registered, ok := lookupScheme(cfg.Scheme)
	if !ok {
		return nil, &UnsupportedSchemeError{Scheme: cfg.Scheme}
	}

	return registered.Connect(cfg)
}
