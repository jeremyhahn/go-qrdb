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
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The per-tenant compaction matrix.
//
// MVCC compaction is the owning tenant's act on its own databases' history,
// and nothing else (owner ruling, 2026-10-01). On the shared shard every
// prefix-isolated tenant's databases share one store and one revision
// counter, which is exactly where a compaction that discarded "every revision
// below N" destroyed every tenant's history whoever asked for it. Each cell
// drives the promise over ONE transport against a real raft node, through the
// real authentication and RBAC stack on the remote cells.

// compactionTenants are the two tenants each cell provisions, besides the
// system tenant.
var compactionTenants = []string{"tenant-a", "tenant-b"}

// compactionPermissions is what each tenant's identity holds: enough to write,
// delete, read history and ranges, and compact.
var compactionPermissions = []permission.Permission{
	permission.KVPut, permission.KVGet, permission.KVDelete, permission.KVRange,
	permission.MVCCGetRevision, permission.MVCCGetAtRevision, permission.MVCCRange,
	permission.MVCCCurrentRevision, permission.MVCCCompact,
	permission.AdminHealth, permission.AdminStatus,
	// Every tenant's role holds the platform's node-compaction permission,
	// so a tenant's refusal is the scope rule's -- admin:node_compaction is
	// ScopeCross -- and never a missing grant.
	permission.AdminNodeCompaction,
}

// compactionCell is one booted node and a way to act as each tenant on it.
type compactionCell struct {
	node *node.Node
	tk   transportKind
	// base is each tenant's unscoped client: in process the node's own, which
	// the context's tenant scopes; remote, one dialled with the tenant's
	// certificate.
	base map[string]*sdk.Client
	// roles is each tenant's compaction role id, created on every cell
	// (embedded included): the embedded cell authenticates nothing on its
	// own, so on binds one into its own context as the identity the role
	// grants, the in-process equivalent of what a remote cell's client
	// certificate authenticates as.
	roles map[string]string
}

// handle is a client acting as one tenant on one database.
type handle struct {
	mvcc transport.MVCCClient
	kv   transport.KVClient
	ctx  context.Context
}

// on returns tenantID's handle on database db. db need not be tenantID's own:
// the server, not the client, decides whether tenantID may reach it.
func (c *compactionCell) on(t *testing.T, tenantID, db string) handle {
	t.Helper()
	scoped, err := c.base[tenantID].DatabaseScoped(db)
	if err != nil {
		t.Fatalf("%s: DatabaseScoped(%s) as %s: %v", c.tk, db, tenantID, err)
	}
	ctx := boundedCtx(t)
	if !c.tk.remote() {
		// In process: the context's tenant selects the namespace, through the
		// node's per-(tenant, database) registry, and the bound identity is
		// what RBAC now also requires on every embedded operation (the
		// full-surface widening) -- the same compaction role a remote cell's
		// client certificate authenticates as.
		ctx = tenant.WithTenantID(auth.WithIdentity(ctx, &auth.Identity{
			Subject:  "compactor",
			TenantID: tenantID,
			Roles:    []string{c.roles[tenantID]},
		}), tenantID)
	}
	return handle{mvcc: scoped.MVCC(), kv: scoped.KV(), ctx: ctx}
}

// as returns tenantID's handle on its own database.
func (c *compactionCell) as(t *testing.T, tenantID string) handle {
	t.Helper()
	return c.on(t, tenantID, compactionDatabase(tenantID))
}

// compactionDatabase is the database each tenant owns in a compaction cell.
// Database names are one node-wide namespace, so each tenant's is its own.
func compactionDatabase(tenantID string) string {
	return "db-" + tenantID
}

// bootCompactionCell boots a single-replica raft node (MVCC revisions are
// raft-only) serving tk, provisions the two tenants, and gives each tenant --
// the system tenant included -- a database of its own on the shared shard.
func bootCompactionCell(t *testing.T, tk transportKind, extra ...server.ServerOption) *compactionCell {
	t.Helper()
	return bootCompactionCellWith(t, tk, nil, extra...)
}

// bootCompactionCellWith is bootCompactionCell with the node's configuration
// adjusted by configure before boot.
func bootCompactionCellWith(t *testing.T, tk transportKind, configure func(*qrdbconfig.Config), extra ...server.ServerOption) *compactionCell {
	t.Helper()
	c := cell{backend: database.BackendPebble, mode: raft, transport: tk}
	cfg := c.config(t)
	if configure != nil {
		configure(cfg)
	}

	var serverOpts []server.ServerOption
	var ca *qrdbdharness.CA
	if tk.remote() {
		ca, _, serverOpts = remoteServerTLS(t, tenantid.System) // the cell dials each tenant with its own certificate
	}
	serverOpts = append(serverOpts, extra...)

	members := map[uint64]string{1: cfg.Raft.RaftAddress}
	n, err := node.NewClusteredNode(context.Background(), cfg, members, serverOpts)
	if err != nil {
		t.Fatalf("boot %s: %v", tk, err)
	}
	t.Cleanup(func() { _ = n.Close() })
	awaitWritable(t, c, n.Client().KV())

	ctx := boundedCtx(t)
	for _, id := range compactionTenants {
		if _, err := n.Services().Tenant.CreateTenant(ctx, id, "Tenant "+id, tenant.IsolationPrefix); err != nil {
			t.Fatalf("CreateTenant(%s): %v", id, err)
		}
	}

	out := &compactionCell{node: n, tk: tk, base: map[string]*sdk.Client{}, roles: map[string]string{}}
	for _, id := range append([]string{tenantid.System}, compactionTenants...) {
		db := compactionDatabase(id)
		if _, err := n.Services().Database.CreateForTenant(tenant.WithTenantID(ctx, id),
			database.DatabaseConfig{Name: db, Backend: database.BackendPebble}); err != nil {
			t.Fatalf("create database %s for %s: %v", db, id, err)
		}
		// Every cell, embedded included, needs the role: the embedded
		// identity `on` binds names it, and a remote cell's client
		// certificate authenticates as the user holding it. The user id
		// is needed too, on the remote path only: under the default
		// auth.mtls_binding_policy "required", a certificate matching no
		// enrolled binding authenticates as no one, regardless of CN/OU
		// match, so dialAsTenant must bind the minted certificate to it.
		roleID, userID := seedCompactionIdentity(t, n, id)
		out.roles[id] = roleID
		if !tk.remote() {
			out.base[id] = n.Client()
			continue
		}
		out.base[id] = dialAsTenant(t, tk, n, ca, id, userID)
	}
	return out
}

// seedCompactionIdentity creates, in tenantID, a role holding
// compactionPermissions and the user its client certificate names. It
// returns both the created role's id (the embedded cell's `on` binds this
// into its own context as the identity the role grants) and the user's id
// (so dialAsTenant can bind the certificate it mints to it: under the
// default auth.mtls_binding_policy "required", a certificate matching no
// enrolled binding authenticates as no one, regardless of CN/OU match).
func seedCompactionIdentity(t *testing.T, n *node.Node, tenantID string) (roleID string, userID uint64) {
	t.Helper()
	svc := n.Services()
	ctx := tenant.WithTenantID(boundedCtx(t), tenantID)
	r, err := svc.Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name:        "compaction-" + tenantID,
		TenantID:    tenantID,
		Permissions: compactionPermissions,
	})
	if err != nil {
		t.Fatalf("create role in %s: %v", tenantID, err)
	}
	u, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             "compactor",
		TenantID:             tenantID,
		RoleIDs:              []string{r.ID},
	})
	if err != nil {
		t.Fatalf("create user in %s: %v", tenantID, err)
	}
	return r.ID, u.ID
}

// dialAsTenant connects an SDK client whose certificate authenticates as the
// "compactor" user of tenantID. The minted certificate's SPKI thumbprint is
// bound to userID first, matching what first boot's own AddCertBinding call
// does for the bootstrap admin: under the default auth.mtls_binding_policy
// "required", an account with no enrolled binding admits no certificate at
// all, CN/OU match or not.
func dialAsTenant(t *testing.T, tk transportKind, n *node.Node, ca *qrdbdharness.CA, tenantID string, userID uint64) *sdk.Client {
	t.Helper()
	kp, err := ca.Issue(t.TempDir(), qrdbdharness.WithSubject("compactor", tenantID))
	if err != nil {
		t.Fatalf("issue client cert for %s: %v", tenantID, err)
	}
	cert, err := kp.TLSCertificate()
	if err != nil {
		t.Fatalf("client TLS cert: %v", err)
	}

	block, _ := pem.Decode(kp.CertPEM)
	if block == nil {
		t.Fatalf("issued client cert for %s: no CERTIFICATE block in PEM", tenantID)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse issued client cert for %s: %v", tenantID, err)
	}
	if _, err := n.Services().User.AddCertBinding(tenant.WithTenantID(boundedCtx(t), tenantID), userID, user.ComputeSPKIThumbprint(leaf), 0); err != nil {
		t.Fatalf("bind client cert for %s: %v", tenantID, err)
	}

	client, err := sdk.NewClient(tk.dial(t, n, &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      ca.Pool,
		ServerName:   "127.0.0.1",
		MinVersion:   tls.VersionTLS13,
	}))
	if err != nil {
		t.Fatalf("new SDK client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Connect(boundedCtx(t)); err != nil {
		t.Fatalf("%s connect as %s: %v", tk, tenantID, err)
	}
	return client
}

// writeHistory writes two versions of key as tenantID and returns the
// revision of the first.
func (c *compactionCell) writeHistory(t *testing.T, tenantID, key string) int64 {
	t.Helper()
	h := c.as(t, tenantID)
	if err := h.kv.Put(h.ctx, key, []byte(tenantID+"-v1")); err != nil {
		t.Fatalf("%s: Put v1: %v", tenantID, err)
	}
	first, err := h.mvcc.GetRevision(h.ctx, key)
	if err != nil {
		t.Fatalf("%s: GetRevision: %v", tenantID, err)
	}
	if err := h.kv.Put(h.ctx, key, []byte(tenantID+"-v2")); err != nil {
		t.Fatalf("%s: Put v2: %v", tenantID, err)
	}
	return first.ModRevision
}

// requireHistory asserts tenantID still reads key's first version at rev.
func (c *compactionCell) requireHistory(t *testing.T, tenantID, key string, rev int64, after string) {
	t.Helper()
	h := c.as(t, tenantID)
	kv, err := h.mvcc.GetAtRevision(h.ctx, key, rev)
	if err != nil {
		t.Fatalf("CROSS-TENANT COMPACTION: after %s, %s's history at revision %d is gone: %v", after, tenantID, rev, err)
	}
	if string(kv.Value) != tenantID+"-v1" {
		t.Fatalf("after %s, %s reads %q at revision %d, want %q", after, tenantID, kv.Value, rev, tenantID+"-v1")
	}
}

// requireCompacted asserts tenantID's history of key at rev is gone while its
// current value remains.
func (c *compactionCell) requireCompacted(t *testing.T, tenantID, key string, rev int64) {
	t.Helper()
	h := c.as(t, tenantID)
	if _, err := h.mvcc.GetAtRevision(h.ctx, key, rev); !isCompacted(err) {
		t.Fatalf("%s: reading %s's compacted revision %d = %v; want the compaction refusal", c.tk, tenantID, rev, err)
	}
	if kv, err := h.mvcc.GetRevision(h.ctx, key); err != nil || string(kv.Value) != tenantID+"-v2" {
		t.Fatalf("%s: %s lost its current value: %v, %v", c.tk, tenantID, kv, err)
	}
}

// isCompacted reports whether err is the refusal of a read below a
// compaction floor.
func isCompacted(err error) bool {
	var compacted *transport.RevisionCompactedError
	return errors.As(err, &compacted)
}

// currentRevision returns the store's current revision as tenantID sees it.
func (c *compactionCell) currentRevision(t *testing.T, tenantID string) int64 {
	t.Helper()
	h := c.as(t, tenantID)
	cur, err := h.mvcc.CurrentRevision(h.ctx)
	if err != nil {
		t.Fatalf("%s: CurrentRevision: %v", tenantID, err)
	}
	return cur
}

// compactNow compacts tenantID's own database to the store's current
// revision.
func (c *compactionCell) compactNow(t *testing.T, tenantID string) int64 {
	t.Helper()
	cur := c.currentRevision(t, tenantID)
	h := c.as(t, tenantID)
	if err := h.mvcc.Compact(h.ctx, cur); err != nil {
		t.Fatalf("%s: Compact(%d) of its own database: %v", tenantID, cur, err)
	}
	return cur
}

// TestCompaction_NeverCrossesTenantsOnEveryTransport: a tenant compacts its
// own database's history, and that compaction -- or the system tenant's own
// -- never discards another tenant's. No tenant, the system tenant included,
// can compact a database it does not own.
func TestCompaction_NeverCrossesTenantsOnEveryTransport(t *testing.T) {
	for _, tk := range transportKinds {
		t.Run(string(tk), func(t *testing.T) {
			c := bootCompactionCell(t, tk)

			revA := c.writeHistory(t, "tenant-a", "k")
			revB := c.writeHistory(t, "tenant-b", "k")
			revSys := c.writeHistory(t, tenantid.System, "k")

			// Tenant A compacts its own database: allowed, and A's old
			// revision is gone while its current value stays ...
			c.compactNow(t, "tenant-a")
			c.requireCompacted(t, "tenant-a", "k", revA)
			// ... and B's and the system tenant's history is untouched.
			c.requireHistory(t, "tenant-b", "k", revB, "tenant-a's compaction")
			c.requireHistory(t, tenantid.System, "k", revSys, "tenant-a's compaction")

			// The system tenant compacts its own database -- and only that.
			c.compactNow(t, tenantid.System)
			c.requireCompacted(t, tenantid.System, "k", revSys)
			c.requireHistory(t, "tenant-b", "k", revB, "the system tenant's compaction")

			// Neither tenant-a nor the system tenant can aim a compaction at
			// tenant-b's database.
			cur := c.currentRevision(t, "tenant-b")
			for _, intruder := range []string{"tenant-a", tenantid.System} {
				h := c.on(t, intruder, compactionDatabase("tenant-b"))
				if err := h.mvcc.Compact(h.ctx, cur); err == nil {
					t.Fatalf("%s: %s compacted tenant-b's database", tk, intruder)
				}
				c.requireHistory(t, "tenant-b", "k", revB, intruder+"'s compaction of tenant-b's database")
			}
		})
	}
}

// TestCompaction_SystemTenantNeverDiscardsAnotherTenantsHistoryOnEveryTransport
// isolates the defect the ruling closes: the platform operator compacting its
// own database used to compact the whole shared store, discarding every
// tenant's history below the revision.
func TestCompaction_SystemTenantNeverDiscardsAnotherTenantsHistoryOnEveryTransport(t *testing.T) {
	for _, tk := range transportKinds {
		t.Run(string(tk), func(t *testing.T) {
			c := bootCompactionCell(t, tk)
			revB := c.writeHistory(t, "tenant-b", "k")
			c.writeHistory(t, tenantid.System, "k")

			c.compactNow(t, tenantid.System)
			c.requireHistory(t, "tenant-b", "k", revB, "the system tenant's compaction of its own database")
		})
	}
}
