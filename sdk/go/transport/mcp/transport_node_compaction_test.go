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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// nodeCompactionAdmin holds the node-keyspace policy the way the admin
// service does: validated on set, off stored as none.
type nodeCompactionAdmin struct {
	*mockAdminService
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

func TestNodeCompaction_Failures(t *testing.T) {
	ctx := context.Background()
	admin := &nodeCompactionAdmin{mockAdminService: newMockAdminService()}
	tr, cleanup := connectInProcess(t, newMockKVService(), admin)
	defer cleanup()
	if _, err := tr.SetNodeCompaction(ctx, &transport.DatabaseCompactionConfig{Mode: "revision"}); err == nil {
		t.Fatal("an invalid policy was accepted")
	}

	down := &nodeCompactionAdmin{mockAdminService: newMockAdminService(), err: &service.NodeCompactionUnavailableError{}}
	tr2, cleanup2 := connectInProcess(t, newMockKVService(), down)
	defer cleanup2()
	if _, err := tr2.NodeCompaction(ctx); err == nil {
		t.Fatal("NodeCompaction on a node without the table succeeded")
	}
	if _, err := tr2.SetNodeCompaction(ctx, nil); err == nil {
		t.Fatal("SetNodeCompaction on a node without the table succeeded")
	}

	_ = tr2.Close()
	if _, err := tr2.NodeCompaction(ctx); err == nil {
		t.Fatal("NodeCompaction on a closed transport succeeded")
	}
	if _, err := tr2.SetNodeCompaction(ctx, nil); err == nil {
		t.Fatal("SetNodeCompaction on a closed transport succeeded")
	}
}
