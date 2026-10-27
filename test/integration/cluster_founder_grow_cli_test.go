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
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCluster_OneNodeManagedTLSFounderGrows founds a cluster with ONE node
// under qrdbd-managed TLS (no --tls-*: the node self-issues the cluster CA)
// and grows it to two through the CLI, the documented way: restart the
// founder with the second peer listed so it runs a Raft transport, mint a join
// token, start the second node with --join, then `admin member add`.
//
// The founder runs its first start without a transport, but the membership it
// commits must still bind replica 1 to the identity of the certificate it
// presents. Without that binding, `admin member add` commits a membership
// naming only the new replica's identity, the new replica refuses every
// heartbeat and vote from the founder as missing_identity, and no leader is
// ever elected.
func TestCluster_OneNodeManagedTLSFounderGrows(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	var listeners [2]multiProtoListeners
	peerAdminURLs := make(map[string]string, len(listeners))
	for i := range listeners {
		listeners[i] = allocateMultiProtoListeners(t, fmt.Sprintf("qrdb-fgrow-%d-", i+1))
		peerAdminURLs[listeners[i].raftAddr()] = listeners[i].adminURL()
	}
	bootstrapPorts := allocateTCPPorts(t, 2*len(listeners))
	joinArgs := func(i int) []string {
		return []string{
			"--cluster-join-enabled",
			"--bootstrap-noise-port", fmt.Sprintf("%d", bootstrapPorts[2*i]),
			"--bootstrap-noise-host", "127.0.0.1",
			"--cluster-join-listen-port", fmt.Sprintf("%d", bootstrapPorts[2*i+1]),
			"--cluster-join-listen-host", "127.0.0.1",
		}
	}
	var dataDirs [2]string
	for i := range dataDirs {
		dataDirs[i] = t.TempDir()
	}
	nodeArgs := func(i int, peers string) []string {
		cfgPath := writeSealYAML(t, "seal:\n  enabled: true\n  strategy: none\n"+peerAdminURLsYAML(peerAdminURLs))
		return append(listeners[i].startArgs(cfgPath, dataDirs[i], peers, uint64(i+1)), joinArgs(i)...)
	}

	const soPin, userPin = "SOPIN-fgrow", "USERPIN-fgrow"
	credDir := t.TempDir()
	adminCertPath := filepath.Join(credDir, "admin-cert.pem")
	adminKeyPath := filepath.Join(credDir, "admin-key.pem")
	caPath := filepath.Join(dataDirs[0], "ca.pem")
	sealedArgs := func(i int) []string {
		return []string{"--server", listeners[i].adminURL(), "--transport", "rest", "--ca-file", caPath, "--timeout", "20s"}
	}

	// --- Found the cluster with one node -----------------------------------
	founderPeers := fmt.Sprintf("1=%s", listeners[0].raftAddr())
	founderProc := startNode(t, bin, nodeArgs(0, founderPeers), "[fgrow-node1] ")
	founderLeaf := filepath.Join(dataDirs[0], "node-cert.pem")
	waitForFile(t, caPath, 30*time.Second)
	waitForFile(t, founderLeaf, 30*time.Second)
	ca := loadDaemonCA(t, dataDirs[0])
	founderIdentity, founderPin := nodeIdentityAndPin(t, founderLeaf)
	pollUntilSealedListenerUp(t, cli, sealedArgs(0), 30*time.Second)
	bootAdminIdentityVia(t, cli, sealedArgs(0), soPin, userPin, adminCertPath, adminKeyPath, "",
		"--barrier-strategy", "none")
	waitForHealthy(t, dataDirs[0], 60*time.Second)

	nodeFor := func(i int) *multiProtoNode {
		return &multiProtoNode{
			cli:        cli,
			ca:         ca,
			dataDir:    dataDirs[i],
			clientCert: adminCertPath,
			clientKey:  adminKeyPath,
			unixSocket: listeners[i].unixSocket,
			restPort:   listeners[i].restPort,
			grpcPort:   listeners[i].grpcPort,
			mcpPort:    listeners[i].mcpPort,
			quicPort:   listeners[i].quicPort,
		}
	}
	founder := nodeFor(0)
	rest := multiProtocols[0]
	waitForCLIProtoReady(t, founder, rest, 60*time.Second)
	if out, err := runCLIProto(t, founder, rest, "kv", "put", "fgrow/before", "founded"); err != nil {
		t.Fatalf("kv put on the one-node founder: %v\noutput: %s", err, out)
	}

	// --- Restart the founder with the second peer listed --------------------
	// A one-peer founder runs no Raft transport; listing the peer it is about
	// to admit gives it one, over the certificate it already holds.
	grownPeers := fmt.Sprintf("1=%s,2=%s", listeners[0].raftAddr(), listeners[1].raftAddr())
	stopNode(t, founderProc, 15*time.Second)
	// Its none barrier unseals itself at boot (docs/barrier-seal/cli.md), so
	// the founder is awaited through the admin identity bound at init, not
	// unsealed: the full server answers a certificate it never bound with 401.
	startNode(t, bin, nodeArgs(0, grownPeers), "[fgrow-node1] ")
	waitForHealthy(t, dataDirs[0], 60*time.Second)
	waitForCLIProtoReady(t, founder, rest, 60*time.Second)
	out, err := runCLIProto(t, founder, rest, "--output", "json", "barrier", "status")
	if err != nil {
		t.Fatalf("barrier status on the restarted founder: %v\noutput: %s", err, out)
	}
	var founderBarrier struct {
		Sealed bool `json:"sealed"`
	}
	decodeJSON(t, out, &founderBarrier)
	if founderBarrier.Sealed {
		t.Fatal("expected the founder's none barrier to unseal itself at restart")
	}

	// --- Join the second node through the CLI -------------------------------
	joinerAddr := listeners[1].raftAddr()
	out, err = runCLIProto(t, founder, rest, "--output", "json",
		"admin", "join-token", "mint", joinerAddr, "--ttl", "1h")
	if err != nil {
		t.Fatalf("admin join-token mint %s: %v\noutput: %s", joinerAddr, err, out)
	}
	var minted mintJoinTokenCLIResponse
	decodeJSON(t, out, &minted)
	startNode(t, bin, append(nodeArgs(1, grownPeers),
		"--join",
		"--member-identities", fmt.Sprintf("1=%s", founderIdentity),
		"--bootstrap-join-token-file", writeTokenFile(t, minted.Token),
		"--bootstrap-spki-pin", founderPin,
		"--bootstrap-addr", fmt.Sprintf("127.0.0.1:%d", listeners[0].restPort),
		"--bootstrap-join-url", listeners[0].adminURL(),
	), "[fgrow-node2] ")
	joinerLeaf := filepath.Join(dataDirs[1], "node-cert.pem")
	waitForFile(t, joinerLeaf, 30*time.Second)
	joinerIdentity, _ := nodeIdentityAndPin(t, joinerLeaf)

	// Admission comes first: the joiner exits if it sees no leader within its
	// fixed internal budget.
	out, err = runCLIProto(t, founder, rest, "admin", "member", "add", joinerAddr,
		"--shard-id", "1", "--replica-id", "2", "--identity", joinerIdentity)
	if err != nil {
		t.Fatalf("admin member add %s: %v\noutput: %s", joinerAddr, err, out)
	}
	pollUntilSealedListenerUp(t, cli, sealedArgs(1), 30*time.Second)
	bootAdminIdentityVia(t, cli, sealedArgs(1), soPin, userPin, filepath.Join(t.TempDir(), "admin-cert.pem"), "", "",
		"--admin-cert", adminCertPath, "--barrier-strategy", "none")
	waitForHealthy(t, dataDirs[1], 60*time.Second)

	// --- Both members elect one leader and serve each other's writes -------
	// A write commits only through an elected leader with a quorum of both
	// members, so a put that succeeds is the leader assertion.
	joiner := nodeFor(1)
	waitForCLIProtoReady(t, joiner, rest, 60*time.Second)

	readsBack := func(node *multiProtoNode, key, want string) bool {
		return pollUntil(30*time.Second, 200*time.Millisecond, func() bool {
			out, err := runCLIProto(t, node, rest, "kv", "get", key)
			return err == nil && strings.Contains(out, want)
		})
	}
	if !readsBack(joiner, "fgrow/before", "founded") {
		t.Fatal("the founder's pre-growth write never read back on the joined node")
	}
	var putOut string
	var putErr error
	if !pollUntil(60*time.Second, 500*time.Millisecond, func() bool {
		putOut, putErr = runCLIProto(t, joiner, rest, "kv", "put", "fgrow/after", "grown")
		return putErr == nil
	}) {
		t.Fatalf("kv put on the joined node never committed (no leader with a quorum): %v\noutput: %s", putErr, putOut)
	}
	if !readsBack(founder, "fgrow/after", "grown") {
		t.Fatal("the joined node's write never read back on the founder")
	}
}
