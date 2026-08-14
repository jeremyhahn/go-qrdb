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

package engine

import (
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// mvccHistoryAdapter adapts an mvcc.Store to watch.HistoryReplayer so a watch
// created with a start revision replays historical changes from the store's
// durable revision index before switching to live delivery. It converts the
// store's revision-ordered change log into watch events; because
// service.KeyValue is an alias of kv.KeyValue, the store values need no copy.
type mvccHistoryAdapter struct {
	store *mvcc.Store
}

func newMVCCHistoryAdapter(store *mvcc.Store) *mvccHistoryAdapter {
	return &mvccHistoryAdapter{store: store}
}

// CurrentRevision implements watch.HistoryReplayer.
func (a *mvccHistoryAdapter) CurrentRevision() int64 { return a.store.CurrentRevision() }

// CompactedRevision implements watch.HistoryReplayer.
func (a *mvccHistoryAdapter) CompactedRevision() int64 { return a.store.CompactedRevision() }

// Replay implements watch.HistoryReplayer by streaming the store's revision
// index over [startRev, endRev] and yielding one watch event per matching
// change, in revision order.
func (a *mvccHistoryAdapter) Replay(startRev, endRev int64, keyMatch func([]byte) bool, wantPrev bool, fn func(*watch.Event) bool) error {
	return a.store.ScanChanges(startRev, endRev, func(c mvcc.Change) bool {
		if !keyMatch(c.Key) {
			return true
		}

		ev := &watch.Event{}
		if c.IsDelete {
			ev.Type = watch.EventDelete
			// A delete event's KV carries only the key and its mod revision,
			// matching the live delete notification shape.
			ev.Kv = &kv.KeyValue{Key: c.Key, ModRevision: c.Revision}
		} else {
			ev.Type = watch.EventPut
			ev.Kv = &kv.KeyValue{
				Key:            c.Key,
				Value:          c.Value,
				CreateRevision: c.CreateRevision,
				ModRevision:    c.Revision,
				Version:        c.Version,
				LeaseID:        c.LeaseID,
			}
		}

		if wantPrev {
			// The previous state is the newest version strictly before this
			// change's revision. A first create (or a revision that has been
			// compacted) leaves PrevKv nil, matching live semantics.
			if prev, err := a.store.GetAtRevision(c.Key, c.Revision-1); err == nil {
				ev.PrevKv = prev
			}
		}

		return fn(ev)
	})
}

// Compile-time assertion that the adapter satisfies the replayer contract.
var _ watch.HistoryReplayer = (*mvccHistoryAdapter)(nil)
