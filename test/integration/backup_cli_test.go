// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

// backup_cli_test.go drives the entire `qrdb backup` command tree (create,
// list, get, verify, restore, delete) through the real client CLI binary
// (cmd/qrdb), against every transport (REST, gRPC, QUIC, MCP, Unix) using
// the shared startMultiProtocolNode harness (multi_protocol_helpers_test.go).
//
// Backup was doubly broken and both parts are now fixed:
//
//   - Part 1: pkg/service/shard_snapshot_provider.go's snapshotExporter
//     interface's ImportSnapshot omitted the trailing ...quicraft.ImportOption
//     variadic that *quicraft.Host.ImportSnapshot carries, so the clustered
//     raft.HostAdapter (which promotes it) never satisfied the interface --
//     p.host.(snapshotExporter) was silently false for every host, and every
//     backup/restore returned BackupUnsupportedError no matter what.
//   - Part 2: with Part 1 fixed, `backup create` still failed with
//     "logdb: no saved snapshot" -- *quicraft.Host.ExportSnapshot only reads
//     the LATEST SAVED raft snapshot, and a freshly written shard has none
//     until raft's own log-size-triggered auto-snapshot threshold is crossed.
//     hostShardSnapshotProvider.SnapshotShard (shard_snapshot_provider.go) now
//     calls the host's SyncRequestSnapshot BEFORE ExportSnapshot to force one
//     into existence and block until it is durably saved. quicraft's snapshot
//     worker persists to LogDB (logdb.SaveSnapshot) strictly BEFORE resolving
//     the RequestState SyncRequestSnapshot blocks on (see
//     go-quicraft/pkg/internal/engine/snapshot_pool.go's handleRequest), so a
//     successful SyncRequestSnapshot return already guarantees ExportSnapshot
//     has something to read -- no race in the common case. A residual
//     ErrSnapshotInProgress (a concurrent snapshot, e.g. raft's own
//     auto-snapshot, already held the shard's slot) is tolerated and covered
//     by a bounded poll on ExportSnapshot's ErrSnapshotNotFound
//     (HostShardSnapshotProviderOption-configurable; production default 10s).
//
// `backup create` now produces a REAL, non-empty, checksummed snapshot
// artifact on every transport (asserted below), and `backup restore` reaches
// REAL *quicraft.Host.ImportSnapshot plumbing (not a stub) rather than
// unconditionally returning BackupUnsupportedError.
//
// What this file does NOT attempt, and why: *quicraft.Host.ImportSnapshot
// refuses to import into a shard that is currently RUNNING
// (ErrInvalidOperation, go-quicraft/pkg/host.go importPreflight) and,
// separately, refuses an artifact whose index does not strictly ADVANCE the
// shard's currently recorded snapshot (ImportSnapshotOutOfDateError) --
// both deliberate raft safety invariants that prevent a live,
// majority-elected replica's already-applied log from being silently
// clobbered by an out-of-band artifact. Since a shard's own `backup create`
// call IS what sets its current recorded-snapshot high-water mark, no
// backup this suite can produce from the harness's single always-running
// shard can ever satisfy the "advances the recorded snapshot" check against
// that SAME shard while it is live -- true disaster-recovery-style data
// reappearance requires stopping the target shard (or a shard that has never
// been snapshotted) first. Shard stop/start is not exposed via the CLI, and
// was confirmed empirically NOT viable to drive as raw-admin-API test
// scaffolding here either: this harness's single shard also hosts the
// system tenant's auth/user data, so stopping it takes down authentication
// for every subsequent admin call (including the one that would restart it)
// with no unauthenticated recovery path -- i.e. it wedges the node, not just
// "isn't CLI-exposed." So every restore attempt below is against the live
// shard it was captured from, and is correctly, safely refused; this is
// asserted as the real, documented, non-vacuous outcome, and the assertions
// explicitly guard against ever regressing to the OLD (Part 1) failure mode.
// The restore code path's SUCCESS case -- ImportSnapshot actually invoked
// and actually applying bytes when the target host permits it -- is proven
// at the unit level in pkg/service/shard_snapshot_provider_test.go
// (TestHostShardSnapshotProvider_RestoreShard_Success and friends), which
// this file's header used to lack any coverage for at all.
//
// Each protocol subtest drives its own fully independent sequence (unique
// destination and KV keys), so the five subtests never contend over the same
// backup ID or data.
package integration_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeIntegrationKeyFile writes a fresh 32-byte key file inside dir (the
// daemon's own --backup-key-dir, e.g. node.backupKeyDir) and returns its
// bare NAME, for use as --encryption-key-file / --decryption-key-file
// against the real CLI binary: the daemon process under test runs in this
// same test binary's filesystem, so a file written here is exactly what
// the server-side "software" keywrap provider reads -- by NAME, never a
// path (go-qrdb security review, backup-encrypt-default #B4).
func writeIntegrationKeyFile(t *testing.T, dir string, fill byte) string {
	t.Helper()
	name := "backup.key"
	data := make([]byte, 32)
	for i := range data {
		data[i] = fill + byte(i)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatalf("writeIntegrationKeyFile: %v", err)
	}
	return name
}

// backupInfoDisplay mirrors cmd/qrdb/backup.go's printBackupInfo JSON shape
// (transport.BackupInfo), decoded from `backup get/create --output json`.
type backupInfoDisplay struct {
	ID           string            `json:"id"`
	Timestamp    string            `json:"timestamp"`
	Size         int64             `json:"size"`
	ShardCount   int               `json:"shard_count"`
	Status       string            `json:"status"`
	Checksum     string            `json:"checksum"`
	Destination  string            `json:"destination"`
	Compression  string            `json:"compression"`
	Encryption   string            `json:"encryption"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	Duration     int64             `json:"duration,omitempty"`
	ErrorMessage string            `json:"error_message,omitempty"`
}

// backupListDisplay mirrors the JSON shape of `backup list --output json`:
// a bare array of transport.BackupInfo.
type backupListDisplay []backupInfoDisplay

// backupVerifyDisplay mirrors transport.VerifyResult's JSON shape, decoded
// from `backup verify --output json`.
type backupVerifyDisplay struct {
	Valid            bool     `json:"valid"`
	ChecksumMatch    bool     `json:"checksum_match"`
	ExpectedChecksum string   `json:"expected_checksum"`
	ActualChecksum   string   `json:"actual_checksum"`
	Errors           []string `json:"errors,omitempty"`
}

// backupRestoreDisplay mirrors transport.RestoreResult's JSON shape, decoded
// from `backup restore --output json`.
type backupRestoreDisplay struct {
	BackupID       string `json:"backup_id"`
	ShardsRestored int    `json:"shards_restored"`
	Errors         []struct {
		ShardID uint64 `json:"shard_id"`
		Message string `json:"message"`
	} `json:"errors,omitempty"`
}

// restoreRefusedSubstring is the refusal every restore attempt in this file
// must surface. The fixture's shard 1 hosts the SYSTEM tenant, so qrdb's own
// online-restore guard (pkg/service's system-shard preflight) refuses the
// request before it ever reaches quicraft: stopping the serving system shard
// would take down auth for every subsequent admin call, so the guard directs
// the operator to the offline `qrdbd start --restore-backup-id` boot flow
// instead. This guard message REPLACED the deeper quicraft-level
// "quicraft: invalid operation" refusal this file originally pinned, which
// is now reachable only for a non-system shard.
const restoreRefusedSubstring = "online restore of shard 1 refused"

// oldBrokenUnsupportedSubstring is the message every backup/restore call
// returned unconditionally before Part 1 fixed the snapshotExporter
// interface-satisfaction bug (service.BackupUnsupportedError). It must never
// appear again now that the real Raft plumbing is reached.
const oldBrokenUnsupportedSubstring = "not supported on this host"

// TestBackup_CLI_PerProtocol drives the `qrdb backup` command tree --
// create, list, get, verify, restore, delete, and their flag/argument
// validation paths -- through the CLI binary against every transport
// sharing one multiProtoNode.
func TestBackup_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testBackupCommandTreeOverProtocol(t, node, proto)
		})
	}
}

func testBackupCommandTreeOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	destination := "/backups/" + proto.name + "-lifecycle"
	keyPrefix := "backup-" + proto.name + "-"

	// --- seed known KV data before backing up -------------------------------
	seed := map[string]string{
		keyPrefix + "alpha": "one",
		keyPrefix + "beta":  "two",
	}
	for k, v := range seed {
		out, err := runCLIProto(t, node, proto, "kv", "put", k, v)
		if err != nil {
			t.Fatalf("[%s] kv put %s: %v\noutput: %s", proto.name, k, err, out)
		}
	}

	// --- backup create: now produces a REAL, non-empty, checksummed
	// snapshot artifact -- the Part 2 fix under test. Explicit "none": this
	// subtest's own subject is the snapshot-capture plumbing, not
	// encryption (covered separately below and in
	// sdk/go/conformance/backup_encryption_default_test.go) -------------------
	out, err := runCLIProto(t, node, proto, "--output", "json", "backup", "create",
		"--destination", destination, "--encryption", "none")
	if err != nil {
		t.Fatalf("[%s] backup create: %v\noutput: %s", proto.name, err, out)
	}
	var created backupInfoDisplay
	// jsonObjectSuffix (backup_restore_test.go): the explicit --encryption
	// none above makes the CLI print a plaintext warning to stderr before
	// the JSON response hits stdout, and runCLIProto's CombinedOutput
	// merges the two -- strip the warning prefix before decoding.
	if jerr := json.Unmarshal([]byte(jsonObjectSuffix(out)), &created); jerr != nil {
		t.Fatalf("[%s] decode `backup create` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if created.ID == "" {
		t.Fatalf("[%s] backup create: expected non-empty ID, got %+v", proto.name, created)
	}
	if created.Status != "complete" {
		t.Fatalf("[%s] backup create: Status=%q, want complete (real snapshot capture must succeed): %+v", proto.name, created.Status, created)
	}
	if created.Size <= 0 {
		t.Fatalf("[%s] backup create: Size=%d, want > 0 (a real snapshot artifact was captured)", proto.name, created.Size)
	}
	if created.ShardCount != 1 {
		t.Fatalf("[%s] backup create: ShardCount=%d, want 1", proto.name, created.ShardCount)
	}
	if created.Checksum == "" {
		t.Fatalf("[%s] backup create: expected non-empty checksum", proto.name)
	}
	if created.Destination != destination {
		t.Fatalf("[%s] backup create: Destination=%q, want %q", proto.name, created.Destination, destination)
	}
	if created.Compression != "none" || created.Encryption != "none" {
		t.Fatalf("[%s] backup create: expected default compression/encryption=none, got compression=%s encryption=%s",
			proto.name, created.Compression, created.Encryption)
	}
	id := created.ID

	// --- backup create with compression + encryption also produces a real
	// artifact (compression/encryption are recorded metadata; the underlying
	// snapshot capture path is identical to the plain case exercised above) --
	out, err = runCLIProto(t, node, proto, "--output", "json", "backup", "create",
		"--destination", destination+"-encrypted",
		"--compression", "zstd",
		"--encryption", "aes256",
		"--encryption-key-file", writeIntegrationKeyFile(t, node.backupKeyDir, 0x01),
	)
	if err != nil {
		t.Fatalf("[%s] backup create (compressed+encrypted): %v\noutput: %s", proto.name, err, out)
	}
	var createdEnc backupInfoDisplay
	if jerr := json.Unmarshal([]byte(out), &createdEnc); jerr != nil {
		t.Fatalf("[%s] decode `backup create` (compressed+encrypted) JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if createdEnc.Status != "complete" || createdEnc.Size <= 0 {
		t.Fatalf("[%s] backup create (compressed+encrypted): got %+v, want Status=complete Size>0", proto.name, createdEnc)
	}
	if createdEnc.Compression != "zstd" || createdEnc.Encryption != "aes256" {
		t.Fatalf("[%s] backup create (compressed+encrypted): got compression=%s encryption=%s, want zstd/aes256",
			proto.name, createdEnc.Compression, createdEnc.Encryption)
	}

	// --- backup create rejects encryption without a key (validated BEFORE
	// the snapshot-provider call) ---------------------------------------
	out, err = runCLIProto(t, node, proto, "backup", "create",
		"--destination", destination+"-rejected", "--encryption", "aes256")
	if err == nil {
		t.Fatalf("[%s] backup create --encryption aes256 (no key) unexpectedly succeeded, output: %s", proto.name, out)
	}
	if !strings.Contains(out, "encryption") {
		t.Fatalf("[%s] backup create --encryption aes256 (no key): expected an encryption-key error, got: %s", proto.name, out)
	}

	// --- backup create rejects an invalid compression value -----------------
	out, err = runCLIProto(t, node, proto, "backup", "create",
		"--destination", destination+"-rejected", "--compression", "bogus")
	if err == nil {
		t.Fatalf("[%s] backup create --compression bogus unexpectedly succeeded, output: %s", proto.name, out)
	}
	if !strings.Contains(out, "compression") {
		t.Fatalf("[%s] backup create --compression bogus: expected a compression error, got: %s", proto.name, out)
	}

	// --- backup create rejects an invalid encryption value ------------------
	out, err = runCLIProto(t, node, proto, "backup", "create",
		"--destination", destination+"-rejected", "--encryption", "bogus")
	if err == nil {
		t.Fatalf("[%s] backup create --encryption bogus unexpectedly succeeded, output: %s", proto.name, out)
	}

	// --- backup list: the real, completed entry is present with metadata
	// matching what `backup create` returned ---------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "backup", "list")
	if err != nil {
		t.Fatalf("[%s] backup list: %v\noutput: %s", proto.name, err, out)
	}
	var list backupListDisplay
	if jerr := json.Unmarshal([]byte(out), &list); jerr != nil {
		t.Fatalf("[%s] decode `backup list` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	found := false
	for _, b := range list {
		if b.ID != id {
			continue
		}
		found = true
		if b.Status != "complete" || b.Checksum != created.Checksum || b.Size != created.Size {
			t.Fatalf("[%s] backup list: entry %+v does not match created %+v", proto.name, b, created)
		}
	}
	if !found {
		t.Fatalf("[%s] backup list: entry %s not found in %+v", proto.name, id, list)
	}

	// --- backup get: the real backup record is retrievable ------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "backup", "get", id)
	if err != nil {
		t.Fatalf("[%s] backup get %s: %v\noutput: %s", proto.name, id, err, out)
	}
	var got backupInfoDisplay
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("[%s] decode `backup get` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if got.ID != id || got.Status != "complete" || got.Checksum != created.Checksum {
		t.Fatalf("[%s] backup get %s: got %+v, want ID=%s Status=complete Checksum=%s", proto.name, id, got, id, created.Checksum)
	}

	// --- backup get: unknown ID -------------------------------------------
	out, err = runCLIProto(t, node, proto, "backup", "get", "does-not-exist-"+proto.name)
	if err == nil {
		t.Fatalf("[%s] backup get (unknown ID) unexpectedly succeeded, output: %s", proto.name, out)
	}

	// --- backup verify: a real, complete backup passes integrity
	// verification -- its checksum was computed from the exact bytes
	// SnapshotShard captured, so recomputing it here must match -------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "backup", "verify", id)
	if err != nil {
		t.Fatalf("[%s] backup verify %s: %v\noutput: %s", proto.name, id, err, out)
	}
	var verify backupVerifyDisplay
	if jerr := json.Unmarshal([]byte(out), &verify); jerr != nil {
		t.Fatalf("[%s] decode `backup verify` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if !verify.Valid || !verify.ChecksumMatch {
		t.Fatalf("[%s] backup verify %s: Valid=%t ChecksumMatch=%t, want true/true for a real complete backup",
			proto.name, id, verify.Valid, verify.ChecksumMatch)
	}
	if verify.ExpectedChecksum != created.Checksum || verify.ActualChecksum != created.Checksum {
		t.Fatalf("[%s] backup verify %s: ExpectedChecksum=%q ActualChecksum=%q, want both to equal %q",
			proto.name, id, verify.ExpectedChecksum, verify.ActualChecksum, created.Checksum)
	}
	if len(verify.Errors) != 0 {
		t.Fatalf("[%s] backup verify %s: Errors=%+v, want none for a valid backup", proto.name, id, verify.Errors)
	}

	// --- backup verify: unknown ID ------------------------------------------
	out, err = runCLIProto(t, node, proto, "backup", "verify", "does-not-exist-"+proto.name)
	if err == nil {
		t.Fatalf("[%s] backup verify (unknown ID) unexpectedly succeeded, output: %s", proto.name, out)
	}

	// --- backup restore requires --id ---------------------------------------
	out, err = runCLIProto(t, node, proto, "backup", "restore")
	if err == nil {
		t.Fatalf("[%s] backup restore (missing --id) unexpectedly succeeded, output: %s", proto.name, out)
	}

	// --- backup restore: unknown ID (refused at the service level, before
	// any provider call) -----------------------------------------------------
	out, err = runCLIProto(t, node, proto, "backup", "restore", "--id", "does-not-exist-"+proto.name)
	if err == nil {
		t.Fatalf("[%s] backup restore (unknown ID) unexpectedly succeeded, output: %s", proto.name, out)
	}

	// --- backup restore: invalid --shards value ------------------------------
	out, err = runCLIProto(t, node, proto, "backup", "restore", "--id", id, "--shards", "not-a-number")
	if err == nil {
		t.Fatalf("[%s] backup restore --shards not-a-number unexpectedly succeeded, output: %s", proto.name, out)
	}

	// --- backup restore --validate: a real, complete backup passes
	// pre-restore verification and proceeds to the REAL per-shard restore
	// attempt, which quicraft correctly and safely refuses because shard 1
	// is currently running (see this file's header comment) -----------------
	assertRestoreRefusedLive(t, proto, runCLIRestoreJSON(t, node, proto, id, true))

	// --- backup restore (no --validate): same real per-shard refusal,
	// exercising the path that skips pre-restore verification ---------------
	assertRestoreRefusedLive(t, proto, runCLIRestoreJSON(t, node, proto, id, false))

	// The failed restore attempts above must not have touched the seeded
	// data -- ImportSnapshot's preflight refuses before writing anything.
	for k, v := range seed {
		out, err := runCLIProto(t, node, proto, "kv", "get", k)
		if err != nil || strings.TrimSpace(out) != v {
			t.Fatalf("[%s] kv get %s after refused restore = %q (err=%v), want %q unchanged", proto.name, k, out, err, v)
		}
	}

	// --- backup delete -------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "backup", "delete", id)
	if err != nil {
		t.Fatalf("[%s] backup delete %s: %v\noutput: %s", proto.name, id, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] backup delete %s: expected OK, got: %s", proto.name, id, out)
	}

	// Confirm the delete actually removed it.
	out, err = runCLIProto(t, node, proto, "backup", "get", id)
	if err == nil {
		t.Fatalf("[%s] backup get %s (post-delete) unexpectedly succeeded, output: %s", proto.name, id, out)
	}

	// --- backup delete: unknown ID ------------------------------------------
	out, err = runCLIProto(t, node, proto, "backup", "delete", "does-not-exist-"+proto.name)
	if err == nil {
		t.Fatalf("[%s] backup delete (unknown ID) unexpectedly succeeded, output: %s", proto.name, out)
	}
}

// runCLIRestoreJSON runs `backup restore --id id [--validate] --output json`
// and decodes the RestoreResult. A restore with per-shard failures (the
// scenario every caller of this helper drives) is a PARTIAL failure: the
// CLI still prints the full RestoreResult JSON to stdout, but now exits
// non-zero (*RestorePartialFailureError) so scripted/automation callers can
// detect it -- this is intentional (see cmd/qrdb/errors.go
// RestorePartialFailureError) and this helper asserts exactly that
// combination rather than treating the non-zero exit as a hard test
// failure. A decode failure of the JSON payload itself remains fatal.
func runCLIRestoreJSON(t *testing.T, node *multiProtoNode, proto multiProtocol, id string, validate bool) backupRestoreDisplay {
	t.Helper()
	// --allow-plaintext: every caller of this helper restores the
	// explicit-"none" backup created above.
	args := []string{"--output", "json", "backup", "restore", "--id", id, "--allow-plaintext"}
	if validate {
		args = append(args, "--validate")
	}
	out, err := runCLIProto(t, node, proto, args...)
	// runCLIProto captures CombinedOutput (stdout+stderr interleaved): on a
	// partial-failure restore the CLI still writes the full RestoreResult
	// JSON to stdout first, then (since it now exits non-zero) main.go's
	// "Error: ..." line to stderr follows in the buffer. Decode only the
	// leading JSON value and ignore anything after it -- json.Unmarshal
	// would reject the trailing text as invalid JSON.
	var restore backupRestoreDisplay
	if jerr := json.NewDecoder(strings.NewReader(out)).Decode(&restore); jerr != nil {
		t.Fatalf("[%s] decode `backup restore` JSON: %v\noutput: %s (exec err: %v)", proto.name, jerr, out, err)
	}
	// The JSON decoded successfully; a non-zero exit is expected here IFF
	// the decoded result actually carries per-shard errors (partial
	// failure). Any other non-zero exit (e.g. a genuine CLI/service fault
	// unrelated to the documented live-shard refusal) is a real failure.
	if err != nil && len(restore.Errors) == 0 {
		t.Fatalf("[%s] backup restore --id %s (validate=%t): unexpected non-zero exit with no per-shard errors: %v\noutput: %s", proto.name, id, validate, err, out)
	}
	if err == nil && len(restore.Errors) > 0 {
		t.Fatalf("[%s] backup restore --id %s (validate=%t): expected non-zero exit for a partial restore failure, got success\noutput: %s", proto.name, id, validate, out)
	}
	return restore
}

// assertRestoreRefusedLive asserts that a restore attempt against the live
// (still-serving) shard it was captured from was refused by REAL quicraft
// ImportSnapshot plumbing -- not the old, unconditional
// BackupUnsupportedError this feature used to return no matter what.
func assertRestoreRefusedLive(t *testing.T, proto multiProtocol, restore backupRestoreDisplay) {
	t.Helper()
	if restore.ShardsRestored != 0 {
		t.Fatalf("[%s] backup restore: ShardsRestored=%d, want 0 (shard 1 is live and must refuse the import)", proto.name, restore.ShardsRestored)
	}
	if len(restore.Errors) != 1 {
		t.Fatalf("[%s] backup restore: Errors=%+v, want exactly one per-shard error", proto.name, restore.Errors)
	}
	shardErr := restore.Errors[0]
	if shardErr.ShardID != 1 {
		t.Fatalf("[%s] backup restore: error ShardID=%d, want 1", proto.name, shardErr.ShardID)
	}
	if !strings.Contains(shardErr.Message, restoreRefusedSubstring) {
		t.Fatalf("[%s] backup restore: error message %q does not contain the system-shard guard refusal %q",
			proto.name, shardErr.Message, restoreRefusedSubstring)
	}
	if strings.Contains(shardErr.Message, oldBrokenUnsupportedSubstring) {
		t.Fatalf("[%s] backup restore: error message %q regressed to the OLD (Part 1) BackupUnsupportedError contract",
			proto.name, shardErr.Message)
	}
}
