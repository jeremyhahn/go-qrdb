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

package query

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
)

// ---------------------------------------------------------------------------
// This service is what REST, gRPC, QUIC, MCP and the network SQL driver all
// return rows through, and it pre-formats every cell as text. Formatting a
// cell without consulting its column type renders ENUM as its stored uint16
// ordinal and SET as its stored uint64 bitmask, so a caller that wrote
// 'active' read back "2". See catalog.ResolveColumnValue.
// ---------------------------------------------------------------------------

// newSymbolicService returns a service over a fresh harness with the shared
// ENUM/SET table already created.
func newSymbolicService(t *testing.T) service.SQLServicer {
	t.Helper()

	h := sqltest.NewHarness()
	svc := NewService(h.KVSvc, nil, WithRegistry(h.Registry))

	const ddl = `CREATE TABLE accounts (
		id INT PRIMARY KEY,
		status ENUM('pending','active','banned'),
		perms SET('read','write','admin')
	)`
	_, err := svc.Execute(context.Background(), "qrdb", ddl)
	require.NoError(t, err)
	return svc
}

// TestExecute_EnumRendersLabel asserts every ENUM member renders as its
// label rather than its ordinal.
func TestExecute_EnumRendersLabel(t *testing.T) {
	svc := newSymbolicService(t)
	ctx := context.Background()

	labels := []string{"pending", "active", "banned"}
	for i, label := range labels {
		_, err := svc.Execute(ctx, "qrdb",
			"INSERT INTO accounts VALUES ("+strconv.Itoa(i+1)+", '"+label+"', 'read')")
		require.NoError(t, err)
	}

	res, err := svc.Execute(ctx, "qrdb", "SELECT id, status FROM accounts ORDER BY id")
	require.NoError(t, err)
	require.Len(t, res.Rows, len(labels))

	for i, label := range labels {
		assert.Equal(t, label, res.Rows[i]["status"], "row %d rendered the ordinal, not the label", i)
	}
}

// TestExecute_SetRendersLabels covers the SET analogue, including the
// bitmask 5 ('read,admin') that renders as a plausible-looking integer.
func TestExecute_SetRendersLabels(t *testing.T) {
	svc := newSymbolicService(t)
	ctx := context.Background()

	tests := []struct {
		id   string
		set  string
		want string
	}{
		{"1", "", ""},
		{"2", "read", "read"},
		{"3", "read,admin", "read,admin"},
		{"4", "read,write,admin", "read,write,admin"},
	}

	for _, tc := range tests {
		_, err := svc.Execute(ctx, "qrdb",
			"INSERT INTO accounts VALUES ("+tc.id+", 'active', '"+tc.set+"')")
		require.NoError(t, err)

		res, err := svc.Execute(ctx, "qrdb", "SELECT perms FROM accounts WHERE id = "+tc.id)
		require.NoError(t, err)
		require.Len(t, res.Rows, 1)
		assert.Equal(t, tc.want, res.Rows[0]["perms"])
	}
}

// TestExecute_SymbolicNull asserts a NULL ENUM or SET renders as this API's
// established empty-string encoding for NULL, and never as ordinal zero's
// (non-existent) label.
func TestExecute_SymbolicNull(t *testing.T) {
	svc := newSymbolicService(t)
	ctx := context.Background()

	_, err := svc.Execute(ctx, "qrdb", "INSERT INTO accounts VALUES (1, NULL, NULL)")
	require.NoError(t, err)

	res, err := svc.Execute(ctx, "qrdb", "SELECT status, perms FROM accounts WHERE id = 1")
	require.NoError(t, err)
	require.Len(t, res.Rows, 1)
	assert.Equal(t, "", res.Rows[0]["status"])
	assert.Equal(t, "", res.Rows[0]["perms"])
}

// TestExecute_SymbolicPredicate asserts resolution on the read path left the
// engine's ordinal-based comparisons intact.
func TestExecute_SymbolicPredicate(t *testing.T) {
	svc := newSymbolicService(t)
	ctx := context.Background()

	_, err := svc.Execute(ctx, "qrdb",
		"INSERT INTO accounts VALUES (1,'active','read'), (2,'banned','write'), (3,'active','admin')")
	require.NoError(t, err)

	res, err := svc.Execute(ctx, "qrdb", "SELECT id FROM accounts WHERE status = 'active' ORDER BY id")
	require.NoError(t, err)
	require.Len(t, res.Rows, 2)
	assert.Equal(t, "1", res.Rows[0]["id"])
	assert.Equal(t, "3", res.Rows[1]["id"])
}
