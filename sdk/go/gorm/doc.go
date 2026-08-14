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

// Package gorm is a gorm.io/gorm dialector for qrdb, built on top of
// sdk/go/driver's database/sql driver (registered as "qrdb"). It plugs
// qrdb into GORM the same way gorm.io/driver/mysql plugs in a real MySQL
// server:
//
//	import (
//	    "gorm.io/gorm"
//	    qrdbgorm "github.com/jeremyhahn/go-qrdb/sdk/go/gorm"
//	)
//
//	db, err := gorm.Open(qrdbgorm.Open("embedded://memory?database=mydb"), &gorm.Config{})
//
// The import is aliased because this package's own name, like every other
// GORM dialector's, collides with gorm.io/gorm's package name; unlike
// gorm.io/driver/mysql, this one is not named after its wire dialect ("mysql"
// is already spoken for by Name — see below) so an alias is needed at the
// call site instead.
//
// # Why Name reports "mysql"
//
// [Dialector.Name] returns "mysql" by default, not "qrdb". That is
// deliberate, and it is the single most load-bearing decision in this
// package, so it is documented here rather than left for a reader to
// discover by surprise:
//
//  1. qrdb's SQL surface is go-mysql-server. MySQL is not an approximation
//     qrdb happens to resemble — it is qrdb's actual, only SQL dialect.
//     Reporting anything else would be reporting the wrong dialect, not a
//     neutral one.
//  2. GORM's own test suite branches on Dialector.Name() in thirteen files.
//     Those branches are almost all of the form "if name != mysql, assume
//     PostgreSQL-flavored defaults" — there is no generic fallback path,
//     only a MySQL path and a Postgres-shaped catch-all. Reporting "qrdb"
//     would silently route every one of those tests into the Postgres
//     branch, where they would keep passing while testing nothing: RETURNING
//     clauses that qrdb's engine rejects, quoting rules qrdb doesn't use,
//     and so on. A dialector for a MySQL-speaking engine that fails GORM's
//     own conformance suite by misreporting its name is a worse foundation
//     than one that plainly says what it is.
//  3. Third-party GORM plugins (gorm.io/plugin/dbresolver, gorm.io/gen, and
//     others) switch on Name() too, for the same reason: there is a MySQL
//     code path and there is everything else, and "everything else" is not
//     built with qrdb's actual syntax in mind.
//
// A caller who needs a different reported name — to disambiguate multiple
// dialectors in logs, for instance — can set Config.DialectName.
package gorm
