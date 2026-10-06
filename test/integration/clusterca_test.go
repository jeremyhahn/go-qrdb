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

package integration_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestClusterCA_SelfIssued_SurvivesKeyDeletionAndRestart is the end-to-end
// proof for go-qrdb hotfix/cakey-restart's first cause: a single-node
// daemon with NO --tls-* flags self-generates the cluster CA (Key A) and
// its own leaf, confirms Key A durably persisted into the replicated
// store, and -- per docs/certificate-authority/architecture.md, "Bootstrap
// crash window" -- deletes its own local ca-key.pem cache once that is
// confirmed (cmd/qrdbd's confirmClusterCAPersisted). A restart over the
// SAME data directory then offers the cached ca.pem with NO key, and must
// LOAD the persisted material rather than refuse: before
// cakey.SoftwareProvider.AdoptIfAbsent's fix (c3225812c), it PEM-decoded
// the empty key and refused with "adopted key is not valid EC PRIVATE KEY
// PEM", so no v0.40.0-alpha node could restart.
func TestClusterCA_SelfIssued_SurvivesKeyDeletionAndRestart(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	dataDir := t.TempDir()
	cfgPath := writeSealYAML(t, "seal:\n  enabled: true\n  strategy: none\n")
	raftPort := allocatePorts(t, 1)[0]
	adminPort := allocateTCPPorts(t, 1)[0]
	grpcPort := allocateTCPPorts(t, 1)[0]

	args := []string{
		"start",
		"--config", cfgPath,
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPort),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPort),
		"--data-dir", dataDir,
		"--sm", "pebble",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPort),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--auth-enabled=false",
		"--enable-rest",
		// Deliberately no --tls-ca-file/--tls-cert-file/--tls-key-file: the
		// daemon must self-generate Key A (and its own TLS leaf) under
		// dataDir, exercising confirmClusterCAPersisted's adopt-then-delete
		// path rather than the BYO-TLS one (TLSBundle.ExplicitTLS).
	}

	caPath := filepath.Join(dataDir, "ca.pem")
	keyPath := filepath.Join(dataDir, "ca-key.pem")

	cmd := startNode(t, bin, args, "[clusterca-r1] ")
	waitForFile(t, caPath, 30*time.Second)

	connArgs := []string{
		"--server", fmt.Sprintf("https://127.0.0.1:%d", adminPort),
		"--ca-file", caPath,
		"--timeout", "20s",
	}
	pollUntilCLISucceeds(t, cli, connArgs, 30*time.Second)
	// `qrdb init` is the only system bootstrap (go-qrdb#548). Its admin
	// credential goes unused here -- auth is disabled -- but issuing it is
	// what consumes Key A while ca-key.pem is still on disk.
	credDir := t.TempDir()
	bootAdminIdentity(t, cli, caPath, adminPort, "SOPIN12345", "USERPIN12345",
		filepath.Join(credDir, "admin-cert.pem"), filepath.Join(credDir, "admin-key.pem"), "",
		"--barrier-strategy", "none")
	waitForHealthy(t, dataDir, 60*time.Second)
	pollUntilCLISucceeds(t, cli, connArgs, 60*time.Second)

	if out, err := runQRDB(t, cli, connArgs, "kv", "put", "clusterca-survivor", "v1"); err != nil {
		t.Fatalf("kv put: %v\noutput: %s", err, out)
	}

	// confirmClusterCAPersisted deletes ca-key.pem only after it has
	// confirmed the key durably persisted into the replicated store --
	// poll rather than assert immediately, since that runs after Raft
	// leader election settles, not at the moment waitForHealthy returns.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(keyPath); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ca-key.pem was never removed after confirmed persistence")
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Kill hard, same as the sm-storage-file durability test: the fix must
	// not depend on a graceful shutdown path.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill first daemon: %v", err)
	}
	_ = cmd.Wait()

	// Restart over the SAME data directory and ports: resolveTLS's path 2
	// loads the cached ca.pem/node-cert.pem/node-key.pem, with NO
	// ca-key.pem (deleted above) -- bundle.CAKeyPEM is empty and
	// bundle.ExplicitTLS is false, so BuildServices' built-in
	// cakey.SoftwareProvider must LOAD the already-persisted Key A
	// (AdoptIfAbsent's loadMatching path) rather than refuse with
	// *cakey.NotInitializedError.
	startNode(t, bin, args, "[clusterca-r2] ")
	// A restart ALWAYS comes back up sealed, even under strategy "none"
	// (server.ServeSealedWindow blocks until the unseal API is driven,
	// whatever the strategy): reach the sealed-window listener, then
	// unseal with an empty passphrase, which StrategyNone ignores.
	pollUntilCLISucceeds(t, cli, connArgs, 30*time.Second)
	if out, err := runQRDB(t, cli, connArgs, "barrier", "unseal", "--passphrase", ""); err != nil {
		t.Fatalf("barrier unseal (restart): %v\noutput: %s", err, out)
	}
	waitForHealthy(t, dataDir, 60*time.Second)
	pollUntilCLISucceeds(t, cli, connArgs, 60*time.Second)

	deadline = time.Now().Add(30 * time.Second)
	var out string
	var err error
	for time.Now().Before(deadline) {
		out, err = runQRDB(t, cli, connArgs, "kv", "get", "clusterca-survivor")
		if err == nil && strings.Contains(out, "v1") {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("value did not survive the restart: err=%v out=%s", err, out)
}
