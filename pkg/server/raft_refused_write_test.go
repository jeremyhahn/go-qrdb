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

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// This file is UNCONSTRAINED so assertShardStillReadable is a superset of
// every caller's constraint: it is called from here and from
// raft_refused_ts_write_test.go, which is !qrdb_ts.

// readAfterRefusalBudget bounds the read that follows a refused write.
//
// The assertion is on LATENCY, not only on the error, because the defect is
// that the answer arrives minutes late: a message-only assertion passes
// against a halted shard right up until the deadline expires. Measured on this
// harness the read is ~30us fixed and ~50us before the refusal, so a second is
// four orders of magnitude of headroom for a loaded CI runner and still three
// orders below the failure being guarded (go-qrdb#520 measured 5m6s).
const readAfterRefusalBudget = time.Second

// refusedWriteCeiling bounds the whole test rather than letting a halted shard
// burn the package's 30-minute budget. Without it the un-fixed tree does not
// fail here, it HANGS here, and a hang reads as infrastructure rather than as
// this defect.
const refusedWriteCeiling = 20 * time.Second

// TestRaftRefusedOpcodeLeavesTheShardReadable is the tag-independent twin, and
// the more alarming one: an opcode no engine in this tree implements is
// reachable from any client able to propose, and under EVERY build
// configuration it halted the shard exactly as the time-series stub did. The
// time-series refusal was merely the one easiest to trigger.
func TestRaftRefusedOpcodeLeavesTheShardReadable(t *testing.T) {
	stack := newRaftE2EStack(t)
	ctx := context.Background()
	_, info := stack.createRaftTenantDB(t, "acme", "orders")

	kv, err := stack.resolver.KVService("acme", "orders")
	if err != nil {
		t.Fatalf("resolver KVService: %v", err)
	}
	if err := kv.Put(ctx, "order:1", []byte("v")); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}
	if got, err := kv.Get(ctx, "order:1"); err != nil || string(got) != "v" {
		t.Fatalf("KV.Get before the refused proposal = %q, %v; want v", got, err)
	}

	// 0xFE is in no dispatch table in this repository.
	result, err := stack.host.SyncPropose(ctx, info.ShardID, []byte{0xFE, 0x00})
	if err != nil {
		t.Fatalf("SyncPropose of an unknown opcode returned %v; the refusal must "+
			"travel in the result payload, not halt the state machine", err)
	}
	msg, capability, ok := protocol.DecodeAppErrorKind(result.Data)
	if !ok || !capability {
		t.Fatalf("refused proposal result %q is not an encoded capability error; the "+
			"proposer would read the refusal as an ordinary result", result.Data)
	}
	t.Logf("refused proposal: %s", msg)

	assertShardStillReadable(t, kv.Get, "order:1", "v")
}

// assertShardStillReadable performs the third step of PROBE-B: a linearizable
// read of a key written BEFORE the refusal, under a bounded context, asserting
// both the value and the latency.
func assertShardStillReadable(t *testing.T, get func(context.Context, string) ([]byte, error), key, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), refusedWriteCeiling)
	defer cancel()

	start := time.Now()
	got, err := get(ctx, key)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("KV.Get after the refusal failed after %v: %v; a refused write must "+
			"not cost the shard its reads (go-qrdb#520)", elapsed, err)
	}
	if string(got) != want {
		t.Fatalf("KV.Get after the refusal = %q, want %q", got, want)
	}
	if elapsed > readAfterRefusalBudget {
		t.Fatalf("KV.Get after the refusal took %v, budget %v; the read index is "+
			"stalled even though the value eventually arrived", elapsed, readAfterRefusalBudget)
	}
	t.Logf("read after the refusal: %v", elapsed)
}
