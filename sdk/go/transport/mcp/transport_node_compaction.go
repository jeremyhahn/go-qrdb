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

// nodeCompactionResult is the result of the qrdb_node_compaction and
// qrdb_update_node_compaction tools. An absent compaction is no policy.
type nodeCompactionResult struct {
	Compaction *transport.DatabaseCompactionConfig `json:"compaction,omitempty"`
}

// NodeCompaction returns the platform's node-keyspace compaction policy by
// calling the qrdb_node_compaction MCP tool.
func (t *Transport) NodeCompaction(ctx context.Context) (*transport.DatabaseCompactionConfig, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	text, err := t.callTool(ctx, "qrdb_node_compaction", nil)
	if err != nil {
		return nil, err
	}
	return decodeNodeCompaction("NodeCompaction", text)
}

// SetNodeCompaction sets the platform's node-keyspace compaction policy by
// calling the qrdb_update_node_compaction MCP tool. A nil or mode-less policy
// turns it off.
func (t *Transport) SetNodeCompaction(ctx context.Context, policy *transport.DatabaseCompactionConfig) (*transport.DatabaseCompactionConfig, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	args := map[string]any{}
	if policy != nil && policy.Mode != "" {
		args["mode"] = policy.Mode
		if policy.RetainRevisions != 0 {
			args["retainRevisions"] = policy.RetainRevisions
		}
		if policy.RetainPeriod != "" {
			args["retainPeriod"] = policy.RetainPeriod
		}
	}
	text, err := t.callTool(ctx, "qrdb_update_node_compaction", args)
	if err != nil {
		return nil, err
	}
	return decodeNodeCompaction("SetNodeCompaction", text)
}

func decodeNodeCompaction(method, text string) (*transport.DatabaseCompactionConfig, error) {
	var out nodeCompactionResult
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, &transport.RequestError{Method: method, Err: &jsonDecodeError{Err: err}}
	}
	return out.Compaction, nil
}
