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

// backup_restore_test.go proves the OFFLINE, boot-time disaster-recovery
// restore workflow added to cmd/qrdbd (restore.go): `qrdbd start
// --backup-dir <dir> --restore-backup-id <id>` recovers real KV data into a
// shard that has lost its on-disk Raft state, via the real CLI binary
// (cmd/qrdb) over REST.
//
// Why this is a SEPARATE daemon flag/boot sequence rather than a live
// `backup restore` API call: backup_cli_test.go (TestBackup_CLI_PerProtocol)
// proves that a LIVE `backup restore` is, correctly and safely, always
// refused by *quicraft.Host.ImportSnapshot's "shard must not be running"
// invariant -- and on this daemon's single-shard deployment that shard also
// hosts the system tenant's auth/user data, so stopping it while the server
// is serving requests takes down authentication for the very admin call that
// would restart it (see that file's header for the confirmed deadlock).
// restore.go's restoreShardFromBackup sidesteps this by running the
// bootstrap -> stop -> import -> start sequence BEFORE any API listener
// opens -- nothing has authenticated and nothing is mid-request -- which
// requires no change to either quicraft safety invariant.
//
// The proof: seed KV data, `backup create` it to a durable, file-backed
// repository (--backup-dir, OUTSIDE the node's --data-dir so it survives
// the simulated loss), SIGTERM the daemon and wipe its entire --data-dir
// (simulating total loss of the node's on-disk Raft state -- LogDB, WAL,
// and state-machine Pebble all gone), then relaunch with
// --restore-backup-id and confirm `kv get` returns the original values.
// A second relaunch with the SAME --restore-backup-id and NO wipe proves
// the idempotent-retry path (the artifact no longer advances the
// already-restored shard's recorded snapshot) boots cleanly and loses
// nothing, rather than wedging or corrupting the shard on a repeated flag.
package integration_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// restoreDRNode holds the connection details and reusable start arguments
// for a single-node, none-strategy daemon driven by the disaster-recovery
// restore test. restartWithOfflineRestore lets the test SIGTERM the process,
// optionally wipe its --data-dir (simulating total node loss), and relaunch
// it with --restore-backup-id appended to the same base arguments.
type restoreDRNode struct {
	ca            *testCA
	dataDir       string
	backupDir     string
	backupKeyDir  string
	adminPort     int
	grpcPort      int
	startArgs     []string
	adminCertPath string
	adminKeyPath  string
	connArgs      []string
	proc          *exec.Cmd
}

// startRestoreDRNode boots a fresh single-node daemon with an unencrypted
// ("none" strategy) system barrier and a durable, file-backed backup
// repository rooted OUTSIDE the node's --data-dir (so `restart with
// simulated loss` below can wipe the data dir without also destroying the
// backup artifact restore needs to read). It waits for full serving and
// returns a handle with the REST connection flags a `qrdb` subprocess needs
// to reach it as the bootstrapped system admin identity.
func startRestoreDRNode(t *testing.T, bin string, prefix string) *restoreDRNode {
	t.Helper()

	ca := generateTestCA(t)
	nodeCertPath, nodeKeyPath := ca.generateNodeCert(t)
	raftPort := allocatePorts(t, 1)[0]
	adminPort := allocateTCPPorts(t, 1)[0]
	grpcPort := allocateTCPPorts(t, 1)[0]
	dataDir := t.TempDir()
	backupDir := t.TempDir()
	// The built-in "software" key-wrap provider confines every key file to
	// one configured directory (go-qrdb security review,
	// backup-encrypt-default #B4); kept OUTSIDE --data-dir, like backupDir,
	// so a simulated-loss wipe of dataDir never destroys a key an
	// encrypted backup needs to restore.
	backupKeyDir := t.TempDir()
	// The admin client cert lives in its own directory, independent of the
	// node's --data-dir, so a simulated-loss wipe of dataDir never destroys
	// the credential the test CLI needs to reach the node afterward.
	certDir := t.TempDir()
	cfgPath := writeNoneSealConfig(t)

	args := []string{
		"start",
		"--config", cfgPath,
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPort),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPort),
		"--data-dir", dataDir,
		"--backup-dir", backupDir,
		"--backup-key-dir", backupKeyDir,
		"--sm", "mvcc",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPort),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--tls-ca-file", ca.CertPath,
		"--tls-cert-file", nodeCertPath,
		"--tls-key-file", nodeKeyPath,
	}

	n := &restoreDRNode{
		ca:           ca,
		dataDir:      dataDir,
		backupDir:    backupDir,
		backupKeyDir: backupKeyDir,
		adminPort:    adminPort,
		grpcPort:     grpcPort,
		startArgs:    args,
	}
	n.proc = startNode(t, bin, args, prefix)

	waitForBarrierStatusReachable(t, ca, adminPort, 90*time.Second)
	initAndUnsealBarrier(t, ca, adminPort, "")
	waitForHealthy(t, dataDir, 60*time.Second)
	waitForAdmin(t, ca.Pool, adminPort, 90*time.Second)
	waitForLeaderViaAdmin(t, ca, []int{adminPort}, 60*time.Second)

	n.adminCertPath, n.adminKeyPath = ca.boundAdminCert(t, certDir)
	n.connArgs = restConnArgs(ca, n.adminCertPath, n.adminKeyPath, adminPort)
	return n
}

// restartWithOfflineRestore SIGTERMs the node's current process, waits for
// it to exit, optionally wipes its ENTIRE --data-dir (simulating total loss
// of the node's on-disk Raft state -- the disaster-recovery scenario this
// test proves recovery from), and relaunches it with the same start
// arguments plus --restore-backup-id.
//
// A barrier -- even a "none"-strategy one -- always comes back up SEALED
// after a cold OS-process restart and must be driven through the sealed
// window again before the boot can reach restore.go's offline restore step
// (confirmed empirically: unlike the in-process migration recycle,
// strategy=none grants no free pass across a real process restart). A wiped
// data dir also destroys the barrier's persisted state, so that case needs a
// fresh initialize+unseal; a non-wiped restart's barrier is already
// initialized, so only unseal is valid (a second initialize is refused).
func (n *restoreDRNode) restartWithOfflineRestore(t *testing.T, bin, prefix, backupID string, wipeDataDir bool) {
	t.Helper()

	if n.proc != nil && n.proc.Process != nil {
		if err := n.proc.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatalf("restartWithOfflineRestore: signal previous process: %v", err)
		}
		_ = awaitNodeExit(t, n.proc, nodeStopTimeout)
	}

	if wipeDataDir {
		if err := os.RemoveAll(n.dataDir); err != nil {
			t.Fatalf("restartWithOfflineRestore: wipe data dir: %v", err)
		}
	}

	// --allow-plaintext-restore is harmless (and ignored) when the named
	// backup is encrypted; passed unconditionally here so this one helper
	// serves both the plaintext and encrypted disaster-recovery scenarios
	// below without needing to know which backup it is restoring.
	args := append(append([]string{}, n.startArgs...), "--restore-backup-id", backupID, "--allow-plaintext-restore")
	n.proc = startNode(t, bin, args, prefix)

	waitForBarrierStatusReachable(t, n.ca, n.adminPort, 90*time.Second)
	if wipeDataDir {
		initAndUnsealBarrier(t, n.ca, n.adminPort, "")
	} else {
		unsealOnlyBarrier(t, n.ca, n.adminPort)
	}
	waitForHealthy(t, n.dataDir, 90*time.Second)
	waitForAdmin(t, n.ca.Pool, n.adminPort, 90*time.Second)
	waitForLeaderViaAdmin(t, n.ca, []int{n.adminPort}, 60*time.Second)
}

// unsealOnlyBarrier unseals an ALREADY-INITIALIZED barrier (secret "",
// matching the "none" strategy) without attempting to re-initialize it --
// unlike initAndUnsealBarrier, which is only valid on a barrier's first
// boot. Used after a restart that did not wipe the data directory, where
// the barrier's persisted state (and therefore its initialized status)
// survived. It returns once the node reports the barrier unsealed.
func unsealOnlyBarrier(t *testing.T, ca *testCA, port int) {
	t.Helper()
	unsealOnlyBarrierWithSecret(t, ca, port, nil)
}

// unsealOnlyBarrierWithSecret is unsealOnlyBarrier for a barrier sealed with
// a passphrase: it unseals with secret.
func unsealOnlyBarrierWithSecret(t *testing.T, ca *testCA, port int, secret []byte) {
	t.Helper()

	// The budget and the unseal client's timeout are barrierBootTimeout, as
	// in initAndUnsealBarrier: the unseal spans the sealed window and the
	// full boot, and a client that gives up early on a slow reply cannot
	// tell an unseal that failed from one whose reply was late.
	ctx, cancel := context.WithTimeout(context.Background(), barrierBootTimeout)
	defer cancel()
	client := mtlsHTTPClient(t, ca, barrierBootTimeout)
	statusClient := mtlsHTTPClient(t, ca, 5*time.Second)
	if err := unsealInitializedBarrier(ctx, client, statusClient, port, secret); err != nil {
		t.Fatalf("unsealOnlyBarrier: %v", err)
	}
}

// unsealInitializedBarrier is unsealOnlyBarrier's loop, returning its outcome
// rather than failing a test so the helper's own behaviour is testable.
func unsealInitializedBarrier(ctx context.Context, client, statusClient *http.Client, port int, secret []byte) error {
	body, err := json.Marshal(map[string]interface{}{"secret": secret})
	if err != nil {
		return fmt.Errorf("marshal unseal body: %w", err)
	}
	url := fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/unseal", port)
	var lastErr error
	for ctx.Err() == nil {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if reqErr != nil {
			return fmt.Errorf("build unseal request: %w", reqErr)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, postErr := client.Do(req)
		if postErr == nil {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusConflict {
				return nil
			}
			lastErr = fmt.Errorf("status %d, body: %s", resp.StatusCode, respBody)
		} else {
			lastErr = postErr
		}
		// As in initAndUnsealBarrier, the condition is the barrier's state,
		// not the POST's reply: the sealed-window listener tears down as soon
		// as the barrier unseals, which can cost the reply, and the full
		// server that replaces it may refuse the retry while it settles.
		if barrierUnsealedOn(ctx, statusClient, port) {
			return nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("unseal on port %d never unsealed the barrier: %v", port, lastErr)
}

// TestBackupRestore_CLI_DisasterRecovery drives the full offline restore
// workflow through the real client CLI (REST transport): seed KV data,
// `backup create` + `backup verify` it into a durable repository, simulate
// total loss of the node (SIGTERM + wipe --data-dir), relaunch the daemon
// with --restore-backup-id, and prove the seeded data is recoverable via
// `kv get`. It then relaunches a SECOND time with the identical
// --restore-backup-id and no wipe, proving the idempotent-retry path (the
// artifact no longer advances an already-restored shard) boots cleanly and
// does not corrupt or lose the just-restored data.
func TestBackupRestore_CLI_DisasterRecovery(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := startRestoreDRNode(t, bin, "[dr-restore] ")

	// --- seed known KV data before backing up --------------------------
	seed := map[string]string{
		"dr/alpha":   "one",
		"dr/bravo":   "two",
		"dr/charlie": "three",
	}
	for k, v := range seed {
		out, err := runQRDB(t, cli, node.connArgs, "kv", "put", k, v)
		if err != nil {
			t.Fatalf("kv put %s: %v\noutput: %s", k, err, out)
		}
		if !strings.Contains(out, "OK") {
			t.Fatalf("kv put %s: expected OK, got: %s", k, out)
		}
	}
	for k, v := range seed {
		out, err := runQRDB(t, cli, node.connArgs, "kv", "get", k)
		if err != nil {
			t.Fatalf("pre-loss kv get %s: %v\noutput: %s", k, err, out)
		}
		if strings.TrimSpace(out) != v {
			t.Fatalf("pre-loss kv get %s = %q, want %q", k, strings.TrimSpace(out), v)
		}
	}

	// --- backup create + verify into the durable repository -------------
	// Explicit "none": this test's own subject is the offline restore
	// sequence, not encryption -- TestBackupRestore_CLI_DisasterRecovery_Encrypted
	// covers the encrypted path, including that it still recovers correctly.
	out, err := runQRDB(t, cli, node.connArgs, "--output", "json", "backup", "create",
		"--destination", "/backups/dr", "--encryption", "none")
	if err != nil {
		t.Fatalf("backup create: %v\noutput: %s", err, out)
	}
	created := decodeBackupInfoDisplay(t, out)
	if created.ID == "" {
		t.Fatalf("backup create: expected non-empty ID, got %+v", created)
	}
	if created.Status != "complete" || created.Size <= 0 {
		t.Fatalf("backup create: got %+v, want Status=complete Size>0", created)
	}
	id := created.ID

	out, err = runQRDB(t, cli, node.connArgs, "--output", "json", "backup", "verify", id)
	if err != nil {
		t.Fatalf("backup verify %s: %v\noutput: %s", id, err, out)
	}
	verify := decodeBackupVerifyDisplay(t, out)
	if !verify.Valid || !verify.ChecksumMatch {
		t.Fatalf("backup verify %s: Valid=%t ChecksumMatch=%t, want true/true", id, verify.Valid, verify.ChecksumMatch)
	}

	// --- simulate total loss of the node, then recover via the offline,
	// boot-time restore workflow -----------------------------------------
	node.restartWithOfflineRestore(t, bin, "[dr-restore-2] ", id, true /* wipe data dir */)

	for k, v := range seed {
		out, err := runQRDB(t, cli, node.connArgs, "kv", "get", k)
		if err != nil {
			t.Fatalf("post-restore kv get %s: %v\noutput: %s", k, err, out)
		}
		if strings.TrimSpace(out) != v {
			t.Fatalf("post-restore kv get %s = %q, want %q (DATA NOT RECOVERED)", k, strings.TrimSpace(out), v)
		}
	}

	// New writes after the restore must also work normally -- the shard is
	// genuinely back in service, not merely readable.
	out, err = runQRDB(t, cli, node.connArgs, "kv", "put", "dr/post-restore", "delta")
	if err != nil {
		t.Fatalf("post-restore kv put: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("post-restore kv put: expected OK, got: %s", out)
	}

	// --- idempotent repeat: relaunch with the SAME --restore-backup-id,
	// this time WITHOUT wiping (the shard's recorded snapshot already
	// carries -- or has advanced past -- what the artifact would apply).
	// Must boot cleanly (treated as a no-op) and must not lose data. -------
	node.restartWithOfflineRestore(t, bin, "[dr-restore-3] ", id, false /* no wipe */)

	for k, v := range seed {
		out, err := runQRDB(t, cli, node.connArgs, "kv", "get", k)
		if err != nil {
			t.Fatalf("post-idempotent-repeat kv get %s: %v\noutput: %s", k, err, out)
		}
		if strings.TrimSpace(out) != v {
			t.Fatalf("post-idempotent-repeat kv get %s = %q, want %q (DATA LOSS ON REPEATED RESTORE)", k, strings.TrimSpace(out), v)
		}
	}
	out, err = runQRDB(t, cli, node.connArgs, "kv", "get", "dr/post-restore")
	if err != nil {
		t.Fatalf("post-idempotent-repeat kv get dr/post-restore: %v\noutput: %s", err, out)
	}
	if strings.TrimSpace(out) != "delta" {
		t.Fatalf("post-idempotent-repeat kv get dr/post-restore = %q, want %q", strings.TrimSpace(out), "delta")
	}
}

// TestBackupRestore_CLI_DisasterRecovery_Encrypted is
// TestBackupRestore_CLI_DisasterRecovery's encrypted counterpart: the same
// total-loss-and-recover workflow, but with a real --encryption-key-file
// backup. It additionally proves the backup is genuinely unreadable
// without the KEK -- every file under the durable, file-backed repository
// is read directly off disk and checked for the seeded plaintext, which
// must appear nowhere -- and that the offline, boot-time restore path
// (cmd/qrdbd/restore.go's restoreShardFromBackup, which reads shard bytes
// directly from the BackupSink rather than through BackupService's own
// RestoreBackup dispatch) decrypts correctly via DecryptBackupShard rather
// than importing ciphertext.
func TestBackupRestore_CLI_DisasterRecovery_Encrypted(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := startRestoreDRNode(t, bin, "[dr-restore-enc] ")

	// The key file lives in node.backupKeyDir -- OUTSIDE --data-dir, so it
	// survives the simulated total-loss wipe below, exactly like
	// node.backupDir does and for the same reason. --encryption-key-file
	// is a bare NAME inside that directory, never a path (go-qrdb security
	// review, backup-encrypt-default #B4).
	const keyName = "backup.key"
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	if err := os.WriteFile(filepath.Join(node.backupKeyDir, keyName), key, 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	// A long, distinctive value unlikely to appear in ciphertext, nonces,
	// checksums, or any other incidental byte sequence the backup format
	// carries -- what the "unreadable without the KEK" check greps for.
	const distinctivePlaintext = "qrdb-dr-encrypted-fixture-07f3c9a1-do-not-leak-me"

	seed := map[string]string{
		"dr-enc/alpha": distinctivePlaintext,
		"dr-enc/bravo": "two",
	}
	for k, v := range seed {
		out, err := runQRDB(t, cli, node.connArgs, "kv", "put", k, v)
		if err != nil {
			t.Fatalf("kv put %s: %v\noutput: %s", k, err, out)
		}
	}

	// --- backup create, encrypted ---------------------------------------
	out, err := runQRDB(t, cli, node.connArgs, "--output", "json", "backup", "create",
		"--destination", "/backups/dr-encrypted", "--encryption-key-file", keyName)
	if err != nil {
		t.Fatalf("backup create (encrypted): %v\noutput: %s", err, out)
	}
	created := decodeBackupInfoDisplay(t, out)
	if created.Status != "complete" || created.Size <= 0 {
		t.Fatalf("backup create (encrypted): got %+v, want Status=complete Size>0", created)
	}
	if created.Encryption != "aes256" {
		t.Fatalf("backup create (encrypted): Encryption=%q, want aes256", created.Encryption)
	}
	id := created.ID

	out, err = runQRDB(t, cli, node.connArgs, "--output", "json", "backup", "verify", id)
	if err != nil {
		t.Fatalf("backup verify %s: %v\noutput: %s", id, err, out)
	}
	verify := decodeBackupVerifyDisplay(t, out)
	if !verify.Valid || !verify.ChecksumMatch {
		t.Fatalf("backup verify %s: Valid=%t ChecksumMatch=%t, want true/true", id, verify.Valid, verify.ChecksumMatch)
	}

	// --- unreadable without the KEK: the durable repository's own files,
	// read directly off disk, must not contain the seeded plaintext -------
	assertBackupDirHasNoPlaintext(t, node.backupDir, distinctivePlaintext)

	// --- simulate total loss of the node, then recover via the offline,
	// boot-time restore workflow -- this exercises DecryptBackupShard
	// (cmd/qrdbd/restore.go), not merely BackupService.RestoreBackup -------
	node.restartWithOfflineRestore(t, bin, "[dr-restore-enc-2] ", id, true /* wipe data dir */)

	for k, v := range seed {
		out, err := runQRDB(t, cli, node.connArgs, "kv", "get", k)
		if err != nil {
			t.Fatalf("post-restore kv get %s: %v\noutput: %s", k, err, out)
		}
		if strings.TrimSpace(out) != v {
			t.Fatalf("post-restore kv get %s = %q, want %q (ENCRYPTED DATA NOT RECOVERED)", k, strings.TrimSpace(out), v)
		}
	}
}

// assertBackupDirHasNoPlaintext walks every file under dir (the durable,
// file-backed backup repository) and fails the test if any of them
// contain plaintext as a byte substring -- the integration-level proof
// that an encrypted backup's artifact is genuine ciphertext, not merely
// labeled "aes256".
func assertBackupDirHasNoPlaintext(t *testing.T, dir, plaintext string) {
	t.Helper()
	needle := []byte(plaintext)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if bytes.Contains(data, needle) {
			t.Fatalf("backup artifact %s contains the seeded plaintext %q in the clear -- the backup was not actually encrypted", path, plaintext)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("assertBackupDirHasNoPlaintext: walking %s: %v", dir, err)
	}
}

// jsonObjectSuffix returns out starting at its first '{', so a leading
// line runCLI's CombinedOutput merged in ahead of the JSON payload --
// e.g. an explicit `--encryption none`'s plaintext warning, written to
// stderr before the command's own JSON response hits stdout -- does not
// break decoding. Already-bare JSON (no such prefix) is returned
// unchanged.
func jsonObjectSuffix(out string) string {
	if i := strings.IndexByte(out, '{'); i >= 0 {
		return out[i:]
	}
	return out
}

// decodeBackupInfoDisplay decodes a `backup create`/`backup get` JSON
// response into backupInfoDisplay (declared in backup_cli_test.go).
func decodeBackupInfoDisplay(t *testing.T, out string) backupInfoDisplay {
	t.Helper()
	var info backupInfoDisplay
	if err := json.Unmarshal([]byte(jsonObjectSuffix(out)), &info); err != nil {
		t.Fatalf("decode backup info JSON: %v\noutput: %s", err, out)
	}
	return info
}

// decodeBackupVerifyDisplay decodes a `backup verify` JSON response into
// backupVerifyDisplay (declared in backup_cli_test.go).
func decodeBackupVerifyDisplay(t *testing.T, out string) backupVerifyDisplay {
	t.Helper()
	var v backupVerifyDisplay
	if err := json.Unmarshal([]byte(jsonObjectSuffix(out)), &v); err != nil {
		t.Fatalf("decode backup verify JSON: %v\noutput: %s", err, out)
	}
	return v
}

// startFakeBarrierNode serves h over mTLS-capable HTTPS on a reserved
// loopback port, presenting a node certificate from ca, so the barrier
// helpers can be driven against scripted node behaviour.
func startFakeBarrierNode(t *testing.T, ca *testCA, h http.Handler) int {
	t.Helper()

	certPath, keyPath := ca.generateNodeCert(t)
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("startFakeBarrierNode: load node cert: %v", err)
	}
	port := allocateTCPPorts(t, 1)[0]
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("startFakeBarrierNode: listen on %d: %v", port, err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.Listener.Close()
	srv.Listener = ln
	srv.EnableHTTP2 = true
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return port
}

// fakeBarrierStatus answers GET /v1/barrier/status with the given state.
func fakeBarrierStatus(w http.ResponseWriter, sealed bool) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"sealed": sealed})
}

// TestUnsealInitializedBarrier_ReplyLostToTeardown drives the restart the
// disaster-recovery test performs: the sealed-window listener unseals the
// barrier and tears itself down before the reply reaches the client, and the
// full server that replaces it refuses the retried unseal while it settles.
// The barrier IS unsealed, which is the only fact the helper is for.
func TestUnsealInitializedBarrier_ReplyLostToTeardown(t *testing.T) {
	ca := generateTestCA(t)
	var unsealed, posts atomic.Int32
	port := startFakeBarrierNode(t, ca, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/barrier/status":
			fakeBarrierStatus(w, unsealed.Load() == 0)
		case "/v1/barrier/unseal":
			if posts.Add(1) == 1 {
				unsealed.Store(1)
				panic(http.ErrAbortHandler)
			}
			http.Error(w, `{"error":"auth lookup unavailable"}`, http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mtlsHTTPClient(t, ca, 5*time.Second)
	if err := unsealInitializedBarrier(ctx, client, client, port, nil); err != nil {
		t.Fatalf("unsealInitializedBarrier on an unsealed barrier: %v", err)
	}
}

// TestUnsealInitializedBarrier_AlreadyUnsealed accepts the 409 a full server
// answers for a barrier that is already unsealed.
func TestUnsealInitializedBarrier_AlreadyUnsealed(t *testing.T) {
	ca := generateTestCA(t)
	port := startFakeBarrierNode(t, ca, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/barrier/status":
			fakeBarrierStatus(w, false)
		case "/v1/barrier/unseal":
			http.Error(w, `{"error":"already unsealed"}`, http.StatusConflict)
		default:
			http.NotFound(w, r)
		}
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mtlsHTTPClient(t, ca, 5*time.Second)
	if err := unsealInitializedBarrier(ctx, client, client, port, nil); err != nil {
		t.Fatalf("unsealInitializedBarrier on 409: %v", err)
	}
}

// TestUnsealInitializedBarrier_NeverUnseals fails, naming the last refusal,
// when the barrier stays sealed for the whole budget.
func TestUnsealInitializedBarrier_NeverUnseals(t *testing.T) {
	ca := generateTestCA(t)
	port := startFakeBarrierNode(t, ca, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/barrier/status":
			fakeBarrierStatus(w, true)
		case "/v1/barrier/unseal":
			http.Error(w, `{"error":"invalid secret"}`, http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client := mtlsHTTPClient(t, ca, 5*time.Second)
	err := unsealInitializedBarrier(ctx, client, client, port, nil)
	if err == nil {
		t.Fatal("unsealInitializedBarrier on a barrier that stays sealed: got nil error")
	}
	if !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("unsealInitializedBarrier error = %v, want it to name the last refusal (status 400)", err)
	}
}
