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
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
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

// The coordination families -- watch, lease, lock and the MVCC surface they
// stand on -- are served identically by a standalone node and a raft node, on
// every transport. ONE suite runs against both modes, cell by cell, so "works
// in standalone" and "works in raft" cannot come to mean different things.
//
// Standalone used to refuse all four with "requires mode: raft"; hosts worked
// around it with in-process fallbacks and ran their burn-in as a one-member
// raft cluster. The standalone node now composes them over the same engine,
// lessor and watch hub raft mode's MVCC state machine uses, applied in
// process instead of through the log.

// coordinationCells is the parity grid: standalone over every backend, and the
// default raft state machine, on every transport. Every backend composes the
// MVCC engine, so every backend serves the same families. The objstore row runs
// on the embedded transport only: a remote cell seeds an RBAC identity, and the
// identity store's claim keys carry a separator go-objstore's key validation
// rejects, so no objstore node can create a user on any transport.
func coordinationCells() []cell {
	var cells []cell
	for _, tk := range transportKinds {
		cells = append(cells,
			cell{backend: database.BackendMemory, mode: standalone, transport: tk},
			cell{backend: database.BackendFile, mode: standalone, transport: tk},
			cell{backend: database.BackendPebble, mode: standalone, transport: tk},
			cell{backend: database.BackendPebble, mode: raft, transport: tk},
		)
	}
	return append(cells, cell{backend: database.BackendObjstore, mode: standalone, transport: embeddedTransport})
}

// coordinationPermissions are the verbs the suite exercises.
var coordinationPermissions = []permission.Permission{
	permission.KVPut, permission.KVGet, permission.KVDelete,
	permission.TransactionExecute,
	permission.MVCCPutWithLease, permission.MVCCGetRevision, permission.MVCCCurrentRevision,
	permission.WatchWatch, permission.WatchWatchPrefix,
	permission.LeaseGrant, permission.LeaseRevoke, permission.LeaseKeepAlive,
	permission.LeaseTTL, permission.LeaseList,
	permission.LocksAcquire, permission.LocksRelease, permission.LocksExtend,
	permission.LocksStatus, permission.LocksList,
	permission.AdminHealth, permission.AdminStatus,
}

// seedCoordinationIdentity grants a role the verbs the suite calls, so a
// failure is the node's and never the authorizer's, and returns its id.
//
// It does NOT create the "matrix" user remote cells authenticate as -- that
// is createMatrixUser's job, separated out because the identity STORE on the
// objstore backend rejects the user record's generated key (null bytes;
// go-objstore's own validation, independent of these families), so no
// objstore node can create a user on any transport. A role alone has no such
// problem and is what the embedded cell needs: its identity is bound
// directly via auth.WithIdentity, with no user lookup at all.
func seedCoordinationIdentity(t *testing.T, n *node.Node) string {
	t.Helper()
	r, err := n.Services().Role.CreateRole(boundedCtx(t), &role.CreateRoleRequest{
		Name:        "coordination-matrix",
		TenantID:    tenantid.System,
		Permissions: coordinationPermissions,
	})
	if err != nil {
		t.Fatalf("create role: %v", err)
	}
	return r.ID
}

// createMatrixUser creates the "matrix" user a remote cell's client
// certificate authenticates as, holding roleID. See seedCoordinationIdentity
// for why this is never called for the embedded cell.
func createMatrixUser(t *testing.T, n *node.Node, roleID string) {
	t.Helper()
	if _, err := n.Services().User.CreateUser(boundedCtx(t), &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             "matrix",
		TenantID:             tenantid.System,
		RoleIDs:              []string{roleID},
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
}

// bootClient brings the cell up and returns an SDK client that reaches it
// over the cell's transport, and the context every call in the suite must
// use: for the embedded cell, which authenticates nothing on its own, an
// identity holding coordinationPermissions, paired with tenant.WithTenantID
// (the convention every embedded authorization gate reads); for a remote
// cell, the client certificate it dials with already carries it, so the
// context is unmodified.
func (c cell) bootClient(t *testing.T) (*sdk.Client, context.Context) {
	t.Helper()
	cfg := c.config(t)

	var serverOpts []server.ServerOption
	var ca *qrdbdharness.CA
	var clientKP *qrdbdharness.KeyPair
	if c.transport.remote() {
		ca, clientKP, serverOpts = remoteServerTLS(t, tenantid.System)
	}

	var n *node.Node
	var err error
	switch c.mode {
	case standalone:
		n, err = node.NewStandaloneNode(context.Background(), cfg, serverOpts)
	case raft:
		n, err = node.NewClusteredNode(context.Background(), cfg, map[uint64]string{1: cfg.Raft.RaftAddress}, serverOpts)
	}
	if err != nil {
		t.Fatalf("boot %s: %v", c.name(), err)
	}
	t.Cleanup(func() { _ = n.Close() })
	if c.mode == raft {
		awaitWritable(t, c, n.Client().KV())
	}
	roleID := seedCoordinationIdentity(t, n)
	if !c.transport.remote() {
		ctx := tenant.WithTenantID(auth.WithIdentity(boundedCtx(t), &auth.Identity{
			Subject:  "matrix",
			TenantID: tenantid.System,
			Roles:    []string{roleID},
		}), tenantid.System)
		return n.Client(), ctx
	}

	// auth.mtls_binding_policy defaults to "required": creating the user
	// alone is not enough, since an account with no live binding admits no
	// certificate at all, CN/OU match or not -- enrollMatrixCert binds the
	// one clientKP carries before it is ever dialled with.
	createMatrixUser(t, n, roleID)
	enrollMatrixCert(t, n, tenantid.System, clientKP)
	clientCert, err := clientKP.TLSCertificate()
	if err != nil {
		t.Fatalf("client TLS cert: %v", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      ca.Pool,
		ServerName:   "127.0.0.1",
		MinVersion:   tls.VersionTLS13,
	}
	client, err := sdk.NewClient(c.transport.dial(t, n, tlsCfg))
	if err != nil {
		t.Fatalf("new SDK client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Connect(boundedCtx(t)); err != nil {
		t.Fatalf("%s connect: %v", c.transport, err)
	}
	return client, boundedCtx(t)
}

// TestCoordinationFamilies_SameOnStandaloneAndRaft is the parity suite.
func TestCoordinationFamilies_SameOnStandaloneAndRaft(t *testing.T) {
	for _, c := range coordinationCells() {
		t.Run(c.name(), func(t *testing.T) {
			client, ctx := c.bootClient(t)
			t.Run("lease", func(t *testing.T) { assertLeaseLifecycle(t, ctx, client) })
			t.Run("lease-expiry", func(t *testing.T) { assertLeaseExpiryDeletesKeys(t, ctx, client) })
			t.Run("watch", func(t *testing.T) { assertPrefixWatch(t, ctx, client) })
			t.Run("watch-resume", func(t *testing.T) { assertWatchResumesFromRevision(t, ctx, client) })
			t.Run("watch-cancel", func(t *testing.T) { assertWatchEndsOnCancel(t, ctx, client) })
			t.Run("lock", func(t *testing.T) { assertLockLifecycle(t, ctx, client) })
		})
	}
}

// assertLeaseLifecycle: grant, attach, time-to-live, keep-alive, list,
// revoke -- and the revoke deletes the attached key.
func assertLeaseLifecycle(t *testing.T, ctx context.Context, c *sdk.Client) {
	l, err := c.Lease().LeaseGrant(ctx, 30)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := c.MVCC().PutWithLease(ctx, "cp/lease/key", []byte("v"), l.ID); err != nil {
		t.Fatalf("put with lease: %v", err)
	}
	ttl, err := c.Lease().LeaseTimeToLive(ctx, l.ID)
	if err != nil {
		t.Fatalf("time to live: %v", err)
	}
	if ttl.GrantedTTL != 30 || ttl.RemainingTTL <= 0 {
		t.Fatalf("time to live = %+v, want granted 30 and a positive remainder", ttl)
	}
	if _, err := c.Lease().LeaseKeepAlive(ctx, l.ID); err != nil {
		t.Fatalf("keep-alive: %v", err)
	}
	leases, err := c.Lease().LeaseList(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, x := range leases {
		found = found || x.ID == l.ID
	}
	if !found {
		t.Fatalf("lease %d missing from the list", l.ID)
	}
	if err := c.Lease().LeaseRevoke(ctx, l.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := c.KV().Get(ctx, "cp/lease/key"); err == nil {
		t.Fatal("key attached to the revoked lease survived")
	}
	var notFound *transport.LeaseNotFoundError
	if _, err := c.Lease().LeaseTimeToLive(ctx, l.ID); !errors.As(err, &notFound) {
		t.Fatalf("time to live of a revoked lease: err = %T (%v), want *transport.LeaseNotFoundError", err, err)
	}
}

// assertLeaseExpiryDeletesKeys: a lease nobody keeps alive expires and its
// key goes with it. This is the property a host's leader election rests on,
// and it holds only when the node runs the expiry sweeper.
func assertLeaseExpiryDeletesKeys(t *testing.T, ctx context.Context, c *sdk.Client) {
	l, err := c.Lease().LeaseGrant(ctx, 1)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := c.MVCC().PutWithLease(ctx, "cp/expiry/key", []byte("v"), l.ID); err != nil {
		t.Fatalf("put with lease: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := c.KV().Get(ctx, "cp/expiry/key"); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the key outlived its 1s lease by 15s: nothing swept the expired lease")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// eventStream yields a watch's events one at a time. A transport may batch
// several events into one response -- MCP's polling client returns everything
// a collection window saw -- so a reader that took only the first event of
// each response would silently drop the rest.
type eventStream struct {
	ch      <-chan transport.WatchResponse
	pending []*transport.WatchEvent
}

// next returns the next event, re-running write (when non-nil) each half
// second until one arrives: some transports open a watch asynchronously, so
// a single write can land before the watch is registered.
func (s *eventStream) next(t *testing.T, ctx context.Context, write func()) *transport.WatchEvent {
	t.Helper()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for len(s.pending) == 0 {
		select {
		case resp, ok := <-s.ch:
			if !ok {
				t.Fatal("watch stream closed before an event arrived")
			}
			if err := resp.Err(); err != nil {
				t.Fatalf("watch canceled: %v", err)
			}
			s.pending = append(s.pending, resp.Events...)
		case <-tick.C:
			if write != nil {
				write()
			}
		case <-ctx.Done():
			t.Fatal("no watch event before the deadline")
		}
	}
	ev := s.pending[0]
	s.pending = s.pending[1:]
	return ev
}

// assertPrefixWatch: a prefix watch sees a put and a delete under its prefix.
func assertPrefixWatch(t *testing.T, parent context.Context, c *sdk.Client) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	ch, err := c.Watch().WatchPrefix(ctx, "cp/watch/", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	events := &eventStream{ch: ch}
	put := func() {
		if err := c.KV().Put(ctx, "cp/watch/a", []byte("1")); err != nil {
			t.Errorf("put: %v", err)
		}
	}
	put()
	ev := events.next(t, ctx, put)
	if ev.Type != transport.EventPut || string(ev.Kv.Key) != "cp/watch/a" {
		t.Fatalf("first event = %v %q, want PUT cp/watch/a", ev.Type, ev.Kv.Key)
	}
	if err := c.KV().Delete(ctx, "cp/watch/a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Retried puts may still be in flight ahead of the delete.
	for ev.Type != transport.EventDelete {
		ev = events.next(t, ctx, nil)
	}
	if string(ev.Kv.Key) != "cp/watch/a" {
		t.Fatalf("delete event for %q, want cp/watch/a", ev.Kv.Key)
	}
}

// assertWatchResumesFromRevision: a watch opened at a past revision replays
// the history from that revision before it goes live.
func assertWatchResumesFromRevision(t *testing.T, parent context.Context, c *sdk.Client) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if err := c.KV().Put(ctx, "cp/resume/one", []byte("1")); err != nil {
		t.Fatalf("put: %v", err)
	}
	first, err := c.MVCC().GetRevision(ctx, "cp/resume/one")
	if err != nil {
		t.Fatalf("get revision: %v", err)
	}
	if err := c.KV().Put(ctx, "cp/resume/two", []byte("2")); err != nil {
		t.Fatalf("put: %v", err)
	}
	ch, err := c.Watch().WatchPrefix(ctx, "cp/resume/", transport.WatchOptions{StartRevision: first.ModRevision})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	events := &eventStream{ch: ch}
	if ev := events.next(t, ctx, nil); string(ev.Kv.Key) != "cp/resume/one" {
		t.Fatalf("first replayed event is %q, want cp/resume/one", ev.Kv.Key)
	}
	if ev := events.next(t, ctx, nil); string(ev.Kv.Key) != "cp/resume/two" {
		t.Fatalf("second replayed event is %q, want cp/resume/two", ev.Kv.Key)
	}
}

// assertWatchEndsOnCancel: cancelling the caller's context ends the stream.
func assertWatchEndsOnCancel(t *testing.T, parent context.Context, c *sdk.Client) {
	ctx, cancel := context.WithCancel(parent)
	ch, err := c.Watch().WatchPrefix(ctx, "cp/cancel/", transport.WatchOptions{})
	if err != nil {
		cancel()
		t.Fatalf("watch: %v", err)
	}
	cancel()
	deadline := time.After(callTimeout)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("the watch stream did not end after its context was cancelled")
		}
	}
}

// assertLockLifecycle: acquire, refuse a second holder, report status,
// extend and release -- after which the second holder acquires it.
func assertLockLifecycle(t *testing.T, ctx context.Context, c *sdk.Client) {
	held, err := c.Lock().LockTryLock(ctx, &transport.LockAcquireRequest{Key: "cp/lock/scheduler", HolderID: "a", TTL: 30})
	if err != nil {
		t.Fatalf("try-lock: %v", err)
	}
	if _, err := c.Lock().LockTryLock(ctx, &transport.LockAcquireRequest{Key: "cp/lock/scheduler", HolderID: "b", TTL: 30}); err == nil {
		t.Fatal("a second holder acquired a held lock")
	}
	status, err := c.Lock().LockStatus(ctx, "cp/lock/scheduler")
	if err != nil || status == nil || status.HolderID != "a" {
		t.Fatalf("status = %+v, %v; want held by a", status, err)
	}
	if _, err := c.Lock().LockExtend(ctx, &transport.LockExtendRequest{Key: "cp/lock/scheduler", FencingToken: held.FencingToken}); err != nil {
		t.Fatalf("extend: %v", err)
	}
	if err := c.Lock().LockRelease(ctx, &transport.LockReleaseRequest{Key: "cp/lock/scheduler", FencingToken: held.FencingToken}); err != nil {
		t.Fatalf("release: %v", err)
	}
	next, err := c.Lock().LockTryLock(ctx, &transport.LockAcquireRequest{Key: "cp/lock/scheduler", HolderID: "b", TTL: 30})
	if err != nil {
		t.Fatalf("try-lock after release: %v", err)
	}
	if next.FencingToken <= held.FencingToken {
		t.Fatalf("fencing token %d after release, want greater than %d", next.FencingToken, held.FencingToken)
	}
}

// TestCoordinator_SameOnEveryTransport covers the composed CoordinationClient
// surface itself (sdk.Client.Coordination: Get, Put, CompareAndSwap,
// AcquireLease, Watch) end to end on every transport.
//
// This is a different layer from TestCoordinationFamilies_SameOnStandaloneAndRaft
// above, which drives the lease, watch and lock sub-clients the Coordinator is
// built from. The composition had never run over a real transport:
// coordination_standalone_test.go and coordination_raft_test.go construct it
// only from an in-process node.Client() with every protocol server disabled.
// There is deliberately no CLI command for it -- see
// ../../docs/coordination/cli.md's "Why not" -- so test/integration has
// nothing to drive; this is the SDK-level coverage that gap calls for instead.
func TestCoordinator_SameOnEveryTransport(t *testing.T) {
	for _, c := range coordinationCells() {
		t.Run(c.name(), func(t *testing.T) {
			client, ctx := c.bootClient(t)
			coord, err := client.Coordination()
			if err != nil {
				t.Fatalf("Coordination: %v", err)
			}
			t.Run("get-put-cas", func(t *testing.T) { assertCoordinatorGetPutCAS(t, ctx, coord) })
			t.Run("lease", func(t *testing.T) { assertCoordinatorLease(t, ctx, coord) })
			t.Run("watch", func(t *testing.T) { assertCoordinatorWatch(t, ctx, coord) })
		})
	}
}

// assertCoordinatorGetPutCAS: Get, Put and CompareAndSwap served through the
// composed surface -- absence, a round trip, create-if-absent exclusivity and
// a value-match swap.
func assertCoordinatorGetPutCAS(t *testing.T, ctx context.Context, coord *sdk.Coordinator) {
	got, err := coord.Get(ctx, "coord/compose/x")
	if err != nil || got != nil {
		t.Fatalf("Get absent = (%v, %v), want (nil, nil)", got, err)
	}
	if err := coord.Put(ctx, "coord/compose/x", []byte("v1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err = coord.Get(ctx, "coord/compose/x")
	if err != nil || string(got) != "v1" {
		t.Fatalf("Get = (%q, %v), want (v1, nil)", got, err)
	}

	ok, err := coord.CompareAndSwap(ctx, "coord/compose/leader", nil, []byte("node-a"))
	if err != nil || !ok {
		t.Fatalf("CAS create = (%v, %v), want (true, nil)", ok, err)
	}
	ok, err = coord.CompareAndSwap(ctx, "coord/compose/leader", nil, []byte("node-b"))
	if err != nil || ok {
		t.Fatalf("CAS create (second) = (%v, %v), want (false, nil)", ok, err)
	}
	ok, err = coord.CompareAndSwap(ctx, "coord/compose/leader", []byte("node-a"), []byte("node-c"))
	if err != nil || !ok {
		t.Fatalf("CAS value-match = (%v, %v), want (true, nil)", ok, err)
	}
	got, err = coord.Get(ctx, "coord/compose/leader")
	if err != nil || string(got) != "node-c" {
		t.Fatalf("final value = (%q, %v), want (node-c, nil)", got, err)
	}
}

// assertCoordinatorLease: exclusive acquisition, a contender refused with
// LeaseHeldError, a successful renewal, and release freeing the bound key for
// the next acquirer.
func assertCoordinatorLease(t *testing.T, ctx context.Context, coord *sdk.Coordinator) {
	l, err := coord.AcquireLease(ctx, "coord/compose/elect", 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	if l.Key() != "coord/compose/elect" {
		t.Fatalf("Key() = %q, want coord/compose/elect", l.Key())
	}

	var held *sdk.LeaseHeldError
	if _, err := coord.AcquireLease(ctx, "coord/compose/elect", 30*time.Second); !errors.As(err, &held) {
		t.Fatalf("second acquirer: err = %T (%v), want *sdk.LeaseHeldError", err, err)
	}

	if err := l.Renew(ctx); err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if err := l.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}

	got, err := coord.Get(ctx, "coord/compose/elect")
	if err != nil || got != nil {
		t.Fatalf("Get after release = (%v, %v), want (nil, nil): the bound key must be deleted", got, err)
	}
	if _, err := coord.AcquireLease(ctx, "coord/compose/elect", 30*time.Second); err != nil {
		t.Fatalf("AcquireLease after release: %v", err)
	}
}

// assertCoordinatorWatch: a prefix watch opened through the composed surface
// sees a put made through the same surface, and ends when its context is
// cancelled.
func assertCoordinatorWatch(t *testing.T, parent context.Context, coord *sdk.Coordinator) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	w, err := coord.Watch(ctx, "coord/compose/watch/")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	put := func() {
		if err := coord.Put(ctx, "coord/compose/watch/a", []byte("1")); err != nil {
			t.Errorf("put: %v", err)
		}
	}
	put()

	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	var ev sdk.Event
	for {
		select {
		case got, ok := <-w.Events():
			if !ok {
				t.Fatalf("watch stream closed before an event arrived, err = %v", w.Err())
			}
			ev = got
		case <-tick.C:
			put()
			continue
		case <-ctx.Done():
			t.Fatal("no watch event before the deadline")
		}
		break
	}
	if ev.Type != sdk.EventPut || ev.Key != "coord/compose/watch/a" {
		t.Fatalf("event = %+v, want a PUT of coord/compose/watch/a", ev)
	}

	cancel()
	deadline := time.After(callTimeout)
	for {
		select {
		case _, ok := <-w.Events():
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("the watch stream did not end after its context was cancelled")
		}
	}
}
