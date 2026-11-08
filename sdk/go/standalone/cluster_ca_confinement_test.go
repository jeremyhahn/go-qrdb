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

package standalone

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// clusterCAMaterialKey is the literal key pkg/cakey.SoftwareProvider
// stores the cluster CA's private key under on the system shard --
// "__sys:cluster_ca/material". It is reproduced here, not imported, for
// the same reason a security regression test should: this test proves
// the WIRE-LEVEL name is unreachable, so it must name the wire-level
// string itself rather than a constant that could silently drift with
// the production code it is meant to catch drifting.
const clusterCAMaterialKey = "__sys:cluster_ca/material"

// TestEmbedded_ClusterCAKeyMaterialUnreachable is go-qrdb#555's B2
// regression proof: a data directory that was once part of a Raft
// cluster carries "__sys:cluster_ca/material" on its default shard, and
// the embedded SDK transport -- which the desktop webview's bindings
// (cmd/qrdb/bindings_kv.go) talk to directly -- serves that shard with
// NO composite namespace prefix in front of it, unlike pkg/server's
// registry. Before this fix, KV().Get, MVCC().Range (even "from
// revision 1", the reviewer's own probe) and Watch/WatchPrefix all
// returned the cluster CA's private key material in the clear.
//
// The key is seeded by a SEPARATE, unwrapped service.KVServicer
// constructed directly over the running server's own backend and shard
// -- never through srv.Client(), which is the hard-confined surface
// under test and would refuse the write. This reproduces, without
// actually running pkg/cakey, exactly what a real cluster node's
// cakey.SoftwareProvider would have left on disk: pkg/cakey is never
// wired into this embedded composition at all (see
// service.hardConfinedPrefix's own doc comment), so this is the only
// way the key could get there, and it is also untouched by (and
// unaffected by) the wrapping under test.
func TestEmbedded_ClusterCAKeyMaterialUnreachable(t *testing.T) {
	ctx := context.Background()
	srv, err := NewServer(ctx, &ServerConfig{Backend: database.BackendMemory})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() {
		if err := srv.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}()

	// Seed the real key material directly on the shard, bypassing the
	// hard-confined KVServicer under test entirely.
	raw := service.NewKVService(srv.localBackend, serverDefaultShardID)
	// The armour is assembled at run time so the source carries no literal
	// private-key header, which secret scanners flag even on a placeholder.
	keyType := "EC PRIVATE" + " KEY"
	secretMaterial := []byte("-----BEGIN " + keyType + "----- the cluster's actual signing key -----END " + keyType + "-----")
	if err := raw.Put(ctx, clusterCAMaterialKey, secretMaterial); err != nil {
		t.Fatalf("seeding the cluster CA key material directly: %v", err)
	}
	// Confirm the seed actually landed on the SAME shard the client-facing
	// surfaces below read from, so a pass below is not a false negative
	// from seeding the wrong place.
	if got, err := raw.Get(ctx, clusterCAMaterialKey); err != nil || string(got) != string(secretMaterial) {
		t.Fatalf("seed readback via the raw servicer = %q, %v; want the seeded material, nil", got, err)
	}

	client := srv.Client()

	// --- Exact Get ---
	if _, err := client.KV().Get(ctx, clusterCAMaterialKey); err == nil {
		t.Fatal("KV().Get(cluster CA key) succeeded, want a refusal")
	} else {
		var confErr *service.HardConfinedKeyError
		if !errors.As(err, &confErr) {
			t.Fatalf("KV().Get error = %v (%T), want *service.HardConfinedKeyError", err, err)
		}
	}

	// --- Put / Delete / CompareAndSwap ---
	if err := client.KV().Put(ctx, clusterCAMaterialKey, []byte("overwrite")); err == nil {
		t.Fatal("KV().Put(cluster CA key) succeeded, want a refusal")
	}
	if err := client.KV().Delete(ctx, clusterCAMaterialKey); err == nil {
		t.Fatal("KV().Delete(cluster CA key) succeeded, want a refusal")
	}

	// --- Range, "from revision 1" (the reviewer's own probe shape):
	// must not refuse the whole call (a wide-open range is legitimate),
	// and must not include the confined key in its result. ---
	rangeResp, err := client.MVCC().Range(ctx, []byte(""), []byte("\xff"), transport.RangeOptions{Revision: 1})
	if err != nil {
		t.Fatalf("MVCC().Range(\"\", \"\\xff\", revision 1): %v", err)
	}
	for _, kv := range rangeResp.Kvs {
		if string(kv.Key) == clusterCAMaterialKey {
			t.Fatal("MVCC().Range returned the cluster CA key material")
		}
	}

	// --- Count and Revision correctness: the review found
	// MVCCKVServicer.Range's return order (kvs, count, revision, err)
	// bound backwards in the decorator -- a mistake that compiles, since
	// both are int64, and passed every pre-existing test because the test
	// double encoded the identical swap. A real write here, after which
	// Revision must equal the store's own CurrentRevision (not, for
	// instance, the count) and Count must equal len(Kvs) exactly (the
	// confined key is the only thing either value ever excludes in this
	// cell), is what a live cell catches that a fake cannot. ---
	// Overwritten (not merely written once) several times: this advances
	// the revision counter on each write without adding a second distinct
	// key, so the eventual Revision and Count are forced APART (distinct
	// values), which is what makes a swap between them detectable here --
	// the first run of this exact assertion, with only one write, passed
	// even with the swap reverted because count and revision happened to
	// coincide at that exact point.
	for i := 0; i < 4; i++ {
		if err := client.KV().Put(ctx, "ordinary/for-range-check", []byte("v")); err != nil {
			t.Fatalf("Put(ordinary/for-range-check) #%d: %v", i, err)
		}
	}
	currentRev, err := client.MVCC().CurrentRevision(ctx)
	if err != nil {
		t.Fatalf("MVCC().CurrentRevision: %v", err)
	}
	wideResp, err := client.MVCC().Range(ctx, []byte(""), []byte("\xff"), transport.RangeOptions{})
	if err != nil {
		t.Fatalf("MVCC().Range(\"\", \"\\xff\"): %v", err)
	}
	if wideResp.Revision != currentRev {
		t.Fatalf("MVCC().Range revision = %d, want %d (CurrentRevision) -- a count/revision swap would report the count (%d) here instead",
			wideResp.Revision, currentRev, wideResp.Count)
	}
	if wideResp.Count != int64(len(wideResp.Kvs)) {
		t.Fatalf("MVCC().Range count = %d, want %d (len(Kvs): the confined key is the only thing either value should ever exclude here)",
			wideResp.Count, len(wideResp.Kvs))
	}

	// --- Watch from revision 1 over a wide-open prefix: must not refuse
	// the whole watch, and must never deliver an event naming the
	// confined key. ---
	watchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ch, err := client.Watch().WatchPrefix(watchCtx, "", transport.WatchOptions{StartRevision: 1})
	if err != nil {
		t.Fatalf("Watch().WatchPrefix(\"\", revision 1): %v", err)
	}

	// Trigger a write that would be delivered as an event IF the watch
	// were unfiltered, racing nothing: the seed Put above already
	// happened before the watch was created, so a buggy implementation
	// that replays history from revision 1 would deliver it. Also prove
	// an ordinary, concurrent write while (not through) the watch is
	// delivered, so a timeout below is known to mean "filtered", not
	// "the watch itself is broken".
	if err := client.KV().Put(ctx, "ordinary/sentinel", []byte("v")); err != nil {
		t.Fatalf("Put(ordinary/sentinel): %v", err)
	}

	sawSentinel := false
	for {
		select {
		case resp, ok := <-ch:
			if !ok {
				if !sawSentinel {
					t.Fatal("watch channel closed before the ordinary sentinel event arrived")
				}
				return
			}
			for _, ev := range resp.Events {
				if ev.Kv != nil && string(ev.Kv.Key) == clusterCAMaterialKey {
					t.Fatal("WatchPrefix delivered an event naming the cluster CA key material")
				}
				if ev.PrevKv != nil && string(ev.PrevKv.Key) == clusterCAMaterialKey {
					t.Fatal("WatchPrefix delivered a delete/update event whose PrevKv names the cluster CA key material")
				}
				if ev.Kv != nil && string(ev.Kv.Key) == "ordinary/sentinel" {
					sawSentinel = true
				}
			}
			if sawSentinel {
				return
			}
		case <-watchCtx.Done():
			t.Fatal("timed out waiting for the ordinary sentinel event (watch did not deliver anything)")
		}
	}
}
