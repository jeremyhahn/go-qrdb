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

package embedded

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newCoordinationServer boots the flat embedded server over memory storage.
func newCoordinationServer(t *testing.T) *Server {
	t.Helper()
	srv, err := NewServer(context.Background(), &ServerConfig{Backend: database.BackendMemory})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return srv
}

// The flat embedded server used to build its engine with no watch hub, so a
// watch opened through it registered on a hub nothing published to and never
// delivered -- and it composed no watch or lock service at all. It now
// composes the engine, watch hub and lessor the standalone node does.
func TestServer_WatchDeliversChanges(t *testing.T) {
	srv := newCoordinationServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ch, err := srv.Client().Watch().WatchPrefix(ctx, "flat/", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	if err := srv.Client().KV().Put(ctx, "flat/a", []byte("1")); err != nil {
		t.Fatalf("put: %v", err)
	}
	select {
	case resp, ok := <-ch:
		if !ok || len(resp.Events) == 0 || string(resp.Events[0].Kv.Key) != "flat/a" {
			t.Fatalf("watch delivered %+v (open=%v), want the PUT of flat/a", resp, ok)
		}
	case <-ctx.Done():
		t.Fatal("the watch never delivered the put")
	}
}

// A lease nobody keeps alive expires and takes its key with it: the server
// runs the expiry sweeper over the engine's lessor.
func TestServer_LeaseExpiryDeletesKeys(t *testing.T) {
	srv := newCoordinationServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := srv.Client()

	l, err := c.Lease().LeaseGrant(ctx, 1)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := c.MVCC().PutWithLease(ctx, "flat/leased", []byte("v"), l.ID); err != nil {
		t.Fatalf("put with lease: %v", err)
	}
	for {
		if _, err := c.KV().Get(ctx, "flat/leased"); err != nil {
			var notFound *transport.KeyNotFoundError
			if !errors.As(err, &notFound) {
				t.Fatalf("get after expiry: %v, want KeyNotFoundError", err)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("the leased key outlived its 1s lease: nothing swept it")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func TestServer_LockServed(t *testing.T) {
	srv := newCoordinationServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lk := srv.Client().Lock()

	held, err := lk.LockTryLock(ctx, &transport.LockAcquireRequest{Key: "flat/lock", HolderID: "a", TTL: 30})
	if err != nil {
		t.Fatalf("try-lock: %v", err)
	}
	if _, err := lk.LockTryLock(ctx, &transport.LockAcquireRequest{Key: "flat/lock", HolderID: "b", TTL: 30}); err == nil {
		t.Fatal("a second holder acquired a held lock")
	}
	if err := lk.LockRelease(ctx, &transport.LockReleaseRequest{Key: "flat/lock", FencingToken: held.FencingToken}); err != nil {
		t.Fatalf("release: %v", err)
	}
}

// A transport built without a family's servicer reports a wiring defect, the
// same for every family: there is no longer a mode in which a family is
// absent by design.
func TestTransport_AbsentServiceIsAWiringDefect(t *testing.T) {
	tr := newExtTransport(t)
	var nse *NilServiceError
	if _, err := tr.LeaseGrant(t.Context(), 5); !errors.As(err, &nse) {
		t.Errorf("LeaseGrant: err = %T (%v), want *NilServiceError", err, err)
	}
	if _, err := tr.LockAcquire(t.Context(), &transport.LockAcquireRequest{Key: "k", HolderID: "h", TTL: 5}); !errors.As(err, &nse) {
		t.Errorf("LockAcquire: err = %T (%v), want *NilServiceError", err, err)
	}
	if _, err := tr.GetRevision(t.Context(), "k"); !errors.As(err, &nse) {
		t.Errorf("GetRevision: err = %T (%v), want *NilServiceError", err, err)
	}
}
