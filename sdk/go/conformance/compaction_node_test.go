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

package conformance

import (
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestNodeKeyspaceCompaction_OffByDefault: with no policy in the node's
// configuration the raw keyspace keeps its history across many passes.
func TestNodeKeyspaceCompaction_OffByDefault(t *testing.T) {
	c := bootCompactionCell(t, embeddedTransport, server.WithCompactionInterval(25*time.Millisecond))
	ctx := boundedCtx(t)
	raw := c.node.Client()
	if err := raw.KV().Put(ctx, "node/raw-key", []byte("r1")); err != nil {
		t.Fatalf("raw put: %v", err)
	}
	first, err := raw.MVCC().GetRevision(ctx, "node/raw-key")
	if err != nil {
		t.Fatalf("raw GetRevision: %v", err)
	}
	if err := raw.KV().Put(ctx, "node/raw-key", []byte("r2")); err != nil {
		t.Fatalf("raw put: %v", err)
	}
	time.Sleep(250 * time.Millisecond)
	if kv, err := raw.MVCC().GetAtRevision(ctx, "node/raw-key", first.ModRevision); err != nil || string(kv.Value) != "r1" {
		t.Fatalf("the raw keyspace was compacted with no policy configured: %v, %v", kv, err)
	}
}

// TestNodeKeyspaceCompaction_NeverTouchesATenantPrefix: the platform's
// node-keyspace policy, set in the node's configuration, compacts the system
// tenant's raw keyspace on a real raft node -- and no key of any tenant's
// database, the system tenant's own databases included, over many passes.
// The policy has no API; the node keyspace is reached in process.
func TestNodeKeyspaceCompaction_NeverTouchesATenantPrefix(t *testing.T) {
	c := bootCompactionCellWith(t, embeddedTransport, func(cfg *qrdbconfig.Config) {
		cfg.MVCC.NodeCompaction = &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 1}
	}, server.WithCompactionInterval(25*time.Millisecond))

	revA := c.writeHistory(t, "tenant-a", "k")
	revSys := c.writeHistory(t, tenant.SystemTenantID, "k")

	// The raw keyspace: the node's own client, unscoped.
	ctx := boundedCtx(t)
	raw := c.node.Client()
	if err := raw.KV().Put(ctx, "node/raw-key", []byte("r1")); err != nil {
		t.Fatalf("raw put: %v", err)
	}
	first, err := raw.MVCC().GetRevision(ctx, "node/raw-key")
	if err != nil {
		t.Fatalf("raw GetRevision: %v", err)
	}
	if err := raw.KV().Put(ctx, "node/raw-key", []byte("r2")); err != nil {
		t.Fatalf("raw put: %v", err)
	}
	// "node/raw-key" has one '/', so it is outside every database namespace.

	deadline := time.Now().Add(callTimeout)
	for {
		if _, err := raw.MVCC().GetAtRevision(ctx, "node/raw-key", first.ModRevision); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the platform's node-keyspace policy never compacted the raw keyspace")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if v, err := raw.KV().Get(ctx, "node/raw-key"); err != nil || string(v) != "r2" {
		t.Fatalf("the raw key lost its current value: %q, %v", v, err)
	}

	// Many more passes; no tenant database's history moved.
	time.Sleep(250 * time.Millisecond)
	c.requireHistory(t, "tenant-a", "k", revA, "node-keyspace compaction")
	c.requireHistory(t, tenant.SystemTenantID, "k", revSys, "node-keyspace compaction")
}
