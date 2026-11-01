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

package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// assertLimitRefusal fails unless r is the capacity refusal of resource at
// maxCount.
func assertLimitRefusal(t *testing.T, r sm.Result, resource protocol.LimitResource, maxCount uint32) {
	t.Helper()
	got, gotMax, ok := protocol.DecodeLimitRefusal(r.Data)
	if !ok || got != resource || gotMax != maxCount {
		t.Fatalf("result %+v is not a limit refusal of %d at %d", r, resource, maxCount)
	}
}

func assertAdmitted(t *testing.T, r sm.Result, what string) {
	t.Helper()
	if _, _, ok := protocol.DecodeAppErrorKind(r.Data); ok {
		t.Fatalf("%s was refused: %q", what, r.Data)
	}
}

// Two creates proposed through different nodes land in one apply batch; the
// cap is decided against the index set each applies over, so only the first
// takes the last slot (go-qrdb#1088).
func TestMVCCEngine_IndexCreate_CapRefusesTheSecondCreateInABatch(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	capped := protocol.IndexCreateOptions{MaxIndexes: 1}
	results := applyMVCCEntries(t, eng, []sm.Entry{
		indexCreateEntry(t, 1, "db.a", "a", capped),
		indexCreateEntry(t, 2, "db.b", "b", capped),
		indexCreateEntry(t, 3, "db.a", "a", capped),
	})
	assertAdmitted(t, results[0], "the first create")
	assertLimitRefusal(t, results[1], protocol.LimitIndexes, 1)
	assertAdmitted(t, results[2], "a create naming the held index")
	if names := indexNames(t, eng); len(names) != 1 {
		t.Fatalf("indexes = %v, want one", names)
	}
}

func TestMVCCEngine_IndexCreate_NoCapIsUnlimited(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	results := applyMVCCEntries(t, eng, []sm.Entry{
		indexCreateEntry(t, 1, "db.a", "a", protocol.IndexCreateOptions{}),
		indexCreateEntry(t, 2, "db.b", "b", protocol.IndexCreateOptions{}),
	})
	for _, r := range results {
		assertAdmitted(t, r, "an uncapped create")
	}
	if names := indexNames(t, eng); len(names) != 2 {
		t.Fatalf("indexes = %v, want two", names)
	}
}

func TestKVEngine_IndexCreate_CapRefusesTheSecondCreateInABatch(t *testing.T) {
	eng := newScopedKVEngine(t)
	capped := protocol.IndexCreateOptions{MaxIndexes: 1}
	results := applyKVEntries(t, eng, []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeIndexCreateOpts("db.a", "a", capped))},
		{Index: 2, Cmd: mustEncode(protocol.EncodeIndexCreateOpts("db.b", "b", capped))},
		{Index: 3, Cmd: mustEncode(protocol.EncodeIndexCreateOpts("db.a", "a", capped))},
	})
	assertAdmitted(t, results[0], "the first create")
	assertLimitRefusal(t, results[1], protocol.LimitIndexes, 1)
	assertAdmitted(t, results[2], "a create naming the held index")
}

func newCappedTSEngine(t *testing.T) *MVCCEngine {
	t.Helper()
	stg := memory.NewStorage()
	t.Cleanup(func() { _ = stg.Close() })
	eng, err := NewMVCCEngine(stg, WithMVCCTimeSeries(newTS(t, stg)))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close(context.Background()) })
	return eng
}

func tsCappedEntry(index uint64, cmd []byte, maxMetrics uint32) sm.Entry {
	return sm.Entry{Index: index, Cmd: protocol.AppendTSMetricCap(cmd, maxMetrics)}
}

// Writes creating metrics through different nodes land in one apply batch;
// the cap counts a metric an earlier entry of the batch created, and a write
// to a held metric is admitted at the cap.
func TestMVCCEngine_TSWrite_MetricCapCountsTheBatchsOwnWrites(t *testing.T) {
	eng := newCappedTSEngine(t)
	now := time.Now().UnixNano()
	results := applyMVCCEntries(t, eng, []sm.Entry{
		tsCappedEntry(1, mustEncode(protocol.EncodeTSWrite("cpu", now, 1, nil)), 1),
		tsCappedEntry(2, mustEncode(protocol.EncodeTSWrite("mem", now, 1, nil)), 1),
		tsCappedEntry(3, mustEncode(protocol.EncodeTSWritePoint("disk", now, 1, nil)), 1),
		tsCappedEntry(4, mustEncode(protocol.EncodeTSWriteBatch([]protocol.TSWriteParams{
			{Metric: "cpu", Timestamp: now + 1, Value: 2},
			{Metric: "net", Timestamp: now + 1, Value: 2},
		})), 1),
		tsCappedEntry(5, mustEncode(protocol.EncodeTSWrite("cpu", now+2, 3, nil)), 1),
		tsCappedEntry(6, mustEncode(protocol.EncodeTSWriteBatch([]protocol.TSWriteParams{
			{Metric: "cpu", Timestamp: now + 3, Value: 4},
		})), 1),
	})
	assertAdmitted(t, results[0], "the first new metric")
	assertLimitRefusal(t, results[1], protocol.LimitMetrics, 1)
	assertLimitRefusal(t, results[2], protocol.LimitMetrics, 1)
	assertLimitRefusal(t, results[3], protocol.LimitMetrics, 1)
	assertAdmitted(t, results[4], "a write to the held metric")
	assertAdmitted(t, results[5], "a batch naming only the held metric")
}

func TestMVCCEngine_TSWrite_NoCapIsUnlimited(t *testing.T) {
	eng := newCappedTSEngine(t)
	now := time.Now().UnixNano()
	results := applyMVCCEntries(t, eng, []sm.Entry{
		tsCappedEntry(1, mustEncode(protocol.EncodeTSWrite("cpu", now, 1, nil)), 0),
		tsCappedEntry(2, mustEncode(protocol.EncodeTSWrite("mem", now, 1, nil)), 0),
	})
	for _, r := range results {
		assertAdmitted(t, r, "an uncapped write")
	}
}

func TestTSMetricAdmission_Helpers(t *testing.T) {
	if _, ok := MetricLimitResult(errors.New("other")); ok {
		t.Fatal("a non-limit error mapped to a limit refusal")
	}
	r, ok := MetricLimitResult(&timeseries.MetricLimitError{Max: 4})
	if !ok {
		t.Fatal("a metric limit error was not mapped")
	}
	assertLimitRefusal(t, r, protocol.LimitMetrics, 4)
	got := TSBatchMetrics([]protocol.TSWriteParams{{Metric: "a"}, {Metric: "b"}})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("TSBatchMetrics = %v", got)
	}
}
