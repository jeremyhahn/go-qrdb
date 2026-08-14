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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// spySequenceService
// ---------------------------------------------------------------------------

// spySequenceService wraps a real service.SequenceServicer, recording every
// Allocate call's requested block size so tests can assert on the block-
// growth schedule (sequenceBlock.nextBlockSizeLocked) without depending on
// its internal state directly.
type spySequenceService struct {
	service.SequenceServicer
	allocateSizes []uint64
}

func (s *spySequenceService) Allocate(ctx context.Context, database, name string, n uint64) (uint64, error) {
	s.allocateSizes = append(s.allocateSizes, n)
	return s.SequenceServicer.Allocate(ctx, database, name, n)
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

func autoIncCreateRequest() *service.CreateTableRequest {
	return &service.CreateTableRequest{
		Database: "shop",
		Name:     "widgets",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED", Nullable: false, AutoIncrement: true},
			{Name: "name", Type: "VARCHAR(64)", Nullable: true},
		},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	}
}

// newAutoIncHarnessTable creates an AUTO_INCREMENT table backed by a
// spySequenceService, so block-allocation behavior can be observed.
func newAutoIncHarnessTable(t *testing.T, h *ddlHarness) (*RelationalTable, *spySequenceService) {
	t.Helper()
	def, err := h.tableSvc.CreateTable(context.Background(), autoIncCreateRequest())
	require.NoError(t, err)
	spy := &spySequenceService{SequenceServicer: h.seqSvc}
	tbl, err := NewRelationalTable(def, h.kvSvc, h.mvccSvc,
		WithTableService(h.tableSvc),
		WithTransactionService(h.txnSvc),
		WithSequenceService(spy),
		WithBatchService(h.batchSvc),
	)
	require.NoError(t, err)
	return tbl, spy
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestRelationalTable_GetNextAutoIncrementValue_Monotonic(t *testing.T) {
	h := newDDLHarness()
	tbl, _ := newAutoIncHarnessTable(t, h)

	var got []uint64
	for i := 0; i < 5; i++ {
		v, err := tbl.GetNextAutoIncrementValue(newTestContext(), nil)
		require.NoError(t, err)
		got = append(got, v)
	}
	for i := 1; i < len(got); i++ {
		assert.Greater(t, got[i], got[i-1], "auto-increment values must be strictly increasing")
	}
	assert.Equal(t, []uint64{1, 2, 3, 4, 5}, got)
}

func TestRelationalTable_GetNextAutoIncrementValue_BlockAllocationGrows(t *testing.T) {
	h := newDDLHarness()
	tbl, spy := newAutoIncHarnessTable(t, h)

	// Consume enough values to force several block refills; the exact count
	// of refills only matters in that it must be far fewer than the number
	// of values consumed (proving blocks, not one-at-a-time allocation).
	const n = 50
	for i := 0; i < n; i++ {
		_, err := tbl.GetNextAutoIncrementValue(newTestContext(), nil)
		require.NoError(t, err)
	}

	require.NotEmpty(t, spy.allocateSizes)
	assert.Less(t, len(spy.allocateSizes), n, "block allocation must issue far fewer SequenceServicer.Allocate calls than rows generated")

	// Sizes must be non-decreasing (see sequenceBlock.nextBlockSizeLocked).
	for i := 1; i < len(spy.allocateSizes); i++ {
		assert.GreaterOrEqual(t, spy.allocateSizes[i], spy.allocateSizes[i-1])
	}

	// The sum of every allocated block must cover at least n values.
	var total uint64
	for _, sz := range spy.allocateSizes {
		total += sz
	}
	assert.GreaterOrEqual(t, total, uint64(n))
}

func TestRelationalTable_GetNextAutoIncrementValue_ExplicitValueAdvancesSequence(t *testing.T) {
	h := newDDLHarness()
	tbl, _ := newAutoIncHarnessTable(t, h)

	// An explicit value far above the sequence's current watermark must
	// advance the sequence past it.
	_, err := tbl.GetNextAutoIncrementValue(newTestContext(), int64(100))
	require.NoError(t, err)

	peek, err := tbl.PeekNextAutoIncrementValue(newTestContext())
	require.NoError(t, err)
	assert.Equal(t, uint64(101), peek)

	// A subsequently generated value must not collide with the explicit one.
	next, err := tbl.GetNextAutoIncrementValue(newTestContext(), nil)
	require.NoError(t, err)
	assert.Greater(t, next, uint64(100))
}

func TestRelationalTable_GetNextAutoIncrementValue_ExplicitValueBelowWatermark_DoesNotLowerSequence(t *testing.T) {
	h := newDDLHarness()
	tbl, _ := newAutoIncHarnessTable(t, h)

	_, err := tbl.GetNextAutoIncrementValue(newTestContext(), int64(50))
	require.NoError(t, err)

	// A lower explicit value must not move the sequence backwards.
	_, err = tbl.GetNextAutoIncrementValue(newTestContext(), int64(10))
	require.NoError(t, err)

	peek, err := tbl.PeekNextAutoIncrementValue(newTestContext())
	require.NoError(t, err)
	assert.Equal(t, uint64(51), peek)
}

func TestRelationalTable_PeekNextAutoIncrementValue_EmptySequence(t *testing.T) {
	h := newDDLHarness()
	tbl, _ := newAutoIncHarnessTable(t, h)

	peek, err := tbl.PeekNextAutoIncrementValue(newTestContext())
	require.NoError(t, err)
	assert.Equal(t, uint64(1), peek)
}

func TestRelationalTable_AutoIncrementSetter_SetsExplicitValue(t *testing.T) {
	h := newDDLHarness()
	tbl, _ := newAutoIncHarnessTable(t, h)

	setter := tbl.AutoIncrementSetter(newTestContext())
	release, err := setter.AcquireAutoIncrementLock(newTestContext())
	require.NoError(t, err)
	defer release()

	require.NoError(t, setter.SetAutoIncrementValue(newTestContext(), 1000))
	require.NoError(t, setter.Close(newTestContext()))

	peek, err := tbl.PeekNextAutoIncrementValue(newTestContext())
	require.NoError(t, err)
	assert.Equal(t, uint64(1000), peek)

	next, err := tbl.GetNextAutoIncrementValue(newTestContext(), nil)
	require.NoError(t, err)
	assert.Equal(t, uint64(1000), next)
}

func TestRelationalTable_AutoIncrementSetter_ZeroResetsSequence(t *testing.T) {
	h := newDDLHarness()
	tbl, _ := newAutoIncHarnessTable(t, h)

	_, err := tbl.GetNextAutoIncrementValue(newTestContext(), nil)
	require.NoError(t, err)

	setter := tbl.AutoIncrementSetter(newTestContext())
	require.NoError(t, setter.SetAutoIncrementValue(newTestContext(), 0))

	peek, err := tbl.PeekNextAutoIncrementValue(newTestContext())
	require.NoError(t, err)
	assert.Equal(t, uint64(1), peek)
}

func TestRelationalTable_AutoIncrement_NoAutoIncrementColumn_Errors(t *testing.T) {
	def := testUsersDef() // no AUTO_INCREMENT column.
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	_, err = tbl.PeekNextAutoIncrementValue(newTestContext())
	var e *AutoIncrementUnavailableError
	require.ErrorAs(t, err, &e)

	_, err = tbl.GetNextAutoIncrementValue(newTestContext(), nil)
	require.ErrorAs(t, err, &e)

	err = tbl.AutoIncrementSetter(newTestContext()).SetAutoIncrementValue(newTestContext(), 1)
	require.ErrorAs(t, err, &e)
}

func TestRelationalTable_AutoIncrement_NoSequenceService_Errors(t *testing.T) {
	def := autoIncTableDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	_, err = tbl.PeekNextAutoIncrementValue(newTestContext())
	var e *SequenceServiceUnavailableError
	require.ErrorAs(t, err, &e)
}

// autoIncTableDef returns a TableDef with an AUTO_INCREMENT column, for
// tests that need one without going through the full CreateTable flow.
func autoIncTableDef() *service.TableDef {
	return &service.TableDef{
		TableID:  9,
		Name:     "widgets",
		Database: "shop",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED", Nullable: false, AutoIncrement: true},
			{Name: "name", Type: "VARCHAR(64)", Nullable: true},
		},
		PrimaryKey:    []service.PKColumn{{Name: "id"}},
		AutoIncColumn: "id",
		SchemaVersion: 1,
	}
}
