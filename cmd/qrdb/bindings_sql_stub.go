//go:build !qrdb_sql

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

package main

import (
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// SQLQueryResult holds the result of a SQL query execution. The alias keeps
// the Wails IPC surface name stable (columns/rows/rowCount/duration).
type SQLQueryResult = transport.SQLQueryResult

// SQLBinding is a stub that returns FeatureDisabledError for Execute when
// the SQL feature is not compiled in. It preserves the full Wails binding
// surface of the real SQLBinding so callers (EmbeddedServerBinding, the
// desktop GUI bind list) compile unchanged.
type SQLBinding struct {
	logger *sdk.ConcreteLogger
	conn   *ConnectionBinding
}

// NewSQLBinding creates a new SQLBinding.
func NewSQLBinding(log *sdk.ConcreteLogger, conn *ConnectionBinding) *SQLBinding {
	return &SQLBinding{logger: log, conn: conn}
}

// Execute returns a FeatureDisabledError.
func (b *SQLBinding) Execute(_ string, _ string) (*SQLQueryResult, error) {
	return nil, &FeatureDisabledError{Feature: "sql"}
}
