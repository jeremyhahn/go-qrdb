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
	"encoding/binary"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// openStoreWithAuditChain creates a PebbleStateMachine with a log registry
// attached, mirroring openStoreWithLessor exactly, and returns the
// registry's audit instance.
func openStoreWithAuditChain(t *testing.T) (*PebbleStateMachine, *audit.Chain) {
	t.Helper()
	s := NewPebbleStateMachine(nil)
	s.SetLogs(vlog.NewChain())
	dir := filepath.Join(t.TempDir(), "db")
	if _, err := s.Open(context.Background(), dir, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s, s.AuditChain()
}

func auditEvent(tenantID string) audit.AuditEvent {
	return audit.AuditEvent{
		TenantID:  tenantID,
		Actor:     "operator",
		Resource:  "barrier",
		Action:    "unseal",
		Outcome:   audit.OutcomeSuccess,
		Timestamp: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
	}
}

func auditAppendCmd(t *testing.T, tenantID string, ev audit.AuditEvent) []byte {
	t.Helper()
	return mustEncode(protocol.EncodeAuditAppend(tenantID, ev))
}

// rawAuditAppendCmd builds an OpAuditAppend/AuditSubtypeAppend command
// byte-for-byte, bypassing EncodeAuditAppend's encode-time validation. Used
// only by tests that exercise the apply-time BACKSTOP (pkg/audit.Chain's own
// checks), which protocol.EncodeAuditAppend's own encode-time checks would
// otherwise make unreachable through the normal encode path.
func rawAuditAppendCmd(tenantID string, body []byte) []byte {
	buf := []byte{protocol.OpAuditAppend, protocol.AuditSubtypeAppend}
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(tenantID)))
	buf = append(buf, tenantID...)
	buf = append(buf, body...)
	return buf
}

// TestPebbleStateMachine_SetLogs exercises the setter/getter pair,
// mirroring TestPebbleStateMachine_SetLessor.
func TestPebbleStateMachine_SetLogs(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	if s.Logs() != nil || s.AuditChain() != nil {
		t.Fatal("expected no registry and no audit chain before SetLogs")
	}
	l := vlog.NewChain()
	s.SetLogs(l)
	if s.Logs() != l {
		t.Fatal("Logs() did not return the attached registry")
	}
	if s.AuditChain() == nil {
		t.Fatal("AuditChain() is nil with a registry attached")
	}
}

// TestMVCCStateMachine_Logs_BeforeOpen asserts the pre-Open getters answer
// from the state machine's own stored field, exactly as
// MVCCStateMachine.Lessor() does, before an engine exists to delegate to.
func TestMVCCStateMachine_Logs_BeforeOpen(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	if s.Logs() != nil || s.AuditChain() != nil {
		t.Fatal("expected no registry and no audit chain before SetLogs")
	}
	l := vlog.NewChain()
	s.SetLogs(l)
	if s.Logs() != l {
		t.Fatal("Logs() before Open() did not return the attached registry")
	}
	if s.AuditChain() == nil {
		t.Fatal("AuditChain() before Open() is nil with a registry attached")
	}
}

// TestPebbleStateMachine_AuditAppend_NoChainConfigured asserts the dispatch
// refuses the opcode deterministically -- a successful Apply encoding a
// capability refusal, never a Go error -- when no Chain is attached.
func TestPebbleStateMachine_AuditAppend_NoChainConfigured(t *testing.T) {
	s := openExtendedStore(t) // no audit chain configured

	cmd := auditAppendCmd(t, "acme", auditEvent("acme"))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})

	msg, ok := protocol.DecodeAppError(results[0].Data)
	if !ok {
		t.Fatalf("expected an encoded application error, got result=%+v", results[0])
	}
	if !strings.Contains(msg, "audit") {
		t.Fatalf("refusal message = %q, want it to name the missing audit service", msg)
	}
}

// TestPebbleStateMachine_AuditAppend_AssignsSequence drives two appends
// through a real PebbleStateMachine.Update and asserts the sequence
// assigned at apply time, plus that Query answers from the same storage.
func TestPebbleStateMachine_AuditAppend_AssignsSequence(t *testing.T) {
	s, chain := openStoreWithAuditChain(t)

	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: auditAppendCmd(t, "acme", auditEvent("acme"))}})
	if results[0].Value != 1 {
		t.Fatalf("sequence = %d, want 1", results[0].Value)
	}

	results = applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: auditAppendCmd(t, "acme", auditEvent("acme"))}})
	if results[0].Value != 2 {
		t.Fatalf("sequence = %d, want 2", results[0].Value)
	}

	page, err := chain.Query(pebbleStorage.NewFromDB(s.db, true), "acme", audit.Filter{Limit: 100})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("got %d events, want 2", len(page.Events))
	}
}

// TestPebbleStateMachine_AuditPrune drives the retention sub-type through a
// real Update and asserts the pruned count and the surviving range.
func TestPebbleStateMachine_AuditPrune(t *testing.T) {
	s, chain := openStoreWithAuditChain(t)

	for i := 0; i < 5; i++ {
		applyExtEntries(t, s, []sm.Entry{{Index: uint64(i + 1), Cmd: auditAppendCmd(t, "acme", auditEvent("acme"))}})
	}

	pruneCmd := mustEncode(protocol.EncodeAuditPrune("acme", 3))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 6, Cmd: pruneCmd}})
	if results[0].Value != 2 {
		t.Fatalf("pruned %d events, want 2", results[0].Value)
	}

	page, err := chain.Query(pebbleStorage.NewFromDB(s.db, true), "acme", audit.Filter{Limit: 100})
	if err != nil {
		t.Fatalf("query after prune: %v", err)
	}
	if len(page.Events) != 3 {
		t.Fatalf("got %d events after prune, want 3", len(page.Events))
	}
}

// TestPebbleStateMachine_AuditAppend_SurvivesRestart closes and reopens the
// state machine over the same directory -- a real restart, not a stand-in --
// and asserts the chain continues rather than restarting at sequence 1.
func TestPebbleStateMachine_AuditAppend_SurvivesRestart(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	s.SetLogs(vlog.NewChain())
	dir := filepath.Join(t.TempDir(), "db")
	if _, err := s.Open(context.Background(), dir, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: auditAppendCmd(t, "acme", auditEvent("acme"))}})
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2 := NewPebbleStateMachine(nil)
	s2.SetLogs(vlog.NewChain())
	t.Cleanup(func() { _ = s2.Close(context.Background()) })
	if _, err := s2.Open(context.Background(), dir, nil); err != nil {
		t.Fatalf("Open (restart): %v", err)
	}
	results := applyExtEntries(t, s2, []sm.Entry{{Index: 2, Cmd: auditAppendCmd(t, "acme", auditEvent("acme"))}})
	if results[0].Value != 2 {
		t.Fatalf("sequence after restart = %d, want 2 (continuing the chain)", results[0].Value)
	}
}

// TestPebbleStateMachine_AuditAppend_MalformedCommand asserts a payload that
// does not decode is a protocol error, not a silent no-op.
func TestPebbleStateMachine_AuditAppend_MalformedCommand(t *testing.T) {
	s, _ := openStoreWithAuditChain(t)
	results := make([]sm.Result, 1)
	entries := []sm.Entry{{Index: 1, Cmd: []byte{protocol.OpAuditAppend}}}
	if err := s.Update(context.Background(), entries, results); err == nil {
		t.Fatal("expected an error for a truncated audit command")
	}
}

// TestPebbleStateMachine_AuditAppend_MalformedEvent asserts a body that does
// not unmarshal into an AuditEvent is refused as an application result, not
// a halt (N3): the outer command decoded fine, so this is a well-formed
// body that merely fails to unmarshal, a deterministic consequence of the
// committed entry's own bytes. protocol.EncodeAuditAppend cannot build this
// malformed a command (it marshals internally), so the test constructs the
// raw bytes to drive the FSM's own backstop directly.
func TestPebbleStateMachine_AuditAppend_MalformedEvent(t *testing.T) {
	s, _ := openStoreWithAuditChain(t)
	cmd := rawAuditAppendCmd("acme", []byte("not json"))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})
	msg, ok := protocol.DecodeAppError(results[0].Data)
	if !ok {
		t.Fatalf("expected an encoded application refusal, got result=%+v", results[0])
	}
	if !strings.Contains(msg, "malformed event") {
		t.Fatalf("refusal message = %q, want it to name the malformed event", msg)
	}
}

// TestPebbleStateMachine_AuditAppend_UnknownSubtype asserts a sub-type byte
// neither append nor prune decode to is a protocol error (a halt): unlike a
// malformed EVENT body, an unrecognised sub-type names a shape this binary
// does not understand at all, the same class as protocol.UnknownOpError.
func TestPebbleStateMachine_AuditAppend_UnknownSubtype(t *testing.T) {
	s, _ := openStoreWithAuditChain(t)
	cmd := rawAuditAppendCmd("acme", nil)
	cmd[1] = 99 // the subtype byte, neither AuditSubtypeAppend nor AuditSubtypePrune
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results); err == nil {
		t.Fatal("expected an error for an unrecognised audit sub-type")
	}
}

// TestPebbleStateMachine_AuditPrune_MalformedCutoff asserts a prune command
// whose cutoff is not exactly 8 bytes is a protocol error.
func TestPebbleStateMachine_AuditPrune_MalformedCutoff(t *testing.T) {
	s, _ := openStoreWithAuditChain(t)
	cmd := mustEncode(protocol.EncodeAuditPrune("acme", 0))
	cmd = cmd[:len(cmd)-1] // truncate the cutoff by one byte
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results); err == nil {
		t.Fatal("expected an error for a truncated prune cutoff")
	}
}

// NOTE: pebbleHandleAuditAppend's TableWriteError branches (a local storage
// fault from w.Set/w.Delete) are not covered by a test here, for the same
// reason pebbleHandleLeaseGrant's identical branch (lease.TableWriteError)
// is not: there is no fault-injection seam for a raw *pebble.Batch in this
// package (unlike pkg/audit's own ChainFileLogger, which has writeFunc/
// syncFunc precisely so its own write faults are testable). Closing the
// batch early does not make Set/Delete return an error on this pebble
// version, so that approach was tried and dropped rather than shipped as a
// test that could start silently asserting the wrong thing. Adding a real
// seam is follow-on work, not specific to this feature.
//
// The MVCC engine family does not share this gap:
// pkg/engine/mvcc_audit_fault_test.go's
// TestMVCCEngine_AuditAppend_TableWriteFaultHaltsApply covers the identical
// branch there, because handleAuditAppend's batch parameter is the
// storage.Batch INTERFACE on that engine, not a concrete *pebble.Batch, so a
// fault-injecting wrapper is straightforward.

// TestPebbleStateMachine_AuditAppend_MissingTimestampIsARefusalNotAHalt is
// the regression test for a defect caught by code review, not by a failing
// test: the first version of this handler returned EVERY Chain.Append error
// as a raw Go error, which halts Raft apply. audit.MissingTimestampError is
// a deterministic consequence of the committed entry's bytes -- every
// replica reaches it identically -- so it must be encoded as an application
// refusal (statemachine.RefusalResult), exactly like an invalid lease TTL,
// leaving the state machine free to keep applying later entries. Only a
// genuine local storage fault (*vlog.TableWriteError) may halt apply.
func TestPebbleStateMachine_AuditAppend_MissingTimestampIsARefusalNotAHalt(t *testing.T) {
	s, _ := openStoreWithAuditChain(t)
	ev := auditEvent("acme")
	ev.Timestamp = time.Time{} // the proposer forgot to set it
	// protocol.EncodeAuditAppend now refuses this at encode time (N3), so
	// this FSM-level test of the apply-time BACKSTOP builds the raw bytes
	// directly rather than through the normal encode path.
	body, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	cmd := rawAuditAppendCmd("acme", body)

	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results); err != nil {
		t.Fatalf("Update returned a Go error (would halt Raft apply): %v", err)
	}
	msg, ok := protocol.DecodeAppError(results[0].Data)
	if !ok {
		t.Fatalf("expected an encoded application refusal, got result=%+v", results[0])
	}
	if !strings.Contains(msg, "timestamp") {
		t.Fatalf("refusal message = %q, want it to name the missing timestamp", msg)
	}

	// The state machine is still usable: a well-formed append right after
	// the refusal must succeed, proving apply was never halted.
	results = applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: auditAppendCmd(t, "acme", auditEvent("acme"))}})
	if results[0].Value != 1 {
		t.Fatalf("sequence after the refusal = %d, want 1 (the refusal assigned no sequence)", results[0].Value)
	}
}
