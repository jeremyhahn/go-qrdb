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

package gorm

import (
	"gorm.io/driver/mysql"

	qrdbdriver "github.com/jeremyhahn/go-qrdb/sdk/go/driver"
)

// DriverName is the database/sql driver name sdk/go/driver registers itself
// under. This package imports sdk/go/driver, so a caller needs no separate
// blank import for the driver itself; the node:// scheme is registered by
// importing sdk/go/driver/node.
const DriverName = "qrdb"

// DefaultStringSize is the varchar width NewConfig sets for a string field
// with no gorm:"size:N" tag. Without it gorm.io/driver/mysql maps such a
// field to longtext, which cannot be indexed or given a default.
const DefaultStringSize = 256

// DefaultDatetimePrecision is the fractional-second precision NewConfig sets
// for DATETIME columns and for the NowFunc gorm.io/driver/mysql installs.
//
// Microseconds: the most MySQL's DATETIME offers and exactly what qrdb stores
// at that width. A coarser precision makes two writes close together round to
// the same instant, so a caller asking whether UpdatedAt changed is told no;
// gorm.io/driver/mysql's own default of 3 is that coarser precision.
const DefaultDatetimePrecision = 6

// NewConfig returns the gorm.io/driver/mysql Config for a qrdb in-process
// connection: DriverName "qrdb", DefaultStringSize and
// DefaultDatetimePrecision. The DSN and Conn fields are left for the caller.
// Use it with mysql.New when the pool is opened by other means, for example
// from a shared driver.Connector through sql.OpenDB.
func NewConfig() mysql.Config {
	precision := DefaultDatetimePrecision
	return mysql.Config{
		DriverName:               DriverName,
		DefaultStringSize:        DefaultStringSize,
		DefaultDatetimePrecision: &precision,
	}
}

// Open returns a gorm.io/driver/mysql dialector that opens dsn through qrdb's
// database/sql driver, configured as NewConfig describes.
//
// dsn is parsed before the dialector is built, so a malformed DSN or an
// unregistered scheme is reported here rather than from gorm.Open. A rest://
// or grpc:// DSN is refused with *UnsupportedTransportError; see that type
// for why. embedded:// is accepted but is KV-only, so DDL, and therefore
// AutoMigrate, fails on it: node:// is the scheme GORM wants.
//
// The returned dialector's Config is the caller's to adjust before
// gorm.Open.
func Open(dsn string) (*mysql.Dialector, error) {
	parsed, err := qrdbdriver.ParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == qrdbdriver.SchemeREST || parsed.Scheme == qrdbdriver.SchemeGRPC {
		return nil, &UnsupportedTransportError{Scheme: parsed.Scheme}
	}

	cfg := NewConfig()
	cfg.DSN = dsn
	return &mysql.Dialector{Config: &cfg}, nil
}
