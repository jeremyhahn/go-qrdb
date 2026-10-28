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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBarrierCLI_ChangePassphrase_PerProtocol drives `qrdb barrier
// change-passphrase` end to end on every transport: a wrong current
// passphrase is refused with the typed credential mismatch and writes
// nothing, the right one re-wraps the root key, and after a seal the new
// passphrase unseals the node while the old one is refused. Data written
// under the old passphrase survives the change. Before init, an unseal is
// refused with the typed not-initialized error.
func TestBarrierCLI_ChangePassphrase_PerProtocol(t *testing.T) {
	t.Parallel()
	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			t.Parallel()
			testBarrierChangePassphraseOverProtocol(t, proto)
		})
	}
}

func testBarrierChangePassphraseOverProtocol(t *testing.T, proto multiProtocol) {
	t.Helper()
	binary := buildBinary(t)
	cli := buildCLIBinary(t)

	configPath := writeSealConfig(t, "passphrase", 0, 0)
	dataDir := t.TempDir()
	sockDir, err := os.MkdirTemp("", "qrdb-cp-")
	if err != nil {
		t.Fatalf("testBarrierChangePassphraseOverProtocol: socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	unixSocket := filepath.Join(sockDir, "qrdbd.sock")

	raftPorts := allocatePorts(t, 1)
	quicPorts := allocatePorts(t, 1)
	tcpPorts := allocateTCPPorts(t, 3)

	node := startSealNodeAllTransports(t, binary, dataDir, configPath,
		raftPorts[0], tcpPorts[0], tcpPorts[1], quicPorts[0], tcpPorts[2], unixSocket)
	waitForBarrierReachable(t, cli, node.noAuthFlagsFor(proto), 60*time.Second)

	const (
		oldPassphrase = "correct-horse-battery-staple-old"
		newPassphrase = "correct-horse-battery-staple-new"
	)
	// Unsealing a barrier nobody has initialized is refused with the typed
	// not-initialized error, not a corrupt-blob one.
	_, stderr, err := runCLISplit(t, cli, append(append([]string{}, node.noAuthFlagsFor(proto)...),
		"barrier", "unseal", "--passphrase", oldPassphrase)...)
	if err == nil {
		t.Fatalf("[%s] unseal before init must fail", proto.name)
	}
	if !strings.Contains(stderr, "not initialized") {
		t.Fatalf("[%s] unseal before init: want the not-initialized error, got stderr: %s", proto.name, stderr)
	}

	node.initSystem(t, cli, proto, "--barrier-strategy", "passphrase", "--passphrase", oldPassphrase)
	node.waitForFullBoot(t)
	flags := node.adminFlagsFor(proto)

	run := func(args ...string) (string, string, error) {
		return runCLISplit(t, cli, append(append([]string{}, flags...), args...)...)
	}

	if _, stderr, err := run("kv", "put", "change-passphrase-key", "change-passphrase-value"); err != nil {
		t.Fatalf("[%s] kv put: %v\nstderr: %s", proto.name, err, stderr)
	}

	// A wrong current passphrase is refused, typed, and changes nothing.
	_, stderr, err = run("barrier", "change-passphrase",
		"--current", "definitely-the-wrong-passphrase", "--next", newPassphrase, "--so-pin", sealNodeSOPIN)
	if err == nil {
		t.Fatalf("[%s] change-passphrase with a wrong current passphrase must fail", proto.name)
	}
	if !strings.Contains(stderr, "do not match") {
		t.Fatalf("[%s] wrong current passphrase: want the credential mismatch, got stderr: %s", proto.name, stderr)
	}

	if _, stderr, err := run("barrier", "change-passphrase",
		"--current", oldPassphrase, "--next", newPassphrase, "--so-pin", sealNodeSOPIN); err != nil {
		t.Fatalf("[%s] barrier change-passphrase: %v\nstderr: %s", proto.name, err, stderr)
	}
	if status := barrierStatus(t, cli, flags); status.Sealed {
		t.Fatalf("[%s] a passphrase change must leave the barrier unsealed", proto.name)
	}

	if _, stderr, err := run("barrier", "seal", "-y", "--so-pin", sealNodeSOPIN); err != nil {
		t.Fatalf("[%s] barrier seal: %v\nstderr: %s", proto.name, err, stderr)
	}

	if _, _, err := run("barrier", "unseal", "--passphrase", oldPassphrase); err == nil {
		t.Fatalf("[%s] the old passphrase must no longer unseal the barrier", proto.name)
	}
	if status := barrierStatus(t, cli, flags); !status.Sealed {
		t.Fatalf("[%s] the old passphrase must not have left the barrier unsealed", proto.name)
	}

	if _, stderr, err := run("barrier", "unseal", "--passphrase", newPassphrase); err != nil {
		t.Fatalf("[%s] barrier unseal with the new passphrase: %v\nstderr: %s", proto.name, err, stderr)
	}
	if status := barrierStatus(t, cli, flags); status.Sealed {
		t.Fatalf("[%s] the new passphrase must unseal the barrier", proto.name)
	}

	stdout, stderr, err := run("kv", "get", "change-passphrase-key")
	if err != nil {
		t.Fatalf("[%s] kv get after the change: %v\nstderr: %s", proto.name, err, stderr)
	}
	if !strings.Contains(stdout, "change-passphrase-value") {
		t.Fatalf("[%s] kv get after the change: want the surviving value, got: %s", proto.name, stdout)
	}
}
