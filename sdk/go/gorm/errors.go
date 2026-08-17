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

import "strconv"

// UnsupportedTransportError is returned by Initialize when the Dialector's
// DSN names a transport GORM cannot run over.
//
// The REST and gRPC SQL API accepts a single statement per request with no
// field for bind parameters, no session affinity for transactions, and no
// typed result metadata — sdk/go/driver already refuses parameters and
// transactions on those schemes. GORM issues parameterized statements and
// relies on transactions for SavePoint/RollbackTo, so the same connection is
// refused here rather than opened and left to fail on the first query.
type UnsupportedTransportError struct {
	// Scheme is the DSN scheme that was refused ("rest" or "grpc").
	Scheme string
}

// Error returns a human-readable description naming the refused scheme and
// the alternatives that do work.
func (e *UnsupportedTransportError) Error() string {
	return "gorm: qrdb " + strconv.Quote(e.Scheme) + " transport does not support GORM " +
		"(no bind parameters, transactions, or typed results over the network SQL API); " +
		"use an embedded:// DSN or qrdb's MySQL wire protocol instead"
}

// InvalidSavePointNameError is returned by SavePoint and RollbackTo when the
// caller-supplied name is not a bare SQL identifier.
//
// GORM generates its own savepoint names ("sp%d"), which always match, but a
// third-party plugin can call SavePoint with an attacker-influenced string.
// SAVEPOINT and ROLLBACK TO SAVEPOINT take no bind parameter for the name, so
// an unvalidated name concatenated into the statement text is a SQL
// injection surface; this error is returned instead of building that
// statement.
type InvalidSavePointNameError struct {
	// Name is the rejected savepoint name.
	Name string
}

// Error returns a human-readable description of the rejected savepoint name.
func (e *InvalidSavePointNameError) Error() string {
	return "gorm: invalid savepoint name " + strconv.Quote(e.Name) +
		"; savepoint names must match ^[A-Za-z_][A-Za-z0-9_]*$"
}

// FieldNotFoundError is returned by Migrator.AlterColumn and
// Migrator.RenameColumn when the field name they were asked to operate on
// does not resolve against the target's parsed schema.Schema — either
// because dst carries no gorm schema at all (a bare table-name string) or
// because no field or column matches the requested name.
type FieldNotFoundError struct {
	// Field is the field or column name that could not be resolved.
	Field string
}

// Error returns a human-readable description naming the unresolved field.
func (e *FieldNotFoundError) Error() string {
	return "gorm: failed to look up field with name " + strconv.Quote(e.Field)
}
