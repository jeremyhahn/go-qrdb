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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// nodeCompactionAdmin holds the node-keyspace policy the way the admin
// service does: validated on set, off stored as none.
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

func TestEmbedded_NodeCompaction_Failures(t *testing.T) {
	ctx := context.Background()
	tr, err := New(newMockKVService(), &nodeCompactionAdmin{err: &service.NodeCompactionUnavailableError{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := tr.NodeCompaction(ctx); err == nil {
		t.Fatal("NodeCompaction on a node without the table succeeded")
	}
	if _, err := tr.SetNodeCompaction(ctx, nil); err == nil {
		t.Fatal("SetNodeCompaction on a node without the table succeeded")
	}
	_ = tr.Close()
	var closed *transport.ClosedError
	if _, err := tr.NodeCompaction(ctx); !errors.As(err, &closed) {
		t.Fatalf("NodeCompaction on a closed transport = %v", err)
	}
	if _, err := tr.SetNodeCompaction(ctx, nil); !errors.As(err, &closed) {
		t.Fatalf("SetNodeCompaction on a closed transport = %v", err)
	}
}
