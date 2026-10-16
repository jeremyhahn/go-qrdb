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

package lifecycle_test

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// keyCheckingPauser records how often it was asked to pause and whether the
// node barrier still held its key at the last pause.
type keyCheckingPauser struct {
	barrier *seal.StorageBarrier
	calls   int
	keyHeld bool
}

var _ service.ShardPauser = (*keyCheckingPauser)(nil)

func (p *keyCheckingPauser) PauseShards(context.Context) error {
	p.calls++
	p.keyHeld = !p.barrier.IsSealed()
	return nil
}

// buildBarrierNode builds the node's services over a fresh barrier, with
// pauser as Infra.ShardPauser when it is non-nil.
func buildBarrierNode(t *testing.T, pauser *keyCheckingPauser) (*lifecycle.Node, *seal.StorageBarrier) {
	t.Helper()
	host := newLocalHost()
	t.Cleanup(func() { _ = host.Close() })
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	barrier, registry, credSvc := newTestBarrier(t)
	infra := lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
	}
	if pauser != nil {
		pauser.barrier = barrier
		infra.ShardPauser = pauser
	}
	node, err := lifecycle.BuildServices(context.Background(), infra)
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	t.Cleanup(func() { _ = node.Stop(context.Background()) })
	return node, barrier
}

func TestBuildServices_TheBarrierSealPausesThroughTheShardPauser(t *testing.T) {
	pauser := &keyCheckingPauser{}
	node, barrier := buildBarrierNode(t, pauser)
	if err := node.Barrier.BarrierSeal(context.Background()); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}
	if pauser.calls != 1 || !pauser.keyHeld {
		t.Fatalf("paused %d times, key held %v; want once, before the wipe", pauser.calls, pauser.keyHeld)
	}
	if !barrier.IsSealed() {
		t.Fatal("the node barrier kept its key")
	}
}

func TestBuildServices_TheBarrierSealWithoutAShardPauserStillSeals(t *testing.T) {
	node, barrier := buildBarrierNode(t, nil)
	if err := node.Barrier.BarrierSeal(context.Background()); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}
	if !barrier.IsSealed() {
		t.Fatal("the node barrier kept its key")
	}
}
