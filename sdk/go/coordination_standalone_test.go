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

package sdk_test

import (
	"context"
	"testing"
	"time"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
)

// bootStandaloneNode brings up a real single-node, non-raft qrdb with no
// protocol servers and returns the coordination surface a consumer gets from
// node.Client(). This is the exact in-process surface go-trusted-platform would
// hold when it embeds qrdb standalone; the assertions run against real
// services, not fakes.
func bootStandaloneNode(t *testing.T) *sdk.Coordinator {
	t.Helper()

	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
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

	coord, err := n.Client().Coordination()
	if err != nil {
		t.Fatalf("Coordination: %v", err)
	}
	return coord
}

// TestStandalone_GetPutCAS is the identical-in-both-modes half of the surface:
// Get, Put and CompareAndSwap must serve on a standalone node exactly as they
// do under raft (go-qrdb#62).
func TestStandalone_GetPutCAS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	coord := bootStandaloneNode(t)

	// Absent key reads as (nil, nil).
	got, err := coord.Get(ctx, "cfg/x")
	if err != nil || got != nil {
		t.Fatalf("Get absent = (%v, %v), want (nil, nil)", got, err)
	}

	// Put then Get round-trips.
	if err := coord.Put(ctx, "cfg/x", []byte("v1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err = coord.Get(ctx, "cfg/x")
	if err != nil || string(got) != "v1" {
		t.Fatalf("Get = (%q, %v), want (v1, nil)", got, err)
	}

	// CompareAndSwap create-if-absent: first wins, second sees the key present.
	ok, err := coord.CompareAndSwap(ctx, "lock/leader", nil, []byte("node-a"))
	if err != nil || !ok {
		t.Fatalf("CAS create = (%v, %v), want (true, nil)", ok, err)
	}
	ok, err = coord.CompareAndSwap(ctx, "lock/leader", nil, []byte("node-b"))
	if err != nil {
		t.Fatalf("CAS create (second) errored: %v", err)
	}
	if ok {
		t.Fatal("CAS create-if-absent must fail once the key exists")
	}

	// CompareAndSwap value-match: correct expect swaps, wrong expect does not.
	ok, err = coord.CompareAndSwap(ctx, "lock/leader", []byte("node-a"), []byte("node-c"))
	if err != nil || !ok {
		t.Fatalf("CAS value-match = (%v, %v), want (true, nil)", ok, err)
	}
	ok, err = coord.CompareAndSwap(ctx, "lock/leader", []byte("stale"), []byte("node-d"))
	if err != nil {
		t.Fatalf("CAS wrong-expect errored: %v", err)
	}
	if ok {
		t.Fatal("CAS with a stale expect must not swap")
	}
	got, err = coord.Get(ctx, "lock/leader")
	if err != nil || string(got) != "node-c" {
		t.Fatalf("final value = (%q, %v), want (node-c, nil)", got, err)
	}
}

// TestStandalone_LeaseAndWatchServed: AcquireLease and Watch are served on a
// standalone node exactly as on a raft node (go-qrdb#582). They used to be
// refused with "requires mode: raft", which is what pushed hosts onto
// in-process fallbacks and a one-member raft cluster.
func TestStandalone_LeaseAndWatchServed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	coord := bootStandaloneNode(t)

	w, err := coord.Watch(ctx, "lease/")
	if err != nil {
		t.Fatalf("Watch on a standalone node: %v", err)
	}
	l, err := coord.AcquireLease(ctx, "lease/k", 10*time.Second)
	if err != nil {
		t.Fatalf("AcquireLease on a standalone node: %v", err)
	}
	select {
	case ev, ok := <-w.Events():
		if !ok || ev.Key != "lease/k" {
			t.Fatalf("watch delivered %+v (open=%v), want the lease's key", ev, ok)
		}
	case <-ctx.Done():
		t.Fatal("the watch never delivered the leased put")
	}
	if err := l.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
}
