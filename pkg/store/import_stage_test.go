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

// go-qrdb#576's security review finding 3: in an IsolationRaft database a
// client's commands and queries reach a state machine's Update/Lookup
// directly, bypassing pkg/service entirely, so the ONLY place a staged
// import record can be made unreachable from the client data plane is
// inside the state machine itself -- exactly the guard the lease table
// already has (TestPebbleSM_RefusesUserKeysInTheReservedNamespace). These
// tests drive both lease-capable state machines (forEachLeaseSM: "pebble",
// the daemon's default, and "mvcc") through the same committed-command
// path a client's own request takes, and prove a staged record can be
// neither read, nor overwritten, nor forged by naming its literal key.

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/importstage"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// stageSession applies an ImportStageSetMeta command under leaseID and
// returns the session's literal metadata key.
func (h *leaseHarness) stageSession(sessionID, tenantID string, leaseID int64, body string) []byte {
	h.t.Helper()
	h.applyOK(protocol.EncodeImportStage(protocol.ImportStageCommand{
		Kind:      protocol.ImportStageSetMeta,
		SessionID: sessionID,
		LeaseID:   leaseID,
		Body:      []byte(body),
		TenantID:  tenantID,
	}))
	return importstage.MetaKey(sessionID)
}

// stageChunk applies an ImportStageSetChunk command. metaBody, when
// non-empty, also updates the session's metadata record in the same call.
func (h *leaseHarness) stageChunk(sessionID string, seq uint64, leaseID int64, body, metaBody string) {
	h.t.Helper()
	cmd := protocol.ImportStageCommand{
		Kind:      protocol.ImportStageSetChunk,
		SessionID: sessionID,
		LeaseID:   leaseID,
		Seq:       seq,
		Body:      []byte(body),
	}
	if metaBody != "" {
		cmd.MetaBody = []byte(metaBody)
	}
	h.applyOK(protocol.EncodeImportStage(cmd))
}

// chunkBody reads sessionID's chunk seq back through the structured query
// every transport's ImportCommit actually uses
// (pkg/service.ImportStageChunkQuery).
func (h *leaseHarness) chunkBody(sessionID string, seq uint64) (string, bool) {
	h.t.Helper()
	v, err := h.sm.Lookup(context.Background(), service.ImportStageChunkQuery{SessionID: sessionID, Seq: seq})
	if err != nil {
		h.t.Fatalf("ImportStageChunkQuery: %v", err)
	}
	b, ok := v.([]byte)
	if !ok || b == nil {
		return "", false
	}
	return string(b), true
}

// openSessions reads tenantID's open-session count through the structured
// query ImportBegin's quota check answers from
// (pkg/service.ImportStageOpenSessionsQuery).
func (h *leaseHarness) openSessions(tenantID string) int {
	h.t.Helper()
	v, err := h.sm.Lookup(context.Background(), service.ImportStageOpenSessionsQuery{TenantID: tenantID})
	if err != nil {
		h.t.Fatalf("ImportStageOpenSessionsQuery: %v", err)
	}
	n, ok := v.(int)
	if !ok {
		h.t.Fatalf("ImportStageOpenSessionsQuery returned %T, want int", v)
	}
	return n
}

// metaBody reads sessionID's metadata back through the structured query
// every transport's ImportAppend/ImportCommit/ImportAbort actually uses
// (pkg/service.ImportStageMetaQuery), never through a raw key -- proving
// what the real record holds, independent of whatever the test's client-side
// attack attempted against the literal key.
func (h *leaseHarness) metaBody(sessionID string) (string, bool) {
	h.t.Helper()
	v, err := h.sm.Lookup(context.Background(), service.ImportStageMetaQuery{SessionID: sessionID})
	if err != nil {
		h.t.Fatalf("ImportStageMetaQuery: %v", err)
	}
	b, ok := v.([]byte)
	if !ok || b == nil {
		return "", false
	}
	return string(b), true
}

// TestImportStage_ClientCannotReadStagedRecord proves a plain Get/BytesLookup
// of a staged session's literal key -- the read path go-qrdb#576's security
// review found answered with the record's data on sm_type pebble -- reads as
// absent on every lease-capable state machine, exactly like a lease-table
// record.
func TestImportStage_ClientCannotReadStagedRecord(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		leaseID := h.grant(60)
		metaKey := h.stageSession("sess-read", "acme", leaseID, "the real metadata")

		if _, ok := h.value(string(metaKey)); ok {
			t.Fatalf("a plain Get of %q returned the staged record; it must read as absent", metaKey)
		}

		// BytesLookup is the same guard through go-quicraft's fast NALookup
		// path (see getClientKey's doc comment); confirm it independently
		// of h.value, which always calls Lookup.
		if bl, ok := h.sm.(interface {
			BytesLookup(key []byte) ([]byte, error)
		}); ok {
			v, err := bl.BytesLookup(metaKey)
			if err == nil && v != nil {
				t.Fatalf("BytesLookup(%q) returned %q; it must read as absent", metaKey, v)
			}
		}

		// The record itself is untouched: the real path still sees it.
		if body, ok := h.metaBody("sess-read"); !ok || body != "the real metadata" {
			t.Fatalf("ImportStageMetaQuery after a refused read = (%q, %v), want (%q, true)", body, ok, "the real metadata")
		}
	})
}

// TestImportStage_ClientCannotOverwriteStagedRecord proves a plain write
// naming a staged session's literal key -- an ordinary MVCC put, the same
// shape a client Put encodes -- is refused before it stages anything, on
// every lease-capable state machine. On "pebble" the write is refused
// outright (ReservedKeyRefusal, the reserved 0x00 namespace). On "mvcc" the
// write is not itself refused -- s.PutInBatch writes through mvcc.Store's
// own key encoding (EncodeDataKey), a physically different key than the
// raw batch.Set handleImportStage used, so the two can never collide -- but
// it must still leave the real record, read back through the structured
// query, untouched. Either way the security property under test is the
// same: the attacker's write never reaches the real record.
func TestImportStage_ClientCannotOverwriteStagedRecord(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		leaseID := h.grant(60)
		metaKey := h.stageSession("sess-overwrite", "acme", leaseID, "the real metadata")

		h.apply(protocol.EncodeMVCCPut(metaKey, []byte("attacker-controlled"), lease.NoLease))

		if body, ok := h.metaBody("sess-overwrite"); !ok || body != "the real metadata" {
			t.Fatalf("ImportStageMetaQuery after an attempted overwrite = (%q, %v), want (%q, true)", body, ok, "the real metadata")
		}
	})
}

// TestImportStage_ClientCannotForgeMetaRecord is
// TestImportStage_ClientCannotOverwriteStagedRecord's variant for a session
// the attacker never staged at all: naming the exact /meta key format a
// session it does not control (or one that never existed) would use, to
// manufacture a metadata record ImportCommit would later trust. The
// forged write never becomes a real staged record, by the same reasoning as
// the overwrite case above.
func TestImportStage_ClientCannotForgeMetaRecord(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		forgedKey := importstage.MetaKey("never-staged-by-anyone")

		h.apply(protocol.EncodeMVCCPut(forgedKey, []byte(`{"Tenant":"acme","KeyPrefix":""}`), lease.NoLease))

		if _, ok := h.metaBody("never-staged-by-anyone"); ok {
			t.Fatalf("the forged record at %q is visible through ImportStageMetaQuery", forgedKey)
		}
	})
}

// TestImportStage_SetChunkUpdatesMetaInTheSameCall proves ImportStageSetChunk
// writes the chunk and (when MetaBody is non-empty) the session's updated
// metadata record in the SAME apply call, exactly as putRawBatch committed
// both as one Raft command before pkg/importstage existed.
func TestImportStage_SetChunkUpdatesMetaInTheSameCall(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		leaseID := h.grant(60)
		h.stageSession("sess-chunk", "acme", leaseID, "initial metadata")

		h.stageChunk("sess-chunk", 0, leaseID, "chunk-zero-bytes", "updated metadata")

		if body, ok := h.chunkBody("sess-chunk", 0); !ok || body != "chunk-zero-bytes" {
			t.Fatalf("ImportStageChunkQuery(seq=0) = (%q, %v), want (%q, true)", body, ok, "chunk-zero-bytes")
		}
		if body, ok := h.metaBody("sess-chunk"); !ok || body != "updated metadata" {
			t.Fatalf("ImportStageMetaQuery after the chunk write = (%q, %v), want (%q, true)", body, ok, "updated metadata")
		}
	})
}

// TestImportStage_SetChunkWithNoMetaBodyLeavesMetaUnchanged is
// TestImportStage_SetChunkUpdatesMetaInTheSameCall's complement: an empty
// MetaBody (ImportAppend's replay path, which stages nothing new) writes
// only the chunk, leaving the session's existing metadata exactly as it was.
func TestImportStage_SetChunkWithNoMetaBodyLeavesMetaUnchanged(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		leaseID := h.grant(60)
		h.stageSession("sess-chunk-nometa", "acme", leaseID, "untouched metadata")

		h.stageChunk("sess-chunk-nometa", 0, leaseID, "chunk-zero-bytes", "")

		if body, ok := h.chunkBody("sess-chunk-nometa", 0); !ok || body != "chunk-zero-bytes" {
			t.Fatalf("ImportStageChunkQuery(seq=0) = (%q, %v), want (%q, true)", body, ok, "chunk-zero-bytes")
		}
		if body, ok := h.metaBody("sess-chunk-nometa"); !ok || body != "untouched metadata" {
			t.Fatalf("ImportStageMetaQuery after a no-MetaBody chunk write = (%q, %v), want (%q, true)", body, ok, "untouched metadata")
		}
	})
}

// TestImportStage_OpenSessionsQuotaRefusesAtLimit proves ImportStageSetMeta's
// MaxSessions check refuses a session that would exceed the tenant's
// configured concurrent-session quota, counted through the SAME tenant
// index (pkg/importstage.TenantIndexKey) ImportStageOpenSessionsQuery reads
// -- and that the refused session's metadata is never written at all.
func TestImportStage_OpenSessionsQuotaRefusesAtLimit(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		leaseA := h.grant(60)
		h.stageSession("sess-quota-a", "acme", leaseA, "a")
		if n := h.openSessions("acme"); n != 1 {
			t.Fatalf("openSessions(acme) = %d, want 1", n)
		}

		leaseB := h.grant(60)
		res := h.apply(protocol.EncodeImportStage(protocol.ImportStageCommand{
			Kind:        protocol.ImportStageSetMeta,
			SessionID:   "sess-quota-b",
			LeaseID:     leaseB,
			Body:        []byte("b"),
			TenantID:    "acme",
			MaxSessions: 1,
		}))
		if _, _, ok := protocol.DecodeAppErrorKind(res.Data); !ok {
			t.Fatalf("a second session over MaxSessions=1 was not refused (result=%q)", res.Data)
		}

		// The refused session's metadata was never written, and the open
		// count is still exactly the one session that was.
		if _, ok := h.metaBody("sess-quota-b"); ok {
			t.Error("the refused session's metadata is visible through ImportStageMetaQuery")
		}
		if n := h.openSessions("acme"); n != 1 {
			t.Fatalf("openSessions(acme) after the refusal = %d, want 1", n)
		}

		// A DIFFERENT tenant is unaffected by acme's quota.
		leaseC := h.grant(60)
		h.stageSession("sess-quota-c", "globex", leaseC, "c")
		if n := h.openSessions("globex"); n != 1 {
			t.Fatalf("openSessions(globex) = %d, want 1", n)
		}
	})
}

// TestImportStage_UnknownLeaseRefused proves ImportStageSetMeta refuses a
// lease ID the lessor does not hold -- never staged, bound to nothing that
// would ever reclaim it -- rather than writing an orphaned record.
func TestImportStage_UnknownLeaseRefused(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		const neverGranted int64 = 999999

		res := h.apply(protocol.EncodeImportStage(protocol.ImportStageCommand{
			Kind:      protocol.ImportStageSetMeta,
			SessionID: "sess-unknown-lease",
			LeaseID:   neverGranted,
			Body:      []byte("x"),
			TenantID:  "acme",
		}))
		if _, _, ok := protocol.DecodeAppErrorKind(res.Data); !ok {
			t.Fatalf("a session naming an unknown lease was not refused (result=%q)", res.Data)
		}
		if _, ok := h.metaBody("sess-unknown-lease"); ok {
			t.Error("the orphaned session's metadata is visible through ImportStageMetaQuery")
		}
	})
}

// TestImportStage_NoLeaseRefused is TestImportStage_UnknownLeaseRefused's
// variant for lease.NoLease itself: a staged record with no lease at all
// would never be reclaimed, so it is refused the same way an ordinary
// write's OPTIONAL no-lease is not.
func TestImportStage_NoLeaseRefused(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		res := h.apply(protocol.EncodeImportStage(protocol.ImportStageCommand{
			Kind:      protocol.ImportStageSetMeta,
			SessionID: "sess-no-lease",
			LeaseID:   lease.NoLease,
			Body:      []byte("x"),
			TenantID:  "acme",
		}))
		if _, _, ok := protocol.DecodeAppErrorKind(res.Data); !ok {
			t.Fatalf("a session naming lease.NoLease was not refused (result=%q)", res.Data)
		}
		if _, ok := h.metaBody("sess-no-lease"); ok {
			t.Error("the lease-less session's metadata is visible through ImportStageMetaQuery")
		}
	})
}

// TestImportStage_MalformedPayloadHaltsApply proves a committed
// OpImportStage entry too short for protocol.DecodeImportStage to decode --
// bytes no writer of this version produced -- halts Apply on every lease
// capable state machine, exactly like any other corrupt committed command,
// rather than being answered as a soft per-request refusal.
func TestImportStage_MalformedPayloadHaltsApply(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		// protocol.OpImportStage followed by a kind byte and nothing else:
		// DecodeImportStage requires at least 3 bytes.
		cmd := []byte{protocol.OpImportStage, byte(protocol.ImportStageSetMeta)}
		results := make([]sm.Result, 1)
		err := h.sm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results)
		if err == nil {
			t.Fatal("expected Update to fail for a too-short OpImportStage payload")
		}
		var ice *protocol.InvalidCommandError
		if !errors.As(err, &ice) {
			t.Fatalf("error = %T (%v), want *protocol.InvalidCommandError", err, err)
		}
	})
}
