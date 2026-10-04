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

package lifecycle

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// PlacementChanges is the placement table's change feed (Node.PlacementChanges).
type PlacementChanges interface {
	Watch(ctx context.Context) (<-chan placement.Event, error)
}

// placementKV is the replicated system KV the placement table lives in,
// with its change feed: a placement.Store over it surfaces the feed through
// Watch, so a node sees a placement change as it applies it, and replica
// forwarding drops the leader it cached for the database. It keeps the
// adapter's CompareAndSwap (placement.AtomicKV).
type placementKV struct {
	*kvStoreAdapter
	watch service.WatchServicer
}

var (
	_ placement.KV       = (*placementKV)(nil)
	_ placement.AtomicKV = (*placementKV)(nil)
	_ placement.Watcher  = (*placementKV)(nil)
)

// newPlacementKV returns the placement table's KV over svc and idx, with
// changes fed from w.
func newPlacementKV(svc service.KVServicer, idx service.IndexServicer, w service.WatchServicer) *placementKV {
	return &placementKV{kvStoreAdapter: &kvStoreAdapter{svc: svc, idx: idx}, watch: w}
}

// Watch returns the changes to the keys under prefix, as this node applies
// them, until ctx ends or the watch does: a watch the hub cancels -- a
// watcher that fell behind, say -- closes the channel, and the consumer
// re-subscribes. A node built without a watch service has no feed:
// placement.ErrWatchUnsupported.
func (p *placementKV) Watch(ctx context.Context, prefix string) (<-chan placement.WatchEvent, error) {
	if p.watch == nil {
		return nil, placement.ErrWatchUnsupported
	}
	w, err := p.watch.WatchPrefix(ctx, prefix, watch.WithPrevKV())
	if err != nil {
		return nil, err
	}
	out := make(chan placement.WatchEvent)
	go func() {
		defer close(out)
		defer func() { _ = w.Close() }()
		for {
			select {
			case <-ctx.Done():
				return
			case resp, ok := <-w.Events():
				if !ok || resp.Canceled {
					return
				}
				for _, ev := range resp.Events {
					pe, ok := placementWatchEvent(ev)
					if !ok {
						continue
					}
					select {
					case <-ctx.Done():
						return
					case out <- pe:
					}
				}
			}
		}
	}()
	return out, nil
}

// placementWatchEvent translates one KV change.
func placementWatchEvent(ev *watch.Event) (placement.WatchEvent, bool) {
	if ev == nil || ev.Kv == nil {
		return placement.WatchEvent{}, false
	}
	pe := placement.WatchEvent{Deleted: ev.Type == watch.EventDelete, Key: string(ev.Kv.Key)}
	if !pe.Deleted {
		pe.Value = ev.Kv.Value
	}
	if ev.PrevKv != nil {
		pe.PrevValue = ev.PrevKv.Value
	}
	return pe, true
}
