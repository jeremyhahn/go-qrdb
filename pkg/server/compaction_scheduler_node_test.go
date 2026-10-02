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

package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// schedNodeStore is the raw shard service as the node target sees it.
type schedNodeStore struct {
	mu        sync.Mutex
	current   int64
	compacted []int64
	err       error
}

func (n *schedNodeStore) CurrentRevision(context.Context) (int64, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.current, nil
}

func (n *schedNodeStore) CompactNodeKeyspace(_ context.Context, revision int64) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.compacted = append(n.compacted, revision)
	return n.err
}

var _ nodeKeyspaceStore = (*schedNodeStore)(nil)

// The node target compacts through the raw shard's node-keyspace compaction
// -- never through any database's view -- on the platform's policy.
func TestCompactionScheduler_NodeTargetUsesTheNodeKeyspaceCompaction(t *testing.T) {
	node := &schedNodeStore{current: 30}
	view := &schedMVCC{current: 30}
	reg := &schedRegistry{views: map[string]*schedMVCC{"system/system": view}}
	s := newSched(reg, []compactionTarget{{Tenant: tenant.SystemTenantID, Node: true,
		Policy: &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 10}}}, fixedClock(time.Now()))
	s.node = node

	if n := s.passOnce(context.Background()); n != 1 {
		t.Fatalf("pass compacted %d, want 1", n)
	}
	if len(node.compacted) != 1 || node.compacted[0] != 20 {
		t.Fatalf("node compactions = %v, want [20]", node.compacted)
	}
	if calls := view.calls(); len(calls) != 0 {
		t.Fatalf("the node target compacted a database view: %v", calls)
	}
}

func TestCompactionScheduler_NodeTargetWithoutAStoreIsSkipped(t *testing.T) {
	s := newSched(&schedRegistry{}, []compactionTarget{{Tenant: tenant.SystemTenantID, Node: true,
		Policy: &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 1}}}, fixedClock(time.Now()))
	if n := s.passOnce(context.Background()); n != 0 {
		t.Fatalf("compacted %d with no node store", n)
	}
	node := &schedNodeStore{current: 30, err: errors.New("boom")}
	s.node = node
	if n := s.passOnce(context.Background()); n != 0 || len(node.compacted) != 1 {
		t.Fatalf("a failing node compaction: n=%d calls=%v", n, node.compacted)
	}
}

// compactionTargets lists the node keyspace only when the platform's
// configuration sets an enabled policy for it.
func TestServer_CompactionTargetsIncludeTheConfiguredNodeKeyspace(t *testing.T) {
	cfg := &config.Config{}
	s := &Server{config: cfg}
	if got := s.compactionTargets(); len(got) != 0 {
		t.Fatalf("targets with no node policy = %v", got)
	}
	cfg.MVCC.NodeCompaction = &compaction.Policy{Mode: compaction.Off}
	if got := s.compactionTargets(); len(got) != 0 {
		t.Fatalf("targets with an off node policy = %v", got)
	}
	cfg.MVCC.NodeCompaction = &compaction.Policy{Mode: compaction.Period, RetainPeriod: "1h"}
	got := s.compactionTargets()
	if len(got) != 1 || !got[0].Node || got[0].Policy.Mode != compaction.Period {
		t.Fatalf("targets = %+v, want the node keyspace", got)
	}
	if got[0].key() == (compactionTarget{Tenant: tenant.SystemTenantID}).key() {
		t.Fatal("the node target shares a sample key with a database")
	}
	if (&Server{}).nodeCompactionPolicy() != nil {
		t.Fatal("a server with no configuration has a node policy")
	}
}

// The raw shard service is the node keyspace's compactor.
func TestMVCCKVService_IsANodeKeyspaceStore(t *testing.T) {
	var raw service.MVCCKVServicer = service.NewMVCCKVService(nil, 1)
	if _, ok := raw.(nodeKeyspaceStore); !ok {
		t.Fatal("the raw MVCC service does not compact the node keyspace")
	}
}
