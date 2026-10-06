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

package grpc

import (
	"bytes"
	"context"
	"testing"

	"google.golang.org/grpc/codes"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// sessionBatchService is a BatchServicer that keeps real per-session state
// for the chunked import RPCs: chunks must arrive in sequence, commit counts
// the staged rows, and a finished session is gone.
type sessionBatchService struct {
	mockBatchService
	begun    service.ImportOptions
	sessions map[string][][]byte
}

func newSessionBatchService() *sessionBatchService {
	return &sessionBatchService{sessions: map[string][][]byte{}}
}

func (m *sessionBatchService) ImportBegin(_ context.Context, opts service.ImportOptions) (string, error) {
	if opts.Format == "" {
		return "", &service.ImportInvalidFormatError{Format: opts.Format}
	}
	m.begun = opts
	m.sessions["sess-1"] = nil
	return "sess-1", nil
}

func (m *sessionBatchService) ImportAppend(_ context.Context, id string, seq uint64, chunk []byte) error {
	chunks, ok := m.sessions[id]
	if !ok {
		return &service.ImportSessionNotFoundError{SessionID: id}
	}
	if seq != uint64(len(chunks)) {
		return &service.ImportSequenceError{SessionID: id, Got: seq, Want: uint64(len(chunks))}
	}
	m.sessions[id] = append(chunks, chunk)
	return nil
}

func (m *sessionBatchService) ImportCommit(_ context.Context, id string) (*service.ImportResult, error) {
	chunks, ok := m.sessions[id]
	if !ok {
		return nil, &service.ImportSessionNotFoundError{SessionID: id}
	}
	delete(m.sessions, id)
	rows := bytes.Count(bytes.Join(chunks, nil), []byte("\n"))
	return &service.ImportResult{
		Total: rows, Succeeded: rows - 1, Failed: 1,
		Errors: []service.ImportError{{Row: rows, Key: "bad", Message: "invalid value"}},
	}, nil
}

func (m *sessionBatchService) ImportAbort(_ context.Context, id string) error {
	if _, ok := m.sessions[id]; !ok {
		return &service.ImportSessionNotFoundError{SessionID: id}
	}
	delete(m.sessions, id)
	return nil
}

func TestImportSessionRPCs_BeginAppendCommit(t *testing.T) {
	batch := newSessionBatchService()
	s := &qrdbServiceServer{batchSvc: batch}
	ctx := context.Background()

	begin, err := s.ImportBegin(ctx, &pb.ImportBeginRequest{
		Format: "csv", Filename: "rows.csv", KeyField: "id", ValueMode: "field",
		ValueField: "v", SkipHeader: true, Transactional: true,
	})
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}
	want := service.ImportOptions{Format: "csv", Filename: "rows.csv", KeyField: "id", ValueMode: "field", ValueField: "v", SkipHeader: true, Transactional: true}
	if batch.begun != want {
		t.Fatalf("service saw %+v, want %+v", batch.begun, want)
	}
	id := begin.GetSessionId()

	for seq, chunk := range []string{"a,1\nb,2\n", "bad,x\n"} {
		if _, err := s.ImportAppend(ctx, &pb.ImportAppendRequest{SessionId: id, Seq: uint64(seq), Chunk: []byte(chunk)}); err != nil {
			t.Fatalf("ImportAppend seq %d: %v", seq, err)
		}
	}
	// A gap in the sequence is the caller's error, not the server's.
	_, err = s.ImportAppend(ctx, &pb.ImportAppendRequest{SessionId: id, Seq: 7, Chunk: []byte("c,3\n")})
	requireCode(t, err, codes.InvalidArgument)

	res, err := s.ImportCommit(ctx, &pb.ImportCommitRequest{SessionId: id})
	if err != nil {
		t.Fatalf("ImportCommit: %v", err)
	}
	if res.GetTotal() != 3 || res.GetSucceeded() != 2 || res.GetFailed() != 1 || len(res.GetErrors()) != 1 {
		t.Fatalf("commit = %+v", res)
	}
	if e := res.GetErrors()[0]; e.GetRow() != 3 || e.GetKey() != "bad" || e.GetMessage() != "invalid value" {
		t.Fatalf("row error = %+v", e)
	}

	// The session is consumed by commit.
	_, err = s.ImportCommit(ctx, &pb.ImportCommitRequest{SessionId: id})
	requireCode(t, err, codes.NotFound)
}

func TestImportSessionRPCs_AbortAndErrors(t *testing.T) {
	batch := newSessionBatchService()
	s := &qrdbServiceServer{batchSvc: batch}
	ctx := context.Background()

	_, err := s.ImportBegin(ctx, &pb.ImportBeginRequest{})
	requireCode(t, err, codes.InvalidArgument)

	begin, err := s.ImportBegin(ctx, &pb.ImportBeginRequest{Format: "jsonl"})
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}
	if _, err := s.ImportAbort(ctx, &pb.ImportAbortRequest{SessionId: begin.GetSessionId()}); err != nil {
		t.Fatalf("ImportAbort: %v", err)
	}
	if len(batch.sessions) != 0 {
		t.Fatalf("abort left sessions %v", batch.sessions)
	}
	_, err = s.ImportAbort(ctx, &pb.ImportAbortRequest{SessionId: begin.GetSessionId()})
	requireCode(t, err, codes.NotFound)
	_, err = s.ImportAppend(ctx, &pb.ImportAppendRequest{SessionId: begin.GetSessionId()})
	requireCode(t, err, codes.NotFound)
}

func TestImportSessionRPCs_NoBatchServiceIsUnimplemented(t *testing.T) {
	s := &qrdbServiceServer{}
	ctx := context.Background()
	calls := map[string]func() error{
		"begin":  func() error { _, err := s.ImportBegin(ctx, &pb.ImportBeginRequest{Format: "csv"}); return err },
		"append": func() error { _, err := s.ImportAppend(ctx, &pb.ImportAppendRequest{SessionId: "s"}); return err },
		"commit": func() error { _, err := s.ImportCommit(ctx, &pb.ImportCommitRequest{SessionId: "s"}); return err },
		"abort":  func() error { _, err := s.ImportAbort(ctx, &pb.ImportAbortRequest{SessionId: "s"}); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) { requireCode(t, call(), codes.Unimplemented) })
	}
}

// TestGuardedKVRPCs exercises the claim / edit / release cycle the guarded
// primitives exist for, through the RPC layer.
func TestGuardedKVRPCs(t *testing.T) {
	kv := newMockKVService()
	s := &qrdbServiceServer{kvSvc: kv}
	ctx := context.Background()

	claim, err := s.PutIfAbsent(ctx, &pb.PutIfAbsentRequest{Key: "lock", Value: []byte("owner-a")})
	if err != nil || !claim.GetStored() {
		t.Fatalf("first claim: stored=%v err=%v", claim.GetStored(), err)
	}
	claim, err = s.PutIfAbsent(ctx, &pb.PutIfAbsentRequest{Key: "lock", Value: []byte("owner-b")})
	if err != nil || claim.GetStored() {
		t.Fatalf("second claim must lose: stored=%v err=%v", claim.GetStored(), err)
	}

	swap, err := s.CompareAndSwap(ctx, &pb.CompareAndSwapRequest{Key: "lock", Expected: []byte("owner-b"), Next: []byte("owner-c")})
	if err != nil || swap.GetSwapped() {
		t.Fatalf("stale swap must fail: swapped=%v err=%v", swap.GetSwapped(), err)
	}
	swap, err = s.CompareAndSwap(ctx, &pb.CompareAndSwapRequest{Key: "lock", Expected: []byte("owner-a"), Next: []byte("owner-a2")})
	if err != nil || !swap.GetSwapped() {
		t.Fatalf("matching swap: swapped=%v err=%v", swap.GetSwapped(), err)
	}
	if got := string(kv.store["lock"]); got != "owner-a2" {
		t.Fatalf("stored = %q, want owner-a2", got)
	}

	del, err := s.CompareAndDelete(ctx, &pb.CompareAndDeleteRequest{Key: "lock", Expected: []byte("owner-a")})
	if err != nil || del.GetDeleted() {
		t.Fatalf("stale release must fail: deleted=%v err=%v", del.GetDeleted(), err)
	}
	del, err = s.CompareAndDelete(ctx, &pb.CompareAndDeleteRequest{Key: "lock", Expected: []byte("owner-a2")})
	if err != nil || !del.GetDeleted() {
		t.Fatalf("matching release: deleted=%v err=%v", del.GetDeleted(), err)
	}
	if _, ok := kv.store["lock"]; ok {
		t.Fatal("key survived its release")
	}
}

func TestGuardedKVRPCs_NoKVServiceIsUnimplemented(t *testing.T) {
	s := &qrdbServiceServer{}
	ctx := context.Background()
	_, err := s.PutIfAbsent(ctx, &pb.PutIfAbsentRequest{Key: "k"})
	requireCode(t, err, codes.Unimplemented)
	_, err = s.CompareAndDelete(ctx, &pb.CompareAndDeleteRequest{Key: "k"})
	requireCode(t, err, codes.Unimplemented)
	_, err = s.CompareAndSwap(ctx, &pb.CompareAndSwapRequest{Key: "k"})
	requireCode(t, err, codes.Unimplemented)
}

// TestPutDelete_ExplicitWriteModeTakesModePath proves a request carrying a
// write mode is honoured through the mode-aware service call, and that a
// service error on that path is surfaced rather than swallowed.
func TestPutDelete_ExplicitWriteModeTakesModePath(t *testing.T) {
	kv := newMockKVService()
	s := &qrdbServiceServer{kvSvc: kv}
	ctx := context.Background()

	for _, mode := range []pb.WriteMode{pb.WriteMode_WRITE_MODE_SYNC, pb.WriteMode_WRITE_MODE_WAL_DURABLE, pb.WriteMode_WRITE_MODE_BATCHED} {
		if _, err := s.Put(ctx, &pb.PutRequest{Key: "k", Value: []byte(mode.String()), WriteMode: mode}); err != nil {
			t.Fatalf("Put(%v): %v", mode, err)
		}
		if got := string(kv.store["k"]); got != mode.String() {
			t.Fatalf("stored %q after Put(%v)", got, mode)
		}
		if _, err := s.Delete(ctx, &pb.DeleteRequest{Key: "k", WriteMode: mode}); err != nil {
			t.Fatalf("Delete(%v): %v", mode, err)
		}
		if _, ok := kv.store["k"]; ok {
			t.Fatalf("key survived Delete(%v)", mode)
		}
	}

	kv.putErr = &service.KeyNotFoundError{Key: "k"}
	kv.delErr = &service.KeyNotFoundError{Key: "k"}
	_, err := s.Put(ctx, &pb.PutRequest{Key: "k", WriteMode: pb.WriteMode_WRITE_MODE_SYNC})
	requireCode(t, err, codes.NotFound)
	_, err = s.Delete(ctx, &pb.DeleteRequest{Key: "k", WriteMode: pb.WriteMode_WRITE_MODE_SYNC})
	requireCode(t, err, codes.NotFound)
}
