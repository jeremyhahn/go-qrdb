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

package conformance

import (
	"context"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// clusterCAMaterialKey is pkg/cakey.SoftwareProvider's literal storage key
// for the cluster CA's private key, reproduced (not imported) for the same
// reason sdk/go/transport/embedded's own confinement test does: a security
// regression test should name the wire-level string itself, not a constant
// that could silently drift with the production code it is meant to catch
// drifting.
const clusterCAMaterialKey = "__sys:cluster_ca/material"

// TestClusterCAKeyMaterial_UnreachableOnEveryRemoteTransport is
// go-qrdb#555's B2 regression proof for the five registry-routed
// transports (REST, gRPC, QUIC, Unix, MCP): every (tenant, db) pair
// pkg/server's restServiceRegistry resolves is composite-namespaced
// (tenant.ComposeNamespace), including the system tenant's own default
// database, so a literal "__sys:cluster_ca/material" request never
// physically reaches the raw record regardless of the caller's identity
// or permissions -- proven here with a caller EXPLICITLY granted full raw
// KV/MVCC/Watch permissions, so a pass is not merely "the caller lacked
// the right," it is "the key itself is unreachable."
//
// This is structural, pre-existing behavior (composite namespacing), not
// new code -- sdk/go/transport/embedded's OWN cluster_ca_confinement_test.go
// is where the actual B2 fix is proven (the UN-namespaced embedded
// transport's services wrapped with the hard-confinement decorators),
// because that surface has no namespace prefix to rely on. This test
// exists so "unreachable on every transport" is checked on the other
// five, not merely assumed from the namespacing argument.
//
// The material is seeded directly through the node's own raw, unprefixed
// service.KVServicer (booted.Services().KV) during the seed callback,
// before the client connects -- never through the client under test,
// which would composite-prefix the write to a harmless key under its own
// namespace instead of the real one.
func TestClusterCAKeyMaterial_UnreachableOnEveryRemoteTransport(t *testing.T) {
	rawKVPerms := []permission.Permission{
		permission.KVGet, permission.KVPut, permission.KVDelete,
		permission.KVList, permission.KVExists, permission.KVRange,
		permission.MVCCRange, permission.WatchWatch,
	}

	for _, tk := range transportKinds {
		if tk == embeddedTransport {
			// Covered by sdk/go/transport/embedded's own dedicated test,
			// which seeds through that package's own un-namespaced raw
			// servicer -- this harness's "embedded" cell is the full
			// pkg/server/lifecycle composition reached in-process, which
			// IS behind the registry like every remote transport, not the
			// sdk/go/transport/embedded surface B2 is actually about.
			continue
		}
		t.Run(string(tk), func(t *testing.T) {
			var booted *node.Node
			secretMaterial := []byte("-----BEGIN EC PRIVATE KEY----- the cluster's actual signing key -----END EC PRIVATE KEY-----")
			client := bootAuthorizedCell(t, tk, func(n *node.Node) {
				booted = n
				seedIdentityFixtures(t, n, rawKVPerms...)
				if err := n.Services().KV.Put(context.Background(), clusterCAMaterialKey, secretMaterial); err != nil {
					t.Fatalf("%s: seeding the cluster CA key material directly: %v", tk, err)
				}
			})

			// Confirm the seed landed on the physical key, so a pass below
			// is not a false negative from seeding the wrong place.
			if got, err := booted.Services().KV.Get(context.Background(), clusterCAMaterialKey); err != nil || string(got) != string(secretMaterial) {
				t.Fatalf("%s: seed readback via the raw servicer = %q, %v; want the seeded material, nil", tk, got, err)
			}

			ctx := boundedCtx(t)

			if v, err := client.KV().Get(ctx, clusterCAMaterialKey); err == nil && v != nil {
				t.Fatalf("%s: KV().Get(cluster CA key) = %q, want absent or refused", tk, v)
			}

			rangeResp, err := client.MVCC().Range(ctx, []byte(""), []byte("\xff"), transport.RangeOptions{Revision: 1})
			if err == nil {
				for _, kv := range rangeResp.Kvs {
					if string(kv.Key) == clusterCAMaterialKey {
						t.Fatalf("%s: MVCC().Range returned the cluster CA key material", tk)
					}
				}
			}

			watchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			ch, err := client.Watch().WatchPrefix(watchCtx, "", transport.WatchOptions{StartRevision: 1})
			if err != nil {
				return
			}
			if err := client.KV().Put(ctx, "ordinary/sentinel-"+string(tk), []byte("v")); err != nil {
				t.Fatalf("%s: Put(ordinary sentinel): %v", tk, err)
			}
			for {
				select {
				case resp, ok := <-ch:
					if !ok {
						return
					}
					for _, ev := range resp.Events {
						if ev.Kv != nil && string(ev.Kv.Key) == clusterCAMaterialKey {
							t.Fatalf("%s: WatchPrefix delivered an event naming the cluster CA key material", tk)
						}
						if ev.PrevKv != nil && string(ev.PrevKv.Key) == clusterCAMaterialKey {
							t.Fatalf("%s: WatchPrefix delivered a delete/update event whose PrevKv names the cluster CA key material", tk)
						}
						if ev.Kv != nil && string(ev.Kv.Key) == "ordinary/sentinel-"+string(tk) {
							return
						}
					}
				case <-watchCtx.Done():
					return
				}
			}
		})
	}
}
