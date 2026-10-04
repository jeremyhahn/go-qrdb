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
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// establishClusterCA forces Key A's lazy cakey.Provider to materialize its
// key and certificate, the way a client-facing TLS listener's first
// resolve would. Remote cells trigger this themselves when their listener
// wires pkg/clienttrust.Pool; the embedded cell opens no network
// listener at all (sdk/go/transport/embedded has no TLS to wire), so
// nothing ever touches ClusterCAProv there unless a test does it
// explicitly -- mirroring issueThroughNode's role for Key B below.
func establishClusterCA(t *testing.T, n *node.Node) {
	t.Helper()
	prov := n.Services().ClusterCAProv
	if prov == nil {
		t.Fatal("the node has no cluster CA provider")
	}
	if _, err := prov.Certificate(boundedCtx(t)); err != nil {
		t.Fatalf("establish cluster CA: %v", err)
	}
}

// certCARecordKeys names the ONE record under a given CA prefix this test
// confines on: "material", the private-key record (go-qrdb#560 review: the
// two-record "wrapped_key"/"cert_pem" layout this test used to name does
// not exist any more -- a test naming nonexistent keys proves nothing,
// since Get on them is "not found" whether or not confinement is actually
// enforced).
//
// cakey.SoftwareProvider also writes a SEPARATE sibling record,
// "certificate" (go-qrdb#591's review, B1), in the SAME atomic batch as
// "material" -- but that record is deliberately NOT hard-confined (it is
// public data: the CA's own certificate, not its key) and is deliberately
// excluded here, not merely forgotten: this test proves the PRIVATE
// material is unreachable, and asserting "certificate" belongs on that same
// list would assert a confinement property this package does not and must
// not provide for it.
func certCARecordKeys(prefix string) []string {
	return []string{prefix + "material"}
}

// grantKV grants kv:put/kv:get/kv:list, mvcc:range and watch:watch in
// tenantID to the user named username, on top of whatever roles it already
// holds. bootConfinedCell's seeded Admin does not itself carry data-plane
// KV permissions (it is scoped to administrative actions), and the
// built-in Admin role cannot be re-permissioned (it is a system role), so
// a confinement test over the KV/MVCC/Watch surface needs its own,
// explicitly-granted role -- one broad enough that a refusal the test
// observes is confinement, not an incidental RBAC denial.
func grantKV(t *testing.T, n *node.Node, tenantID, username string) {
	t.Helper()
	svc := n.Services()
	ctx := boundedCtx(t)

	r, err := svc.Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name:     "kv-confinement-check",
		TenantID: tenantID,
		Permissions: []permission.Permission{
			permission.KVPut, permission.KVGet, permission.KVList, permission.KVRange,
			permission.MVCCRange, permission.WatchWatch,
		},
	})
	if err != nil {
		t.Fatalf("create KV role: %v", err)
	}
	u, err := svc.User.GetUserByUsername(ctx, tenantID, username)
	if err != nil {
		t.Fatalf("GetUserByUsername(%s): %v", username, err)
	}
	roleIDs := append([]string{}, u.RoleIDs...)
	roleIDs = append(roleIDs, r.ID)
	if _, err := svc.User.UpdateUser(ctx, &user.UpdateUserRequest{ID: u.ID, RoleIDs: roleIDs}); err != nil {
		t.Fatalf("grant KV role to %s: %v", username, err)
	}
}

// assertCAKeyUnreachable proves key's VALUE never reaches the caller
// through Get or Exists, regardless of which of two legitimate shapes the
// refusal takes: the registry-routed transports make the key physically
// absent in the caller's own rewritten tenant namespace (an ordinary
// *transport.KeyNotFoundError, which assertKeyAbsent already covers), and
// the embedded transport's hard-confinement wrapper instead REFUSES the
// point-key call outright with a *service.HardConfinedKeyError, the same
// treatment every other point-key verb (Put, Delete, ...) gets on that
// namespace -- consistent with that wrapper's own documented, approved
// design, which never silently reports a confined key as "not found".
// Both shapes satisfy the only property that matters here: the secret
// value is never returned successfully.
func assertCAKeyUnreachable(t *testing.T, cl clients, key, msg string) {
	t.Helper()
	ctx := boundedCtx(t)

	if exists, err := cl.kv.Exists(ctx, key); err == nil && exists {
		t.Fatalf("%s: Exists(%q) = true, want false or a refusal", msg, key)
	}
	if got, err := cl.kv.Get(ctx, key); err == nil {
		t.Fatalf("%s: Get(%q) succeeded and returned %q, want a refusal or not-found", msg, key, got)
	}
}

// assertValueBytesNeverLeak scans data (an MVCC Range result, or raw
// Watch event payloads) and fails if any entry's VALUE bytes contain
// secret, regardless of what key it is filed under. A confinement bug
// that renames or re-prefixes a key while leaving its value attached to
// something else would pass a key-name-only check and still leak the
// actual secret; this is why go-qrdb#591's review asked for byte-level
// assertions, not only name-level ones.
func assertValueBytesNeverLeak(t *testing.T, msg string, secret []byte, values [][]byte) {
	t.Helper()
	for _, v := range values {
		if len(secret) > 0 && bytes.Contains(v, secret) {
			t.Fatalf("%s: a returned value contains the confined secret bytes verbatim", msg)
		}
	}
}

// assertCANotInRangeWatchOrList proves key (whose privileged value is
// secret) is unreachable through MVCC().Range, Watch/WatchPrefix and
// KV().List -- not merely Get/Exists (assertKeyAbsent's job) -- on both
// the key name AND the raw value bytes.
func assertCANotInRangeWatchOrList(t *testing.T, tk transportKind, c *clients, ctx context.Context, key string, secret []byte) {
	t.Helper()

	// --- MVCC Range, wide open. []byte{0x00}, not []byte(""): an empty
	// start is the "shared store root" sentinel on the registry-routed
	// transports (pkg/api/grpc/service.go's own Range handler: "a nil
	// lower bound... would scan across the (tenant, db) namespace
	// boundary"), refused outright rather than treated as a real key, so
	// it must never be mistaken for one here either. ---
	rangeResp, err := c.mvcc.Range(ctx, []byte{0x00}, []byte("\xff"), transport.RangeOptions{})
	if err != nil {
		t.Fatalf("%s: MVCC().Range: %v", tk, err)
	}
	var rangeValues [][]byte
	for _, kv := range rangeResp.Kvs {
		if kv == nil {
			continue
		}
		if string(kv.Key) == key {
			t.Fatalf("%s: MVCC().Range returned the confined key %q", tk, key)
		}
		rangeValues = append(rangeValues, kv.Value)
	}
	assertValueBytesNeverLeak(t, string(tk)+": MVCC().Range("+key+")", secret, rangeValues)

	// --- List and Watch, rooted at "__sys:" rather than "": the REST
	// transport's handlers for both treat an empty key/prefix as "missing
	// parameter" and refuse it outright (handlers_watch.go,
	// go-qrdb#591's review). "__sys:" is every confined prefix's own
	// ancestor (hardConfinedPrefixOverlaps), so it is exactly as wide a
	// sweep for proving confinement, and it is accepted everywhere. ---
	const sweepPrefix = "__sys:"
	keys, err := c.kv.List(ctx, sweepPrefix)
	if err != nil {
		t.Fatalf("%s: KV().List(%q): %v", tk, sweepPrefix, err)
	}
	for _, k := range keys {
		if k == key {
			t.Fatalf("%s: KV().List(%q) returned the confined key %q", tk, sweepPrefix, key)
		}
	}

	// No sentinel write terminates this loop early: "__sys:" is the
	// reserved internal-key namespace, and an ordinary caller's own Put
	// is refused inside it (as it must be), so there is nothing this
	// caller could write that the watch would ever deliver back to it.
	// The loop simply drains for the full timeout instead.
	watchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ch, err := c.watch.WatchPrefix(watchCtx, sweepPrefix, transport.WatchOptions{StartRevision: 1})
	if err != nil {
		t.Fatalf("%s: Watch().WatchPrefix(%q): %v", tk, sweepPrefix, err)
	}
	for {
		select {
		case resp, ok := <-ch:
			if !ok {
				return
			}
			var values [][]byte
			for _, ev := range resp.Events {
				if ev.Kv != nil {
					if string(ev.Kv.Key) == key {
						t.Fatalf("%s: WatchPrefix delivered an event naming the confined key %q", tk, key)
					}
					values = append(values, ev.Kv.Value)
				}
				if ev.PrevKv != nil {
					if string(ev.PrevKv.Key) == key {
						t.Fatalf("%s: WatchPrefix delivered an event whose PrevKv names the confined key %q", tk, key)
					}
					values = append(values, ev.PrevKv.Value)
				}
			}
			assertValueBytesNeverLeak(t, string(tk)+": WatchPrefix(\""+key+"\")", secret, values)
		case <-watchCtx.Done():
			return
		}
	}
}

// TestCertRecords_NotReadableThroughGenericKV proves that the CA records
// cakey.SoftwareProvider and cert.DefaultCertProvider persist --
// __sys:cert_ca/<tenantID>/material (Key B) and __sys:cluster_ca/material
// (Key A) -- are never readable through the generic KV, MVCC, Watch or List
// surfaces, by ANY tenant, on ANY transport -- embedded included: not a
// tenant reading its OWN Key B record by guessing the path, and not a
// cross-tenant or cluster-level read either.
//
// go-qrdb#591's security review (B1) found this test SKIPPED the embedded
// transport entirely (the exact cell the review reproduced a plaintext
// Key B read on) and checked only Get/Exists, never MVCC Range, Watch or
// List, and never the raw value bytes -- only key names. All four gaps are
// closed here.
//
// Each record's existence is proven FIRST, with a privileged read through
// the node's own raw KV service (n.Services().KV, which the confined SDK
// client never goes through) -- so a confined Get reporting "not found"
// actually means confinement rewrote or refused the key before it reached
// the store, not that the record was never there to find in the first
// place. go-qrdb#560 review: the two-record "wrapped_key"/"cert_pem" paths
// this test used to name do not exist any more, and "err == nil &&
// len(val) > 0" as the failure condition passed on ANY error (including
// "no such key"), so neither the names nor the check this test used to
// make actually exercised the thing it claimed to.
//
// pkg/service/internal_keys.go's reserved-prefix guard is explicitly a
// DISCOVERY guard on bulk enumeration, not an authorization boundary: it
// does not stop a caller that already knows a key from reading it with Get.
// So the protection here has to come from elsewhere -- tenant keyspace
// confinement (pkg/tenant) on the registry-routed transports, and
// pkg/service's hard-confinement wrapping on the embedded transport -- and
// this test is what proves that holds for the CA prefixes specifically,
// since they are exactly the keys an attacker with the storage layout
// (this architecture's own docs) would try first.
func TestCertRecords_NotReadableThroughGenericKV(t *testing.T) {
	for _, tk := range userNotFoundTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootConfinedCell(t, tk, confineAcme)
			grantKV(t, c.node, confineAcme, "matrix")

			// Force Key B material to actually exist for both tenants, so a
			// successful read would be a real secret disclosure and not
			// merely a not-yet-initialized namespace.
			issueThroughNode(t, c.node, "alice", confineAcme)
			issueThroughNode(t, c.node, "bob", confineGlobex)
			establishClusterCA(t, c.node)

			// Positive control: acme's Admin CAN read and write its own
			// ordinary keys. Without this, every assertion below would also
			// pass if KV.Get refused every key unconditionally, which would
			// prove nothing about the CA prefixes specifically.
			if err := c.client.KV().Put(c.ctx, "ordinary-key", []byte("v")); err != nil {
				t.Fatalf("%s: positive control Put failed: %v", tk, err)
			}
			if got, err := c.client.KV().Get(c.ctx, "ordinary-key"); err != nil || string(got) != "v" {
				t.Fatalf("%s: positive control Get failed: got %q, err %v", tk, got, err)
			}

			keys := append(append(
				certCARecordKeys("__sys:cert_ca/"+confineAcme+"/"),
				certCARecordKeys("__sys:cert_ca/"+confineGlobex+"/")...),
				certCARecordKeys("__sys:cluster_ca/")...)

			rawKV := c.node.Services().KV
			cl := clients{kv: c.client.KV(), mvcc: c.client.MVCC(), watch: c.client.Watch()}
			for _, key := range keys {
				// Prove the record actually exists -- a privileged read
				// through the node's own raw KV service, which the confined
				// SDK client never reaches.
				val, err := rawKV.Get(c.ctx, key)
				if err != nil || len(val) == 0 {
					t.Fatalf("%s: setup: expected %q to exist via the privileged KV service, got val=%q err=%v",
						tk, key, val, err)
				}

				// The confined caller must never read the secret through
				// Get or Exists. Remote transports make the key physically
				// absent in the caller's own namespace, so the stricter
				// not-found contract (assertKeyAbsent) holds for them; the
				// embedded transport's hard-confinement wrapper refuses
				// outright instead (assertCAKeyUnreachable), which is its
				// own namespace's documented, approved behavior.
				if tk.remote() {
					assertKeyAbsent(t, cl, key, string(tk)+": acme's Admin reading "+key)
				} else {
					assertCAKeyUnreachable(t, cl, key, string(tk)+": acme's Admin reading "+key)
				}

				// ... and must never reach it through MVCC Range, Watch or
				// List either -- by key name or by value bytes.
				assertCANotInRangeWatchOrList(t, tk, &cl, c.ctx, key, val)
			}
		})
	}
}
