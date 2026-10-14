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

package workload

import (
	"bytes"
	"context"
	"testing"
	"time"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/ledger"
)

// embeddedConn is the node's own client. The node outlives the workload, so
// closing a client's connection leaves it running.
type embeddedConn struct {
	transport.GuardedKVClient
}

func (embeddedConn) Close() error { return nil }

// startNode runs an in-process standalone node with no network listeners.
func startNode(t *testing.T) Dialer {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	// The test proves the workload's recording against a real store, not
	// authorization; the embedded transport's own tests cover auth.
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

	kv, ok := n.Client().KV().(transport.GuardedKVClient)
	if !ok {
		t.Fatalf("embedded client %T does not carry compare-and-swap", n.Client().KV())
	}
	return func(context.Context, string) (Conn, error) { return embeddedConn{kv}, nil }
}

func TestWorkloadAgainstStandaloneNode(t *testing.T) {
	dial := startNode(t)
	cfg := Config{
		Endpoints:     []string{"embedded"},
		Clients:       4,
		Keys:          4,
		Seed:          11,
		OpsPerClient:  100,
		OpTimeout:     Duration(5 * time.Second),
		RedialBackoff: Duration(10 * time.Millisecond),
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	rec := history.NewRecorder(history.NewMonotonicClock())
	w, err := New(cfg, dial, rec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	stats := w.Run(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := w.FinalRead(ctx); err != nil {
		t.Fatalf("FinalRead: %v", err)
	}
	if stats.Ops() != uint64(cfg.Clients*cfg.OpsPerClient) || stats.Fail != 0 || stats.Indeterminate != 0 {
		t.Fatalf("a healthy node answered with failures: %+v", stats)
	}

	// The evidence survives its own format: check the decoded history, not
	// the in-memory one.
	var buf bytes.Buffer
	if err := history.Encode(&buf, rec.History()); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	h, err := history.Decode(&buf)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	res, err := ledger.Checker{}.Check(h)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if !res.Valid || res.Checked == 0 {
		t.Fatalf("ledger on a single healthy node: valid=%v checked=%d anomalies=%+v", res.Valid, res.Checked, res.Anomalies)
	}
	swapped := 0
	for _, op := range h {
		if op.Kind == history.KindCAS && op.Output != nil && op.Output.Swapped {
			swapped++
		}
	}
	if swapped == 0 {
		t.Fatal("no compare-and-swap swapped against the node")
	}
}
