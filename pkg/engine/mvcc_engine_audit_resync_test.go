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

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// This file mirrors mvcc_engine_lessor_resync_test.go exactly, for
// resyncAuditChain rather than resyncLessor (review finding N7): in
// standalone the engine IS the state machine and LocalBackend keeps serving
// after a failed apply, so a Chain.Append that mutated the in-memory head
// for a batch that never committed would leave the chain describing a
// sequence the store does not hold. A failed Update re-reads the audit
// table so the two agree again.

func resyncTestEvent(tenantID string) audit.AuditEvent {
	return audit.AuditEvent{
		TenantID:  tenantID,
		Actor:     "operator",
		Resource:  "barrier",
		Action:    "unseal",
		Outcome:   audit.OutcomeSuccess,
		Timestamp: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
	}
}

func TestMVCCEngine_FailedCommitRestoresTheAuditChain(t *testing.T) {
	stg := &commitFailingStorage{Storage: memory.NewStorage()}
	defer func() { _ = stg.Close() }()
	chain := audit.NewChain()
	eng, err := NewMVCCEngine(stg, WithMVCCAuditChain(chain))
	if err != nil {
		t.Fatal(err)
	}

	appendCmd := func() []byte {
		return mustEncode(protocol.EncodeAuditAppend("acme", resyncTestEvent("acme")))
	}

	kept := applyMVCCEntries(t, eng, []sm.Entry{{Index: 1, Cmd: appendCmd()}})
	keptSeq := kept[0].Value

	stg.fail.Store(true)
	err = eng.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: appendCmd()}}, make([]sm.Result, 1))
	if !errors.Is(err, errCommitRefused) {
		t.Fatalf("Update = %v, want the commit fault", err)
	}

	page, qerr := chain.Query(stg.Storage, "acme", audit.Filter{Limit: 100})
	if qerr != nil {
		t.Fatalf("query after failed commit: %v", qerr)
	}
	if uint64(len(page.Events)) != keptSeq {
		t.Fatalf("chain holds %d events after a failed commit, want only the committed %d", len(page.Events), keptSeq)
	}

	// The next append reuses nothing the failed batch claimed in memory only.
	stg.fail.Store(false)
	next := applyMVCCEntries(t, eng, []sm.Entry{{Index: 3, Cmd: appendCmd()}})
	if next[0].Value != keptSeq+1 {
		t.Fatalf("next sequence = %d, want %d", next[0].Value, keptSeq+1)
	}
}

// When the audit table cannot be read after a failed apply either, Update
// reports both failures: the chain may no longer match the store.
func TestMVCCEngine_FailedCommitOverAnUnreadableAuditTableReportsBoth(t *testing.T) {
	stg := &commitFailingStorage{Storage: memory.NewStorage()}
	defer func() { _ = stg.Close() }()
	eng, err := NewMVCCEngine(stg, WithMVCCAuditChain(audit.NewChain()))
	if err != nil {
		t.Fatal(err)
	}
	// A head record that fails decodeHead's length check (headRecordSize),
	// so Chain.Restore returns *audit.CorruptTableError rather than reading
	// cleanly.
	if err := stg.Storage.Set([]byte(audit.TablePrefix+"h/acme"), []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	stg.fail.Store(true)
	cmd := mustEncode(protocol.EncodeAuditAppend("acme", resyncTestEvent("acme")))
	err = eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, make([]sm.Result, 1))
	var resync *AuditChainResyncError
	if !errors.As(err, &resync) || !errors.Is(err, errCommitRefused) || resync.Error() == "" {
		t.Fatalf("Update = %v, want *AuditChainResyncError wrapping the commit fault", err)
	}
}
