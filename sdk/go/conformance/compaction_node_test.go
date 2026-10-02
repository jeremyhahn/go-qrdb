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
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestNodeKeyspaceCompaction_OffByDefault: with no node-keyspace policy set,
// the raw keyspace keeps its history across many passes.
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
	if got, err := raw.Admin().NodeCompaction(ctx); err != nil || got != nil {
		t.Fatalf("NodeCompaction on a fresh node = %+v, %v; want none", got, err)
	}
	time.Sleep(250 * time.Millisecond)
	if kv, err := raw.MVCC().GetAtRevision(ctx, "node/raw-key", first.ModRevision); err != nil || string(kv.Value) != "r1" {
		t.Fatalf("the raw keyspace was compacted with no policy set: %v, %v", kv, err)
	}
}

// adminAs returns tenantID's admin client and the context it calls with: in
// process the node's own client, scoped by the context's tenant; remote, the
// client dialled with the tenant's certificate.
func (c *compactionCell) adminAs(t *testing.T, tenantID string) (transport.AdminClient, context.Context) {
	t.Helper()
	ctx := boundedCtx(t)
	if !c.tk.remote() {
		ctx = tenant.WithTenantID(ctx, tenantID)
	}
	return c.base[tenantID].Admin(), ctx
}

// TestNodeKeyspaceCompaction_OnEveryTransport: the platform's node-keyspace
// policy is set through the platform verb on each transport, by the system
// tenant only. A tenant whose role holds admin:node_compaction -- the grant
// a tenant Admin or SO could give itself -- is refused both the read and
// the write and changes nothing. The policy the system tenant sets compacts
// the raw keyspace on a real raft node, and no key of any tenant's database,
// the system tenant's own databases included, over many passes.
func TestNodeKeyspaceCompaction_OnEveryTransport(t *testing.T) {
	for _, tk := range transportKinds {
		t.Run(string(tk), func(t *testing.T) {
			c := bootCompactionCell(t, tk, server.WithCompactionInterval(25*time.Millisecond))
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
			// "node/raw-key" has one '/', so it is outside every database
			// namespace.

			policy := &transport.DatabaseCompactionConfig{Mode: "revision", RetainRevisions: 1}

			// A tenant holding the permission is refused, and nothing is set.
			tenantAdmin, tenantCtx := c.adminAs(t, "tenant-a")
			_, err = tenantAdmin.SetNodeCompaction(tenantCtx, policy)
			if reason := classifyCrossTenantRefusal(tk, err); reason != nil {
				t.Fatalf("%s: tenant-a setting the platform's node-keyspace policy: %v", tk, reason)
			}
			_, err = tenantAdmin.NodeCompaction(tenantCtx)
			if reason := classifyCrossTenantRefusal(tk, err); reason != nil {
				t.Fatalf("%s: tenant-a reading the platform's node-keyspace policy: %v", tk, reason)
			}
			time.Sleep(150 * time.Millisecond)
			if kv, err := raw.MVCC().GetAtRevision(ctx, "node/raw-key", first.ModRevision); err != nil || string(kv.Value) != "r1" {
				t.Fatalf("%s: the raw keyspace was compacted after a refused policy: %v, %v", tk, kv, err)
			}

			// The system tenant sets it, and reads it back.
			sysAdmin, sysCtx := c.adminAs(t, tenant.SystemTenantID)
			set, err := sysAdmin.SetNodeCompaction(sysCtx, policy)
			if err != nil {
				t.Fatalf("%s: the system tenant sets the node-keyspace policy: %v", tk, err)
			}
			if set == nil || set.Mode != "revision" || set.RetainRevisions != 1 {
				t.Fatalf("%s: SetNodeCompaction returned %+v; want the policy set", tk, set)
			}
			if got, err := sysAdmin.NodeCompaction(sysCtx); err != nil || got == nil || got.RetainRevisions != 1 {
				t.Fatalf("%s: NodeCompaction = %+v, %v; want the policy set", tk, got, err)
			}

			deadline := time.Now().Add(callTimeout)
			for {
				_, err := raw.MVCC().GetAtRevision(ctx, "node/raw-key", first.ModRevision)
				if isCompacted(err) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("%s: the platform's node-keyspace policy never compacted the raw keyspace (last: %v)", tk, err)
				}
				time.Sleep(25 * time.Millisecond)
			}
			if v, err := raw.KV().Get(ctx, "node/raw-key"); err != nil || string(v) != "r2" {
				t.Fatalf("%s: the raw key lost its current value: %q, %v", tk, v, err)
			}

			// Many more passes; no tenant database's history moved.
			time.Sleep(250 * time.Millisecond)
			c.requireHistory(t, "tenant-a", "k", revA, "node-keyspace compaction")
			c.requireHistory(t, tenant.SystemTenantID, "k", revSys, "node-keyspace compaction")

			// Off removes it.
			if off, err := sysAdmin.SetNodeCompaction(sysCtx, nil); err != nil || off != nil {
				t.Fatalf("%s: turning the policy off = %+v, %v", tk, off, err)
			}
			if got, err := sysAdmin.NodeCompaction(sysCtx); err != nil || got != nil {
				t.Fatalf("%s: NodeCompaction after off = %+v, %v; want none", tk, got, err)
			}
		})
	}
}

// isCompacted reports whether err is the refusal of a read below a
// compaction floor.
func isCompacted(err error) bool {
	var compacted *transport.RevisionCompactedError
	return errors.As(err, &compacted)
}
