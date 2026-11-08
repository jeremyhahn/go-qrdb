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

package quic

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// handleBatch
// ---------------------------------------------------------------------------

func TestHandleBatch_Success(t *testing.T) {
	batchSvc := &mockBatchService{
		executeFn: func(_ context.Context, ops []service.Op) ([]service.OpResponse, error) {
			responses := make([]service.OpResponse, len(ops))
			for i := range ops {
				responses[i] = service.OpResponse{Succeeded: true}
			}
			return responses, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBatch(batchSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.BatchRequest{
		Ops: []*pb.Op{
			{Type: pb.OpType_OP_PUT, Key: []byte("k1"), Value: []byte("v1")},
			{Type: pb.OpType_OP_PUT, Key: []byte("k2"), Value: []byte("v2")},
			{Type: pb.OpType_OP_DELETE, Key: []byte("k3")},
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal BatchRequest: %v", err)
	}

	resp, err := srv.handleBatch(ctx, payload)
	if err != nil {
		t.Fatalf("handleBatch returned unexpected error: %v", err)
	}

	var batchResp pb.BatchResponse
	if err := proto.Unmarshal(resp, &batchResp); err != nil {
		t.Fatalf("failed to unmarshal BatchResponse: %v", err)
	}
	if len(batchResp.GetResponses()) != 3 {
		t.Fatalf("expected 3 responses, got %d", len(batchResp.GetResponses()))
	}
	for i, r := range batchResp.GetResponses() {
		if !r.GetSucceeded() {
			t.Errorf("expected response %d to succeed", i)
		}
	}
}

func TestHandleBatch_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "batch failed"}
	batchSvc := &mockBatchService{
		executeFn: func(_ context.Context, _ []service.Op) ([]service.OpResponse, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBatch(batchSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.BatchRequest{
		Ops: []*pb.Op{
			{Type: pb.OpType_OP_PUT, Key: []byte("k"), Value: []byte("v")},
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal BatchRequest: %v", err)
	}

	_, err = srv.handleBatch(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleBatch, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleBatch_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleBatch(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil batch service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if snc.Service != "batch" {
		t.Errorf("expected service %q, got %q", "batch", snc.Service)
	}
}

func TestHandleBatch_UnmarshalError(t *testing.T) {
	batchSvc := &mockBatchService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBatch(batchSvc))
	ctx := context.Background()

	_, err := srv.handleBatch(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleImport_NotConfigured(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	handlers := map[string]func(context.Context, []byte) ([]byte, error){
		"begin":  srv.handleImportBegin,
		"append": srv.handleImportAppend,
		"commit": srv.handleImportCommit,
		"abort":  srv.handleImportAbort,
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			_, err := h(context.Background(), nil)
			var nc *ServiceNotConfiguredError
			if !errors.As(err, &nc) || nc.Service != "batch" {
				t.Fatalf("err = %v, want *ServiceNotConfiguredError{batch}", err)
			}
		})
	}
}

func TestHandleImport_UnmarshalAndServiceErrors(t *testing.T) {
	svcErr := &testError{msg: "import refused"}
	batchSvc := &mockBatchService{
		importBeginFn:  func(context.Context, service.ImportOptions) (string, error) { return "", svcErr },
		importAppendFn: func(context.Context, string, uint64, []byte) error { return svcErr },
		importCommitFn: func(context.Context, string) (*service.ImportResult, error) { return nil, svcErr },
		importAbortFn:  func(context.Context, string) error { return svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBatch(batchSvc))
	handlers := map[string]func(context.Context, []byte) ([]byte, error){
		"begin":  srv.handleImportBegin,
		"append": srv.handleImportAppend,
		"commit": srv.handleImportCommit,
		"abort":  srv.handleImportAbort,
	}
	for name, h := range handlers {
		t.Run(name+"/unmarshal", func(t *testing.T) {
			var ue *UnmarshalError
			if _, err := h(context.Background(), badProto); !errors.As(err, &ue) {
				t.Fatalf("err = %v, want *UnmarshalError", err)
			}
		})
		t.Run(name+"/service", func(t *testing.T) {
			if _, err := h(context.Background(), nil); !errors.Is(err, svcErr) {
				t.Fatalf("err = %v, want the service error", err)
			}
		})
	}
}

func TestHandleImport_Success(t *testing.T) {
	var (
		gotOpts   service.ImportOptions
		gotSeq    uint64
		gotChunk  []byte
		gotCommit string
		gotAbort  string
	)
	batchSvc := &mockBatchService{
		importBeginFn: func(_ context.Context, opts service.ImportOptions) (string, error) {
			gotOpts = opts
			return "sess-1", nil
		},
		importAppendFn: func(_ context.Context, _ string, seq uint64, chunk []byte) error {
			gotSeq, gotChunk = seq, chunk
			return nil
		},
		importCommitFn: func(_ context.Context, id string) (*service.ImportResult, error) {
			gotCommit = id
			return &service.ImportResult{Total: 3, Succeeded: 2, Failed: 1,
				Errors: []service.ImportError{{Row: 2, Key: "k2", Message: "bad row"}}}, nil
		},
		importAbortFn: func(_ context.Context, id string) error {
			gotAbort = id
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBatch(batchSvc))
	ctx := context.Background()

	out, err := srv.handleImportBegin(ctx, mustMarshal(t, &pb.ImportBeginRequest{
		Format: "csv", Filename: "f.csv", KeyField: "id", SkipHeader: true, Transactional: true,
	}))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	var begin pb.ImportBeginResponse
	if err := proto.Unmarshal(out, &begin); err != nil || begin.GetSessionId() != "sess-1" {
		t.Fatalf("begin resp = %+v, err = %v", &begin, err)
	}
	if gotOpts.Format != "csv" || gotOpts.Filename != "f.csv" || gotOpts.KeyField != "id" || !gotOpts.SkipHeader || !gotOpts.Transactional {
		t.Fatalf("opts = %+v", gotOpts)
	}

	if _, err := srv.handleImportAppend(ctx, mustMarshal(t, &pb.ImportAppendRequest{SessionId: "sess-1", Seq: 4, Chunk: []byte("a,b")})); err != nil {
		t.Fatalf("append: %v", err)
	}
	if gotSeq != 4 || string(gotChunk) != "a,b" {
		t.Fatalf("append got seq=%d chunk=%q", gotSeq, gotChunk)
	}

	out, err = srv.handleImportCommit(ctx, mustMarshal(t, &pb.ImportCommitRequest{SessionId: "sess-1"}))
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	var commit pb.ImportCommitResponse
	if err := proto.Unmarshal(out, &commit); err != nil {
		t.Fatalf("unmarshal commit: %v", err)
	}
	if gotCommit != "sess-1" || commit.GetTotal() != 3 || commit.GetSucceeded() != 2 || commit.GetFailed() != 1 ||
		len(commit.GetErrors()) != 1 || commit.GetErrors()[0].GetRow() != 2 || commit.GetErrors()[0].GetKey() != "k2" {
		t.Fatalf("commit resp = %+v", &commit)
	}

	if _, err := srv.handleImportAbort(ctx, mustMarshal(t, &pb.ImportAbortRequest{SessionId: "sess-2"})); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if gotAbort != "sess-2" {
		t.Fatalf("abort got %q", gotAbort)
	}
}
