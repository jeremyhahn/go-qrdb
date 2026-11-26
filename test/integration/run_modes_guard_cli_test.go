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
	"strings"
	"testing"
	"time"
)

// runModeGuardNode holds the qrdbd start arguments of both modes over one
// data directory, so a test can restart the directory in either mode.
type runModeGuardNode struct {
	bin        string
	ca         *testCA
	restPort   int
	standalone []string
	raft       []string
}

func newRunModeGuardNode(t *testing.T) *runModeGuardNode {
	t.Helper()
	n := &runModeGuardNode{bin: buildBinary(t), ca: generateTestCA(t)}
	dataDir := t.TempDir()
	tcp := allocateTCPPorts(t, 2)
	quic := allocatePorts(t, 1)
	n.restPort = tcp[0]

	n.standalone = []string{
		"start",
		"--config", writeStandaloneModeConfig(t, "pebble", ""),
		"--mode", "standalone",
		"--data-dir", dataDir,
		"--admin-port", fmt.Sprintf("%d", tcp[0]),
		"--grpc-port", fmt.Sprintf("%d", tcp[1]),
		"--auth-enabled",
		"--enable-rest",
	}
	n.standalone = append(n.standalone, n.ca.tlsFlags(t)...)

	n.raft = []string{
		"start",
		"--config", writeNoneSealConfig(t),
		"--mode", "raft",
		"--listen", fmt.Sprintf("0.0.0.0:%d", quic[0]),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", quic[0]),
		"--data-dir", dataDir,
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", quic[0]),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", tcp[0]),
		"--grpc-port", fmt.Sprintf("%d", tcp[1]),
	}
	n.raft = append(n.raft, n.ca.tlsFlags(t)...)
	return n
}

// startReachable starts qrdbd with args and waits for its barrier status.
func (n *runModeGuardNode) startReachable(t *testing.T, args []string, prefix string) {
	t.Helper()
	proc := startNode(t, n.bin, args, prefix)
	waitForBarrierStatusReachable(t, n.ca, n.restPort, 90*time.Second)
	stopNode(t, proc, 15*time.Second)
}

// expectRefused starts qrdbd with args and requires it to exit naming both
// modes and the remedy.
func (n *runModeGuardNode) expectRefused(t *testing.T, args []string) {
	t.Helper()
	out := &watchOutputBuf{}
	cmd := spawnNode(t, n.bin, args, out)
	if err := awaitNodeExit(t, cmd, 60*time.Second); err == nil {
		t.Fatalf("qrdbd started a data directory of the other mode:\n%s", out)
	}
	for _, want := range []string{"mode: standalone", "mode: raft", "fresh data directory"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("refusal lacks %q:\n%s", want, out)
		}
	}
}

func TestRunModeGuard_StandaloneThenRaftRefused(t *testing.T) {
	n := newRunModeGuardNode(t)
	n.startReachable(t, n.standalone, "[guard standalone] ")
	n.expectRefused(t, n.raft)
	n.startReachable(t, n.standalone, "[guard standalone again] ")
}

func TestRunModeGuard_RaftThenStandaloneRefused(t *testing.T) {
	n := newRunModeGuardNode(t)
	n.startReachable(t, n.raft, "[guard raft] ")
	n.expectRefused(t, n.standalone)
	n.startReachable(t, n.raft, "[guard raft again] ")
}
