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

package provider

import (
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestNewReadOnlyTable_Nil verifies newReadOnlyTable passes nil through
// unchanged instead of wrapping it.
func TestNewReadOnlyTable_Nil(t *testing.T) {
	assert.Nil(t, newReadOnlyTable(nil))
}

// TestNewReadOnlyTable_PreservesReadSurface verifies the wrapper still
// answers the base sql.Table surface (Name, String, Schema, Collation,
// Partitions, PartitionRows) by delegating to the wrapped table.
func TestNewReadOnlyTable_PreservesReadSurface(t *testing.T) {
	kv := &mockKVService{}
	inner := NewKVTable(kv, tenant.ComposeNamespace("system", ""), nil)

	wrapped := newReadOnlyTable(inner)
	require.NotNil(t, wrapped)

	assert.Equal(t, inner.Name(), wrapped.Name())
	assert.Equal(t, inner.String(), wrapped.String())
	assert.Equal(t, inner.Schema(newTestContext()), wrapped.Schema(newTestContext()))
	assert.Equal(t, inner.Collation(), wrapped.Collation())

	ctx := newTestContext()
	_, err := wrapped.Partitions(ctx)
	require.NoError(t, err)
}

// TestNewReadOnlyTable_StripsWriteCapability verifies that a table
// implementing every write-capability interface no longer exposes any of
// them once wrapped: a type assertion for InsertableTable, UpdatableTable,
// DeletableTable, or ReplaceableTable against the wrapped value must fail
// even though the underlying concrete *KVTable satisfies all four. This is
// the structural guarantee the ad-hoc SQL query engine's read-only mode
// relies on.
func TestNewReadOnlyTable_StripsWriteCapability(t *testing.T) {
	inner := NewKVTable(&mockKVService{}, tenant.ComposeNamespace("system", ""), nil)

	// Sanity check: the unwrapped concrete table DOES implement every
	// write-capability interface, so the assertions below are meaningful.
	var _ sqle.InsertableTable = inner
	var _ sqle.UpdatableTable = inner
	var _ sqle.DeletableTable = inner
	var _ sqle.ReplaceableTable = inner

	wrapped := newReadOnlyTable(inner)

	_, ok := wrapped.(sqle.InsertableTable)
	assert.False(t, ok, "readOnlyTable must not satisfy sql.InsertableTable")

	_, ok = wrapped.(sqle.UpdatableTable)
	assert.False(t, ok, "readOnlyTable must not satisfy sql.UpdatableTable")

	_, ok = wrapped.(sqle.DeletableTable)
	assert.False(t, ok, "readOnlyTable must not satisfy sql.DeletableTable")

	_, ok = wrapped.(sqle.ReplaceableTable)
	assert.False(t, ok, "readOnlyTable must not satisfy sql.ReplaceableTable")
}

// TestNewReadOnlyTable_StripsRelationalDDLCapability verifies the same
// structural guarantee for a *RelationalTable: AlterableTable,
// IndexAlterableTable, IndexBuildingTable, and RewritableTable must all be
// unreachable through the wrapper even though the concrete
// *RelationalTable genuinely implements every one of them.
func TestNewReadOnlyTable_StripsRelationalDDLCapability(t *testing.T) {
	def := testUsersDef()
	inner, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	// Sanity check: the unwrapped concrete table DOES implement every DDL
	// capability interface.
	var _ sqle.AlterableTable = inner
	var _ sqle.IndexAlterableTable = inner
	var _ sqle.IndexBuildingTable = inner
	var _ sqle.RewritableTable = inner

	wrapped := newReadOnlyTable(inner)

	_, ok := wrapped.(sqle.AlterableTable)
	assert.False(t, ok, "readOnlyTable must not satisfy sql.AlterableTable")
	_, ok = wrapped.(sqle.IndexAlterableTable)
	assert.False(t, ok, "readOnlyTable must not satisfy sql.IndexAlterableTable")
	_, ok = wrapped.(sqle.IndexBuildingTable)
	assert.False(t, ok, "readOnlyTable must not satisfy sql.IndexBuildingTable")
	_, ok = wrapped.(sqle.RewritableTable)
	assert.False(t, ok, "readOnlyTable must not satisfy sql.RewritableTable")
}
