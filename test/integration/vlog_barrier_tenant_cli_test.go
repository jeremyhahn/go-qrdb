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

//go:build integration

// vlog_barrier_tenant_cli_test.go drives `qrdb vlog` as a barrier-isolated
// tenant's identity through the real client CLI on every transport. The
// tenant's logs live in its own barrier-sealed home group: while the barrier
// is unsealed every verb works and its proofs verify; once the barrier is
// sealed every verb is refused as sealed; after the unseal the log is still
// there, read back from the same store.
package integration_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/transparency-dev/merkle/proof"
	"github.com/transparency-dev/merkle/rfc6962"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

func TestVlog_CLI_BarrierTenant_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testVlogBarrierTenantOverProtocol(t, node, proto)
		})
	}
}

// TestVlog_CLI_BarrierDatabase_PerProtocol pins that a verifiable log in a
// barrier-enabled database is refused over every transport, although the
// database's barrier is unsealed: the log has no store under the database's
// key, and a log kept outside it would stay readable while it is sealed.
func TestVlog_CLI_BarrierDatabase_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			dbName := fmt.Sprintf("vlogbarrier-%s-%d", proto.name, time.Now().UnixNano())
			t.Cleanup(func() {
				_, _ = runCLIProto(t, node, proto, "database", "drop", dbName)
			})
			if out, err := runCLIProto(t, node, proto, "database", "create", dbName,
				"--data-dir", t.TempDir(),
				"--backend", "pebble", "--barrier-enabled",
				"--barrier-strategy", "passphrase",
				"--barrier-passphrase", "vlog-barrier-database-passphrase",
			); err != nil {
				t.Fatalf("[%s] database create %s (barrier-enabled): %v\noutput: %s", proto.name, dbName, err, out)
			}

			// A log outside any database names the caller's routed tenant,
			// the one the refusal renders.
			probe := vlogCLIJSON[vlogwire.CreateResult](t, node, proto, "create", "--origin", vlogCLIOrigin, "--log", "probe-"+dbName)
			want := (&service.BarrierDatabaseLogNotSupportedError{TenantID: probe.TenantID, Database: dbName}).Error()

			logName := "barrier-db-" + proto.name
			for _, args := range [][]string{
				{"create", "--origin", vlogCLIOrigin, "--database", dbName, "--log", logName},
				{"append", "--database", dbName, "--log", logName, "--entry", "e0"},
				{"head", "--database", dbName, "--log", logName},
			} {
				vlogCLIRefused(t, node, proto, want, args...)
			}
		})
	}
}

func testVlogBarrierTenantOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()
	tenantID := fmt.Sprintf("vlb-%s-%d", proto.name, time.Now().UnixNano())

	// A self-managed barrier tenant: its barrier registers sealed and
	// uninitialized; its first admin initializes it (trust-on-first-init).
	adminCert, adminKey := createTenantWithAdminCert(t, node, proto, tenantID, "Vlog Barrier Tenant", "barrier", false, tenantID+"-admin")
	adminConn := tenantCLIArgsForProto(node, proto, adminCert, adminKey)
	if out, err := runCLI(t, node.cli, append(adminConn, "tenant", "barrier-init", "--id", tenantID, "--strategy", "none")...); err != nil {
		t.Fatalf("[%s] barrier-init: %v\noutput: %s", proto.name, err, out)
	}

	certPath, keyPath := node.ca.generateClientCertForTenant(t, tenantID)
	provisionTenantIdentityFor(t, node, tenantID, "vlog-barrier-"+proto.name, append([]string{
		"tenant_barrier:status", "tenant_barrier:seal", "tenant_barrier:unseal", "vlog:sign_head",
	}, vlogTenantPermissions...), certPath)
	conn := tenantCLIArgsForProto(node, proto, certPath, keyPath)
	logName := "barrier-" + proto.name

	// Unsealed: every verb is served from the tenant's own store, the
	// software signing key's sealed record included.
	created := vlogTenantJSON[vlogwire.CreateResult](t, node, proto, conn, "create", "--origin", vlogCLIOrigin, "--log", logName, "--signing-key-type", "ed25519")
	if created.Log != logName || created.Key == nil || created.Key.Version != 1 || len(created.Key.PublicKey) == 0 {
		t.Fatalf("[%s] vlog create = %+v, want log %q with a version-1 key and its public key", proto.name, created, logName)
	}
	vlogTenantJSON[vlogwire.AppendResult](t, node, proto, conn, "append", "--log", logName, "--entry", "e0", "--entry", "e1")
	head2 := vlogTenantJSON[vlogwire.Head](t, node, proto, conn, "head", "--log", logName)
	vlogTenantJSON[vlogwire.AppendResult](t, node, proto, conn, "append", "--log", logName, "--entry", "e2", "--entry", "e3", "--entry", "e4")
	head5 := vlogTenantJSON[vlogwire.Head](t, node, proto, conn, "head", "--log", logName)
	if head2.Size != 2 || head5.Size != 5 {
		t.Fatalf("[%s] head sizes %d, %d; want 2 and 5", proto.name, head2.Size, head5.Size)
	}
	got := vlogTenantJSON[vlogwire.GetResult](t, node, proto, conn, "get", "--log", logName, "--from", "1", "--limit", "3")
	if len(got.Entries) != 3 || string(got.Entries[0].Body) != "e1" || string(got.Entries[2].Body) != "e3" {
		t.Fatalf("[%s] get --from 1 --limit 3 = %+v, want e1..e3", proto.name, got.Entries)
	}
	incl := vlogTenantJSON[vlogwire.InclusionProof](t, node, proto, conn, "prove-inclusion", "--log", logName, "--index", "3", "--size", "5")
	if err := proof.VerifyInclusion(rfc6962.DefaultHasher, incl.Index, incl.Size, rfc6962.DefaultHasher.HashLeaf([]byte("e3")), incl.Hashes, head5.Root); err != nil {
		t.Fatalf("[%s] prove-inclusion of e3 does not verify against the size-5 root: %v", proto.name, err)
	}
	cons := vlogTenantJSON[vlogwire.ConsistencyProof](t, node, proto, conn, "prove-consistency", "--log", logName, "--size1", "2", "--size2", "5")
	if err := proof.VerifyConsistency(rfc6962.DefaultHasher, cons.Size1, cons.Size2, cons.Hashes, head2.Root, head5.Root); err != nil {
		t.Fatalf("[%s] prove-consistency 2->5 does not verify against the head roots: %v", proto.name, err)
	}
	signed := vlogTenantJSON[vlogwire.SignHeadResult](t, node, proto, conn, "sign-head", "--log", logName)
	verifyBarrierCheckpoint(t, proto, created, *created.Key, signed.Checkpoint, 5, head5.Root)

	// Sealed: every verb is refused as sealed, and nothing is written.
	if out, err := runCLI(t, node.cli, append(conn, "tenant", "barrier-seal", "--id", tenantID)...); err != nil {
		t.Fatalf("[%s] barrier-seal: %v\noutput: %s", proto.name, err, out)
	}
	for _, args := range [][]string{
		{"create", "--origin", vlogCLIOrigin, "--log", logName + "-sealed"},
		{"append", "--log", logName, "--entry", "sealed"},
		{"get", "--log", logName, "--from", "0", "--limit", "1"},
		{"head", "--log", logName},
		{"prove-inclusion", "--log", logName, "--index", "3", "--size", "5"},
		{"prove-consistency", "--log", logName, "--size1", "2", "--size2", "5"},
		{"sign-head", "--log", logName},
		{"rotate-key", "--log", logName, "--signing-key-type", "ecdsa-p256-sha256"},
	} {
		out, err := runQRDB(t, node.cli, conn, append([]string{"vlog"}, args...)...)
		if err == nil {
			t.Fatalf("[%s] vlog %s served on a sealed barrier: %s", proto.name, args[0], out)
		}
		if !strings.Contains(strings.ToLower(out), "sealed") {
			t.Fatalf("[%s] vlog %s on a sealed barrier: want a sealed refusal, got: %s", proto.name, args[0], out)
		}
	}

	// Unsealed again: the log is where it was, unchanged.
	if out, err := runCLI(t, node.cli, append(conn, "tenant", "barrier-unseal", "--id", tenantID, "--key", "bm9uZQ==")...); err != nil {
		t.Fatalf("[%s] barrier-unseal: %v\noutput: %s", proto.name, err, out)
	}
	after := vlogTenantJSON[vlogwire.Head](t, node, proto, conn, "head", "--log", logName)
	if after.Size != 5 || string(after.Root) != string(head5.Root) {
		t.Fatalf("[%s] head after the unseal = size %d root %x; want size 5 root %x", proto.name, after.Size, after.Root, head5.Root)
	}

	// The signing key read from the tenant's home group rotates, and the
	// rotated key signs the next checkpoint.
	rotated := vlogTenantJSON[vlogwire.RotateKeyResult](t, node, proto, conn, "rotate-key", "--log", logName, "--signing-key-type", "ecdsa-p256-sha256")
	if rotated.Key.Version != 2 || len(rotated.Key.PublicKey) == 0 || string(rotated.Key.PublicKey) == string(created.Key.PublicKey) {
		t.Fatalf("[%s] vlog rotate-key = %+v, want a new key at version 2", proto.name, rotated.Key)
	}
	vlogTenantJSON[vlogwire.AppendResult](t, node, proto, conn, "append", "--log", logName, "--entry", "e5")
	head6 := vlogTenantJSON[vlogwire.Head](t, node, proto, conn, "head", "--log", logName)
	resigned := vlogTenantJSON[vlogwire.SignHeadResult](t, node, proto, conn, "sign-head", "--log", logName)
	verifyBarrierCheckpoint(t, proto, created, rotated.Key, resigned.Checkpoint, 6, head6.Root)
}

// verifyBarrierCheckpoint verifies note, a checkpoint of created's log, under
// key, and requires it to commit to size and root.
func verifyBarrierCheckpoint(t *testing.T, proto multiProtocol, created vlogwire.CreateResult, key vlogwire.HeadKey, note []byte, size uint64, root []byte) {
	t.Helper()
	pub, err := vlog.HeadKey{Version: key.Version, PublicKey: key.PublicKey}.Public()
	if err != nil {
		t.Fatalf("[%s] key version %d public key: %v", proto.name, key.Version, err)
	}
	keyName := vlog.LogID{TenantID: created.TenantID, Database: created.Database, Log: created.Log}.String()
	got, err := vlog.VerifyCheckpoint(note, vlogCLIOrigin, keyName, pub)
	if err != nil {
		t.Fatalf("[%s] checkpoint does not verify under key version %d: %v\n%s", proto.name, key.Version, err, note)
	}
	if got.Size != size || string(got.Root) != string(root) {
		t.Fatalf("[%s] checkpoint = size %d root %x; want size %d root %x", proto.name, got.Size, got.Root, size, root)
	}
}
