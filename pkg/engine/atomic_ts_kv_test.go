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
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// TestMVCCEngine_TSAndKVWriteInOneApplyBatch_CommitFailureLeavesNeither is
// Lane 1A's acceptance criterion 2 (docs/databases/engineering.md, "Target:
// phased implementation plan"): a time-series write and a KV write applied
// in one Update() call -- so both land in the same storage.Batch -- whose
// batch commit is forced to fail must leave NEITHER write durable. Before
// this lane, handleTSWrite discarded the batch it was given and wrote
// straight to e.ts's own storage, so a time-series write was never part of
// the KV write's atomicity unit at all: a failed commit would have rolled
// back the KV write while the time-series point stayed written.
func TestMVCCEngine_TSAndKVWriteInOneApplyBatch_CommitFailureLeavesNeither(t *testing.T) {
	real := memory.NewStorage()
	t.Cleanup(func() { _ = real.Close() })

	ts, err := timeseries.New(real)
	if err != nil {
		t.Fatalf("timeseries.New: %v", err)
	}

	fs := &failingStorage{Storage: real, failBatchCommit: true}
	eng, err := NewMVCCEngine(fs, WithMVCCTimeSeries(ts))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close(context.Background()) })

	now := time.Now().UnixNano()
	tsCmd := mustEncode(protocol.EncodeTSWrite("atomic.metric", now, 1.0, nil))
	kvCmd, kerr := kv.EncodePut([]byte("atomic:kv:a"), []byte("v"))
	if kerr != nil {
		t.Fatalf("kv.EncodePut: %v", kerr)
	}

	entries := []sm.Entry{
		{Index: 1, Cmd: tsCmd},
		{Index: 2, Cmd: kvCmd},
	}
	results := make([]sm.Result, 2)
	if err := eng.Update(context.Background(), entries, results); err == nil {
		t.Fatal("Update with a failing batch commit: expected an error, got nil")
	}

	// Verify against a FRESH engine over the same, real (non-wrapped)
	// storage, so the lookup reads only what actually became durable --
	// never through the failing wrapper, and never through in-memory state
	// a failed commit may have left this engine's own *mvcc.Store holding.
	freshTS, err := timeseries.New(real)
	if err != nil {
		t.Fatalf("timeseries.New: %v", err)
	}
	freshEng, err := NewMVCCEngine(real, WithMVCCTimeSeries(freshTS))
	if err != nil {
		t.Fatalf("NewMVCCEngine (fresh): %v", err)
	}
	t.Cleanup(func() { _ = freshEng.Close(context.Background()) })

	if val, _ := freshEng.Lookup(context.Background(), "atomic:kv:a"); val != nil {
		t.Errorf("KV write survived a failed batch commit: %q", val)
	}

	start := time.Unix(0, now).Add(-time.Second)
	end := time.Unix(0, now).Add(time.Second)
	got, err := freshEng.Lookup(context.Background(), service.TSQueryRequest{
		Metric: "atomic.metric",
		Start:  start,
		End:    end,
	})
	if err != nil {
		t.Fatalf("TS query: %v", err)
	}
	if points := got.([]timeseries.DataPoint); len(points) != 0 {
		t.Errorf("time-series write survived a failed batch commit: %+v", points)
	}
}
