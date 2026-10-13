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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// seedBarrierIdentity grants a role exactly the permission
// BarrierEnableEncryption/BarrierStatus need, mirroring seedGuardedIdentity's
// role shape for the barrier surface instead of the guarded KV plane, and
// returns its id. It no longer also creates the "matrix" user: that is only
// needed for a remote cell's client certificate (createMatrixUser, shared
// with harness_test.go/coordination_parity_test.go's identical convention),
// and waitForRootStrategy below needs the role bound for every cell,
// embedded included, since it polls through the node's own embedded client
// regardless of which transport is under test.
func seedBarrierIdentity(t *testing.T, n *node.Node) string {
	t.Helper()
	r, err := n.Services().Role.CreateRole(boundedCtx(t), &role.CreateRoleRequest{
		Name:        "conformance-barrier",
		TenantID:    tenantid.System,
		Permissions: []permission.Permission{permission.SealEnableEncryption, permission.SealStatus},
	})
	if err != nil {
		t.Fatalf("create role: %v", err)
	}
	return r.ID
}

// bootBarrierNode starts a real standalone node -- seal enabled, strategy
// "none", on the given database backend -- and returns both a
// transport.BarrierClient reached through tk and the underlying *node.Node.
// It is a narrower sibling of cell.boot: the guarded-verb matrix seeds a
// KV/txn identity and returns a KV-shaped clients value, while this seeds a
// barrier identity and returns the one client this suite needs plus the
// node, so the caller can await the real migration through n.Client() before
// letting cleanup close it (see waitForRootStrategy).
//
// The seal store itself is backend-independent -- buildNodeSealSubsystem
// always mints a dedicated Pebble-backed one regardless of the DATA-PLANE
// backend this parameter selects -- so a memory-backend cell exercises a
// real migration exactly as the Pebble cells do, pinning that
// canonicalRootKeyStrategy's now-interface-typed, nil-checked read
// (go-qrdb#550) never segfaults on any backend rather than merely on the one
// that happened to reproduce it first.
func bootBarrierNode(t *testing.T, tk transportKind, backend database.BackendType) (transport.BarrierClient, *node.Node, string) {
	t.Helper()
	c := cell{backend: backend, mode: standalone, transport: tk, want: fullPassage}
	cfg := c.config(t)

	var serverOpts []server.ServerOption
	var ca *qrdbdharness.CA
	var clientKP *qrdbdharness.KeyPair
	if tk.remote() {
		ca, clientKP, serverOpts = remoteServerTLS(t, tenantid.System)
	}

	n, err := node.NewStandaloneNode(context.Background(), cfg, serverOpts)
	if err != nil {
		t.Fatalf("boot %s: %v", tk, err)
	}
	t.Cleanup(func() { _ = n.Close() })

	// Every cell needs the role, embedded included: waitForRootStrategy
	// polls through the node's own embedded client regardless of which
	// transport is under test.
	roleID := seedBarrierIdentity(t, n)

	if !tk.remote() {
		return n.Client().Barrier(), n, roleID
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

	tr := tk.dial(t, n, tlsCfg)
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("new SDK client: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })
	if err := sdkClient.Connect(boundedCtx(t)); err != nil {
		t.Fatalf("%s connect: %v", tk, err)
	}
	return sdkClient.Barrier(), n, roleID
}

// waitForRootStrategy waits for the migration the caller started to end and
// requires the node's root barrier strategy to then be want, always reading it
// through the node's OWN BarrierServicer rather than the
// transport under test or even the embedded TRANSPORT client.
//
// The service layer, not the embedded transport, is deliberate and is no
// longer merely a convenience. A system-scope migration on the memory
// backend REBUILDS the data plane under the reopened barrier
// (nodeMigrationProvider.Reopen -> completeStandaloneBoot), and a memory
// backend keeps nothing durable to rebuild FROM -- so any role created
// before the migration (as the embedded transport's RBAC now requires for
// BarrierStatus) is gone by the time this polls, on every cell, remote
// transports included, since they also only ever polled through n.Client().
// The service layer carries no RBAC gate at all (enforcement lives in the
// transport/handler layer, exactly as crossTenantRefusals' and
// clusterJoinTokenRefusals' doc comments establish for every other
// AdminServicer-shaped verb), so it answers correctly through the rebuild
// with no identity to lose. n.Services() reflects the rebuilt barrier
// service the same way n.Client() does (both read from the atomically
// swapped lifecycle.Node), so this polls the SAME migration completion the
// transport-level read would, just through the layer unaffected by the
// data-plane rebuild's identity loss.
//
// Waiting for the real migration to finish here, rather than returning as
// soon as BarrierEnableEncryption's synchronous response comes back, is not
// optional: Node.Close (called by this test's t.Cleanup) now blocks on
// migrateMu for its entire run (go-qrdb#226's shutdown discipline), so
// skipping this wait would not reintroduce the OLD segfault -- Close would
// simply block until the migration finishes anyway -- but it would leave the
// test asserting only the synchronous "migration_started" acknowledgment
// while claiming to pin the REAL, end-to-end migration promise.
//
// What it waits for is the migration's end, not a strategy reached within a
// window. The migration has no time budget of its own -- every transport runs
// it on a context without cancellation, and a passphrase target derives its
// key twice, so its wall time follows the host's load
// (docs/barrier-seal/engineering.md) -- and a fixed window is a bet on the
// scheduler that a loaded host loses. Its end is an event instead: the recycle
// publishes a new lifecycle.Node whether the migration committed or reopened
// under the source strategy, so a Services() other than before, the graph the
// caller read before starting it, means it is over and the strategy it then
// reports is the outcome. A failed migration therefore fails the cell at once
// and by name. The only bound is the caller's own deadline: this test's, less
// callTimeout so the cell still names itself with time left to clean up.
func waitForRootStrategy(t *testing.T, n *node.Node, before *lifecycle.Node, want seal.StrategyID) {
	t.Helper()
	ctx := context.Background()
	if deadline, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-callTimeout))
		defer cancel()
	}
	start := time.Now()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if svc := n.Services(); svc != nil && svc != before && svc.Barrier != nil {
			status, err := svc.Barrier.BarrierStatus(boundedCtx(t))
			if err != nil {
				t.Fatalf("barrier status once the migration ended: %v", err)
			}
			if status.Strategy != string(want) {
				t.Fatalf("migration ended after %v with root barrier strategy %q, want %q", time.Since(start), status.Strategy, want)
			}
			t.Logf("migration to %q ended %v after the call returned", want, time.Since(start))
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("migration to %q had not ended %v after the call returned, at the test's deadline", want, time.Since(start))
		case <-tick.C:
		}
	}
}

// TestBarrierEnableEncryption_ConformsAcrossEverySDKTransport is the
// go-qrdb#550 conformance test: enabling encryption through every SDK
// transport against a REAL node -- a genuine seal barrier and
// EncryptionMigrator driving a REAL migration, not a fake -- gives the same
// outcome. Every transport must accept the request without error and the
// real migration behind it must actually complete (waitForRootStrategy).
//
// This is also the permanent regression test for two production races the
// fourth and fifth commits fixed, both of which this exact real-migration,
// real-node shape is what surfaced in the first place:
//
//  1. Node.Close segfaulting on a nilled seal store when it ran concurrently
//     with a still-in-flight system-scope migration (fixed by Close
//     acquiring migrateMu -- see Node.Close's doc comment). Every cell here
//     exercises that exact interleaving: t.Cleanup calls n.Close() right
//     after the subtest's assertions return, and on the standalone-Pebble
//     shape this suite uses, the whole migration -- mint target, quiesce,
//     re-encrypt, reopen -- reliably finishes in under a second, so a naive
//     Close arriving while the LAST leg of Reopen is still rebuilding the
//     data plane is the common case here, not a rare one.
//  2. The QUIC transport's response write racing its own server's Quiesce
//     phase ("server shutting down" instead of the real answer), fixed by
//     quic.Server.Stop() waiting for in-flight handlers before force-closing
//     their connections (see quicStopGracePeriod's doc comment). Before that
//     fix, the "quic" cell below failed non-deterministically depending on
//     how quickly MintKeyedTarget's KDF happened to run relative to the
//     750ms accept window -- fast enough on this suite's Pebble-backed
//     standalone node to reproduce on most runs.
func TestBarrierEnableEncryption_ConformsAcrossEverySDKTransport(t *testing.T) {
	for _, tk := range transportKinds {
		t.Run(string(tk), func(t *testing.T) {
			barrier, n, roleID := bootBarrierNode(t, tk, database.BackendPebble)
			ctx := barrierCallerCtx(boundedCtx(t), tk, roleID)

			before := n.Services()
			resp, err := barrier.BarrierEnableEncryption(ctx, &transport.BarrierEnableEncryptionRequest{
				Strategy:   string(seal.StrategyPassphrase),
				Passphrase: []byte("conformance-enable-encryption-passphrase"),
			})
			if err != nil {
				t.Fatalf("%s: BarrierEnableEncryption: %v", tk, err)
			}
			if resp.Status != "migration_started" {
				t.Errorf("%s: Status = %q, want %q", tk, resp.Status, "migration_started")
			}
			if resp.Scope != "system" {
				t.Errorf("%s: Scope = %q, want %q", tk, resp.Scope, "system")
			}

			waitForRootStrategy(t, n, before, seal.StrategyPassphrase)
		})
	}
}

// barrierCallerCtx binds the barrier role into ctx for the embedded cell,
// which authenticates nothing on its own; a remote cell's client certificate
// already carries it.
func barrierCallerCtx(ctx context.Context, tk transportKind, roleID string) context.Context {
	if tk != embeddedTransport {
		return ctx
	}
	return tenant.WithTenantID(auth.WithIdentity(ctx, &auth.Identity{
		Subject:  "matrix",
		TenantID: tenantid.System,
		Roles:    []string{roleID},
	}), tenantid.System)
}

// TestBarrierEnableEncryption_ConformsAcrossEverySDKTransport_MemoryBackend is
// the go-qrdb#550 sixth-commit regression test: canonicalRootKeyStrategy used
// to hand n.sealStore straight to seal.NewStorageAdapter with no nil check,
// and n.sealStore was a concrete *pebbleStorage.PebbleStorage field, so a nil
// value there survived past any `!= nil` check a caller might have added
// (assigning a typed nil pointer to an interface-typed argument produces a
// NON-nil interface wrapping a nil receiver) and panicked deep inside the
// concrete backend instead. n.sealStore is now typed as the
// seal.LegacyStorage interface itself, and both canonicalRootKeyStrategy and
// nodeSystemMigrator.EnableEncryption check it explicitly (see their doc
// comments) and refuse with a typed *MigrationNotSupportedError -- which
// matches service.ErrMigrationProviderUnavailable, so every transport answers
// it 501 / Unimplemented -- instead of reaching that read at all when it is
// genuinely absent. That the absent store also reaches Node.sealStore as a nil
// interface is pinned in sdk/go/node through the one path both node builders
// install the seal subsystem by.
//
// This suite does not currently have a construction path that produces a
// barrier registry with no seal store -- buildNodeSealSubsystem always
// builds both together, regardless of the DATA-PLANE backend selected here
// -- so a real database.BackendMemory node exercises the SAME real-migration
// success path the Pebble cells do (TestCanonicalRootKeyStrategy_NoSealStore_
// RefusesTyped and TestNode_EnableEncryption_NoSealStore_RefusesTyped in
// sdk/go/node directly pin the refusal itself, by re-adopting a real node's
// seal subsystem without its store, and the MigratorRefusal tests in
// pkg/api/{rest,grpc,quic,mcp} pin each transport's answer to it). What this
// test pins is that a memory-backend
// node -- the backend selection under which the original crash this whole
// fix responds to was first observed -- completes a real migration through
// every SDK transport exactly like Pebble does, with no crash anywhere on
// the path.
func TestBarrierEnableEncryption_ConformsAcrossEverySDKTransport_MemoryBackend(t *testing.T) {
	for _, tk := range transportKinds {
		t.Run(string(tk), func(t *testing.T) {
			barrier, n, roleID := bootBarrierNode(t, tk, database.BackendMemory)
			ctx := barrierCallerCtx(boundedCtx(t), tk, roleID)

			before := n.Services()
			resp, err := barrier.BarrierEnableEncryption(ctx, &transport.BarrierEnableEncryptionRequest{
				Strategy:   string(seal.StrategyPassphrase),
				Passphrase: []byte("conformance-enable-encryption-passphrase-memory"),
			})
			if err != nil {
				t.Fatalf("%s: BarrierEnableEncryption: %v", tk, err)
			}
			if resp.Status != "migration_started" {
				t.Errorf("%s: Status = %q, want %q", tk, resp.Status, "migration_started")
			}
			if resp.Scope != "system" {
				t.Errorf("%s: Scope = %q, want %q", tk, resp.Scope, "system")
			}

			waitForRootStrategy(t, n, before, seal.StrategyPassphrase)
		})
	}
}
