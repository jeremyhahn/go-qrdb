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

package service

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
)

func TestEntityCompare_FieldAssignment(t *testing.T) {
	c := EntityCompare{
		EntityType: "user",
		ID:         "u1",
		Target:     CompareVersion,
		Result:     CompareEqual,
		Value:      []byte("val"),
	}
	if c.EntityType != "user" {
		t.Fatalf("expected EntityType 'user', got %q", c.EntityType)
	}
	if c.ID != "u1" {
		t.Fatalf("expected ID 'u1', got %q", c.ID)
	}
	if c.Target != CompareVersion {
		t.Fatalf("expected Target CompareVersion, got %d", c.Target)
	}
	if c.Result != CompareEqual {
		t.Fatalf("expected Result CompareEqual, got %d", c.Result)
	}
	if string(c.Value) != "val" {
		t.Fatalf("expected Value 'val', got %q", c.Value)
	}
}

func TestEntityCompare_EmptyFields(t *testing.T) {
	c := EntityCompare{}
	if c.EntityType != "" {
		t.Fatalf("expected empty EntityType, got %q", c.EntityType)
	}
	if c.ID != "" {
		t.Fatalf("expected empty ID, got %q", c.ID)
	}
	if c.Value != nil {
		t.Fatalf("expected nil Value, got %v", c.Value)
	}
}

func TestTransactionEntityOp_PutFieldAssignment(t *testing.T) {
	op := TransactionEntityOp{
		Type:       "put",
		EntityType: "config",
		ID:         "c1",
		Data:       []byte("payload"),
	}
	if op.Type != "put" {
		t.Fatalf("expected Type 'put', got %q", op.Type)
	}
	if op.EntityType != "config" {
		t.Fatalf("expected EntityType 'config', got %q", op.EntityType)
	}
	if op.ID != "c1" {
		t.Fatalf("expected ID 'c1', got %q", op.ID)
	}
	if string(op.Data) != "payload" {
		t.Fatalf("expected Data 'payload', got %q", op.Data)
	}
}

func TestTransactionEntityOp_DeleteNoData(t *testing.T) {
	op := TransactionEntityOp{
		Type:       "delete",
		EntityType: "session",
		ID:         "s1",
	}
	if op.Data != nil {
		t.Fatalf("expected nil Data for delete, got %v", op.Data)
	}
}

func TestTransactionEntityRequest_FieldAssignment(t *testing.T) {
	req := TransactionEntityRequest{
		Compares: []EntityCompare{
			{EntityType: "user", ID: "u1", Target: CompareVersion, Result: CompareEqual},
		},
		Success: []TransactionEntityOp{
			{Type: "put", EntityType: "user", ID: "u1", Data: []byte("new")},
		},
		Failure: []TransactionEntityOp{
			{Type: "delete", EntityType: "user", ID: "u1"},
		},
		Consistency: consistency.Linearizable,
		ShardID:     7,
	}
	if len(req.Compares) != 1 {
		t.Fatalf("expected 1 compare, got %d", len(req.Compares))
	}
	if len(req.Success) != 1 {
		t.Fatalf("expected 1 success op, got %d", len(req.Success))
	}
	if len(req.Failure) != 1 {
		t.Fatalf("expected 1 failure op, got %d", len(req.Failure))
	}
	if req.Consistency != consistency.Linearizable {
		t.Fatalf("expected Linearizable consistency, got %v", req.Consistency)
	}
	if req.ShardID != 7 {
		t.Fatalf("expected ShardID 7, got %d", req.ShardID)
	}
}

func TestTransactionEntityRequest_EmptyFields(t *testing.T) {
	req := TransactionEntityRequest{}
	if len(req.Compares) != 0 {
		t.Fatalf("expected 0 compares, got %d", len(req.Compares))
	}
	if len(req.Success) != 0 {
		t.Fatalf("expected 0 success ops, got %d", len(req.Success))
	}
	if len(req.Failure) != 0 {
		t.Fatalf("expected 0 failure ops, got %d", len(req.Failure))
	}
}

func TestTransactionEntityResponse_Succeeded(t *testing.T) {
	resp := TransactionEntityResponse{
		Succeeded: true,
		Responses: [][]byte{[]byte("data")},
	}
	if !resp.Succeeded {
		t.Fatal("expected Succeeded=true")
	}
	if len(resp.Responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resp.Responses))
	}
}

func TestTransactionEntityResponse_Failed(t *testing.T) {
	resp := TransactionEntityResponse{
		Succeeded: false,
		Responses: nil,
	}
	if resp.Succeeded {
		t.Fatal("expected Succeeded=false")
	}
	if resp.Responses != nil {
		t.Fatalf("expected nil Responses, got %v", resp.Responses)
	}
}
