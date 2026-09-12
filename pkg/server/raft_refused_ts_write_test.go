//go:build !qrdb_ts

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

package server

import (
	"context"
	"testing"
	"time"
)

// TestRaftRefusedTimeSeriesWriteLeavesTheShardReadable is PROBE-B from
// go-qrdb#520, on a real raft shard, in the build where the time-series engine
// is compiled out.
//
// Sequence: write and read a KV key (fast), propose a time-series write the
// shard cannot serve, then read THE SAME KV KEY again. Before the fix the
// refusal reached Raft as a fatal Apply failure, lastApplied stopped
// advancing, and that third step took 5m6s and returned `quicraft: timeout` —
// on a different engine, on a key read successfully microseconds earlier.
//
// This FILE is constrained to !qrdb_ts because the refusal only exists in that
// build — not the test, and not its helper. The tag-independent twin, which
// refuses an unrecognised OPCODE instead and so runs under every
// configuration, is TestRaftRefusedOpcodeLeavesTheShardReadable in
// raft_refused_write_test.go, whose file carries no constraint and therefore
// holds the shared assertion.
func TestRaftRefusedTimeSeriesWriteLeavesTheShardReadable(t *testing.T) {
	stack := newRaftE2EStack(t)
	ctx := context.Background()
	stack.createRaftTenantDB(t, "acme", "orders")

	kv, err := stack.resolver.KVService("acme", "orders")
	if err != nil {
		t.Fatalf("resolver KVService: %v", err)
	}
	if err := kv.Put(ctx, "order:1", []byte("v")); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}
	if got, err := kv.Get(ctx, "order:1"); err != nil || string(got) != "v" {
		t.Fatalf("KV.Get before the refused write = %q, %v; want v", got, err)
	}

	ts, err := stack.resolver.TimeSeriesService("acme", "orders")
	if err != nil {
		t.Fatalf("resolver TimeSeriesService: %v", err)
	}
	writeErr := ts.Write(ctx, "lat", 1.5, time.Now(), map[string]string{"r": "us"})
	if writeErr == nil {
		t.Fatal("TS.Write succeeded in a build without the time-series engine")
	}
	t.Logf("refused TS.Write: %v", writeErr)

	assertShardStillReadable(t, kv.Get, "order:1", "v")
}
