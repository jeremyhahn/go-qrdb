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

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// stateHashAdmin is an admin double implementing service.ReplicaStateHasher,
// recording the shard IDs it was asked for.
type stateHashAdmin struct {
	mockAdminService
	called    bool
	gotShards []uint64
	resp      *service.StateHashesResponse
	err       error
}

var _ service.ReplicaStateHasher = (*stateHashAdmin)(nil)

func (a *stateHashAdmin) StateHashes(_ context.Context, shardIDs []uint64) (*service.StateHashesResponse, error) {
	a.called = true
	a.gotShards = shardIDs
	return a.resp, a.err
}

func TestStateHashesTool_ReturnsTheServiceResponse(t *testing.T) {
	admin := &stateHashAdmin{resp: &service.StateHashesResponse{
		NodeID:   "node-a",
		Replicas: []service.ReplicaStateHash{{ShardID: 2, ReplicaID: 1, AppliedIndex: 6, Records: 1, Hash: "aa"}},
	}}
	res, err := handleStateHashes(admin)(context.Background(), buildCallToolRequest(t, "qrdb_state_hashes",
		map[string]any{"shard_ids": []any{float64(2), 5}}))
	if err != nil {
		t.Fatalf("qrdb_state_hashes: %v", err)
	}
	if !slices.Equal(admin.gotShards, []uint64{2, 5}) {
		t.Fatalf("service asked for %v, want [2 5]", admin.gotShards)
	}
	var out service.StateHashesResponse
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.NodeID != "node-a" || len(out.Replicas) != 1 || out.Replicas[0].AppliedIndex != 6 {
		t.Fatalf("result = %+v", out)
	}

	if _, err := handleStateHashes(admin)(context.Background(), buildCallToolRequest(t, "qrdb_state_hashes", nil)); err != nil || admin.gotShards != nil {
		t.Fatalf("no shard_ids = (%v, asked %v), want every shard", err, admin.gotShards)
	}
}

func TestStateHashesTool_Refusals(t *testing.T) {
	ctx := context.Background()
	var toolErr *ToolError

	for name, arg := range map[string]any{
		"not an array":   "1",
		"non-number":     []any{"1"},
		"negative":       []any{float64(-1)},
		"fraction":       []any{1.5},
		"above 2^53":     []any{float64(1 << 54)},
		"mixed elements": []any{float64(1), true},
	} {
		admin := &stateHashAdmin{}
		_, err := handleStateHashes(admin)(ctx, buildCallToolRequest(t, "qrdb_state_hashes", map[string]any{"shard_ids": arg}))
		var argErr *ToolArgumentError
		if !errors.As(err, &argErr) || argErr.Argument != "shard_ids" {
			t.Fatalf("%s: err = %v, want a ToolArgumentError on shard_ids", name, err)
		}
		if admin.called {
			t.Fatalf("%s: a refused argument reached the service", name)
		}
	}

	denied := &stateHashAdmin{err: &tenant.CrossTenantDeniedError{}}
	if _, err := handleStateHashes(denied)(ctx, buildCallToolRequest(t, "qrdb_state_hashes", nil)); !errors.As(err, &toolErr) || toolErr.Tool != "qrdb_state_hashes" {
		t.Fatalf("service refusal = %v; want a qrdb_state_hashes ToolError", err)
	}

	var unsupported *service.ReplicaStateHashesUnsupportedError
	if _, err := handleStateHashes(&mockAdminService{})(ctx, buildCallToolRequest(t, "qrdb_state_hashes", nil)); !errors.As(err, &unsupported) {
		t.Fatalf("admin without the capability = %v", err)
	}
}

func TestToolStateHashes_Schema(t *testing.T) {
	tool := toolStateHashes()
	if tool.Name != "qrdb_state_hashes" {
		t.Fatalf("name = %q", tool.Name)
	}
	if _, ok := tool.InputSchema.Properties["shard_ids"]; !ok {
		t.Fatal("schema has no shard_ids argument")
	}
	if len(tool.InputSchema.Required) != 0 {
		t.Fatalf("required = %v, want shard_ids optional", tool.InputSchema.Required)
	}
}
