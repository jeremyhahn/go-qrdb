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
