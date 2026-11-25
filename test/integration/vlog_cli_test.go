// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

// vlog_cli_test.go drives the ten `qrdb vlog` verbs through the real client
// CLI binary (cmd/qrdb, built with qrdb_vlog by qrdbdharness) against every
// transport (REST, gRPC, QUIC, MCP, Unix) of one self-spawned node. The
// proofs the CLI prints are checked with an RFC 9162 verifier independent of
// the server (transparency-dev/merkle) against the roots `vlog head` prints,
// so a server that returned well-formed but wrong proofs fails here.
package integration_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/transparency-dev/merkle/proof"
	"github.com/transparency-dev/merkle/rfc6962"

	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// vlogCLIOrigin is the checkpoint origin line every test log signs under.
const vlogCLIOrigin = "db.example.com/vlog-cli"

// TestVlog_CLI_PerProtocol runs create / list / append / get /
// prove-inclusion / prove-consistency / head / sign-head / rotate-key /
// expire through the CLI binary on every transport, sharing one
// multiProtoNode. Each protocol subtest owns uniquely-named logs.
func TestVlog_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testVlogOverProtocol(t, node, proto)
		})
	}
}

// vlogCLIJSON runs one vlog verb with JSON output and decodes the result.
func vlogCLIJSON[T any](t *testing.T, node *multiProtoNode, proto multiProtocol, args ...string) T {
	t.Helper()
	out, err := runCLIProto(t, node, proto, append([]string{"--output", "json", "vlog"}, args...)...)
	if err != nil {
		t.Fatalf("[%s] vlog %s: %v\noutput: %s", proto.name, strings.Join(args, " "), err, out)
	}
	var v T
	if jerr := json.Unmarshal([]byte(out), &v); jerr != nil {
		t.Fatalf("[%s] decode vlog %s JSON: %v\noutput: %s", proto.name, args[0], jerr, out)
	}
	return v
}

// vlogCLIRefused runs one vlog verb that must fail and requires its output
// or error to mention want.
func vlogCLIRefused(t *testing.T, node *multiProtoNode, proto multiProtocol, want string, args ...string) {
	t.Helper()
	out, err := runCLIProto(t, node, proto, append([]string{"vlog"}, args...)...)
	if err == nil {
		t.Fatalf("[%s] vlog %s succeeded, want a refusal mentioning %q (output: %s)", proto.name, strings.Join(args, " "), want, out)
	}
	if !strings.Contains(strings.ToLower(out+err.Error()), strings.ToLower(want)) {
		t.Fatalf("[%s] vlog %s error should mention %q, got: %s / %v", proto.name, strings.Join(args, " "), want, out, err)
	}
}

// testVlogOverProtocol exercises every vlog verb against a single protocol.
func testVlogOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	logName := "vlog-" + suffix
	plainLog := "vlog-plain-" + suffix

	// --- create ---------------------------------------------------------
	created := vlogCLIJSON[vlogwire.CreateResult](t, node, proto, "create", "--log", logName,
		"--origin", vlogCLIOrigin, "--signing-key-type", "ed25519", "--allow-expire-bodies")
	if created.Log != logName || created.Key == nil || created.Key.Version != 1 || len(created.Key.PublicKey) == 0 {
		t.Fatalf("[%s] vlog create = %+v, want log %s with key version 1", proto.name, created, logName)
	}
	vlogCLIRefused(t, node, proto, "/"+logName+" already exists", "create", "--log", logName, "--origin", vlogCLIOrigin)
	vlogCLIRefused(t, node, proto, "signing-key-type", "create", "--log", "vlog-badkey-"+suffix, "--origin", vlogCLIOrigin, "--signing-key-type", "rsa")
	vlogCLIJSON[vlogwire.CreateResult](t, node, proto, "create", "--log", plainLog, "--origin", vlogCLIOrigin)

	// --- list -----------------------------------------------------------
	listed := vlogCLIJSON[vlogwire.ListResult](t, node, proto, "list")
	if !containsAll(listed.Logs, logName, plainLog) {
		t.Fatalf("[%s] vlog list = %v, want it to contain %s and %s", proto.name, listed.Logs, logName, plainLog)
	}

	// --- append, and the typed not-found on a missing log --------------
	first := vlogCLIJSON[vlogwire.AppendResult](t, node, proto, "append", "--log", logName, "--entry", "e0", "--entry", "e1")
	if first.FirstIndex != 0 || first.Count != 2 {
		t.Fatalf("[%s] vlog append = %+v, want first index 0, count 2", proto.name, first)
	}
	head2 := vlogCLIJSON[vlogwire.Head](t, node, proto, "head", "--log", logName)
	second := vlogCLIJSON[vlogwire.AppendResult](t, node, proto, "append", "--log", logName, "--entry", "e2", "--entry", "e3", "--entry", "e4")
	if second.FirstIndex != 2 || second.Count != 3 {
		t.Fatalf("[%s] vlog append = %+v, want first index 2, count 3", proto.name, second)
	}
	vlogCLIRefused(t, node, proto, "not found", "append", "--log", "vlog-absent-"+suffix, "--entry", "x")

	// --- get --------------------------------------------------------------
	got := vlogCLIJSON[vlogwire.GetResult](t, node, proto, "get", "--log", logName, "--from", "1", "--limit", "3")
	if len(got.Entries) != 3 || got.Entries[0].Index != 1 || string(got.Entries[0].Body) != "e1" || string(got.Entries[2].Body) != "e3" {
		t.Fatalf("[%s] vlog get --from 1 --limit 3 = %+v, want e1..e3", proto.name, got.Entries)
	}

	// --- head -----------------------------------------------------------
	head5 := vlogCLIJSON[vlogwire.Head](t, node, proto, "head", "--log", logName)
	if head2.Size != 2 || head5.Size != 5 || len(head5.Root) != rfc6962.DefaultHasher.Size() || head5.CheckpointSize != 0 {
		t.Fatalf("[%s] vlog head sizes = %d, %d (checkpoint %d), want 2, 5 and no checkpoint", proto.name, head2.Size, head5.Size, head5.CheckpointSize)
	}
	vlogCLIRefused(t, node, proto, "not found", "head", "--log", "vlog-absent-"+suffix)

	// --- prove-inclusion, verified against the head root -----------------
	incl := vlogCLIJSON[vlogwire.InclusionProof](t, node, proto, "prove-inclusion", "--log", logName, "--index", "3", "--size", "5")
	leaf := rfc6962.DefaultHasher.HashLeaf([]byte("e3"))
	if err := proof.VerifyInclusion(rfc6962.DefaultHasher, incl.Index, incl.Size, leaf, incl.Hashes, head5.Root); err != nil {
		t.Fatalf("[%s] vlog prove-inclusion of e3 does not verify against the size-5 root: %v", proto.name, err)
	}
	vlogCLIRefused(t, node, proto, "", "prove-inclusion", "--log", logName, "--index", "9", "--size", "5")

	// --- prove-consistency, verified against both head roots -------------
	cons := vlogCLIJSON[vlogwire.ConsistencyProof](t, node, proto, "prove-consistency", "--log", logName, "--size1", "2", "--size2", "5")
	if err := proof.VerifyConsistency(rfc6962.DefaultHasher, cons.Size1, cons.Size2, cons.Hashes, head2.Root, head5.Root); err != nil {
		t.Fatalf("[%s] vlog prove-consistency 2->5 does not verify against the head roots: %v", proto.name, err)
	}
	vlogCLIRefused(t, node, proto, "", "prove-consistency", "--log", logName, "--size1", "2", "--size2", "9")

	// --- sign-head, and the refusal of a second sign at the same size ----
	// The CLI is a subprocess, so errors.As cannot reach its error: the
	// refusals are matched by the typed error's exact rendering instead.
	signed := vlogCLIJSON[vlogwire.SignHeadResult](t, node, proto, "sign-head", "--log", logName)
	if !strings.HasPrefix(string(signed.Checkpoint), vlogCLIOrigin+"\n5\n") {
		t.Fatalf("[%s] vlog sign-head checkpoint = %q, want origin then size 5", proto.name, signed.Checkpoint)
	}
	vlogCLIRefused(t, node, proto, (&vlog.CheckpointExistsError{Size: 5}).Error(), "sign-head", "--log", logName)
	if h := vlogCLIJSON[vlogwire.Head](t, node, proto, "head", "--log", logName); h.CheckpointSize != 5 || string(h.Checkpoint) != string(signed.Checkpoint) {
		t.Fatalf("[%s] vlog head after sign-head: checkpoint size %d, want 5 and the signed note", proto.name, h.CheckpointSize)
	}

	// --- rotate-key ---------------------------------------------------------
	rotated := vlogCLIJSON[vlogwire.RotateKeyResult](t, node, proto, "rotate-key", "--log", logName, "--signing-key-type", "ecdsa-p256-sha256")
	if rotated.Key.Version != 2 || string(rotated.Key.PublicKey) == string(created.Key.PublicKey) {
		t.Fatalf("[%s] vlog rotate-key = %+v, want a new key at version 2", proto.name, rotated.Key)
	}
	vlogCLIJSON[vlogwire.AppendResult](t, node, proto, "append", "--log", logName, "--entry", "e5")
	resigned := vlogCLIJSON[vlogwire.SignHeadResult](t, node, proto, "sign-head", "--log", logName)
	if !strings.HasPrefix(string(resigned.Checkpoint), vlogCLIOrigin+"\n6\n") {
		t.Fatalf("[%s] vlog sign-head after rotate-key = %q, want size 6", proto.name, resigned.Checkpoint)
	}

	// --- expire -----------------------------------------------------------
	expired := vlogCLIJSON[vlogwire.ExpireResult](t, node, proto, "expire", "--log", logName, "--cutoff", "2")
	if expired.Expired != 2 {
		t.Fatalf("[%s] vlog expire --cutoff 2 = %d, want 2", proto.name, expired.Expired)
	}
	after := vlogCLIJSON[vlogwire.GetResult](t, node, proto, "get", "--log", logName)
	if len(after.Entries) != 6 || !after.Entries[0].Expired || !after.Entries[1].Expired || after.Entries[2].Expired || string(after.Entries[2].Body) != "e2" {
		t.Fatalf("[%s] vlog get after expire = %+v, want 0-1 expired and e2 onward intact", proto.name, after.Entries)
	}
	// Expiry drops bodies, never leaves: the tree still proves e3.
	incl = vlogCLIJSON[vlogwire.InclusionProof](t, node, proto, "prove-inclusion", "--log", logName, "--index", "3", "--size", "5")
	if err := proof.VerifyInclusion(rfc6962.DefaultHasher, incl.Index, incl.Size, leaf, incl.Hashes, head5.Root); err != nil {
		t.Fatalf("[%s] vlog prove-inclusion after expire does not verify: %v", proto.name, err)
	}
	vlogCLIJSON[vlogwire.AppendResult](t, node, proto, "append", "--log", plainLog, "--entry", "p0")
	vlogCLIRefused(t, node, proto, "/"+plainLog+" does not allow expiring entry bodies", "expire", "--log", plainLog, "--cutoff", "1")
	vlogCLIRefused(t, node, proto, "not found", "expire", "--log", "vlog-absent-"+suffix, "--cutoff", "1")
}

// containsAll reports whether every one of want is in have.
func containsAll(have []string, want ...string) bool {
	set := make(map[string]struct{}, len(have))
	for _, h := range have {
		set[h] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; !ok {
			return false
		}
	}
	return true
}

// TestVlog_CLI_DatabaseDrop_PerProtocol proves on every transport that a
// named database's verifiable logs go with the database: a drop reclaims
// them, and a re-created database of the same name starts with none, both
// for a shared-store database and for one on its own dedicated raft group.
func TestVlog_CLI_DatabaseDrop_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	cases := []struct {
		name string
		args []string
	}{
		{name: "shared"},
		{name: "raft", args: []string{"--backend", "raft", "--cluster-mode", "local", "--replicas", "1"}},
	}
	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			for _, tc := range cases {
				testVlogDatabaseDropOverProtocol(t, node, proto, tc.name, tc.args)
			}
		})
	}
}

// testVlogDatabaseDropOverProtocol creates two logs in a fresh database,
// grows, signs and rotates them, drops and re-creates the database, and
// checks the re-created database holds neither log and serves the same
// log name afresh under a new version-1 key.
func testVlogDatabaseDropOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol, kind string, createArgs []string) {
	t.Helper()
	dbName := fmt.Sprintf("vlogdrop-%s-%s-%d", kind, proto.name, time.Now().UnixNano())
	createDB := func() {
		t.Helper()
		args := append([]string{"database", "create", dbName}, createArgs...)
		if out, err := runCLIProto(t, node, proto, args...); err != nil {
			t.Fatalf("[%s/%s] database create %s: %v\noutput: %s", proto.name, kind, dbName, err, out)
		}
	}
	t.Cleanup(func() {
		_, _ = runCLIProto(t, node, proto, "database", "drop", dbName)
	})
	createDB()

	first, second := "drop-a-"+kind, "drop-b-"+kind
	created := vlogCLIJSON[vlogwire.CreateResult](t, node, proto, "create", "--database", dbName, "--log", first,
		"--origin", vlogCLIOrigin, "--signing-key-type", "ed25519")
	if created.Key == nil || created.Key.Version != 1 {
		t.Fatalf("[%s/%s] vlog create = %+v, want a version-1 key", proto.name, kind, created)
	}
	vlogCLIJSON[vlogwire.CreateResult](t, node, proto, "create", "--database", dbName, "--log", second,
		"--origin", vlogCLIOrigin, "--signing-key-type", "ed25519")
	vlogCLIJSON[vlogwire.AppendResult](t, node, proto, "append", "--database", dbName, "--log", first,
		"--entry", "d0", "--entry", "d1", "--entry", "d2")
	signed := vlogCLIJSON[vlogwire.SignHeadResult](t, node, proto, "sign-head", "--database", dbName, "--log", first)
	if !strings.HasPrefix(string(signed.Checkpoint), vlogCLIOrigin+"\n3\n") {
		t.Fatalf("[%s/%s] vlog sign-head = %q, want size 3", proto.name, kind, signed.Checkpoint)
	}
	rotated := vlogCLIJSON[vlogwire.RotateKeyResult](t, node, proto, "rotate-key", "--database", dbName, "--log", first,
		"--signing-key-type", "ed25519")
	if rotated.Key.Version != 2 {
		t.Fatalf("[%s/%s] vlog rotate-key = %+v, want version 2", proto.name, kind, rotated.Key)
	}
	vlogCLIJSON[vlogwire.AppendResult](t, node, proto, "append", "--database", dbName, "--log", second, "--entry", "s0")

	// Drop and re-create the same name: the logs went with the old database.
	if out, err := runCLIProto(t, node, proto, "database", "drop", dbName); err != nil {
		t.Fatalf("[%s/%s] database drop %s: %v\noutput: %s", proto.name, kind, dbName, err, out)
	}
	createDB()
	listed := vlogCLIJSON[vlogwire.ListResult](t, node, proto, "list", "--database", dbName)
	for _, l := range listed.Logs {
		if l == first || l == second {
			t.Fatalf("[%s/%s] vlog list after drop and re-create = %v, want neither %s nor %s", proto.name, kind, listed.Logs, first, second)
		}
	}
	vlogCLIRefused(t, node, proto, "not found", "head", "--database", dbName, "--log", first)
	vlogCLIRefused(t, node, proto, "not found", "head", "--database", dbName, "--log", second)

	// The same log name starts afresh under a new version-1 key.
	fresh := vlogCLIJSON[vlogwire.CreateResult](t, node, proto, "create", "--database", dbName, "--log", first,
		"--origin", vlogCLIOrigin, "--signing-key-type", "ed25519")
	if fresh.Key == nil || fresh.Key.Version != 1 || string(fresh.Key.PublicKey) == string(created.Key.PublicKey) {
		t.Fatalf("[%s/%s] vlog create after re-create = %+v, want a new version-1 key", proto.name, kind, fresh)
	}
	appended := vlogCLIJSON[vlogwire.AppendResult](t, node, proto, "append", "--database", dbName, "--log", first, "--entry", "n0")
	if appended.FirstIndex != 0 || appended.Count != 1 {
		t.Fatalf("[%s/%s] first append after re-create = %+v, want first index 0, count 1", proto.name, kind, appended)
	}
	head := vlogCLIJSON[vlogwire.Head](t, node, proto, "head", "--database", dbName, "--log", first)
	if head.Size != 1 || head.CheckpointSize != 0 {
		t.Fatalf("[%s/%s] head after re-create = size %d checkpoint %d, want size 1 and no checkpoint", proto.name, kind, head.Size, head.CheckpointSize)
	}
	resigned := vlogCLIJSON[vlogwire.SignHeadResult](t, node, proto, "sign-head", "--database", dbName, "--log", first)
	verifyBarrierCheckpoint(t, proto, fresh, *fresh.Key, resigned.Checkpoint, 1, head.Root)
}
