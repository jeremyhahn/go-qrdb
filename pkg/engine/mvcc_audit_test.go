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
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// tenantEvent returns a minimal AuditEvent for tenantID. The replicated
// table does not read ev.TenantID to decide WHERE a record lives -- that is
// the tenantID argument to Append/Query, matching the physical range the key
// carries -- but Query's confinement (matchesFilter) still matches on
// ev.TenantID, exactly as tenant.ConfineAuditFilter's caller-supplied filter
// does against the file sink today, so a test event must carry it like any
// real one would.
//
// Timestamp is set explicitly, by the caller, exactly as a real proposer
// must: audit.Chain.Append refuses a zero Timestamp rather than inventing
// one from its own wall clock, because Append runs identically on every
// replica and a value invented at apply time would diverge between them.
func tenantEvent(tenantID, resource, action string) audit.AuditEvent {
	return audit.AuditEvent{
		TenantID:  tenantID,
		Resource:  resource,
		Action:    action,
		Actor:     "operator",
		Timestamp: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
	}
}

func auditAppendEntry(t *testing.T, idx uint64, tenantID string, ev audit.AuditEvent) sm.Entry {
	t.Helper()
	cmd := mustEncode(protocol.EncodeAuditAppend(tenantID, ev))
	return sm.Entry{Index: idx, Cmd: cmd}
}

// rawAuditAppendEntry builds an OpAuditAppend/AuditSubtypeAppend entry
// byte-for-byte, bypassing EncodeAuditAppend's encode-time validation, for
// tests that drive the apply-time BACKSTOP (pkg/audit.Chain's own checks)
// directly.
func rawAuditAppendEntry(idx uint64, tenantID string, body []byte) sm.Entry {
	buf := []byte{protocol.OpAuditAppend, protocol.AuditSubtypeAppend}
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(tenantID)))
	buf = append(buf, tenantID...)
	buf = append(buf, body...)
	return sm.Entry{Index: idx, Cmd: buf}
}

func auditPruneEntry(idx uint64, tenantID string, cutoff uint64) sm.Entry {
	cmd := mustEncode(protocol.EncodeAuditPrune(tenantID, cutoff))
	return sm.Entry{Index: idx, Cmd: cmd}
}

// TestMVCCEngine_AuditAppend_RefusedWithNoChainConfigured asserts the engine
// refuses protocol.OpAuditAppend deterministically -- a successful Apply
// encoding a capability refusal, never a Go error that would stall Raft --
// exactly like an unconfigured lessor refuses a lease opcode. Every replica
// without the subsystem attached answers identically, which is the "state
// machine that doesn't handle it refuses deterministically" property.
func TestMVCCEngine_AuditAppend_RefusedWithNoChainConfigured(t *testing.T) {
	eng, _ := newMVCCEngine(t)
	entries := []sm.Entry{auditAppendEntry(t, 1, "acme", tenantEvent("acme", "barrier", "unseal"))}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	msg, ok := protocol.DecodeAppError(results[0].Data)
	if !ok {
		t.Fatalf("expected an encoded application error, got result=%+v", results[0])
	}
	if !strings.Contains(msg, "audit") {
		t.Fatalf("refusal message = %q, want it to name the missing audit service", msg)
	}
}

// TestMVCCEngine_AuditAppend_AssignsSequenceAndPersists exercises the whole
// wiring end to end: a configured Chain assigns the sequence at apply time,
// the record lands in the engine's storage under the raw audit table, and a
// second Append continues the same tenant's chain.
func TestMVCCEngine_AuditAppend_AssignsSequenceAndPersists(t *testing.T) {
	chain := audit.NewChain()
	eng, stg := newMVCCEngine(t, WithMVCCAuditChain(chain))

	entries := []sm.Entry{auditAppendEntry(t, 1, "acme", tenantEvent("acme", "barrier", "unseal"))}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if results[0].Value != 1 {
		t.Fatalf("assigned sequence = %d, want 1", results[0].Value)
	}

	entries = []sm.Entry{auditAppendEntry(t, 2, "acme", tenantEvent("acme", "barrier", "seal"))}
	results = make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update (2nd): %v", err)
	}
	if results[0].Value != 2 {
		t.Fatalf("assigned sequence = %d, want 2", results[0].Value)
	}

	page, err := chain.Query(stg, "acme", audit.Filter{Limit: 100})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("got %d events, want 2", len(page.Events))
	}
}

// TestMVCCEngine_AuditAppend_DeterministicAcrossTwoEngines builds two
// independent engines over independent storage -- standing in for two Raft
// replicas -- and feeds each the identical sequence of committed entries.
// Both must assign the identical sequence numbers, which is only possible
// because Chain.Append reads the chain's own prior state rather than
// anything the payload carries.
func TestMVCCEngine_AuditAppend_DeterministicAcrossTwoEngines(t *testing.T) {
	engA, stgA := newMVCCEngine(t, WithMVCCAuditChain(audit.NewChain()))
	engB, stgB := newMVCCEngine(t, WithMVCCAuditChain(audit.NewChain()))

	for i := 0; i < 5; i++ {
		entries := []sm.Entry{auditAppendEntry(t, uint64(i+1), "acme", tenantEvent("acme", "barrier", "unseal"))}

		resA := make([]sm.Result, 1)
		if err := engA.Update(context.Background(), entries, resA); err != nil {
			t.Fatalf("engine A update %d: %v", i, err)
		}
		resB := make([]sm.Result, 1)
		if err := engB.Update(context.Background(), entries, resB); err != nil {
			t.Fatalf("engine B update %d: %v", i, err)
		}
		if resA[0].Value != resB[0].Value {
			t.Fatalf("event %d: sequence diverged: A=%d B=%d", i, resA[0].Value, resB[0].Value)
		}
	}

	pageA, err := engA.AuditChain().Query(stgA, "acme", audit.Filter{Limit: 100})
	if err != nil {
		t.Fatalf("query A: %v", err)
	}
	pageB, err := engB.AuditChain().Query(stgB, "acme", audit.Filter{Limit: 100})
	if err != nil {
		t.Fatalf("query B: %v", err)
	}
	if len(pageA.Events) != 5 || len(pageB.Events) != 5 {
		t.Fatalf("event counts: A=%d B=%d, want 5 each", len(pageA.Events), len(pageB.Events))
	}
	for i := range pageA.Events {
		if pageA.Events[i].PrevHash != pageB.Events[i].PrevHash {
			t.Fatalf("event %d: PrevHash diverged: A=%q B=%q", i, pageA.Events[i].PrevHash, pageB.Events[i].PrevHash)
		}
	}
}

// TestMVCCEngine_AuditPrune_DeletesOldEventsWithoutBreakingTheChain exercises
// the retention sub-type: events below the cutoff are gone, the chain's head
// is untouched, and the next Append continues from where it left off.
func TestMVCCEngine_AuditPrune_DeletesOldEventsWithoutBreakingTheChain(t *testing.T) {
	chain := audit.NewChain()
	eng, stg := newMVCCEngine(t, WithMVCCAuditChain(chain))

	for i := 0; i < 5; i++ {
		entries := []sm.Entry{auditAppendEntry(t, uint64(i+1), "acme", tenantEvent("acme", "barrier", "unseal"))}
		results := make([]sm.Result, 1)
		if err := eng.Update(context.Background(), entries, results); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	entries := []sm.Entry{auditPruneEntry(6, "acme", 3)}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if results[0].Value != 2 {
		t.Fatalf("pruned %d events, want 2", results[0].Value)
	}

	page, err := chain.Query(stg, "acme", audit.Filter{Limit: 100})
	if err != nil {
		t.Fatalf("query after prune: %v", err)
	}
	if len(page.Events) != 3 {
		t.Fatalf("got %d events after prune, want 3", len(page.Events))
	}

	entries = []sm.Entry{auditAppendEntry(t, 7, "acme", tenantEvent("acme", "barrier", "seal"))}
	results = make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("append after prune: %v", err)
	}
	if results[0].Value != 6 {
		t.Fatalf("sequence after prune = %d, want 6", results[0].Value)
	}
}

// TestMVCCEngine_AuditAppend_SurvivesEngineRestart asserts that a fresh
// engine built over the same storage -- standing in for a node restart --
// continues the same tenant's chain rather than restarting it at sequence 1.
func TestMVCCEngine_AuditAppend_SurvivesEngineRestart(t *testing.T) {
	stg := memory.NewStorage()
	chain1 := audit.NewChain()
	eng1, err := NewMVCCEngine(stg, WithMVCCAuditChain(chain1))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	entries := []sm.Entry{auditAppendEntry(t, 1, "acme", tenantEvent("acme", "barrier", "unseal"))}
	results := make([]sm.Result, 1)
	if err := eng1.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("append before restart: %v", err)
	}

	// A fresh engine over the SAME storage, exactly as a restart rebuilds the
	// engine over the data directory it finds on disk.
	chain2 := audit.NewChain()
	eng2, err := NewMVCCEngine(stg, WithMVCCAuditChain(chain2))
	if err != nil {
		t.Fatalf("NewMVCCEngine (restart): %v", err)
	}
	entries = []sm.Entry{auditAppendEntry(t, 2, "acme", tenantEvent("acme", "barrier", "seal"))}
	results = make([]sm.Result, 1)
	if err := eng2.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("append after restart: %v", err)
	}
	if results[0].Value != 2 {
		t.Fatalf("sequence after restart = %d, want 2 (continuing the chain)", results[0].Value)
	}
}

// TestMVCCEngine_AuditAppend_MissingTimestampIsARefusalNotAHalt is the
// regression test for a defect caught by code review, not by a failing
// test: the first version of this handler returned EVERY Chain.Append error
// as a raw Go error, which halts Raft apply. audit.MissingTimestampError is
// a deterministic consequence of the committed entry's bytes -- every
// replica reaches it identically -- so it must be encoded as an application
// refusal, leaving the engine free to keep applying later entries. Only a
// genuine local storage fault (*audit.TableWriteError) may halt apply.
func TestMVCCEngine_AuditAppend_MissingTimestampIsARefusalNotAHalt(t *testing.T) {
	eng, _ := newMVCCEngine(t, WithMVCCAuditChain(audit.NewChain()))
	ev := tenantEvent("acme", "barrier", "unseal")
	ev.Timestamp = time.Time{} // the proposer forgot to set it
	// protocol.EncodeAuditAppend now refuses this at encode time (N3), so
	// this engine-level test of the apply-time BACKSTOP builds the raw
	// bytes directly rather than through the normal encode path.
	body, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	entries := []sm.Entry{rawAuditAppendEntry(1, "acme", body)}

	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update returned a Go error (would halt Raft apply): %v", err)
	}
	msg, ok := protocol.DecodeAppError(results[0].Data)
	if !ok {
		t.Fatalf("expected an encoded application refusal, got result=%+v", results[0])
	}
	if !strings.Contains(msg, "timestamp") {
		t.Fatalf("refusal message = %q, want it to name the missing timestamp", msg)
	}

	// The engine is still usable: a well-formed append right after the
	// refusal must succeed, proving apply was never halted.
	entries = []sm.Entry{auditAppendEntry(t, 2, "acme", tenantEvent("acme", "barrier", "unseal"))}
	results = make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("append after the refusal: %v", err)
	}
	if results[0].Value != 1 {
		t.Fatalf("sequence after the refusal = %d, want 1 (the refusal assigned no sequence)", results[0].Value)
	}
}
