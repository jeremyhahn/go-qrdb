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

package rowlock

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// requestCount reads qrdb_sql_row_lock_requests_total{mode, outcome} from the
// registry the metric is exported through.
func requestCount(t *testing.T, mode Mode, outcome string) float64 {
	t.Helper()
	families, err := metrics.Registry().Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != "qrdb_sql_row_lock_requests_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["mode"] == mode.String() && labels["outcome"] == outcome {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// A victim chosen because the lock table was too large to search is not a
// detected deadlock, and an operator must be able to tell the two apart: the
// first means the table outgrew MaxDetectionRecords.
func TestMetrics_UnsearchedVictimHasItsOwnOutcome(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	bulk, a, b := NewOwner(), NewOwner(), NewOwner()
	t.Cleanup(func() {
		for _, o := range []*Owner{bulk, a, b} {
			_ = o.Release(context.Background())
		}
	})

	keys := make([]string, MaxDetectionRecords)
	for i := range keys {
		keys[i] = "__tbl:shop/9/0/bulk-" + strconv.Itoa(i)
	}
	require.NoError(t, bulk.Acquire(context.Background(), tbl, keys, ModeShared, 0))
	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))

	unsearched := requestCount(t, ModeExclusive, outcomeUnsearched)
	deadlock := requestCount(t, ModeExclusive, outcomeDeadlock)

	var dl *DeadlockError
	require.ErrorAs(t, b.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 5*time.Second), &dl)
	require.True(t, dl.Unsearched)

	assert.Equal(t, unsearched+1, requestCount(t, ModeExclusive, outcomeUnsearched))
	assert.Equal(t, deadlock, requestCount(t, ModeExclusive, outcomeDeadlock))
}

func TestMetrics_DetectedDeadlockCountsAsDeadlock(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a, b := NewOwner(), NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()); _ = b.Release(context.Background()) })

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	require.NoError(t, b.Acquire(context.Background(), tbl, []string{keyB}, ModeExclusive, 0))
	aDone := acquireAsync(a, tbl, []string{keyB}, ModeExclusive, 5*time.Second)
	waitQueued(t, tbl, keyB, 2)

	unsearched := requestCount(t, ModeExclusive, outcomeUnsearched)
	deadlock := requestCount(t, ModeExclusive, outcomeDeadlock)
	var dl *DeadlockError
	require.ErrorAs(t, b.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 5*time.Second), &dl)
	require.False(t, dl.Unsearched)
	require.NoError(t, mustReturn(t, aDone, "survivor"))

	assert.Equal(t, deadlock+1, requestCount(t, ModeExclusive, outcomeDeadlock))
	assert.Equal(t, unsearched, requestCount(t, ModeExclusive, outcomeUnsearched))
}
