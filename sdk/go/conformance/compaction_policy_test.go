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

	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// setPolicy sets the compaction policy of database db as tenantID, over the
// cell's transport.
func (c *compactionCell) setPolicy(t *testing.T, tenantID, db string, policy *transport.DatabaseCompactionConfig) (*transport.DatabaseInfo, error) {
	t.Helper()
	ctx := boundedCtx(t)
	if !c.tk.remote() {
		ctx = tenant.WithTenantID(ctx, tenantID)
	}
	return c.base[tenantID].Database().UpdateDatabaseCompaction(ctx, transport.UpdateDatabaseCompactionRequest{Name: db, Compaction: policy})
}

// TestCompactionPolicy_OnEveryTransport: a tenant configures its database's
// scheduled compaction over the transport, and the node compacts THAT
// database as specified; a database whose tenant configured nothing is never
// compacted, and no tenant -- the system tenant included -- can configure a
// policy on another tenant's database.
func TestCompactionPolicy_OnEveryTransport(t *testing.T) {
	for _, tk := range transportKinds {
		t.Run(string(tk), func(t *testing.T) {
			c := bootCompactionCell(t, tk, server.WithCompactionInterval(25*time.Millisecond))

			revA := c.writeHistory(t, "tenant-a", "k")
			revB := c.writeHistory(t, "tenant-b", "k")
			revSys := c.writeHistory(t, tenantid.System, "k")

			// Nobody configured anything: ten scheduler passes later, every
			// history is intact.
			time.Sleep(250 * time.Millisecond)
			for id, rev := range map[string]int64{"tenant-a": revA, "tenant-b": revB, tenantid.System: revSys} {
				c.requireHistory(t, id, "k", rev, "scheduler passes with no policy configured")
			}

			// Neither another tenant nor the system tenant can configure a
			// policy on tenant-a's database.
			for _, intruder := range []string{"tenant-b", tenantid.System} {
				if _, err := c.setPolicy(t, intruder, compactionDatabase("tenant-a"),
					&transport.DatabaseCompactionConfig{Mode: "revision", RetainRevisions: 1}); err == nil {
					t.Fatalf("%s: %s configured compaction on tenant-a's database", tk, intruder)
				}
			}
			time.Sleep(150 * time.Millisecond)
			c.requireHistory(t, "tenant-a", "k", revA, "refused policy attempts")

			// An invalid policy is refused.
			if _, err := c.setPolicy(t, "tenant-a", compactionDatabase("tenant-a"),
				&transport.DatabaseCompactionConfig{Mode: "revision"}); err == nil {
				t.Fatalf("%s: a revision policy with no retention was accepted", tk)
			}

			// tenant-a configures its own database: keep the last revision.
			info, err := c.setPolicy(t, "tenant-a", compactionDatabase("tenant-a"),
				&transport.DatabaseCompactionConfig{Mode: "revision", RetainRevisions: 1})
			if err != nil {
				t.Fatalf("%s: tenant-a sets its own policy: %v", tk, err)
			}
			if info.Compaction == nil || info.Compaction.Mode != "revision" || info.Compaction.RetainRevisions != 1 {
				t.Fatalf("%s: the returned policy = %+v", tk, info.Compaction)
			}

			// The scheduler compacts tenant-a's database as specified ...
			deadline := time.Now().Add(callTimeout)
			for {
				h := c.as(t, "tenant-a")
				if _, err := h.mvcc.GetAtRevision(h.ctx, "k", revA); err != nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("%s: the configured policy never compacted tenant-a's database", tk)
				}
				time.Sleep(25 * time.Millisecond)
			}
			c.requireCompacted(t, "tenant-a", "k", revA)
			// ... and nothing else.
			c.requireHistory(t, "tenant-b", "k", revB, "tenant-a's scheduled compaction")
			c.requireHistory(t, tenantid.System, "k", revSys, "tenant-a's scheduled compaction")

			// Turning it off returns the database to never-compacted.
			info, err = c.setPolicy(t, "tenant-a", compactionDatabase("tenant-a"), &transport.DatabaseCompactionConfig{Mode: "off"})
			if err != nil || info.Compaction != nil {
				t.Fatalf("%s: turning the policy off = %+v, %v", tk, info, err)
			}
		})
	}
}
