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

//go:build qrdb_pgsql

package pgsql

import (
	"context"
	"testing"

	wire "github.com/jeroenrinzema/psql-wire"
	"github.com/stretchr/testify/assert"
)

// TestSessionDatabase_PrefersClientRequestedDatabase is the regression test for
// a bug the GORM conformance suite's pgsql target surfaced: buildSQLContext
// used to set the current database to the SERVER-WIDE default on every
// statement, discarding the database the client named in its PostgreSQL
// startup message. A client connecting with dbname=gorm was served the
// server's default instead, which (for a tenant that does not own that
// default) failed every statement with "service: database not found: qrdb".
func TestSessionDatabase_PrefersClientRequestedDatabase(t *testing.T) {
	params := wire.Parameters{
		wire.ParamDatabase: "gorm",
		wire.ParamUsername: "gorm",
	}
	assert.Equal(t, "gorm", sessionDatabase(params, "qrdb"))
}

// TestSessionDatabase_FallsBackToServerDefault covers the two ways a
// connection can name no database: no parameters at all (a caller that
// bypassed psql-wire's handshake, as this package's unit tests do) and an
// explicitly empty value.
func TestSessionDatabase_FallsBackToServerDefault(t *testing.T) {
	tests := []struct {
		name   string
		params wire.Parameters
	}{
		{name: "nil parameters", params: nil},
		{name: "empty parameters", params: wire.Parameters{}},
		{name: "empty database", params: wire.Parameters{wire.ParamDatabase: ""}},
		{name: "other parameters only", params: wire.Parameters{wire.ParamUsername: "gorm"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, "qrdb", sessionDatabase(tt.params, "qrdb"))
		})
	}
}

// TestBuildSQLContext_UsesServerDefaultWithoutClientParameters pins the
// wiring: a context with no psql-wire client parameters (every direct
// HandleParse call in this package's tests) still yields the server default,
// so the fix cannot regress into an empty current database.
func TestBuildSQLContext_UsesServerDefaultWithoutClientParameters(t *testing.T) {
	h := NewParseHandler(nil, nil, "qrdb")
	assert.Equal(t, "qrdb", h.buildSQLContext(context.Background()).GetCurrentDatabase())
}
