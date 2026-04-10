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
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

func TestBatchEntityOp_FieldAssignment(t *testing.T) {
	op := BatchEntityOp{
		Type:       "put",
		EntityType: "user",
		ID:         "u1",
		Data:       []byte("data"),
	}
	if op.Type != "put" {
		t.Fatalf("expected Type 'put', got %q", op.Type)
	}
	if op.EntityType != "user" {
		t.Fatalf("expected EntityType 'user', got %q", op.EntityType)
	}
	if op.ID != "u1" {
		t.Fatalf("expected ID 'u1', got %q", op.ID)
	}
	if string(op.Data) != "data" {
		t.Fatalf("expected Data 'data', got %q", op.Data)
	}
}

func TestBatchEntityOp_DeleteHasNoData(t *testing.T) {
	op := BatchEntityOp{
		Type:       "delete",
		EntityType: "config",
		ID:         "c1",
	}
	if op.Data != nil {
		t.Fatalf("expected nil Data for delete, got %v", op.Data)
	}
}

func TestBatchEntityRequest_FieldAssignment(t *testing.T) {
	req := BatchEntityRequest{
		Ops: []BatchEntityOp{
			{Type: "put", EntityType: "user", ID: "u1", Data: []byte("data")},
		},
		Consistency: consistency.Strong,
		WriteMode:   writemode.Sync,
		ShardID:     42,
	}
	if len(req.Ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(req.Ops))
	}
	if req.Consistency != consistency.Strong {
		t.Fatalf("expected Strong consistency, got %v", req.Consistency)
	}
	if req.WriteMode != writemode.Sync {
		t.Fatalf("expected Sync write mode, got %v", req.WriteMode)
	}
	if req.ShardID != 42 {
		t.Fatalf("expected ShardID 42, got %d", req.ShardID)
	}
}

func TestBatchEntityRequest_EmptyOps(t *testing.T) {
	req := BatchEntityRequest{}
	if len(req.Ops) != 0 {
		t.Fatalf("expected 0 ops, got %d", len(req.Ops))
	}
}

func TestBatchEntityResponse_FieldAssignment(t *testing.T) {
	resp := BatchEntityResponse{
		Results: []BatchEntityOpResult{
			{Index: 0},
			{Index: 1},
		},
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}
}

func TestBatchEntityResponse_EmptyResults(t *testing.T) {
	resp := BatchEntityResponse{}
	if len(resp.Results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(resp.Results))
	}
}

func TestBatchEntityOpResult_FieldAssignment(t *testing.T) {
	result := BatchEntityOpResult{
		Index: 3,
		Error: nil,
	}
	if result.Index != 3 {
		t.Fatalf("expected Index 3, got %d", result.Index)
	}
	if result.Error != nil {
		t.Fatalf("expected nil Error, got %v", result.Error)
	}
}

func TestBatchEntityOpResult_WithError(t *testing.T) {
	err := &EntityEmptyTypeError{}
	result := BatchEntityOpResult{
		Index: 0,
		Error: err,
	}
	if result.Error == nil {
		t.Fatal("expected non-nil Error")
	}
}
