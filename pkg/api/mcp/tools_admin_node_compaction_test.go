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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// nodeCompactionAdmin is an admin double that holds the node-keyspace policy
// the way the service does: validated on set, off stored as none.
type nodeCompactionAdmin struct {
	mockAdminService
	policy *compaction.Policy
	err    error
}

func (a *nodeCompactionAdmin) NodeCompaction(context.Context) (*compaction.Policy, error) {
	return a.policy, a.err
}

func (a *nodeCompactionAdmin) SetNodeCompaction(_ context.Context, p *compaction.Policy) error {
	if a.err != nil {
		return a.err
	}
	if p != nil {
		if err := p.Validate(); err != nil {
			return err
		}
	}
	a.policy = p.Normalized()
	return nil
}

func nodeCompactionOf(t *testing.T, text string) *compaction.Policy {
	t.Helper()
	var out nodeCompactionResult
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	return out.Compaction
}

func TestNodeCompactionTools_SetGetAndClear(t *testing.T) {
	admin := &nodeCompactionAdmin{}
	ctx := context.Background()

	res, err := handleUpdateNodeCompaction(admin)(ctx, buildCallToolRequest(t, "qrdb_update_node_compaction",
		map[string]any{"mode": "period", "retainPeriod": "48h"}))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := nodeCompactionOf(t, resultText(t, res)); got == nil || got.Mode != compaction.Period || got.RetainPeriod != "48h" {
		t.Fatalf("update result = %+v; want the period policy", got)
	}

	res, err = handleNodeCompaction(admin)(ctx, buildCallToolRequest(t, "qrdb_node_compaction", nil))
	if err != nil || nodeCompactionOf(t, resultText(t, res)) == nil {
		t.Fatalf("read = %v; want the policy set", err)
	}

	res, err = handleUpdateNodeCompaction(admin)(ctx, buildCallToolRequest(t, "qrdb_update_node_compaction", nil))
	if err != nil || nodeCompactionOf(t, resultText(t, res)) != nil || admin.policy != nil {
		t.Fatalf("clear = %v (held %+v); want no policy", err, admin.policy)
	}
}

func TestNodeCompactionTools_Refusals(t *testing.T) {
	ctx := context.Background()
	var toolErr *ToolError

	_, err := handleUpdateNodeCompaction(&nodeCompactionAdmin{})(ctx, buildCallToolRequest(t, "qrdb_update_node_compaction",
		map[string]any{"mode": "revision"}))
	var invalid *compaction.InvalidPolicyError
	if !errors.As(err, &toolErr) || !errors.As(err, &invalid) {
		t.Fatalf("revision with no retention = %v; want a ToolError wrapping *compaction.InvalidPolicyError", err)
	}

	unavailable := &nodeCompactionAdmin{err: &service.NodeCompactionUnavailableError{}}
	if _, err := handleNodeCompaction(unavailable)(ctx, buildCallToolRequest(t, "qrdb_node_compaction", nil)); !errors.As(err, &toolErr) || toolErr.Tool != "qrdb_node_compaction" {
		t.Fatalf("read (unavailable) = %v; want a qrdb_node_compaction ToolError", err)
	}
	if _, err := handleUpdateNodeCompaction(unavailable)(ctx, buildCallToolRequest(t, "qrdb_update_node_compaction", nil)); !errors.As(err, &toolErr) || toolErr.Tool != "qrdb_update_node_compaction" {
		t.Fatalf("update (unavailable) = %v; want a qrdb_update_node_compaction ToolError", err)
	}
}
