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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

var _ transport.ReplicaStateHasher = (*Transport)(nil)

// StateHashes returns the applied index and state digest of every replica
// the connected node runs whose shard is in shardIDs (every shard when
// empty), by calling the qrdb_state_hashes MCP tool.
func (t *Transport) StateHashes(ctx context.Context, shardIDs []uint64) (*transport.StateHashesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var args map[string]any
	if len(shardIDs) > 0 {
		args = map[string]any{"shard_ids": shardIDs}
	}
	text, err := t.callTool(ctx, "qrdb_state_hashes", args)
	if err != nil {
		return nil, err
	}
	out := &transport.StateHashesResponse{}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		return nil, &transport.RequestError{Method: "StateHashes", Err: &jsonDecodeError{Err: err}}
	}
	if out.Replicas == nil {
		out.Replicas = []transport.ReplicaStateHash{}
	}
	return out, nil
}
