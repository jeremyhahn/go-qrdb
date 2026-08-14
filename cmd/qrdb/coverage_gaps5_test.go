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

//go:build !integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ===========================================================================
// newBatchExecuteCmd — default output format and service error branches
// ===========================================================================

// TestBatchExecuteCmd_YAMLOutputFormat exercises the default switch case
// in newBatchExecuteCmd. The batch execute switch has cases for "json" and
// "table" only; "yaml" falls through to the default JSON encoder path.
func TestBatchExecuteCmd_YAMLOutputFormat(t *testing.T) {
	root := newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
		embedded.WithBatchService(&mockBatchService{}))

	batchReq := transport.BatchRequest{
		Ops: []transport.Op{
			{Type: transport.OpPut, Key: []byte("k1"), Value: []byte("v1")},
		},
	}
	data, err := json.Marshal(batchReq)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	f, err := os.CreateTemp(t.TempDir(), "batch*.json")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = f.Close()

	// "yaml" passes format validation but hits the default case in the
	// batch execute switch (which only has cases for "json" and "table").
	_, cmdErr := executeCommand(root, "--output", "yaml", "batch", "execute", "--file", f.Name())
	if cmdErr != nil {
		t.Fatalf("unexpected error: %v", cmdErr)
	}
}

// TestBatchExecuteCmd_ServiceError exercises the branch where
// client.Batch().ExecuteBatch() returns an error.
func TestBatchExecuteCmd_ServiceError(t *testing.T) {
	errBatchSvc := &errorBatchService{}
	root := newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
		embedded.WithBatchService(errBatchSvc))

	batchReq := transport.BatchRequest{
		Ops: []transport.Op{
			{Type: transport.OpPut, Key: []byte("k1"), Value: []byte("v1")},
		},
	}
	data, err := json.Marshal(batchReq)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	f, err := os.CreateTemp(t.TempDir(), "batch*.json")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = f.Close()

	_, cmdErr := executeCommand(root, "batch", "execute", "--file", f.Name())
	if cmdErr == nil {
		t.Fatal("expected error from batch execute when service fails")
	}
}

// ===========================================================================
// newTxExecuteCmd — default output format and service error branches
// ===========================================================================

// TestTxExecuteCmd_YAMLOutputFormat exercises the default switch case
// in newTxExecuteCmd. The tx execute switch has cases for "json" and
// "table" only; "yaml" falls through to the default JSON encoder path.
func TestTxExecuteCmd_YAMLOutputFormat(t *testing.T) {
	root := newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
		embedded.WithTransactionService(&mockTransactionService{}))

	txReq := transport.TransactionRequest{
		SuccessOps: []transport.Op{
			{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v")},
		},
	}
	data, err := json.Marshal(txReq)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	f, err := os.CreateTemp(t.TempDir(), "tx*.json")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = f.Close()

	// "yaml" passes format validation but hits the default case in the
	// tx execute switch (which only has cases for "json" and "table").
	_, cmdErr := executeCommand(root, "--output", "yaml", "tx", "execute", "--file", f.Name())
	if cmdErr != nil {
		t.Fatalf("unexpected error: %v", cmdErr)
	}
}

// TestTxExecuteCmd_ServiceError exercises the branch where
// client.Transaction().ExecuteTransaction() returns an error.
func TestTxExecuteCmd_ServiceError(t *testing.T) {
	errTxSvc := &errorTransactionService{}
	root := newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
		embedded.WithTransactionService(errTxSvc))

	txReq := transport.TransactionRequest{
		SuccessOps: []transport.Op{
			{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v")},
		},
	}
	data, err := json.Marshal(txReq)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	f, err := os.CreateTemp(t.TempDir(), "tx*.json")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = f.Close()

	_, cmdErr := executeCommand(root, "tx", "execute", "--file", f.Name())
	if cmdErr == nil {
		t.Fatal("expected error from tx execute when service fails")
	}
}

// ===========================================================================
// Error service stubs
// ===========================================================================

// errorBatchService implements service.BatchServicer, returning errors for
// all operations.
type errorBatchService struct{}

func (e *errorBatchService) Execute(_ context.Context, _ []service.Op) ([]service.OpResponse, error) {
	return nil, errors.New("batch: execute failed")
}

func (e *errorBatchService) ExecuteWithMode(_ context.Context, _ []service.Op, _ writemode.Mode) ([]service.OpResponse, error) {
	return nil, errors.New("batch: execute with mode failed")
}

func (e *errorBatchService) ExecuteEntity(_ context.Context, _ *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	return nil, errors.New("batch: execute entity failed")
}

func (e *errorBatchService) Import(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
	return nil, errors.New("batch: import failed")
}

// errorTransactionService implements service.TransactionServicer, returning
// errors for all operations.
type errorTransactionService struct{}

func (e *errorTransactionService) Execute(_ context.Context, _ []service.Compare, _, _ []service.Op) (*service.TransactionResponse, error) {
	return nil, errors.New("transaction: execute failed")
}

func (e *errorTransactionService) ExecuteWithMode(_ context.Context, _ []service.Compare, _, _ []service.Op, _ writemode.Mode) (*service.TransactionResponse, error) {
	return nil, errors.New("transaction: execute with mode failed")
}

func (e *errorTransactionService) ExecuteEntity(_ context.Context, _ *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	return nil, errors.New("transaction: execute entity failed")
}
