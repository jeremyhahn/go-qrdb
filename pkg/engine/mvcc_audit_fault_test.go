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
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// This file completes N7's "malformed/unknown-subtype paths" ask for the
// MVCC engine family: pkg/store/pebble_audit_test.go already covers these
// (TestPebbleStateMachine_AuditAppend_MalformedEvent,
// _UnknownSubtype, _AuditPrune_MalformedCutoff), but mvcc_audit_test.go
// never did. It also adds the one halt-path pebble_audit_test.go documents
// as untestable there for want of a fault-injection seam on a raw
// *pebble.Batch: handleAuditAppend's *audit.TableWriteError branch. The MVCC
// engine's batch parameter is the storage.Batch INTERFACE, not a concrete
// type, so that seam exists here and this is a real red-then-green test,
// not a note explaining its absence.

var errAuditSetRefused = errors.New("audit table write refused by test")

// auditWriteFailingStorage wraps a storage.Storage so every batch it hands
// out refuses Set for any key under failPrefix, simulating a local storage
// fault specific to the replicated audit table without touching any other
// key this engine writes in the same batch (the revision counter, in
// particular, so only the audit write itself is the injected fault).
type auditWriteFailingStorage struct {
	storage.Storage
	failPrefix []byte
}

func (s *auditWriteFailingStorage) Batch() storage.Batch {
	return &auditWriteFailingBatch{Batch: s.Storage.Batch(), failPrefix: s.failPrefix}
}

type auditWriteFailingBatch struct {
	storage.Batch
	failPrefix []byte
}

func (b *auditWriteFailingBatch) Set(key, value []byte) error {
	if bytes.HasPrefix(key, b.failPrefix) {
		return errAuditSetRefused
	}
	return b.Batch.Set(key, value)
}

// TestMVCCEngine_AuditAppend_TableWriteFaultHaltsApply asserts a local
// storage fault staging the audit table into the batch halts apply (a raw
// Go error from Update), rather than being encoded as a per-command
// refusal: the fault is local to this replica, not a property of the
// committed entry every replica would reach identically, so answering it as
// a refusal would let this replica silently diverge from peers whose write
// to the same entry succeeded.
func TestMVCCEngine_AuditAppend_TableWriteFaultHaltsApply(t *testing.T) {
	stg := &auditWriteFailingStorage{Storage: memory.NewStorage(), failPrefix: []byte(audit.TablePrefix)}
	eng, err := NewMVCCEngine(stg, WithMVCCAuditChain(audit.NewChain()))
	if err != nil {
		t.Fatal(err)
	}

	entry := auditAppendEntry(t, 1, "acme", tenantEvent("acme", "barrier", "unseal"))
	results := make([]sm.Result, 1)
	err = eng.Update(context.Background(), []sm.Entry{entry}, results)
	if !errors.Is(err, errAuditSetRefused) {
		t.Fatalf("Update = %v, want it to wrap the injected table-write fault", err)
	}
}

// TestMVCCEngine_AuditAppend_MalformedEvent mirrors
// TestPebbleStateMachine_AuditAppend_MalformedEvent: a body that does not
// unmarshal into an AuditEvent is refused as an application result, not a
// halt (N3), because the outer command decoded fine and every replica
// reaches the identical malformed body from the identical committed entry.
func TestMVCCEngine_AuditAppend_MalformedEvent(t *testing.T) {
	eng, _ := newMVCCEngine(t, WithMVCCAuditChain(audit.NewChain()))
	entry := rawAuditAppendEntry(1, "acme", []byte("not json"))

	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{entry}, results); err != nil {
		t.Fatalf("Update returned a Go error (would halt apply): %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatalf("expected an encoded application refusal, got result=%+v", results[0])
	}
}

// TestMVCCEngine_AuditAppend_UnknownSubtype mirrors
// TestPebbleStateMachine_AuditAppend_UnknownSubtype: an unrecognised
// sub-type byte is a protocol error (a halt), unlike a malformed EVENT body,
// because it names a shape this binary does not understand at all.
func TestMVCCEngine_AuditAppend_UnknownSubtype(t *testing.T) {
	eng, _ := newMVCCEngine(t, WithMVCCAuditChain(audit.NewChain()))
	entry := rawAuditAppendEntry(1, "acme", nil)
	entry.Cmd[1] = 99 // the subtype byte, neither AuditSubtypeAppend nor AuditSubtypePrune

	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{entry}, results); err == nil {
		t.Fatal("expected an error for an unrecognised audit sub-type")
	}
}

// TestMVCCEngine_AuditPrune_MalformedCutoff mirrors
// TestPebbleStateMachine_AuditPrune_MalformedCutoff: a prune command whose
// cutoff is not exactly 8 bytes is a protocol error.
func TestMVCCEngine_AuditPrune_MalformedCutoff(t *testing.T) {
	eng, _ := newMVCCEngine(t, WithMVCCAuditChain(audit.NewChain()))
	entry := auditPruneEntry(1, "acme", 0)
	entry.Cmd = entry.Cmd[:len(entry.Cmd)-1] // truncate the cutoff by one byte

	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{entry}, results); err == nil {
		t.Fatal("expected an error for a truncated prune cutoff")
	}
}
