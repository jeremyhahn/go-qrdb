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
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal/clustersealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestOpenWatch_EndsWhenClusterSealed pins that a seal reaching this node --
// of the node barrier, or of the watched database -- and a cut-off each end
// every open watch on every streaming transport, with the seal's typed
// refusal. Before cluster sealing a watch was checked only when it opened.
func TestOpenWatch_EndsWhenClusterSealed(t *testing.T) {
	cases := []struct {
		name   string
		end    func(*clusterseal.Streams)
		reason string
	}{
		{"node barrier sealed", func(s *clusterseal.Streams) {
			s.EndAll(&clusterseal.SealedError{Barrier: clusterseal.NodeBarrier()})
		}, `clusterseal: barrier "node" sealed`},
		{"database sealed", func(s *clusterseal.Streams) {
			s.EndDatabase("system", "legacyvault", &clusterseal.SealedError{Barrier: clusterseal.DatabaseBarrier("system", "legacyvault")})
		}, `clusterseal: barrier "database/system/legacyvault" sealed`},
		{"cut off", func(s *clusterseal.Streams) {
			s.EndAll(&clusterseal.SealStateUnconfirmedError{})
		}, "cannot confirm the cluster seal state"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := newTestClusterSeal()
			assertOpenWatchesEnd(t, []ServerOption{WithClusterSeal(cs)},
				func(*database.Database) { tc.end(cs.Streams()) }, tc.reason)
		})
	}
}

// clusterSealServer starts a server on every network transport over an
// unsealed node barrier, with cluster sealing enforced through cs.
func clusterSealServer(t *testing.T, cs *testClusterSeal) *Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	strategy, err := seal.NewSoftwareStrategy([]byte("cluster-seal-transport"))
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := seal.NewStorageBarrier(logger, seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "root-key"}, strategy)
	if err != nil {
		t.Fatal(err)
	}
	if err := barrier.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = barrier.Close() })

	socketPath := filepath.Join(t.TempDir(), "qrdb.sock")
	cfg := testConfigFull(t, protocolFlags{rest: true, grpc: true, quic: true, unix: true, mcp: true}, socketPath)
	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithCeremonyCA(testServerCA),
		WithDatabaseServiceRegistry(newLockedKVRegistry()),
		WithBarrierService(service.NewBarrierService(barrier, service.WithClusterSealer(cs))),
		WithSealedWindowProbe(barrier),
		WithClusterSeal(cs),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	return srv
}

// TestClusterSeal_OnEveryTransport pins the cluster-seal surface on REST,
// gRPC, Unix, QUIC and MCP: every barrier status carries the node barrier's
// cluster_seal object; a node that is held by a cluster seal refuses data
// requests but still answers its status and cluster unseal; and cluster
// unseal writes the clear record, after which the node serves again.
func TestClusterSeal_OnEveryTransport(t *testing.T) {
	cs := newTestClusterSeal()
	cs.Put(clusterseal.Record{Barrier: clusterseal.NodeBarrier(), Epoch: 3, Sealed: true,
		Principal: "ops", PrincipalTenant: "system", Node: "192.0.2.1:5000"})
	cs.SetLocal(clusterseal.LocalHeld)
	cs.setRefusing(true)
	srv := clusterSealServer(t, cs)
	ctx := context.Background()

	transports := dialTransports(t, srv)
	scoped := dialSealedDBTransports(t, srv, "default")
	for name, tr := range transports {
		t.Run(name, func(t *testing.T) {
			bc, ok := tr.(transport.BarrierClient)
			if !ok {
				t.Fatalf("%s has no barrier surface", name)
			}
			st, err := bc.BarrierStatus(ctx)
			if err != nil {
				t.Fatalf("BarrierStatus on a held node: %v", err)
			}
			if st.ClusterSeal == nil || !st.ClusterSeal.Active || st.ClusterSeal.Epoch == 0 {
				t.Fatalf("ClusterSeal = %+v, want the active record", st.ClusterSeal)
			}
			if err := scoped[name].Put(ctx, "k", []byte("v")); err == nil || !strings.Contains(err.Error(), "barrier sealed") {
				t.Fatalf("Put on a held node: err = %v, want the sealed refusal", err)
			}
			cleared, err := bc.BarrierClusterUnseal(ctx)
			if err != nil {
				t.Fatalf("BarrierClusterUnseal on a held node: %v", err)
			}
			if !cleared.Active || cleared.Sealed || cleared.Epoch < 4 {
				t.Fatalf("cleared = %+v, want an active clear record past epoch 3", cleared)
			}
		})
	}

	cs.setRefusing(false)
	cs.SetLocal(clusterseal.LocalServing)
	for name, kv := range scoped {
		if err := kv.Put(ctx, "k", []byte("v")); err != nil {
			t.Errorf("%s Put after the cluster unseal: %v", name, err)
		}
	}
}

// TestClusterSeal_InactiveOnEveryTransport: before the cluster activates
// cluster-seal, every status says so and cluster unseal is refused as a
// failed precondition on every transport.
func TestClusterSeal_InactiveOnEveryTransport(t *testing.T) {
	cs := &testClusterSeal{Sealer: clustersealtest.New(false), streams: clusterseal.NewStreams()}
	srv := clusterSealServer(t, cs)
	ctx := context.Background()
	for name, tr := range dialTransports(t, srv) {
		t.Run(name, func(t *testing.T) {
			bc := tr.(transport.BarrierClient)
			st, err := bc.BarrierStatus(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if st.ClusterSeal == nil || st.ClusterSeal.Active {
				t.Fatalf("ClusterSeal = %+v, want present and inactive", st.ClusterSeal)
			}
			_, err = bc.BarrierClusterUnseal(ctx)
			if err == nil || !strings.Contains(err.Error(), "not active") {
				t.Fatalf("BarrierClusterUnseal while inactive: err = %v, want the inactive refusal", err)
			}
		})
	}
}

// TestClusterSeal_DatabaseRecordRefusedOnEveryTransport pins the data-plane
// half of a database seal record on REST, gRPC, Unix, QUIC and MCP, through
// the real registry: with the node serving and the database open on this
// node, the database's own sealed record refuses it with the typed sealed
// refusal; a clear record serves it again.
func TestClusterSeal_DatabaseRecordRefusedOnEveryTransport(t *testing.T) {
	mgr, db := openLegacyVault(t)
	db.SetStatus(database.StatusOpen)
	cs := newTestClusterSeal()

	socketPath := filepath.Join(t.TempDir(), "qrdb.sock")
	cfg := testConfigFull(t, protocolFlags{rest: true, grpc: true, quic: true, unix: true, mcp: true}, socketPath)
	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithCeremonyCA(testServerCA),
		WithDatabaseServiceRegistry(newLockedKVRegistry()),
		WithDatabaseManager(mgr),
		WithClusterSeal(cs),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	ctx := context.Background()

	b := clusterseal.DatabaseBarrier(clusterSealTenant(db.TenantID()), "legacyvault")
	cs.Put(clusterseal.Record{Barrier: b, Epoch: 1, Sealed: true, Principal: "owner", Node: "192.0.2.2:5000"})
	for name, tr := range dialSealedDBTransports(t, srv, "legacyvault") {
		if err := tr.Put(ctx, "secret", []byte("v")); !isSealedRefusal(err) {
			t.Errorf("%s Put on a database sealed by its record: err = %v, want a sealed-barrier refusal", name, err)
		}
		if _, err := tr.Get(ctx, "secret"); !isSealedRefusal(err) {
			t.Errorf("%s Get on a database sealed by its record: err = %v, want a sealed-barrier refusal", name, err)
		}
	}

	cs.Put(clusterseal.Record{Barrier: b, Epoch: 2, Sealed: false, Principal: "owner", Node: "192.0.2.2:5000"})
	for name, tr := range dialSealedDBTransports(t, srv, "legacyvault") {
		if err := tr.Put(ctx, "k", []byte("v")); err != nil {
			t.Errorf("%s Put after the record cleared: %v", name, err)
		}
	}
}
