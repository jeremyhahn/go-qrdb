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
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

var engineLog = vlog.LogID{TenantID: "acme", Database: "certs", Log: "issued"}

func engineLogDescriptor() vlog.Descriptor {
	return vlog.Descriptor{Origin: "db.example.com/acme/certs", Hash: vlog.HashSHA256, VerifierKeys: []string{"key-1"}, AllowExpireBodies: true}
}

func vlogCreateEntry(idx uint64) sm.Entry {
	return sm.Entry{Index: idx, Cmd: mustEncode(protocol.EncodeVerifiableLogCreate(engineLog, engineLogDescriptor()))}
}

func vlogAppendEntry(idx uint64, bodies ...string) sm.Entry {
	entries := make([][]byte, len(bodies))
	for i, b := range bodies {
		entries[i] = []byte(b)
	}
	return sm.Entry{Index: idx, Cmd: mustEncode(protocol.EncodeVerifiableLogAppend(engineLog, entries))}
}

// vlogTable answers every named-log record in stg, key to value, so two
// replicas' tables compare byte for byte.
func vlogTable(t *testing.T, stg storage.Storage) map[string]string {
	t.Helper()
	keys, err := stg.List([]byte(vlog.LogTablePrefix))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		v, err := stg.Get(k)
		if err != nil {
			t.Fatalf("Get(%q): %v", k, err)
		}
		out[string(k)] = string(v)
	}
	return out
}

func lookupHead(t *testing.T, eng *MVCCEngine) *vlog.Head {
	t.Helper()
	got, err := eng.Lookup(context.Background(), vlog.HeadRequest{ID: engineLog})
	if err != nil {
		t.Fatalf("Lookup(HeadRequest): %v", err)
	}
	return got.(*vlog.Head)
}

// TestMVCCEngine_VerifiableLog_DispatchesAndAnswersReads applies every
// sub-type through Update and reads the log back through Lookup.
func TestMVCCEngine_VerifiableLog_DispatchesAndAnswersReads(t *testing.T) {
	eng, stg := newMVCCEngine(t, WithMVCCLogs(vlog.NewChain()))

	res := applyMVCCEntries(t, eng, []sm.Entry{
		vlogCreateEntry(1),
		vlogAppendEntry(2, "a", "b", "c"),
		vlogAppendEntry(3, "d"),
	})
	for i, r := range res {
		if _, ok := protocol.DecodeAppError(r.Data); ok {
			t.Fatalf("entry %d refused: %+v", i, r)
		}
	}
	if res[1].Value != 0 || res[2].Value != 3 {
		t.Fatalf("append results = %d, %d; want first indices 0 and 3", res[1].Value, res[2].Value)
	}

	head := lookupHead(t, eng)
	if head.Size != 4 {
		t.Fatalf("head size = %d, want 4", head.Size)
	}
	ckpt := sm.Entry{Index: 4, Cmd: mustEncode(protocol.EncodeVerifiableLogCheckpoint(engineLog, 4, head.Root, []byte("signed-4")))}
	expire := sm.Entry{Index: 5, Cmd: mustEncode(protocol.EncodeVerifiableLogExpireBodies(engineLog, 1))}
	res = applyMVCCEntries(t, eng, []sm.Entry{ckpt, expire})
	if _, ok := protocol.DecodeAppError(res[0].Data); ok {
		t.Fatalf("checkpoint refused: %+v", res[0])
	}
	if res[1].Value != 1 {
		t.Fatalf("expire result = %d, want 1", res[1].Value)
	}

	head = lookupHead(t, eng)
	if head.CheckpointSize != 4 || string(head.Checkpoint) != "signed-4" || head.BodiesFrom != 1 {
		t.Fatalf("head = %+v, want checkpoint 4 and bodies from 1", head)
	}

	for _, req := range []vlog.Request{
		vlog.GetRequest{ID: engineLog, From: 0, Limit: 10},
		vlog.InclusionRequest{ID: engineLog, Index: 2, Size: 4},
		vlog.ConsistencyRequest{ID: engineLog, Size1: 1, Size2: 4},
	} {
		got, err := eng.Lookup(context.Background(), req)
		if err != nil {
			t.Fatalf("Lookup(%T): %v", req, err)
		}
		want, err := vlog.Read(stg, req)
		if err != nil {
			t.Fatalf("Read(%T): %v", req, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Lookup(%T) = %#v, want %#v", req, got, want)
		}
	}
}

// TestMVCCEngine_VerifiableLog_RefusalVersusHalt pins the classification at
// the engine: a missing log or a malformed body is a refusal in the result,
// an unknown sub-type halts Update.
func TestMVCCEngine_VerifiableLog_RefusalVersusHalt(t *testing.T) {
	eng, _ := newMVCCEngine(t, WithMVCCLogs(vlog.NewChain()))

	missing := vlogAppendEntry(1, "x")
	malformed := vlogAppendEntry(2, "x")
	malformed.Cmd = malformed.Cmd[:len(malformed.Cmd)-1]
	res := applyMVCCEntries(t, eng, []sm.Entry{missing, malformed})
	for i, r := range res {
		if _, kind, ok := protocol.DecodeAppErrorKind(r.Data); !ok || kind != protocol.AppErrorInvalid {
			t.Fatalf("entry %d result = %+v, want an invalid refusal", i, r)
		}
	}

	unknown := vlogAppendEntry(3, "x")
	unknown.Cmd = bytes.Clone(unknown.Cmd)
	unknown.Cmd[1] = 0xFF // the sub-type, after the opcode byte
	var ice *protocol.InvalidCommandError
	if err := eng.Update(context.Background(), []sm.Entry{unknown}, make([]sm.Result, 1)); !errors.As(err, &ice) {
		t.Fatalf("Update(unknown sub-type) = %v, want *protocol.InvalidCommandError", err)
	}
}

// TestMVCCEngine_VerifiableLog_RefusedWithNoRegistry: an engine built
// without a registry answers the opcode as an absent capability.
func TestMVCCEngine_VerifiableLog_RefusedWithNoRegistry(t *testing.T) {
	eng, _ := newMVCCEngine(t)
	res := applyMVCCEntries(t, eng, []sm.Entry{vlogCreateEntry(1)})
	if _, kind, ok := protocol.DecodeAppErrorKind(res[0].Data); !ok || kind != protocol.AppErrorCapability {
		t.Fatalf("result = %+v, want a capability refusal", res[0])
	}
	if _, err := eng.Lookup(context.Background(), vlog.HeadRequest{ID: engineLog}); err == nil {
		t.Fatal("Lookup(HeadRequest) with no registry succeeded")
	}
}

// TestMVCCEngine_AuditAppend_WritesNoMerkleNode: OpAuditAppend's apply is
// unchanged, so it writes only the linear chain and never a named-log record.
func TestMVCCEngine_AuditAppend_WritesNoMerkleNode(t *testing.T) {
	eng, stg := newMVCCEngine(t, WithMVCCLogs(vlog.NewChain()))
	applyMVCCEntries(t, eng, []sm.Entry{auditAppendEntry(t, 1, "acme", tenantEvent("acme", "barrier", "unseal"))})
	if table := vlogTable(t, stg); len(table) != 0 {
		t.Fatalf("OpAuditAppend wrote %d named-log records, want none", len(table))
	}
	audits, err := stg.List([]byte(vlog.TablePrefix))
	if err != nil || len(audits) == 0 {
		t.Fatalf("audit table = %d records, %v; want the appended event", len(audits), err)
	}
}

// TestMVCCEngine_VerifiableLog_RestartMatchesAPeer: an engine reopened over
// its storage mid-log serves the same head and proofs as a peer that never
// restarted, and its next append writes the same records.
func TestMVCCEngine_VerifiableLog_RestartMatchesAPeer(t *testing.T) {
	peer, peerStg := newMVCCEngine(t, WithMVCCLogs(vlog.NewChain()))
	stg := memory.NewStorage()
	first, err := NewMVCCEngine(stg, WithMVCCLogs(vlog.NewChain()))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	before := []sm.Entry{vlogCreateEntry(1), vlogAppendEntry(2, "a", "b", "c", "d", "e")}
	applyMVCCEntries(t, peer, before)
	applyMVCCEntries(t, first, before)

	restarted, err := NewMVCCEngine(stg, WithMVCCLogs(vlog.NewChain()))
	if err != nil {
		t.Fatalf("NewMVCCEngine (restart): %v", err)
	}
	if got, want := lookupHead(t, restarted), lookupHead(t, peer); !reflect.DeepEqual(got, want) {
		t.Fatalf("restarted head = %+v, peer head = %+v", got, want)
	}

	after := []sm.Entry{vlogAppendEntry(3, "f", "g", "h")}
	pr := applyMVCCEntries(t, peer, after)
	rr := applyMVCCEntries(t, restarted, after)
	if pr[0].Value != 5 || rr[0].Value != pr[0].Value {
		t.Fatalf("next append first index: restarted %d, peer %d, want 5", rr[0].Value, pr[0].Value)
	}
	if got, want := vlogTable(t, stg), vlogTable(t, peerStg); !reflect.DeepEqual(got, want) {
		t.Fatalf("restarted replica's log table differs from its peer's")
	}
	proofReq := vlog.ConsistencyRequest{ID: engineLog, Size1: 3, Size2: 8}
	got, err := restarted.Lookup(context.Background(), proofReq)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	want, err := peer.Lookup(context.Background(), proofReq)
	if err != nil {
		t.Fatalf("Lookup (peer): %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("restarted replica's consistency proof differs from its peer's")
	}
}

// TestMVCCEngine_FailedCommitRestoresTheNamedLogs: a batch that never
// committed leaves no trace in the registry, so the next append assigns the
// indices the failed one claimed in memory only.
func TestMVCCEngine_FailedCommitRestoresTheNamedLogs(t *testing.T) {
	stg := &commitFailingStorage{Storage: memory.NewStorage()}
	defer func() { _ = stg.Close() }()
	eng, err := NewMVCCEngine(stg, WithMVCCLogs(vlog.NewChain()))
	if err != nil {
		t.Fatal(err)
	}
	applyMVCCEntries(t, eng, []sm.Entry{vlogCreateEntry(1), vlogAppendEntry(2, "a", "b")})

	stg.fail.Store(true)
	if err := eng.Update(context.Background(), []sm.Entry{vlogAppendEntry(3, "lost")}, make([]sm.Result, 1)); !errors.Is(err, errCommitRefused) {
		t.Fatalf("Update = %v, want the commit fault", err)
	}

	stg.fail.Store(false)
	next := applyMVCCEntries(t, eng, []sm.Entry{vlogAppendEntry(4, "c")})
	if next[0].Value != 2 {
		t.Fatalf("next append first index = %d, want 2", next[0].Value)
	}
	entries, err := vlog.Get(stg.Storage, engineLog, 0, 10)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(entries) != 3 || string(entries[2].Body) != "c" {
		t.Fatalf("entries = %+v, want a, b, c", entries)
	}
}
