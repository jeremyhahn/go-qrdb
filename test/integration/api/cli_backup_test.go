// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeKeyFile writes a fresh 32-byte key file under t.TempDir() and
// returns its path, for use as --encryption-key-file /
// --decryption-key-file: the server under test here is a real subprocess
// on this same machine, so a temp file written by the test is exactly what
// its "software" keywrap provider reads.
func writeKeyFile(t *testing.T, fill byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.key")
	data := make([]byte, 32)
	for i := range data {
		data[i] = fill + byte(i)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writeKeyFile: %v", err)
	}
	return path
}

// ---------------------------------------------------------------------------
// Wire-format mirrors
// ---------------------------------------------------------------------------

// backupInfo mirrors the JSON shape of transport.BackupInfo for integration
// test parsing.
type backupInfo struct {
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

// backupShardError mirrors the JSON shape of transport.ShardError.
type backupShardError struct {
	ShardID uint64 `json:"shard_id"`
	Message string `json:"message"`
}

// backupRestoreResult mirrors the JSON shape of transport.RestoreResult.
type backupRestoreResult struct {
	BackupID       string             `json:"backup_id"`
	ShardsRestored int                `json:"shards_restored"`
	Duration       int64              `json:"duration"`
	Errors         []backupShardError `json:"errors,omitempty"`
}

// backupVerifyResult mirrors the JSON shape of transport.VerifyResult.
type backupVerifyResult struct {
	Valid            bool     `json:"valid"`
	ChecksumMatch    bool     `json:"checksum_match"`
	ExpectedChecksum string   `json:"expected_checksum"`
	ActualChecksum   string   `json:"actual_checksum"`
	Errors           []string `json:"errors,omitempty"`
}

// parseBackupInfo unmarshals a BackupInfo JSON blob from CLI stdout.
func parseBackupInfo(t *testing.T, proto ProtocolType, stdout string) *backupInfo {
	t.Helper()
	var info backupInfo
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &info); err != nil {
		t.Fatalf("[%s] failed to parse BackupInfo JSON: %v\noutput: %s", proto, err, stdout)
	}
	return &info
}

// parseBackupRestoreResult unmarshals a RestoreResult JSON blob from CLI stdout.
func parseBackupRestoreResult(t *testing.T, proto ProtocolType, stdout string) *backupRestoreResult {
	t.Helper()
	var result backupRestoreResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
		t.Fatalf("[%s] failed to parse RestoreResult JSON: %v\noutput: %s", proto, err, stdout)
	}
	return &result
}

// parseBackupVerifyResult unmarshals a VerifyResult JSON blob from CLI stdout.
func parseBackupVerifyResult(t *testing.T, proto ProtocolType, stdout string) *backupVerifyResult {
	t.Helper()
	var result backupVerifyResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
		t.Fatalf("[%s] failed to parse VerifyResult JSON: %v\noutput: %s", proto, err, stdout)
	}
	return &result
}

// ensureShardSnapshot triggers a Raft snapshot for shard 1 (the sole shard
// in config.test.yaml's single-node topology) via "admin snapshot". This is
// required before "backup create" can succeed: go-quicraft's
// Host.ExportSnapshot (which BackupService's ShardSnapshotProvider calls)
// returns ErrSnapshotNotFound unless the shard already has at least one
// on-disk Raft snapshot. AdminService.RequestSnapshot blocks on
// RequestState.Result() until the snapshot completes, so no polling is
// needed after this call returns.
func ensureShardSnapshot(t *testing.T, cfg *TestConfig, proto ProtocolType) {
	t.Helper()
	stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "admin", "snapshot", "--shard-id", "1")
	if err != nil {
		// "logdb: snapshot out of date" means a snapshot already exists at
		// the shard's current applied index (no new entries since the last
		// one) — the on-disk snapshot backup create needs is present, which
		// is exactly what this helper ensures. Any other failure is fatal.
		if strings.Contains(stderr, "snapshot out of date") {
			t.Logf("[%s] shard snapshot already current (out of date): reuse existing", proto)
			return
		}
		t.Logf("stdout: %s", stdout)
		t.Logf("stderr: %s", stderr)
		t.Fatalf("[%s] admin snapshot --shard-id 1 failed: %v", proto, err)
	}
}

// ---------------------------------------------------------------------------
// Full lifecycle
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolBackupCreateGetListVerifyRestoreDeleteLifecycle
// exercises the full backup lifecycle across all protocols: put KV data,
// snapshot the shard, create a backup, get/list/verify it, mutate the
// source data, restore, and delete.
//
// IMPORTANT semantics, re-verified 2026-08-14 on the suite's first run under
// the rebuilt harness. Restore cannot apply on this deployment, and the
// service now refuses it deliberately rather than incidentally: shard 1 hosts
// the system tenant, and stopping it while serving would take down auth for
// every subsequent admin call including the one that would restart it, so the
// service directs the caller to the offline `qrdbd start --restore-backup-id`
// boot flow. RestoreBackup still records a per-shard error and returns a
// RestoreResult with ShardsRestored == 0 and a non-empty Errors slice, but
// the CLI now exits NON-ZERO when any shard failed.
//
// Both of those are changes from what this test was written against — it
// expected an incidental ErrInvalidOperation from go-quicraft's ImportSnapshot
// and an exit status of 0 — and neither was caught until the suite ran again,
// because it had no CI job. The assertions below pin the current contract,
// including the reason for the refusal, so a regression in the guard is a test
// failure rather than a silent loss of the property.
//
// A successful restore needs a shard that does not host the system tenant, so
// it belongs to the cluster-stage harness, not here.
func TestCLIMultiProtocolBackupCreateGetListVerifyRestoreDeleteLifecycle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			key := fmt.Sprintf("backup-lifecycle-%s-%d", proto, ts)
			original := "original-value"
			mutated := "mutated-value"

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
			})

			// Seed data, then force a Raft snapshot so ExportSnapshot has
			// an on-disk artifact to read.
			if _, _, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, original); err != nil {
				t.Fatalf("[%s] kv put failed: %v", proto, err)
			}
			ensureShardSnapshot(t, cfg, proto)

			destination := fmt.Sprintf("/tmp/qrdb-backups/%s-%d", proto, ts)

			// Step 1: create.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"--output", "json", "backup", "create",
				"--destination", destination,
				"--compression", "none",
				"--encryption", "none",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] backup create failed: %v", proto, err)
			}
			created := parseBackupInfo(t, proto, stdout)
			assertNotEmpty(t, created.ID, fmt.Sprintf("[%s] backup create should return an ID", proto))
			assertEqual(t, "complete", created.Status, fmt.Sprintf("[%s] backup status should be complete", proto))
			assertEqual(t, destination, created.Destination, fmt.Sprintf("[%s] backup destination mismatch", proto))
			assertTrue(t, created.ShardCount >= 1, fmt.Sprintf("[%s] backup should include at least one shard", proto))
			id := created.ID

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "backup", "delete", id) //nolint:errcheck
			})

			// Step 2: get.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "--output", "json", "backup", "get", id)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] backup get failed: %v", proto, err)
			}
			got := parseBackupInfo(t, proto, stdout)
			assertEqual(t, id, got.ID, fmt.Sprintf("[%s] backup get ID mismatch", proto))
			assertEqual(t, created.Checksum, got.Checksum, fmt.Sprintf("[%s] backup get checksum mismatch", proto))

			// Step 3: list contains the backup.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "backup", "list")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] backup list failed: %v", proto, err)
			}
			assertContains(t, stdout, "ID", fmt.Sprintf("[%s] backup list should show the ID header", proto))
			assertContains(t, stdout, id, fmt.Sprintf("[%s] backup list should contain the backup ID", proto))

			// Step 4: verify -- freshly created, checksum must match.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "--output", "json", "backup", "verify", id)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] backup verify failed: %v", proto, err)
			}
			vr := parseBackupVerifyResult(t, proto, stdout)
			assertTrue(t, vr.Valid, fmt.Sprintf("[%s] freshly created backup should verify as valid", proto))
			assertTrue(t, vr.ChecksumMatch, fmt.Sprintf("[%s] freshly created backup checksum should match", proto))

			// Mutate the source data after the backup was captured.
			if _, _, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, mutated); err != nil {
				t.Fatalf("[%s] kv put (mutate) failed: %v", proto, err)
			}

			// Step 5: restore with --validate. See the function doc comment
			// for why the server refuses this on a single live-shard
			// deployment and why the CLI exits non-zero when it does.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"--output", "json", "backup", "restore",
				"--id", id, "--validate",
			)
			assertError(t, err, fmt.Sprintf(
				"[%s] restoring into the live system-tenant shard must fail the command, not just the shard", proto))
			assertContains(t, stderr, "failed shard",
				fmt.Sprintf("[%s] restore stderr should name the failed shard", proto))
			rr := parseBackupRestoreResult(t, proto, stdout)
			assertEqual(t, id, rr.BackupID, fmt.Sprintf("[%s] restore result backup ID mismatch", proto))
			assertEqual(t, 0, rr.ShardsRestored,
				fmt.Sprintf("[%s] restore of a live single-node shard must report 0 shards restored", proto))
			assertTrue(t, len(rr.Errors) > 0,
				fmt.Sprintf("[%s] restore of a live single-node shard must report a per-shard error", proto))

			// Because the import could not apply, the mutated value must
			// still be in place -- restore did not (and structurally
			// cannot) revert live data on this deployment.
			stdout, _, err = execCLIWithProtocol(t, cfg, proto, "kv", "get", key)
			if err != nil {
				t.Fatalf("[%s] kv get after restore failed: %v", proto, err)
			}
			assertEqual(t, mutated, strings.TrimSpace(stdout),
				fmt.Sprintf("[%s] kv value should remain the mutated value after a no-op restore", proto))

			// Step 6: delete.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "backup", "delete", id)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] backup delete failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK", fmt.Sprintf("[%s] backup delete should output OK", proto))

			// Step 7: get/verify after delete must error.
			_, _, err = execCLIWithProtocol(t, cfg, proto, "backup", "get", id)
			assertError(t, err, fmt.Sprintf("[%s] backup get after delete should error", proto))

			_, _, err = execCLIWithProtocol(t, cfg, proto, "backup", "verify", id)
			assertError(t, err, fmt.Sprintf("[%s] backup verify after delete should error", proto))
		})
	}
}

// ---------------------------------------------------------------------------
// compression / encryption permutations
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolBackupCompressionEncryptionPermutations creates
// backups across every valid --compression value (none, zstd, snappy) and
// exercises --encryption aes256 with --encryption-key-file, verifying the
// requested algorithms round-trip through BackupInfo.
func TestCLIMultiProtocolBackupCompressionEncryptionPermutations(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	tests := []struct {
		name        string
		compression string
		encryption  string
		useKeyFile  bool
	}{
		{"none-none", "none", "none", false},
		{"zstd-none", "zstd", "none", false},
		{"snappy-none", "snappy", "none", false},
		{"none-aes256", "none", "aes256", true},
	}

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ensureShardSnapshot(t, cfg, proto)

			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					destination := fmt.Sprintf("/tmp/qrdb-backups/%s-%s-%d", proto, tc.name, testTimestamp())

					args := []string{"--output", "json", "backup", "create",
						"--destination", destination,
						"--compression", tc.compression,
						"--encryption", tc.encryption,
					}
					if tc.useKeyFile {
						args = append(args, "--encryption-key-file", writeKeyFile(t, 0x10))
					}

					stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, args...)
					if err != nil {
						t.Logf("stdout: %s", stdout)
						t.Logf("stderr: %s", stderr)
						t.Fatalf("[%s/%s] backup create failed: %v", proto, tc.name, err)
					}
					info := parseBackupInfo(t, proto, stdout)
					assertEqual(t, tc.compression, info.Compression,
						fmt.Sprintf("[%s/%s] compression mismatch", proto, tc.name))
					assertEqual(t, tc.encryption, info.Encryption,
						fmt.Sprintf("[%s/%s] encryption mismatch", proto, tc.name))

					execCLIWithProtocol(t, cfg, proto, "backup", "delete", info.ID) //nolint:errcheck
				})
			}
		})
	}
}

// ---------------------------------------------------------------------------
// encrypted backup restore requires a decryption key
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolBackupRestoreEncryptedNeedsNoKeyByDefault verifies
// that restoring a backup created with --encryption aes256 needs NO key
// flag at all: the backup's own envelope records the key reference it was
// wrapped under, so restore resolves and unwraps it automatically (the
// subsequent per-shard import still fails on this single live-shard
// deployment, per the primary lifecycle test's documented behavior -- the
// distinction this test pins is that the failure is never about a missing
// or wrong key). It also verifies a WRONG --decryption-key-file override is
// refused with a decryption error, proving the override path is honored
// and fails closed rather than silently falling back to the stored
// reference.
func TestCLIMultiProtocolBackupRestoreEncryptedNeedsNoKeyByDefault(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ensureShardSnapshot(t, cfg, proto)

			keyPath := writeKeyFile(t, 0x20)
			destination := fmt.Sprintf("/tmp/qrdb-backups/enc-%s-%d", proto, testTimestamp())
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"--output", "json", "backup", "create",
				"--destination", destination,
				"--encryption", "aes256",
				"--encryption-key-file", keyPath,
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] backup create (encrypted) failed: %v", proto, err)
			}
			info := parseBackupInfo(t, proto, stdout)
			assertEqual(t, "aes256", info.Encryption, fmt.Sprintf("[%s] encryption mismatch", proto))

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "backup", "delete", info.ID) //nolint:errcheck
			})

			// No key flag at all: the envelope's own stored reference
			// resolves automatically. The restore then still fails on the
			// live system-tenant shard (same as every other restore in
			// this harness), so the distinction pinned here is the
			// reason: never "decryption key required" or "decryption
			// failed".
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"--output", "json", "backup", "restore", "--id", info.ID,
			)
			assertError(t, err, fmt.Sprintf(
				"[%s] restoring into the live system-tenant shard must fail the command", proto))
			assertNotContains(t, stderr, "decryption",
				fmt.Sprintf("[%s] with no key override, the failure must not be about decryption", proto))
			rr := parseBackupRestoreResult(t, proto, stdout)
			assertEqual(t, 0, rr.ShardsRestored,
				fmt.Sprintf("[%s] restore of a live single-node shard must report 0 shards restored", proto))

			// A WRONG --decryption-key-file override IS refused with a
			// decryption error -- the override is honored, not ignored.
			wrongKeyPath := writeKeyFile(t, 0x30)
			_, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"backup", "restore", "--id", info.ID, "--decryption-key-file", wrongKeyPath,
			)
			assertError(t, err, fmt.Sprintf("[%s] restoring with the wrong key file override should error", proto))
			assertContains(t, stderr, "decryption",
				fmt.Sprintf("[%s] the wrong key file override should fail with a decryption error", proto))
		})
	}
}

// ---------------------------------------------------------------------------
// negative cases
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolBackupNegativeCases exercises client-side and
// server-side validation failures across all protocols: invalid
// --compression / --encryption values, aes256 encryption without a key, and
// restore/verify of a nonexistent backup ID.
func TestCLIMultiProtocolBackupNegativeCases(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			// Invalid --compression value is rejected client-side
			// (cmd/qrdb/backup.go parseCompressionType).
			_, _, err := execCLIWithProtocol(t, cfg, proto, "backup", "create",
				"--destination", "/tmp/qrdb-backups/invalid-compression",
				"--compression", "bogus",
			)
			assertError(t, err, fmt.Sprintf("[%s] backup create with an invalid --compression should error", proto))

			// Invalid --encryption value is rejected client-side.
			_, _, err = execCLIWithProtocol(t, cfg, proto, "backup", "create",
				"--destination", "/tmp/qrdb-backups/invalid-encryption",
				"--encryption", "bogus",
			)
			assertError(t, err, fmt.Sprintf("[%s] backup create with an invalid --encryption should error", proto))

			// aes256 without --encryption-key-file is rejected server-side
			// (service.BackupEncryptionKeyRequiredError).
			_, _, err = execCLIWithProtocol(t, cfg, proto, "backup", "create",
				"--destination", "/tmp/qrdb-backups/missing-key",
				"--encryption", "aes256",
			)
			assertError(t, err, fmt.Sprintf("[%s] backup create with aes256 and no key should error", proto))

			// Restore of a nonexistent backup ID.
			missingID := fmt.Sprintf("nonexistent-backup-%s-%d", proto, testTimestamp())
			_, _, err = execCLIWithProtocol(t, cfg, proto, "backup", "restore", "--id", missingID)
			assertError(t, err, fmt.Sprintf("[%s] backup restore of a nonexistent ID should error", proto))

			// Verify of a nonexistent backup ID.
			_, _, err = execCLIWithProtocol(t, cfg, proto, "backup", "verify", missingID)
			assertError(t, err, fmt.Sprintf("[%s] backup verify of a nonexistent ID should error", proto))
		})
	}
}
