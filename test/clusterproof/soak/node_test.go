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

//go:build clusterproof

package soak

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/linearizability"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/workload"
)

// embeddedConn is the node's own client; the node outlives every connection.
type embeddedConn struct {
	transport.GuardedKVClient
}

func (embeddedConn) Close() error { return nil }

// adminCluster is the Cluster over one in-process node's own admin client,
// standing in for SDKCluster, which dials over the network.
type adminCluster struct {
	name  string
	admin transport.AdminClient
}

func (c adminCluster) Leader(ctx context.Context) (string, error) {
	st, err := c.admin.Status(ctx)
	if err != nil {
		return "", err
	}
	if !st.IsLeader {
		return "", &LeaderError{}
	}
	return c.name, nil
}

func (c adminCluster) Shards(ctx context.Context, _ string) (map[uint64]uint64, error) {
	peers, err := c.admin.Peers(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[uint64]uint64, len(peers.Shards))
	for _, s := range peers.Shards {
		out[s.ShardID] = s.LeaderID
	}
	return out, nil
}

func (c adminCluster) ReplicaStates(ctx context.Context) ([]ReplicaState, error) {
	resp, err := c.admin.ReplicaStates(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := make([]ReplicaState, 0, len(resp.Replicas))
	for _, r := range resp.Replicas {
		out = append(out, ReplicaState{Node: c.name, ShardID: r.ShardID, ReplicaID: r.ReplicaID, AppliedIndex: r.AppliedIndex, Hash: r.Hash})
	}
	return out, nil
}

// startNode runs an in-process standalone node with no network listeners.
func startNode(t *testing.T) *node.Node {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	// The smoke proves the soak's path end to end, not authorization.
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false)
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	n, err := node.NewStandaloneNode(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("NewStandaloneNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

// TestSmokeAgainstStandaloneNode is make cluster-proof with PROOF_NEMESIS=none
// against one in-process node: the environment, the run, the bundle and the
// report, with only the dialer and the cluster swapped for the node's own
// client. A none run proves nothing about a fault, so it is invalid -- and
// says so -- while the ledger and the linearizability checker pass on a
// healthy node.
func TestSmokeAgainstStandaloneNode(t *testing.T) {
	n := startNode(t)
	kv, ok := n.Client().KV().(transport.GuardedKVClient)
	if !ok {
		t.Fatalf("embedded client %T does not carry compare-and-swap", n.Client().KV())
	}
	bundle := filepath.Join(t.TempDir(), "bundle")
	checkers := []history.Checker{linearizability.Checker{}}
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), Env{
		Getenv: envOf(map[string]string{
			EnvSeal: "none", EnvNodes: "node-1=embedded", EnvNemesis: "none", EnvDuration: "300ms", EnvBundle: bundle,
		}),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Harness: Harness{Version: "smoke"},
		Configure: func(o *Options) error {
			o.Dial = func(context.Context, string) (workload.Conn, error) { return embeddedConn{kv}, nil }
			cluster := adminCluster{name: "node-1", admin: n.Client().Admin()}
			o.Cluster, o.Replicas = cluster, cluster
			o.Checkers = checkers
			return nil
		},
	})
	if code != ExitInvalid {
		t.Fatalf("smoke = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, ExitInvalid, stdout.String(), stderr.String())
	}

	res, err := Check(bundle, CheckOptions{Checkers: checkers})
	if err != nil {
		t.Fatalf("Check of the smoke bundle: %v", err)
	}
	v := res.Verdict
	if !hasReason(v, "no nemesis configured") {
		t.Errorf("reasons %q lack the missing nemesis", v.Reasons)
	}
	// A standalone node hosts no raft shard, so E4 cannot judge leadership,
	// and it refuses the replica-state read, so E3 cannot compare replicas.
	want := map[string]Status{"E1": StatusPass, "E2": StatusPass, "E3": StatusUnavailable, "E4": StatusUnavailable}
	for id, st := range want {
		if c := criterion(t, v, id); c.Status != st {
			t.Errorf("%s = %s (%s), want %s", id, c.Status, c.Detail, st)
		}
	}
	if c := criterion(t, v, "E3"); !strings.Contains(c.Detail, "cannot read replica states") || !strings.Contains(c.Detail, "mode: raft") {
		t.Errorf("E3 detail %q does not carry the standalone node's refusal", c.Detail)
	}
	if !strings.Contains(stdout.String(), "manifest sha256: "+res.ManifestSHA256) {
		t.Errorf("the report does not print the bundle's manifest digest:\n%s", stdout.String())
	}
}
