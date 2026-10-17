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

package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// benchAuditEvent returns a representative audit event. tenantID is left
// for the caller to assign per entry (EncodeAuditAppend's routing argument
// decides the table segment; the event's own TenantID, left empty here, is
// never a mismatch against any routing tenant).
func benchAuditEvent() audit.AuditEvent {
	return audit.AuditEvent{
		Actor:     "operator",
		Resource:  "barrier",
		Action:    "unseal",
		Outcome:   audit.OutcomeSuccess,
		Timestamp: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
	}
}

// auditAppendBenchEntry builds one OpAuditAppend entry for tenantID.
func auditAppendBenchEntry(b *testing.B, index uint64, tenantID string, ev audit.AuditEvent) sm.Entry {
	b.Helper()
	cmd, err := protocol.EncodeAuditAppend(tenantID, ev)
	if err != nil {
		b.Fatalf("encode: %v", err)
	}
	return sm.Entry{Index: index, Cmd: cmd}
}

// BenchmarkMVCCApply_OpPut is the baseline every audit-append number in
// this file should be read against: the ordinary KV write path on the SAME
// no-sync MVCCStateMachine, so the audit opcode's marginal cost (one more
// JSON marshal, two more Pebble Sets for the event and head records) is
// visible as a delta against a write this engine already does constantly,
// not as an isolated, uncontextualized number.
func BenchmarkMVCCApply_OpPut(b *testing.B) {
	value := make([]byte, 128)
	for _, size := range benchBatchSizes {
		b.Run(fmt.Sprintf("batch=%d", size), func(b *testing.B) {
			s := newBenchMVCCStateMachine(b)
			runApplyBenchmark(b, size,
				func(index uint64) sm.Entry {
					return mvccPutEntry(index, []byte(fmt.Sprintf("key-%08d", index)), value, 0)
				},
				func(entries []sm.Entry, results []sm.Result) error {
					return s.Update(context.Background(), entries, results)
				})
		})
	}
}

// BenchmarkMVCCApply_AuditAppend measures the replicated audit chain's apply
// cost on a single, busy tenant -- every entry in a batch appends to the
// SAME tenant's chain, which is the representative-contention case
// docs/audit/architecture.md's "Raft write volume and batching" section asks
// for: a tenant whose authorized requests all gate on this same write path.
// Reuses newBenchMVCCStateMachine (noSync, matching every other apply
// benchmark in this package) with an audit Chain attached.
func BenchmarkMVCCApply_AuditAppend(b *testing.B) {
	ev := benchAuditEvent()
	for _, size := range benchBatchSizes {
		b.Run(fmt.Sprintf("batch=%d", size), func(b *testing.B) {
			s := newBenchMVCCStateMachine(b)
			s.SetLogs(vlog.NewChain())

			runApplyBenchmark(b, size,
				func(index uint64) sm.Entry {
					return auditAppendBenchEntry(b, index, "acme", ev)
				},
				func(entries []sm.Entry, results []sm.Result) error {
					return s.Update(context.Background(), entries, results)
				})
		})
	}
}

// BenchmarkMVCCApply_AuditAppend_ManyTenants is the opposite contention
// shape: every entry in a batch appends to a DIFFERENT tenant's chain, so
// no two entries in the same batch touch the same in-memory head. Compared
// against BenchmarkMVCCApply_AuditAppend, this measures whether per-tenant
// head serialization is visible in the apply cost at this batch scale; it
// does NOT establish a general "no serialization cost under contention"
// property -- that claim was in an earlier draft of this comment and was
// unsupported, since this benchmark applies entries sequentially within one
// goroutine and cannot observe lock contention between concurrent proposers
// at all. A real concurrency measurement would drive SyncPropose from
// multiple goroutines against one shard, which this file does not do for
// any opcode.
func BenchmarkMVCCApply_AuditAppend_ManyTenants(b *testing.B) {
	ev := benchAuditEvent()
	for _, size := range benchBatchSizes {
		b.Run(fmt.Sprintf("batch=%d", size), func(b *testing.B) {
			s := newBenchMVCCStateMachine(b)
			s.SetLogs(vlog.NewChain())

			runApplyBenchmark(b, size,
				func(index uint64) sm.Entry {
					tenantID := fmt.Sprintf("tenant-%08d", index)
					return auditAppendBenchEntry(b, index, tenantID, ev)
				},
				func(entries []sm.Entry, results []sm.Result) error {
					return s.Update(context.Background(), entries, results)
				})
		})
	}
}

// BenchmarkPebbleApply_AuditAppend_Fsync is the same single-hot-tenant
// measurement against the PebbleStateMachine engine family. NAME and
// COMMENT both say Fsync deliberately: PebbleStateMachine has no no-sync
// construction knob anywhere in this package (newBenchPebbleStateMachine
// does not set one, and neither does any other Pebble benchmark here --
// pebbleAuditWriter.Set, like every other Pebble write path, hardcodes
// pebble.Sync), so this number is NOT comparable to
// BenchmarkMVCCApply_AuditAppend's (noSync) at face value. Giving
// PebbleStateMachine a real no-sync knob would mean threading a durability
// option through every handler that hardcodes pebble.Sync today, which is a
// broader change than this feature's own scope; labelling the asymmetry
// here is the honest alternative until that exists.
func BenchmarkPebbleApply_AuditAppend_Fsync(b *testing.B) {
	ev := benchAuditEvent()
	for _, size := range benchBatchSizes {
		b.Run(fmt.Sprintf("batch=%d", size), func(b *testing.B) {
			s := newBenchPebbleStateMachine(b)
			s.SetLogs(vlog.NewChain())

			runApplyBenchmark(b, size,
				func(index uint64) sm.Entry {
					return auditAppendBenchEntry(b, index, "acme", ev)
				},
				func(entries []sm.Entry, results []sm.Result) error {
					return s.Update(context.Background(), entries, results)
				})
		})
	}
}
