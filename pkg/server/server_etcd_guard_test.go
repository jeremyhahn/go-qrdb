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

//go:build qrdb_etcd

package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// The etcd listener guards its streams with its database: the guard closes
// when the database is sealed, and the stream then ends with the sealed
// refusal (Unavailable); when the database is dropped, with its not-found.
func TestEtcdRequestAuthorizer_GuardStream(t *testing.T) {
	cases := []struct {
		name string
		stop func(*database.Database)
		want codes.Code
	}{
		{"sealed", func(db *database.Database) { db.SetStatus(database.StatusSealed) }, codes.Unavailable},
		{"dropped", func(db *database.Database) { _ = db.Close() }, codes.NotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newEtcdScopeFixture(t, true)
			a := &etcdRequestAuthorizer{mgr: f.mgr, owner: "acme", db: "app"}
			unserved, refusal, err := a.GuardStream(context.Background())
			if err != nil {
				t.Fatalf("GuardStream: %v", err)
			}
			select {
			case <-unserved:
				t.Fatal("the guard is closed while the database serves")
			default:
			}
			db, _ := f.mgr.Get("app")
			c.stop(db)
			select {
			case <-unserved:
			default:
				t.Fatal("the guard did not close when the database stopped serving")
			}
			if got := status.Code(refusal()); got != c.want {
				t.Fatalf("refusal = %v, want %v", refusal(), c.want)
			}
		})
	}
}

// A listener whose database is gone cannot guard a stream, and refuses it.
func TestEtcdRequestAuthorizer_GuardStreamUnknownDatabase(t *testing.T) {
	f := newEtcdScopeFixture(t, true)
	a := &etcdRequestAuthorizer{mgr: f.mgr, owner: "acme", db: "nosuchdb"}
	if _, _, err := a.GuardStream(context.Background()); status.Code(err) != codes.NotFound {
		t.Fatalf("GuardStream on a missing database = %v, want NotFound", err)
	}
}

// A listener guards its owner's database by the (owner, name) pair: another
// tenant holding a database of the same name neither makes the guard
// ambiguous nor ends the stream when that other database stops serving.
func TestEtcdRequestAuthorizer_GuardStreamGuardsTheOwnersDatabase(t *testing.T) {
	f := newEtcdScopeFixture(t, true)
	other, err := f.mgr.Create(database.DatabaseConfig{Name: "app", TenantID: "globex", Backend: database.BackendMemory})
	if err != nil {
		t.Fatalf("create globex's app: %v", err)
	}
	a := &etcdRequestAuthorizer{mgr: f.mgr, owner: "acme", db: "app"}
	unserved, _, err := a.GuardStream(context.Background())
	if err != nil {
		t.Fatalf("GuardStream with a second tenant's app: %v", err)
	}
	other.SetStatus(database.StatusSealed)
	select {
	case <-unserved:
		t.Fatal("another tenant's database of the same name ended the stream")
	default:
	}
	own, err := f.mgr.GetOwned("app", "acme")
	if err != nil {
		t.Fatalf("acme's app: %v", err)
	}
	own.SetStatus(database.StatusSealed)
	select {
	case <-unserved:
	default:
		t.Fatal("the guard did not close when the owner's database stopped serving")
	}
}

// With cluster sealing, the etcd stream guard also closes when the database's
// tenant is sealed, or the node is cut off, and the stream ends with that
// refusal; a database that stops serving still ends it with its own. The
// subscription is released when the stream ends.
func TestEtcdRequestAuthorizer_GuardStreamEndsOnClusterSeal(t *testing.T) {
	cases := []struct {
		name string
		end  func(*clusterseal.Streams, *database.Database)
		want codes.Code
	}{
		{"tenant sealed", func(s *clusterseal.Streams, _ *database.Database) {
			s.EndTenant("acme", &clusterseal.SealedError{Barrier: clusterseal.TenantBarrier("acme")})
		}, codes.Unavailable},
		{"cut off", func(s *clusterseal.Streams, _ *database.Database) {
			s.EndAll(&clusterseal.SealStateUnconfirmedError{})
		}, codes.Unavailable},
		{"database dropped", func(_ *clusterseal.Streams, db *database.Database) { _ = db.Close() }, codes.NotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newEtcdScopeFixture(t, true)
			streams := clusterseal.NewStreams()
			a := &etcdRequestAuthorizer{mgr: f.mgr, owner: "acme", db: "app", streams: streams}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			unserved, refusal, err := a.GuardStream(ctx)
			if err != nil {
				t.Fatalf("GuardStream: %v", err)
			}
			db, _ := f.mgr.Get("app")
			c.end(streams, db)
			select {
			case <-unserved:
			case <-time.After(5 * time.Second):
				t.Fatal("the guard did not close")
			}
			if got := status.Code(refusal()); got != c.want {
				t.Fatalf("refusal = %v, want %v", refusal(), c.want)
			}
		})
	}
	t.Run("stream end releases the subscription", func(t *testing.T) {
		f := newEtcdScopeFixture(t, true)
		streams := clusterseal.NewStreams()
		a := &etcdRequestAuthorizer{mgr: f.mgr, owner: "acme", db: "app", streams: streams}
		ctx, cancel := context.WithCancel(context.Background())
		if _, _, err := a.GuardStream(ctx); err != nil {
			t.Fatalf("GuardStream: %v", err)
		}
		if streams.Len() != 1 {
			t.Fatalf("subscriptions = %d", streams.Len())
		}
		cancel()
		deadline := time.Now().Add(5 * time.Second)
		for streams.Len() != 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if streams.Len() != 0 {
			t.Fatal("the subscription outlived its stream")
		}
	})
}

// TestEtcdRequestAuthorizer_GuardStreamSealLandingAsItOpens: a seal that lands
// just after the stream's open was admitted -- the node-refusal check passed,
// the seal then closes the gate and ends every stream -- must still end the
// stream. The refusal check below answers "serving" once and lands the seal as
// it returns, which is the narrowest point of the window.
func TestEtcdRequestAuthorizer_GuardStreamSealLandingAsItOpens(t *testing.T) {
	f := newEtcdScopeFixture(t, true)
	streams := clusterseal.NewStreams()
	cause := &clusterseal.SealStateUnconfirmedError{}
	var calls atomic.Int32
	refusal := func() error {
		if calls.Add(1) == 1 {
			// Admitted; the seal lands right behind the check, as the
			// monitor lands it: gate closed (later calls refuse), then End*.
			streams.EndAll(cause)
			return nil
		}
		return cause
	}
	a := &etcdRequestAuthorizer{mgr: f.mgr, owner: "acme", db: "app", streams: streams, nodeRefusal: refusal}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ended, _, err := a.GuardStream(ctx)
	if err != nil {
		return // refusing the open outright is as good as ending it
	}
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("an etcd stream opened as the seal landed outlived the seal")
	}
}
