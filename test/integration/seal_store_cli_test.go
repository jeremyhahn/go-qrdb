//go:build integration

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

package integration_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// embeddedOnlySealPassphrase initialises every node these tests start.
const embeddedOnlySealPassphrase = "correct-horse-battery-staple-1647"

// startEmbeddedOnlySealNode starts a passphrase-barrier qrdbd serving every
// transport, initialises it over proto and waits for the full server. It
// returns the node and the CLI binary.
func startEmbeddedOnlySealNode(t *testing.T, proto multiProtocol) (*sealNodeAllTransports, string) {
	t.Helper()
	binary := buildBinary(t)
	cli := buildCLIBinary(t)

	configPath := writeSealConfig(t, "passphrase", 0, 0)
	dataDir := t.TempDir()
	sockDir, err := os.MkdirTemp("", "qrdb-ss-")
	if err != nil {
		t.Fatalf("startEmbeddedOnlySealNode: socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	unixSocket := filepath.Join(sockDir, "qrdbd.sock")

	raftPorts := allocatePorts(t, 1)
	quicPorts := allocatePorts(t, 1)
	tcpPorts := allocateTCPPorts(t, 3)

	node := startSealNodeAllTransports(t, binary, dataDir, configPath,
		raftPorts[0], tcpPorts[0], tcpPorts[1], quicPorts[0], tcpPorts[2], unixSocket)
	waitForBarrierReachable(t, cli, node.noAuthFlagsFor(proto), 60*time.Second)
	node.initSystem(t, cli, proto, "--barrier-strategy", "passphrase", "--passphrase", embeddedOnlySealPassphrase)
	node.waitForFullBoot(t)
	return node, cli
}

// runCLISplitStdin runs the CLI with stdin and returns stdout and stderr
// apart.
func runCLISplitStdin(t *testing.T, binary, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

// assertSealStoreCLIRefused runs every `qrdb seal-store` verb and requires
// each to exit non-zero with stderr naming the verb as embedded-only.
func assertSealStoreCLIRefused(t *testing.T, cli string, proto multiProtocol, flags []string, phase string) {
	t.Helper()
	verbs := []struct {
		op    string
		stdin string
		args  []string
	}{
		{transport.OpSealStorePut, "seal-store-cli-secret", []string{"seal-store", "put", "alpha"}},
		{transport.OpSealStoreGet, "", []string{"seal-store", "get", "alpha"}},
		{transport.OpSealStoreList, "", []string{"seal-store", "list"}},
		{transport.OpSealStoreDelete, "", []string{"seal-store", "delete", "alpha"}},
		{transport.OpSealStoreReseal, "", []string{"seal-store", "reseal", "alpha"}},
		{transport.OpSealStoreStatus, "", []string{"seal-store", "status"}},
	}
	for _, v := range verbs {
		stdout, stderr, err := runCLISplitStdin(t, cli, v.stdin, append(append([]string{}, flags...), v.args...)...)
		if err == nil {
			t.Fatalf("[%s %s] %v succeeded, want the embedded-only refusal\nstdout: %s", proto.name, phase, v.args, stdout)
		}
		if !strings.Contains(stderr, "embedded-only") || !strings.Contains(stderr, v.op) {
			t.Fatalf("[%s %s] %v stderr does not name %s as embedded-only\nstderr: %s", proto.name, phase, v.args, v.op, stderr)
		}
	}
}

// TestSealStore_CLI_EmbeddedOnly_PerProtocol: over every network transport
// each `qrdb seal-store` verb is refused as embedded-only (ruling 1234),
// while `qrdb barrier seal` and `qrdb barrier unseal` keep working on the
// same transport; the verbs stay refused after the seal/unseal cycle.
func TestSealStore_CLI_EmbeddedOnly_PerProtocol(t *testing.T) {
	t.Parallel()
	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			t.Parallel()
			node, cli := startEmbeddedOnlySealNode(t, proto)
			flags := node.adminFlagsFor(proto)

			assertSealStoreCLIRefused(t, cli, proto, flags, "unsealed")

			if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...), "barrier", "seal", "-y", "--so-pin", sealNodeSOPIN)...); err != nil {
				t.Fatalf("[%s] barrier seal: %v\nstderr: %s", proto.name, err, stderr)
			}
			if status := barrierStatus(t, cli, flags); !status.Sealed {
				t.Fatalf("[%s] barrier status after seal = %+v, want sealed", proto.name, status)
			}
			if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
				"barrier", "unseal", "--passphrase", embeddedOnlySealPassphrase)...); err != nil {
				t.Fatalf("[%s] barrier unseal: %v\nstderr: %s", proto.name, err, stderr)
			}
			if status := barrierStatus(t, cli, flags); status.Sealed {
				t.Fatalf("[%s] barrier status after unseal = %+v, want unsealed", proto.name, status)
			}

			assertSealStoreCLIRefused(t, cli, proto, flags, "after unseal")
		})
	}
}
