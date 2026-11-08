// Copyright 2026 Jeremy Hahn
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

package node

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward/replicaforwardtest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// TestNodeReplicaForwarder_StoppedServerDoesNotForward: once the server has
// stopped, its forwarder reports so, and an embedded call is answered locally
// (ok=false) with the refusal logged rather than forwarded.
func TestNodeReplicaForwarder_StoppedServerDoesNotForward(t *testing.T) {
	locator := replicaforward.NewLocator(nil, replicaforward.NewLeaderCache(), "a:1", nil)
	ca := replicaforwardtest.NewCA(t)
	srv := server.New(&qrdbconfig.Config{}, nil, nil,
		server.WithTLS(ca.TLS(t, "node-a")), server.WithCACertPool(ca.Pool), server.WithReplicaForwarding(locator, time.Second))
	srv.Stop()

	var logs bytes.Buffer
	n := &Node{logger: slog.New(slog.NewTextHandler(&logs, nil))}
	n.srv.Store(srv)
	f := &nodeReplicaForwarder{node: n}
	ctx := context.Background()

	refusal := &placement.NotAReplicaError{TenantID: "t1", Database: "db1"}
	if status, body, ok := f.Forward(ctx, &auth.Identity{}, "Get", "db1", nil, refusal); ok || status != 0 || body != nil {
		t.Fatalf("Forward on a stopped server = %d, %q, %v; want not forwarded", status, body, ok)
	}
	if status, body, ok := f.ForwardToLeader(ctx, &auth.Identity{}, "Get", "db1", nil, "b:2"); ok || status != 0 || body != nil {
		t.Fatalf("ForwardToLeader on a stopped server = %d, %q, %v; want not forwarded", status, body, ok)
	}
	for _, want := range []string{"embedded call not forwarded", "tenant=t1", "leader_raft_addr=b:2"} {
		if !bytes.Contains(logs.Bytes(), []byte(want)) {
			t.Errorf("log lacks %q:\n%s", want, logs.String())
		}
	}
}

// plainRegistry is a database registry that cannot refuse streams: it does
// not implement service.StreamRefuser.
type plainRegistry struct{ service.Registry }

// TestNodeServiceRegistry_StreamRefusalWithoutARefuser: a registry with no
// stream-refusal capability refuses nothing.
func TestNodeServiceRegistry_StreamRefusalWithoutARefuser(t *testing.T) {
	srv := server.New(&qrdbconfig.Config{}, nil, nil)
	n := &Node{}
	n.srv.Store(srv)
	r := &nodeServiceRegistry{node: n}
	r.cached.Store(&registryForServer{srv: srv, reg: plainRegistry{}})

	if err := r.StreamRefusal("t1", "db1"); err != nil {
		t.Fatalf("StreamRefusal = %v; want nil from a registry that cannot refuse", err)
	}
}

// TestSealedWindowConfig_ClusteredBoot: a node waiting on a clustered boot
// serves its sealed window from that boot's configuration.
func TestSealedWindowConfig_ClusteredBoot(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	n := &Node{pendingClustered: &pendingClusteredBoot{cfg: cfg}}
	if got := n.sealedWindowConfig(); got != cfg {
		t.Fatalf("sealedWindowConfig = %p; want the pending clustered boot's config %p", got, cfg)
	}
}

// lateRecordStore models a pending admin credential that lands after first
// boot resolved there was none: the first read finds nothing, later reads
// find the record, and the store refuses every write.
type lateRecordStore struct {
	reads, puts atomic.Int32
}

var _ firstboot.PendingAdminCertStore = (*lateRecordStore)(nil)

func (s *lateRecordStore) PutIfAbsent(context.Context, string, []byte) (bool, error) {
	return false, errors.New("store is read-only")
}

func (s *lateRecordStore) Get(_ context.Context, key string) ([]byte, error) {
	if key != firstboot.PendingAdminCertKey {
		return nil, nil // only the admin's record arrives late
	}
	if s.reads.Add(1) == 1 {
		return nil, nil
	}
	return []byte("-----BEGIN CERTIFICATE-----"), nil
}

func (s *lateRecordStore) Put(context.Context, string, []byte) error {
	s.puts.Add(1)
	return errors.New("store is read-only")
}

func (s *lateRecordStore) Delete(context.Context, string) error {
	return errors.New("store is read-only")
}

// TestNodeFirstBootWithCeremony_ClearFailureIsNotFatal: the binding already
// stands, so failing to mark the pending record consumed is logged and the
// first boot still succeeds.
func TestNodeFirstBootWithCeremony_ClearFailureIsNotFatal(t *testing.T) {
	users, roles := realStores(t)
	store := &lateRecordStore{}
	n := &Node{}
	n.bindingPolicy.Store(int32(auth.MTLSBindingEnrolled))

	result, err := n.FirstBootWithCeremony(context.Background(), store, nil, 0, fbDeps(users, roles))
	if err != nil || result == nil {
		t.Fatalf("FirstBootWithCeremony = %v, %v; want a result despite the clear failure", result, err)
	}
	if store.puts.Load() == 0 {
		t.Fatal("the clear never tried to mark the late record consumed")
	}
}

// TestCanonicalRootKeyStrategy_StoreFaultIsReturned: a seal store that
// cannot be read fails the lookup instead of reporting a strategy.
func TestCanonicalRootKeyStrategy_StoreFaultIsReturned(t *testing.T) {
	store := memory.NewStorage()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	n := &Node{sealStore: store}
	if id, err := canonicalRootKeyStrategy(context.Background(), n); err == nil || id != "" {
		t.Fatalf("strategy from a closed seal store = %q, %v; want an error", id, err)
	}
}

// listsAGhost is a composer whose listing names a strategy its lookup does
// not have.
type listsAGhost struct{ *seal.DefaultStrategyRegistry }

func (listsAGhost) ListAvailable() []seal.StrategyID { return []seal.StrategyID{"ghost"} }

// namedStrategy is a sealing strategy reporting a fixed ID.
type namedStrategy struct {
	seal.SealingStrategy
	id seal.StrategyID
}

func (s namedStrategy) ID() seal.StrategyID { return s.id }

// TestResolveNodeSealStrategies_RegistryFaults: a composer that cannot
// produce a strategy it lists, and an injected strategy with no name, each
// fail resolution with the registry's typed error.
func TestResolveNodeSealStrategies_RegistryFaults(t *testing.T) {
	ctx := context.Background()
	cfg := qrdbconfig.DefaultConfig()

	var missing *seal.StrategyNotRegisteredError
	_, err := resolveNodeSealStrategies(ctx, cfg, nil, listsAGhost{seal.NewStrategyRegistry()}, nil, "root-key")
	if !errors.As(err, &missing) || missing.Name != "ghost" {
		t.Fatalf("resolve with a ghost composer = %v; want StrategyNotRegisteredError for ghost", err)
	}

	var register *seal.StrategyRegisterError
	_, err = resolveNodeSealStrategies(ctx, cfg, &nodeOptions{sealStrategy: namedStrategy{}}, nil, nil, "root-key")
	if !errors.As(err, &register) {
		t.Fatalf("resolve with an unnamed injected strategy = %v; want StrategyRegisterError", err)
	}
}

// TestNodeUnseal_InitializeRefusalIsTyped: a fresh barrier that refuses its
// first key leaves the node sealed and reports why as an UnsealError.
func TestNodeUnseal_InitializeRefusalIsTyped(t *testing.T) {
	barrier, closeBarrier := newTestBarrier(t)
	defer closeBarrier()
	n := &Node{}
	n.barrier.Store(barrier)

	var unsealErr *UnsealError
	if err := n.Unseal(context.Background(), seal.Credentials{}); !errors.As(err, &unsealErr) || unsealErr.Err == nil {
		t.Fatalf("Unseal with empty credentials = %v; want an UnsealError carrying the refusal", err)
	}
	if !barrier.IsSealed() {
		t.Fatal("a refused Unseal left the barrier unsealed")
	}
}
