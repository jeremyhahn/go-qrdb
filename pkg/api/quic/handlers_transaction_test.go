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
// handleTransaction
// ---------------------------------------------------------------------------

func TestHandleTransaction_Success(t *testing.T) {
	txSvc := &mockTransactionService{
		executeFn: func(_ context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error) {
			return &service.TransactionResponse{
				Succeeded: true,
				Revision:  10,
				Responses: []service.OpResponse{
					{Succeeded: true, Count: 1},
				},
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTransaction(txSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.TransactionRequest{
		Compares: []*pb.Compare{
			{
				Key:         []byte("tx-key"),
				Target:      pb.CompareTarget_COMPARE_VERSION,
				Result:      pb.CompareResult_COMPARE_EQUAL,
				TargetUnion: &pb.Compare_Version{Version: 1},
			},
		},
		SuccessOps: []*pb.Op{
			{
				Type:  pb.OpType_OP_PUT,
				Key:   []byte("tx-key"),
				Value: []byte("new-val"),
			},
		},
		FailureOps: []*pb.Op{},
	})
	if err != nil {
		t.Fatalf("failed to marshal TransactionRequest: %v", err)
	}

	resp, err := srv.handleTransaction(ctx, payload)
	if err != nil {
		t.Fatalf("handleTransaction returned unexpected error: %v", err)
	}

	var txResp pb.TransactionResponse
	if err := proto.Unmarshal(resp, &txResp); err != nil {
		t.Fatalf("failed to unmarshal TransactionResponse: %v", err)
	}
	if !txResp.GetSucceeded() {
		t.Error("expected succeeded to be true")
	}
	if txResp.GetRevision() != 10 {
		t.Errorf("expected revision 10, got %d", txResp.GetRevision())
	}
	if len(txResp.GetResponses()) != 1 {
		t.Fatalf("expected 1 response, got %d", len(txResp.GetResponses()))
	}
}

func TestHandleTransaction_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "transaction failed"}
	txSvc := &mockTransactionService{
		executeFn: func(_ context.Context, _ []service.Compare, _, _ []service.Op) (*service.TransactionResponse, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTransaction(txSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.TransactionRequest{})
	if err != nil {
		t.Fatalf("failed to marshal TransactionRequest: %v", err)
	}

	_, err = srv.handleTransaction(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleTransaction, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleTransaction_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleTransaction(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil transaction service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if snc.Service != "transaction" {
		t.Errorf("expected service %q, got %q", "transaction", snc.Service)
	}
}

func TestHandleTransaction_UnmarshalError(t *testing.T) {
	txSvc := &mockTransactionService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTransaction(txSvc))
	ctx := context.Background()

	_, err := srv.handleTransaction(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}
