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

package clusterseal

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// TestDeferredBeforeSetIsInactive: services are built before the monitor
// exists; until it is set they behave as a cluster with cluster-seal not
// activated -- node-local seals, nothing refused.
func TestDeferredBeforeSetIsInactive(t *testing.T) {
	d := NewDeferred()
	if d.Active() || d.Refusing() || d.IsSealed() || d.Refuses(TenantBarrier("acme")) {
		t.Fatal("an unset Deferred is active or refusing")
	}
	if st := d.Status(NodeBarrier()); st != (Status{}) {
		t.Fatalf("Status = %+v", st)
	}
	if _, err := d.Seal(context.Background(), NodeBarrier(), ops); !errors.Is(err, ErrInactive) {
		t.Fatalf("Seal err = %v", err)
	}
	if _, err := d.Clear(context.Background(), NodeBarrier(), ops); !errors.Is(err, ErrInactive) {
		t.Fatalf("Clear err = %v", err)
	}
	if d.Streams() == nil {
		t.Fatal("Streams is nil before Set")
	}
}

// TestDeferredForwardsAndSharesStreams: once set, every method reaches the
// monitor, and a stream subscribed before Set is ended by the monitor.
func TestDeferredForwardsAndSharesStreams(t *testing.T) {
	d := NewDeferred()
	early := d.Streams().Subscribe("acme", "orders")
	h := newHarness(t, func(c *Config) { c.Streams = d.Streams() })
	d.Set(h.m)
	if !d.Active() || !d.Refusing() {
		t.Fatal("set Deferred is not forwarding")
	}
	h.poll(t)
	h.enf.tenants["acme"] = true
	if _, err := d.Seal(context.Background(), TenantBarrier("acme"), alice); err != nil {
		t.Fatal(err)
	}
	if !ended(early) {
		t.Fatal("a stream subscribed before Set survived the seal")
	}
	if !d.Refuses(TenantBarrier("acme")) || !d.Status(TenantBarrier("acme")).Record.Sealed {
		t.Fatal("Refuses/Status not forwarded")
	}
	h.enf.tenants["acme"] = true
	if _, err := d.Clear(context.Background(), TenantBarrier("acme"), alice); err != nil {
		t.Fatal(err)
	}
	if d.IsSealed() {
		t.Fatal("IsSealed not forwarded")
	}
}

func TestPrincipalFrom(t *testing.T) {
	if got := PrincipalFrom(context.Background()); got != (Principal{}) {
		t.Fatalf("anonymous principal = %+v", got)
	}
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice", TenantID: "acme"})
	if got := PrincipalFrom(ctx); got != alice {
		t.Fatalf("principal = %+v, want %+v", got, alice)
	}
}
