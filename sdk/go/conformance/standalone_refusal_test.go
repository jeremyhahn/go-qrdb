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
// is reached: transport.CapabilityUnsupportedError. The embedded transport
// used to answer *embedded.NilServiceError (the error it reserves for a
// wiring defect), REST a router 404 for lease and lock, and MCP "tool not
// found", so a consumer such as Coordinator.AcquireLease could not tell "this
// mode does not serve leases" from "this composition is broken".

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

// TestStandaloneRaftOnlyFamilies_RefusedTypedOnEveryTransport pins the
// refusal of every raft-only family on a standalone node: every transport
// answers transport.CapabilityUnsupportedError. REST used to answer lease and
// lock with a bare router 404 and MCP did not register the tools at all
// ("tool not found"); both now attach the route or tool and refuse it typed.
func TestStandaloneRaftOnlyFamilies_RefusedTypedOnEveryTransport(t *testing.T) {
	for _, tk := range transportKinds {
		t.Run(string(tk), func(t *testing.T) {
			c := bootAuthorizedCell(t, tk, func(n *node.Node) { seedRaftOnlyIdentity(t, n) })
			for _, rc := range raftOnlyCalls {
				t.Run(rc.name, func(t *testing.T) {
					err := rc.call(boundedCtx(t), c)
					var unsupported *transport.CapabilityUnsupportedError
					if !errors.As(err, &unsupported) {
						t.Fatalf("%s/%s: err = %T (%v), want *transport.CapabilityUnsupportedError", tk, rc.name, err, err)
					}
				})
			}
		})
	}
}

// TestStandaloneWatchIsRefusedTypedOnEveryTransport pins the watch refusal on
// a standalone node. Watch is a stream: embedded and REST refuse at open, while
// gRPC, Unix, QUIC and MCP learn of it on the stream's first receive. Either
// way the caller gets the same *transport.CapabilityUnsupportedError — from
// Watch itself, or from the terminal response's Err — and the stream ends
// (MCP used to re-poll the refusal forever).
func TestStandaloneWatchIsRefusedTypedOnEveryTransport(t *testing.T) {
	for _, tk := range transportKinds {
		t.Run(string(tk), func(t *testing.T) {
			c := bootAuthorizedCell(t, tk, func(n *node.Node) { seedRaftOnlyIdentity(t, n) })
			ctx := boundedCtx(t)
			ch, err := c.Watch().Watch(ctx, "standalone/watch", transport.WatchOptions{})
			if err == nil {
				select {
				case resp, ok := <-ch:
					if !ok {
						t.Fatal("watch stream closed without a terminal refusal")
					}
					err = resp.Err()
				case <-ctx.Done():
					t.Fatal("no refusal before the deadline: the stream neither failed nor ended")
				}
			}
			var unsupported *transport.CapabilityUnsupportedError
			if !errors.As(err, &unsupported) {
				t.Fatalf("err = %T (%v), want *transport.CapabilityUnsupportedError", err, err)
			}
		})
	}
}
