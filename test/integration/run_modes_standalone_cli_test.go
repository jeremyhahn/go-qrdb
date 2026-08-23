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

// run_modes_standalone_cli_test.go is the per-mode integration suite for
// the ratified run-modes split (docs/run-modes.md): a qrdbd booted with
// --mode standalone serves standard database operations plus the security
// core over EVERY transport, refuses the raft-only families with a typed
// "requires mode: raft" error, and reports a truthful capability profile.
package integration_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// writeStandaloneModeConfig writes a daemon YAML enabling the passphrase
// seal barrier (the security core must run in standalone too) and a single
// default database on the given local storage backend, which is what the
// standalone composition branch selects its engine from.
func writeStandaloneModeConfig(t *testing.T, backend string) string {
	t.Helper()
	yaml := fmt.Sprintf("seal:\n  enabled: true\n  strategy: passphrase\ndatabases:\n  - name: \"default\"\n    engine: \"kv\"\n    backend: %q\n", backend)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "qrdbd.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writeStandaloneModeConfig: %v", err)
	}
	return cfgPath
}

// startStandaloneModeNode boots a raft-free (--mode standalone) qrdbd with
// every transport enabled, auth on, TLS configured, and the passphrase
// system barrier initialized and unsealed — the standalone counterpart of
// startMultiProtocolNode. No raft identity flags are passed: standalone
// requires none.
func startStandaloneModeNode(t *testing.T, backend string) *multiProtoNode {
	t.Helper()

	bin := buildBinary(t)
	cli := buildCLIBinary(t)
	ca := generateTestCA(t)
	dataDir := t.TempDir()
	cfgPath := writeStandaloneModeConfig(t, backend)

	sockDir, err := os.MkdirTemp("", "qrdb-sa-")
	if err != nil {
		t.Fatalf("startStandaloneModeNode: socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	unixSocket := filepath.Join(sockDir, "qrdbd.sock")

	quicPorts := allocatePorts(t, 1)
	tcpPorts := allocateTCPPorts(t, 3) // admin (REST), grpc, mcp

	restPort := tcpPorts[0]
	grpcPort := tcpPorts[1]
	mcpPort := tcpPorts[2]
	quicPort := quicPorts[0]

	const passphrase = "standalone-mode-harness-passphrase"

	args := []string{
		"start",
		"--config", cfgPath,
		"--mode", "standalone",
		"--data-dir", dataDir,
		"--admin-port", fmt.Sprintf("%d", restPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--auth-enabled",
		"--enable-rest",
		"--enable-grpc",
		"--enable-quic", "--quic-port", fmt.Sprintf("%d", quicPort),
		"--enable-mcp", "--mcp-port", fmt.Sprintf("%d", mcpPort),
		"--enable-unix", "--unix-socket-path", unixSocket,
	}
	args = append(args, ca.tlsFlags(t)...)

	startNode(t, bin, args, fmt.Sprintf("[standalone backend=%s] ", backend))

	// The pre-host sealed-window listener is mode-independent: the barrier
	// must be initialized and unsealed before the data plane (LocalBackend
	// here, not a raft host) comes up.
	waitForBarrierStatusReachable(t, ca, restPort, 90*time.Second)
	initAndUnsealBarrier(t, ca, restPort, passphrase)

	waitForAdmin(t, ca.Pool, restPort, 90*time.Second)

	clientCert, clientKey := ca.generateAdminClientCert(t, dataDir)

	n := &multiProtoNode{
		cli:        cli,
		ca:         ca,
		dataDir:    dataDir,
		clientCert: clientCert,
		clientKey:  clientKey,
		unixSocket: unixSocket,
		restPort:   restPort,
		grpcPort:   grpcPort,
		mcpPort:    mcpPort,
		quicPort:   quicPort,
		passphrase: passphrase,
	}

	for _, proto := range multiProtocols {
		waitForCLIProtoReady(t, n, proto, 60*time.Second)
	}
	return n
}

// TestStandaloneMode_StandardOpsAndRefusals_PerProtocol boots ONE
// standalone daemon (pebble backend — the production default) and, over
// each of the five transports, verifies:
//
//  1. standard operations serve (kv put/get/exists/delete, database
//     create/list),
//  2. the capability profile is truthful (mode standalone; mvcc, watch,
//     lease, lock, cluster, backup, rebalance all false; kv, batch, index
//     and TRANSACTION true — see the go-qrdb#62 amendment in
//     docs/run-modes.md),
//  3. raft-only families are refused with an error naming the remedy.
func TestStandaloneMode_StandardOpsAndRefusals_PerProtocol(t *testing.T) {
	node := startStandaloneModeNode(t, "pebble")

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			key := "sa-" + proto.name

			// --- 1. Standard operations serve. -------------------------
			if out, err := runCLIProto(t, node, proto, "kv", "put", key, "v1"); err != nil {
				t.Fatalf("kv put: %v\n%s", err, out)
			}
			out, err := runCLIProto(t, node, proto, "kv", "get", key)
			if err != nil {
				t.Fatalf("kv get: %v\n%s", err, out)
			}
			if !strings.Contains(out, "v1") {
				t.Fatalf("kv get output %q does not contain the stored value", out)
			}
			if out, err := runCLIProto(t, node, proto, "kv", "exists", key); err != nil {
				t.Fatalf("kv exists: %v\n%s", err, out)
			}
			if out, err := runCLIProto(t, node, proto, "kv", "delete", key); err != nil {
				t.Fatalf("kv delete: %v\n%s", err, out)
			}
			if out, err := runCLIProto(t, node, proto, "database", "list"); err != nil {
				t.Fatalf("database list: %v\n%s", err, out)
			}

			// --- 2. Capabilities are truthful. --------------------------
			out, err = runCLIProto(t, node, proto, "--output", "json", "admin", "capabilities")
			if err != nil {
				t.Fatalf("admin capabilities: %v\n%s", err, out)
			}
			var caps struct {
				Mode        string `json:"mode"`
				KV          bool   `json:"kv"`
				Batch       bool   `json:"batch"`
				Index       bool   `json:"index"`
				MVCC        bool   `json:"mvcc"`
				Watch       bool   `json:"watch"`
				Lease       bool   `json:"lease"`
				Lock        bool   `json:"lock"`
				Transaction bool   `json:"transaction"`
				Cluster     bool   `json:"cluster"`
				Backup      bool   `json:"backup"`
				Rebalance   bool   `json:"rebalance"`
			}
			jsonStart := strings.Index(out, "{")
			if jsonStart < 0 {
				t.Fatalf("admin capabilities output carries no JSON: %s", out)
			}
			if err := json.Unmarshal([]byte(out[jsonStart:]), &caps); err != nil {
				t.Fatalf("decode capabilities %q: %v", out, err)
			}
			if caps.Mode != "standalone" {
				t.Errorf("capabilities mode = %q, want standalone", caps.Mode)
			}
			if !caps.KV || !caps.Batch || !caps.Index {
				t.Error("core kv/batch/index must be true in standalone")
			}
			for name, v := range map[string]bool{
				"mvcc": caps.MVCC, "watch": caps.Watch, "lease": caps.Lease,
				"lock": caps.Lock, "cluster": caps.Cluster,
				"backup": caps.Backup, "rebalance": caps.Rebalance,
			} {
				if v {
					t.Errorf("capabilities %s = true, want false (raft-only)", name)
				}
			}
			// Transactions are served in every mode (owner ruling
			// 2026-08-23, go-qrdb#62): consensus is what makes a
			// compare-and-swap linearizable across REPLICAS, and a
			// standalone node has none. Asserted per protocol because the
			// capability travels a different encoder on each.
			if !caps.Transaction {
				t.Error("capabilities transaction = false; standalone serves " +
					"compare-and-swap, so the report must say so")
			}

			// --- 2b. Transactions actually EXECUTE. ---------------------
			// The capability assertion above says standalone serves CAS;
			// this proves it over the same transport, because a truthful
			// capability report and a working primitive are different
			// claims. The guarded create is run TWICE: the second must
			// report Succeeded=false rather than overwrite, since "did I
			// win the claim?" is the only thing a key store asks a CAS.
			txKey := "sa-tx-" + proto.name
			if resp := runTxPutIfAbsent(t, node, proto, txKey, "first"); !resp.Succeeded {
				t.Fatalf("guarded create of an absent key must succeed over %s", proto.name)
			}
			if resp := runTxPutIfAbsent(t, node, proto, txKey, "second"); resp.Succeeded {
				t.Errorf("guarded create of a PRESENT key succeeded over %s", proto.name)
			}
			out, err = runCLIProto(t, node, proto, "kv", "get", txKey)
			if err != nil {
				t.Fatalf("kv get after tx: %v\n%s", err, out)
			}
			if !strings.Contains(out, "first") {
				t.Errorf("the losing writer overwrote the winner over %s: %q", proto.name, out)
			}

			// --- 3. Raft-only families are refused. ---------------------
			// kv revision -> MVCC CurrentRevision; lease grant -> lease.
			// The refusal text differs slightly per transport error
			// mapping, but must never succeed and must name either the
			// remedy or the not-configured/not-implemented family.
			for _, neg := range [][]string{
				{"kv", "revision"},
				{"lease", "grant", "--ttl", "5"},
			} {
				out, err := runCLIProto(t, node, proto, neg...)
				if err == nil {
					t.Fatalf("%v must be refused in standalone, got success:\n%s", neg, out)
				}
				combined := strings.ToLower(out + err.Error())
				if !strings.Contains(combined, "requires mode: raft") &&
					!strings.Contains(combined, "not implemented") &&
					!strings.Contains(combined, "not configured") &&
					!strings.Contains(combined, "unimplemented") &&
					// MCP's gate is structural: the raft-only tools are
					// never registered, so the refusal is "tool not found".
					!strings.Contains(combined, "tool not found") {
					t.Errorf("%v refusal %q does not name the remedy or the unimplemented family", neg, combined)
				}
			}
		})
	}
}

// TestStandaloneMode_BootsPerBackend verifies the standalone daemon boots
// and serves a KV round-trip over REST on each remaining supported local
// backend (memory and file; pebble is covered by the per-protocol suite
// above).
func TestStandaloneMode_BootsPerBackend(t *testing.T) {
	for _, backend := range []string{"memory", "file"} {
		backend := backend
		t.Run(backend, func(t *testing.T) {
			node := startStandaloneModeNode(t, backend)
			rest := multiProtocols[0] // rest

			if out, err := runCLIProto(t, node, rest, "kv", "put", "sa-be", "v1"); err != nil {
				t.Fatalf("kv put: %v\n%s", err, out)
			}
			out, err := runCLIProto(t, node, rest, "kv", "get", "sa-be")
			if err != nil {
				t.Fatalf("kv get: %v\n%s", err, out)
			}
			if !strings.Contains(out, "v1") {
				t.Fatalf("kv get output %q does not contain the stored value", out)
			}
		})
	}
}

// runTxPutIfAbsent drives `qrdb tx execute` with a guarded-create
// transaction over the given protocol. The request is built from the SDK's
// own transport types rather than hand-written JSON so the enum values and
// the base64 key/value encoding come from the same source the daemon
// decodes with -- a hand-written fixture that disagreed would fail as a
// transaction bug.
func runTxPutIfAbsent(t *testing.T, node *multiProtoNode, proto multiProtocol, key, value string) txResult {
	t.Helper()

	req := transport.TransactionRequest{
		Compares: []transport.Compare{{
			Key:            []byte(key),
			Target:         transport.CompareCreateRevision,
			Result:         transport.CompareEqual,
			CreateRevision: 0,
		}},
		SuccessOps: []transport.Op{{
			Type:  transport.OpPut,
			Key:   []byte(key),
			Value: []byte(value),
		}},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal transaction: %v", err)
	}
	path := filepath.Join(t.TempDir(), "tx.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write transaction file: %v", err)
	}

	out, err := runCLIProto(t, node, proto, "--output", "json", "tx", "execute", "--file", path)
	if err != nil {
		t.Fatalf("tx execute over %s: %v\n%s", proto.name, err, out)
	}
	jsonStart := strings.Index(out, "{")
	if jsonStart < 0 {
		t.Fatalf("tx execute output carries no JSON: %s", out)
	}
	var res txResult
	if err := json.Unmarshal([]byte(out[jsonStart:]), &res); err != nil {
		t.Fatalf("decode tx result %q: %v", out, err)
	}
	return res
}
