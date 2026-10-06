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

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// importStageEntry builds a committed OpImportStage entry.
func importStageEntry(idx uint64, cmd protocol.ImportStageCommand) sm.Entry {
	return sm.Entry{Index: idx, Cmd: mustEncode(protocol.EncodeImportStage(cmd))}
}

// grantLease grants a lease through eng's own Update path and returns its ID.
func grantLease(t *testing.T, eng *MVCCEngine, ttl int64) int64 {
	t.Helper()
	cmd := mustEncode(protocol.EncodeLeaseGrant(ttl))
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results); err != nil {
		t.Fatalf("lease grant: %v", err)
	}
	return int64(results[0].Value)
}

func TestMVCCEngine_ImportStage_SetMetaAndRead(t *testing.T) {
	eng, stg := newMVCCEngine(t, WithMVCCLessor(lease.NewLessor()))
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	leaseID := grantLease(t, eng, 60)

	results := make([]sm.Result, 1)
	entry := importStageEntry(2, protocol.ImportStageCommand{
		Kind: protocol.ImportStageSetMeta, SessionID: "sess1", LeaseID: leaseID,
		Body: []byte("the metadata"), TenantID: "acme",
	})
	if err := eng.Update(context.Background(), []sm.Entry{entry}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, refused := protocol.DecodeAppError(results[0].Data); refused {
		t.Fatalf("ImportStageSetMeta was refused: %q", results[0].Data)
	}

	got, err := eng.Lookup(context.Background(), service.ImportStageMetaQuery{SessionID: "sess1"})
	if err != nil {
		t.Fatalf("ImportStageMetaQuery: %v", err)
	}
	if string(got.([]byte)) != "the metadata" {
		t.Fatalf("ImportStageMetaQuery = %q, want %q", got, "the metadata")
	}

	if n, err := eng.Lookup(context.Background(), service.ImportStageOpenSessionsQuery{TenantID: "acme"}); err != nil || n.(int) != 1 {
		t.Fatalf("ImportStageOpenSessionsQuery = (%v, %v), want (1, nil)", n, err)
	}
}

func TestMVCCEngine_ImportStage_SetChunkUpdatesMetaInSameCall(t *testing.T) {
	eng, stg := newMVCCEngine(t, WithMVCCLessor(lease.NewLessor()))
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	leaseID := grantLease(t, eng, 60)
	results := make([]sm.Result, 1)
	begin := importStageEntry(2, protocol.ImportStageCommand{
		Kind: protocol.ImportStageSetMeta, SessionID: "sess1", LeaseID: leaseID,
		Body: []byte("initial"), TenantID: "acme",
	})
	if err := eng.Update(context.Background(), []sm.Entry{begin}, results); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}

	chunk := importStageEntry(3, protocol.ImportStageCommand{
		Kind: protocol.ImportStageSetChunk, SessionID: "sess1", LeaseID: leaseID,
		Seq: 0, Body: []byte("chunk-zero"), MetaBody: []byte("updated"),
	})
	results = make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{chunk}, results); err != nil {
		t.Fatalf("SetChunk: %v", err)
	}

	chunkGot, err := eng.Lookup(context.Background(), service.ImportStageChunkQuery{SessionID: "sess1", Seq: 0})
	if err != nil || string(chunkGot.([]byte)) != "chunk-zero" {
		t.Fatalf("ImportStageChunkQuery = (%v, %v), want (\"chunk-zero\", nil)", chunkGot, err)
	}
	metaGot, err := eng.Lookup(context.Background(), service.ImportStageMetaQuery{SessionID: "sess1"})
	if err != nil || string(metaGot.([]byte)) != "updated" {
		t.Fatalf("ImportStageMetaQuery after chunk write = (%v, %v), want (\"updated\", nil)", metaGot, err)
	}
}

func TestMVCCEngine_ImportStage_QuotaRefusesAtLimit(t *testing.T) {
	eng, stg := newMVCCEngine(t, WithMVCCLessor(lease.NewLessor()))
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	leaseA := grantLease(t, eng, 60)
	resultsA := make([]sm.Result, 1)
	entryA := importStageEntry(2, protocol.ImportStageCommand{
		Kind: protocol.ImportStageSetMeta, SessionID: "sess-a", LeaseID: leaseA,
		Body: []byte("a"), TenantID: "acme", MaxSessions: 1,
	})
	if err := eng.Update(context.Background(), []sm.Entry{entryA}, resultsA); err != nil {
		t.Fatalf("first SetMeta: %v", err)
	}
	if _, refused := protocol.DecodeAppError(resultsA[0].Data); refused {
		t.Fatalf("first session was refused: %q", resultsA[0].Data)
	}

	leaseB := grantLease(t, eng, 60)
	resultsB := make([]sm.Result, 1)
	entryB := importStageEntry(4, protocol.ImportStageCommand{
		Kind: protocol.ImportStageSetMeta, SessionID: "sess-b", LeaseID: leaseB,
		Body: []byte("b"), TenantID: "acme", MaxSessions: 1,
	})
	if err := eng.Update(context.Background(), []sm.Entry{entryB}, resultsB); err != nil {
		t.Fatalf("second SetMeta: %v", err)
	}
	if _, refused := protocol.DecodeAppError(resultsB[0].Data); !refused {
		t.Fatalf("a second session over MaxSessions=1 was not refused (result=%q)", resultsB[0].Data)
	}

	if n, err := eng.Lookup(context.Background(), service.ImportStageOpenSessionsQuery{TenantID: "acme"}); err != nil || n.(int) != 1 {
		t.Fatalf("ImportStageOpenSessionsQuery = (%v, %v), want (1, nil)", n, err)
	}
}

func TestMVCCEngine_ImportStage_UnknownLeaseRefused(t *testing.T) {
	eng, stg := newMVCCEngine(t, WithMVCCLessor(lease.NewLessor()))
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	results := make([]sm.Result, 1)
	entry := importStageEntry(1, protocol.ImportStageCommand{
		Kind: protocol.ImportStageSetMeta, SessionID: "sess1", LeaseID: 999999,
		Body: []byte("x"), TenantID: "acme",
	})
	if err := eng.Update(context.Background(), []sm.Entry{entry}, results); err != nil {
		t.Fatalf("Update must not return a fatal error for an unknown lease: %v", err)
	}
	if _, refused := protocol.DecodeAppError(results[0].Data); !refused {
		t.Fatalf("a session naming an unknown lease was not refused (result=%q)", results[0].Data)
	}
}

func TestMVCCEngine_ImportStage_NoLessorConfigured(t *testing.T) {
	eng, stg := newMVCCEngine(t) // no lessor
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	results := make([]sm.Result, 1)
	entry := importStageEntry(1, protocol.ImportStageCommand{
		Kind: protocol.ImportStageSetMeta, SessionID: "sess1", LeaseID: 1,
		Body: []byte("x"), TenantID: "acme",
	})
	if err := eng.Update(context.Background(), []sm.Entry{entry}, results); err != nil {
		t.Fatalf("Update must not return a fatal error with no lessor configured: %v", err)
	}
	if _, kind, ok := protocol.DecodeAppErrorKind(results[0].Data); !ok || kind != protocol.AppErrorCapability {
		t.Fatalf("SetMeta with no lessor: ok=%v kind=%d, want AppErrorCapability", ok, kind)
	}
}

func TestMVCCEngine_ImportStage_MalformedPayloadIsFatal(t *testing.T) {
	eng, stg := newMVCCEngine(t, WithMVCCLessor(lease.NewLessor()))
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// protocol.OpImportStage followed by a kind byte and nothing else:
	// DecodeImportStage requires at least 3 bytes.
	cmd := []byte{protocol.OpImportStage, byte(protocol.ImportStageSetMeta)}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results)
	if err == nil {
		t.Fatal("expected Update to fail for a too-short OpImportStage payload")
	}
	var ice *protocol.InvalidCommandError
	if !errors.As(err, &ice) {
		t.Fatalf("error = %T (%v), want *protocol.InvalidCommandError", err, err)
	}
}
