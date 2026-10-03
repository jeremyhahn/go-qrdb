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

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// sealCauseError is the refusal a test seal ends streams with.
type sealCauseError struct{ name string }

func (e *sealCauseError) Error() string { return "sealed: " + e.name }

// expectSealedEnd reads ch until it closes and requires the stream to have
// ended with a terminal frame carrying cause.
func expectSealedEnd(t *testing.T, ch <-chan transport.WatchResponse, cause error) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	var terminal *transport.WatchResponse
	for {
		select {
		case resp, ok := <-ch:
			if !ok {
				if terminal == nil {
					t.Fatal("the watch closed with no terminal frame; a seal must end it with its refusal")
				}
				if terminal.CancelReason != cause.Error() {
					t.Fatalf("terminal frame reason = %q, want %q", terminal.CancelReason, cause.Error())
				}
				return
			}
			if resp.Canceled {
				r := resp
				terminal = &r
			}
		case <-deadline:
			t.Fatal("the watch was still open 10s after its barrier was sealed")
		}
	}
}

// TestWatch_SealEndsTheStream pins the embedded half of "a seal ends every
// stream on the sealed barrier": an unscoped embedded Watch and WatchPrefix
// subscribe to the node's seal stream registry, as the network listeners'
// watches do through the server's registry, so a seal ends them with its
// refusal as the terminal frame.
func TestWatch_SealEndsTheStream(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(*Transport, context.Context) (<-chan transport.WatchResponse, error)
		end  func(*clusterseal.Streams, error)
	}{
		{"Watch, node seal", func(tr *Transport, ctx context.Context) (<-chan transport.WatchResponse, error) {
			return tr.Watch(ctx, "k", transport.WatchOptions{})
		}, func(s *clusterseal.Streams, c error) { s.EndAll(c) }},
		{"WatchPrefix, node seal", func(tr *Transport, ctx context.Context) (<-chan transport.WatchResponse, error) {
			return tr.WatchPrefix(ctx, "k", transport.WatchOptions{})
		}, func(s *clusterseal.Streams, c error) { s.EndAll(c) }},
		{"Watch, system tenant seal", func(tr *Transport, ctx context.Context) (<-chan transport.WatchResponse, error) {
			return tr.Watch(ctx, "k", transport.WatchOptions{})
		}, func(s *clusterseal.Streams, c error) { s.EndTenant(tenantid.System, c) }},
		{"Watch, system default database seal", func(tr *Transport, ctx context.Context) (<-chan transport.WatchResponse, error) {
			return tr.Watch(ctx, "k", transport.WatchOptions{})
		}, func(s *clusterseal.Streams, c error) { s.EndDatabase(tenantid.System, tenant.DefaultDatabase, c) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streams := clusterseal.NewStreams()
			hub := watch.NewHub()
			t.Cleanup(hub.Close)
			tr := newExtTransport(t, WithWatchService(service.NewWatchService(hub)), WithSealStreams(streams))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ch, err := tc.open(tr, ctx)
			if err != nil {
				t.Fatal(err)
			}
			notifyPut(hub, "k", 1)
			recvWatch(t, ch)
			cause := &sealCauseError{name: tc.name}
			tc.end(streams, cause)
			expectSealedEnd(t, ch, cause)
		})
	}
}

// TestWatch_OtherTenantSealLeavesTheStream proves the subscription is scoped:
// a seal of another tenant leaves the stream open, and closing the stream
// releases its subscription.
func TestWatch_OtherTenantSealLeavesTheStream(t *testing.T) {
	streams := clusterseal.NewStreams()
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	tr := newExtTransport(t, WithWatchService(service.NewWatchService(hub)), WithSealStreams(streams))
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := tr.Watch(ctx, "k", transport.WatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := streams.Len(); got != 1 {
		t.Fatalf("subscriptions after one watch = %d, want 1", got)
	}
	streams.EndTenant("acme", &sealCauseError{name: "acme"})
	notifyPut(hub, "k", 1)
	if resp := recvWatch(t, ch); resp.Canceled {
		t.Fatalf("another tenant's seal ended the stream: %+v", resp)
	}
	cancel()
	for range ch {
	}
	deadline := time.Now().Add(10 * time.Second)
	for streams.Len() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("subscriptions after the watch ended = %d, want 0", streams.Len())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestWithDatabase_LeavesWatchGuardingToTheRegistry: a database-scoped
// transport's watches resolve through the service registry, whose watch
// service the server already guards for the scoped (tenant, database), so the
// scoped clone does not subscribe them a second time -- while the unscoped
// transport keeps its own guard.
func TestWithDatabase_LeavesWatchGuardingToTheRegistry(t *testing.T) {
	streams := clusterseal.NewStreams()
	tr := newExtTransport(t, WithWatchService(service.NewWatchService(watch.NewHub())),
		WithServiceRegistry(newScopeRegistry()), WithSealStreams(streams))
	if tr.sealStreams != streams {
		t.Fatal("the unscoped transport lost its seal stream registry")
	}
	scoped, err := tr.WithDatabase("ledger")
	if err != nil {
		t.Fatal(err)
	}
	if s := scoped.(*Transport).sealStreams; s != nil {
		t.Fatal("the database-scoped transport guards its watches itself; the registry already does")
	}
}

// hookedWatchService runs onOpen as each watch opens, before it is returned,
// so a test can land a seal mid-open.
type hookedWatchService struct {
	service.WatchServicer
	onOpen func()
}

func (h *hookedWatchService) Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error) {
	h.onOpen()
	return h.WatchServicer.Watch(ctx, key, opts...)
}

func (h *hookedWatchService) WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error) {
	h.onOpen()
	return h.WatchServicer.WatchPrefix(ctx, prefix, opts...)
}

// TestWatch_SealLandingMidOpenEndsTheStream: a seal that lands while an
// embedded watch is opening -- after the gate admitted it, before the stream
// subscribed -- must still end it. The seal closes the gate and then ends
// every stream, as the monitor does, from inside the open.
func TestWatch_SealLandingMidOpenEndsTheStream(t *testing.T) {
	for name, open := range map[string]func(*Transport, context.Context) (<-chan transport.WatchResponse, error){
		"Watch": func(tr *Transport, ctx context.Context) (<-chan transport.WatchResponse, error) {
			return tr.Watch(ctx, "k", transport.WatchOptions{})
		},
		"WatchPrefix": func(tr *Transport, ctx context.Context) (<-chan transport.WatchResponse, error) {
			return tr.WatchPrefix(ctx, "k", transport.WatchOptions{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			streams := clusterseal.NewStreams()
			probe := &gateProbe{}
			hub := watch.NewHub()
			t.Cleanup(hub.Close)
			cause := &sealCauseError{name: name}
			inner := &hookedWatchService{WatchServicer: service.NewWatchService(hub), onOpen: func() {
				probe.sealed.Store(true)
				streams.EndAll(cause)
			}}
			tr := newExtTransport(t, WithWatchService(inner), WithSealStreams(streams), WithSealProbe(probe))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ch, err := open(tr, ctx)
			if err != nil {
				return // refusing the open outright is as good as ending it
			}
			deadline := time.After(5 * time.Second)
			for {
				select {
				case _, ok := <-ch:
					if !ok {
						return
					}
				case <-deadline:
					t.Fatal("an embedded watch opened as the seal landed outlived the seal")
				}
			}
		})
	}
}

// refusingRegistry is a service registry whose stream refusal refuses one
// tenant, as the server's does for a tenant sealed by its own record.
type refusingRegistry struct {
	service.Registry
	sealedTenant string
}

var _ service.StreamRefuser = (*refusingRegistry)(nil)

func (r *refusingRegistry) StreamRefusal(tenantID, _ string) error {
	if tenantID == r.sealedTenant {
		return &clusterseal.SealedError{Barrier: clusterseal.TenantBarrier(tenantID)}
	}
	return nil
}

// TestWatch_RechecksTheTenantRefusal: an unscoped embedded watch, once
// subscribed, asks the registry's stream refusal for its own tenant -- not
// only the node probe -- so a watch of a sealed tenant is refused, and another
// tenant's opens.
func TestWatch_RechecksTheTenantRefusal(t *testing.T) {
	streams := clusterseal.NewStreams()
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	tr := newExtTransport(t, WithWatchService(service.NewWatchService(hub)), WithSealStreams(streams),
		WithSealProbe(&gateProbe{}), WithServiceRegistry(&refusingRegistry{sealedTenant: "acme"}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var sealed *clusterseal.SealedError
	if _, err := tr.Watch(tenant.WithTenantID(ctx, "acme"), "k", transport.WatchOptions{}); !errors.As(err, &sealed) {
		t.Fatalf("a watch of a sealed tenant: err = %v, want its sealed refusal", err)
	}
	if streams.Len() != 0 {
		t.Fatalf("a refused watch left %d subscription(s)", streams.Len())
	}
	if _, err := tr.Watch(tenant.WithTenantID(ctx, "globex"), "k", transport.WatchOptions{}); err != nil {
		t.Fatalf("another tenant's watch: %v", err)
	}
}
