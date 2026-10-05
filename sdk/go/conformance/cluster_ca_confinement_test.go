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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
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

// clusterCACertificateKey is pkg/cakey.SoftwareProvider's companion record
// to clusterCAMaterialKey -- the certificate half of the same atomic batch
// write, reproduced for the same reason. Unlike the key material, the
// certificate is public data (pkg/cakey's own Seal doc comment says so) and
// this test does not assert it is confined; it is seeded here only so the
// pair is self-consistent.
const clusterCACertificateKey = "__sys:cluster_ca/certificate"

// generateFakeClusterCA returns a syntactically valid, self-signed EC P-256
// key and certificate pair in the exact DER encoding
// pkg/cakey.SoftwareProvider.generate persists (raw SEC1 DER for the key,
// not PEM -- x509.MarshalECPrivateKey, parsed back with
// x509.ParseECPrivateKey). It exists so this test can seed the cluster CA's
// real storage keys with material that is confined (this test's whole
// point) yet still parses: an earlier version of this test seeded a PEM-
// looking placeholder string directly into the DER-only record, which
// pkg/clienttrust.Pool's live reads of the cluster CA (introduced by the
// per-tenant Key B trust pool) now fail to parse on every connection
// attempt, withdrawing trust for reasons unrelated to what this test
// checks. A real, self-signed, matching pair keeps that resolution healthy
// while still exercising the confinement proof below.
func generateFakeClusterCA(t *testing.T) (keyDER, certDER []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating fake cluster CA key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generating fake cluster CA serial: %v", err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "qrdb confinement-test fake cluster CA"},
		NotBefore:             now,
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	certDER, err = x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("self-signing fake cluster CA certificate: %v", err)
	}
	keyDER, err = x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("encoding fake cluster CA key: %v", err)
	}
	return keyDER, certDER
}

// TestClusterCAKeyMaterial_UnreachableOnEveryRemoteTransport is
// go-qrdb#555's B2 regression proof across every transport this harness
// can boot, including embedded. The five registry-routed transports
// (REST, gRPC, QUIC, Unix, MCP) are safe structurally: every (tenant, db)
// pair pkg/server's restServiceRegistry resolves is composite-namespaced
// (tenant.ComposeNamespace), including the system tenant's own default
// database, so a literal "__sys:cluster_ca/material" request never
// physically reaches the raw record regardless of the caller's identity
// or permissions -- proven here with a caller EXPLICITLY granted full raw
// KV/MVCC/Watch permissions, so a pass is not merely "the caller lacked
// the right," it is "the key itself is unreachable."
//
// The embedded cell is NOT structurally safe the same way: this harness's
// "embedded" cell boots through node.NewStandaloneNode, which calls
// sdk/go/transport/embedded.New directly (node.go's own composition),
// the same un-namespaced construction sdk/go/transport/embedded's own
// server.go uses -- one of the real composers go-qrdb#555's delta review
// found unwrapped when only NewServer wrapped. It was excluded from this
// loop (go-qrdb#570) while that was true, which would have made this
// test pass by skipping the one cell actually exercising the bug. Now
// that embedded.New itself wraps kvSvc and every configured MVCC/
// transaction/batch/watch service, this cell is included like the rest.
func TestClusterCAKeyMaterial_UnreachableOnEveryRemoteTransport(t *testing.T) {
	rawKVPerms := []permission.Permission{
		permission.KVGet, permission.KVPut, permission.KVDelete,
		permission.KVList, permission.KVExists, permission.KVRange,
		permission.MVCCRange, permission.WatchWatch,
	}

	for _, tk := range transportKinds {
		t.Run(string(tk), func(t *testing.T) {
			var booted *node.Node
			var roleID string
			secretMaterial, fakeCertDER := generateFakeClusterCA(t)
			client := bootAuthorizedCell(t, tk, func(n *node.Node) {
				booted = n
				roleID, _ = seedIdentityFixtures(t, n, rawKVPerms...)
				if err := n.Services().KV.Put(context.Background(), clusterCAMaterialKey, secretMaterial); err != nil {
					t.Fatalf("%s: seeding the cluster CA key material directly: %v", tk, err)
				}
				if err := n.Services().KV.Put(context.Background(), clusterCACertificateKey, fakeCertDER); err != nil {
					t.Fatalf("%s: seeding the cluster CA certificate directly: %v", tk, err)
				}
			})

			// Confirm the seed landed on the physical key, so a pass below
			// is not a false negative from seeding the wrong place.
			if got, err := booted.Services().KV.Get(context.Background(), clusterCAMaterialKey); err != nil || string(got) != string(secretMaterial) {
				t.Fatalf("%s: seed readback via the raw servicer = %q, %v; want the seeded material, nil", tk, got, err)
			}

			ctx := boundedCtx(t)
			if tk == embeddedTransport {
				// The embedded transport authenticates nothing on its own;
				// bind the same identity and tenant the remote cells
				// authenticate as via their client certificate (the pair
				// every embedded authorization gate reads: auth.WithIdentity
				// paired with tenant.WithTenantID).
				ctx = tenant.WithTenantID(auth.WithIdentity(ctx, &auth.Identity{
					Subject:  "matrix",
					TenantID: tenantid.System,
					Roles:    []string{roleID},
				}), tenantid.System)
			}

			if v, err := client.KV().Get(ctx, clusterCAMaterialKey); err == nil && v != nil {
				t.Fatalf("%s: KV().Get(cluster CA key) = %q, want absent or refused", tk, v)
			}

			// A wide-open Range or Watch is a legitimate call this
			// namespace's own hard-confinement wrapper FILTERS rather than
			// refuses (pkg/service/hard_confined_mvcc.go, hard_confined_watch.go):
			// go-qrdb#591's review found this test treating either call's
			// OWN error as an acceptable outcome, silently skipping the
			// check it exists to make rather than failing it. Both calls
			// below must succeed; an error here is itself the regression.
			rangeResp, err := client.MVCC().Range(ctx, []byte{0x00}, []byte("\xff"), transport.RangeOptions{Revision: 1})
			if err != nil {
				t.Fatalf("%s: MVCC().Range(\"\", \"\\xff\", revision 1): %v", tk, err)
			}
			for _, kv := range rangeResp.Kvs {
				if kv == nil {
					continue
				}
				if string(kv.Key) == clusterCAMaterialKey {
					t.Fatalf("%s: MVCC().Range returned the cluster CA key material", tk)
				}
				// Assert on value bytes too, not only the key name: a
				// confinement bug that renamed or re-prefixed the key
				// while leaving the secret attached to it would pass a
				// key-name-only check and still leak the actual material.
				if bytes.Contains(kv.Value, secretMaterial) {
					t.Fatalf("%s: MVCC().Range returned a value containing the cluster CA key material", tk)
				}
			}

			// "__sys:cluster_ca/", not "": the REST transport's watch
			// handler treats an empty key/prefix as a missing required
			// parameter and refuses it outright (handlers_watch.go,
			// go-qrdb#591's review), and "__sys:cluster_ca/" is the exact
			// confined prefix anyway -- as targeted a sweep as this test
			// needs. No sentinel write terminates the loop early: it is
			// the reserved internal-key namespace, so there is nothing an
			// ordinary Put inside it that would ever land; the loop drains
			// for the full timeout instead.
			watchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			ch, err := client.Watch().WatchPrefix(watchCtx, "__sys:cluster_ca/", transport.WatchOptions{StartRevision: 1})
			if err != nil {
				t.Fatalf("%s: Watch().WatchPrefix(\"__sys:cluster_ca/\", revision 1): %v", tk, err)
			}
			for {
				select {
				case resp, ok := <-ch:
					if !ok {
						return
					}
					for _, ev := range resp.Events {
						if ev.Kv != nil {
							if string(ev.Kv.Key) == clusterCAMaterialKey {
								t.Fatalf("%s: WatchPrefix delivered an event naming the cluster CA key material", tk)
							}
							if bytes.Contains(ev.Kv.Value, secretMaterial) {
								t.Fatalf("%s: WatchPrefix delivered an event whose value contains the cluster CA key material", tk)
							}
						}
						if ev.PrevKv != nil {
							if string(ev.PrevKv.Key) == clusterCAMaterialKey {
								t.Fatalf("%s: WatchPrefix delivered a delete/update event whose PrevKv names the cluster CA key material", tk)
							}
							if bytes.Contains(ev.PrevKv.Value, secretMaterial) {
								t.Fatalf("%s: WatchPrefix delivered a delete/update event whose PrevKv value contains the cluster CA key material", tk)
							}
						}
					}
				case <-watchCtx.Done():
					return
				}
			}
		})
	}
}

// TestHarnessLogin_SurvivesCorruptClusterCARecord is go-qrdb#591's B3
// regression proof, restoring the unparseable-placeholder scenario this
// file's own TestClusterCAKeyMaterial_UnreachableOnEveryRemoteTransport
// moved away from (see generateFakeClusterCA's own doc comment): seeding a
// non-DER placeholder directly into the cluster CA's real storage key
// corrupts pkg/cakey.SoftwareProvider's ability to resolve Key A, and
// before the B3 fix that cascaded into pkg/clienttrust.Pool's
// isRegisteredKeyBCA answering "uncertain" forever, which made
// IsStaticallyTrusted refuse the exemption for EVERY issuer -- including
// the harness's own ad-hoc CA, which has nothing to do with Key A or Key
// B at all. A harness login (the remote cells' own client certificate,
// issued by remoteServerTLS's ad-hoc CA, authenticated purely through the
// static exemption) must keep authenticating despite the corruption.
func TestHarnessLogin_SurvivesCorruptClusterCARecord(t *testing.T) {
	corruptMaterial := []byte("-----BEGIN EC PRIVATE KEY----- not actually DER -----END EC PRIVATE KEY-----")

	for _, tk := range transportKinds {
		if !tk.remote() {
			continue // the embedded cell has no TLS handshake, and so no static exemption to break
		}
		t.Run(string(tk), func(t *testing.T) {
			client := bootAuthorizedCell(t, tk, func(n *node.Node) {
				seedIdentityFixtures(t, n, permission.KVGet, permission.KVPut)
				if err := n.Services().KV.Put(context.Background(), clusterCAMaterialKey, corruptMaterial); err != nil {
					t.Fatalf("%s: seeding the corrupt cluster CA key material: %v", tk, err)
				}
			})

			ctx := boundedCtx(t)
			// The harness login itself (the mTLS handshake underlying
			// every call client makes) is what this test proves survives
			// the corruption; an ordinary authenticated KV round-trip is
			// the simplest proof it did.
			if err := client.KV().Put(ctx, "harness-login-survives", []byte("v")); err != nil {
				t.Fatalf("%s: harness login was refused by an unrelated corrupt cluster CA record: %v", tk, err)
			}
			if got, err := client.KV().Get(ctx, "harness-login-survives"); err != nil || string(got) != "v" {
				t.Fatalf("%s: Get after a successful harness-login Put = %q, %v", tk, got, err)
			}
		})
	}
}
