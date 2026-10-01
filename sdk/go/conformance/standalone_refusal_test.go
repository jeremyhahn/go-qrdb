// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// A standalone node omits the raft-only families — MVCC, watch, lease and
// lock — by design. The refusal must be the SAME typed error however the node
// is reached: transport.CapabilityUnsupportedError. The network transports
// already gave it; the embedded transport answered *embedded.NilServiceError,
// the error it reserves for a wiring defect, so an in-process consumer such
// as Coordinator.AcquireLease could not tell "this mode does not serve
// leases" from "this composition is broken".

// seedRaftOnlyIdentity grants the "matrix" identity the raft-only verbs, so a
// remote refusal is the mode's and not the authorizer's.
func seedRaftOnlyIdentity(t *testing.T, n *node.Node) {
	t.Helper()
	svc := n.Services()
	ctx := boundedCtx(t)
	r, err := svc.Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name:     "raft-only-matrix",
		TenantID: tenant.SystemTenantID,
		Permissions: []permission.Permission{
			permission.LeaseGrant, permission.LocksAcquire,
			permission.MVCCGetRevision, permission.WatchWatch,
			permission.TransactionExecute, permission.KVPut, permission.KVGet,
			permission.AdminHealth, permission.AdminStatus,
		},
	})
	if err != nil {
		t.Fatalf("create role: %v", err)
	}
	if _, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             "matrix",
		TenantID:             tenant.SystemTenantID,
		RoleIDs:              []string{r.ID},
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
}

// raftOnlyCalls are one unary call into each raft-only family, plus the
// Coordinator verb that motivated the fix.
//
// Watch is not in the list. Opening a watch over gRPC, QUIC, Unix or MCP does
// not fail on a standalone node — those transports deliver the refusal on the
// stream, after the open — so a unary "the open returns this error" assertion
// does not describe them. The embedded transport refuses at open, with the
// same typed error the other families get here.
var raftOnlyCalls = []struct {
	name string
	call func(ctx context.Context, c *sdk.Client) error
}{
	{"Coordination.AcquireLease", func(ctx context.Context, c *sdk.Client) error {
		co, err := c.Coordination()
		if err != nil {
			return err
		}
		_, err = co.AcquireLease(ctx, "standalone/lease", 5*time.Second)
		return err
	}},
	{"Lease.LeaseGrant", func(ctx context.Context, c *sdk.Client) error {
		_, err := c.Lease().LeaseGrant(ctx, 5)
		return err
	}},
	{"Lock.LockAcquire", func(ctx context.Context, c *sdk.Client) error {
		_, err := c.Lock().LockAcquire(ctx, &transport.LockAcquireRequest{Key: "standalone/lock", HolderID: "h", TTL: 5})
		return err
	}},
	{"MVCC.GetRevision", func(ctx context.Context, c *sdk.Client) error {
		_, err := c.MVCC().GetRevision(ctx, "standalone/mvcc")
		return err
	}},
}

// typedRaftOnlyRefusal says which transports deliver the standalone refusal
// as transport.CapabilityUnsupportedError.
//
// REST and MCP do not, and that gap is recorded here rather than hidden: a
// standalone REST server does not register the lease and lock routes (404)
// and answers mvcc with an untyped 501, and a standalone MCP server does not
// register the raft-only tools at all ("tool not found"). Both are deliberate
// properties of those servers' route and tool registration, so changing them
// is a decision of its own. When one is made, its row flips here and the test
// holds it.
var typedRaftOnlyRefusal = map[transportKind]bool{
	embeddedTransport: true,
	grpcTransport:     true,
	quicTransport:     true,
	unixTransport:     true,
	restTransport:     false,
	mcpTransport:      false,
}

// TestStandaloneRaftOnlyFamilies_RefusalTypeByTransport pins the refusal of
// every raft-only family on a standalone node, per transport. The embedded
// cell is the fix: it now matches gRPC, QUIC and Unix.
func TestStandaloneRaftOnlyFamilies_RefusalTypeByTransport(t *testing.T) {
	for _, tk := range transportKinds {
		t.Run(string(tk), func(t *testing.T) {
			wantTyped, listed := typedRaftOnlyRefusal[tk]
			if !listed {
				t.Fatalf("%s has no row in typedRaftOnlyRefusal; declare what it does", tk)
			}
			c := bootAuthorizedCell(t, tk, func(n *node.Node) { seedRaftOnlyIdentity(t, n) })
			for _, rc := range raftOnlyCalls {
				t.Run(rc.name, func(t *testing.T) {
					err := rc.call(boundedCtx(t), c)
					if err == nil {
						t.Fatalf("%s/%s: a raft-only family was served on a standalone node", tk, rc.name)
					}
					var unsupported *transport.CapabilityUnsupportedError
					if typed := errors.As(err, &unsupported); typed != wantTyped {
						t.Fatalf("%s/%s: typed refusal = %v, want %v (err %T: %v)", tk, rc.name, typed, wantTyped, err, err)
					}
				})
			}
		})
	}
}

// TestStandaloneEmbeddedWatchOpenIsRefusedTyped covers the family the unary
// matrix leaves out: in process, opening a watch on a standalone node is
// refused at once with the typed error.
func TestStandaloneEmbeddedWatchOpenIsRefusedTyped(t *testing.T) {
	c := bootAuthorizedCell(t, embeddedTransport, func(n *node.Node) { seedRaftOnlyIdentity(t, n) })
	_, err := c.Watch().Watch(boundedCtx(t), "standalone/watch", transport.WatchOptions{})
	var unsupported *transport.CapabilityUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %T (%v), want *transport.CapabilityUnsupportedError", err, err)
	}
}
