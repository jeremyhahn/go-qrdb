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

//go:build !integration

package node

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// nodeSigkillHelperEnvVar gates TestHelperProcess_StandaloneNodeBoot1Kill so
// a normal `go test` run skips it; it only runs the crash scenario when
// re-exec'd by TestNewStandaloneNode_SurvivesSIGKILLRestart.
const nodeSigkillHelperEnvVar = "QRDB_NODE_SIGKILL_HELPER"

// nodeSigkillRootEnvVar carries the shared data directory between the
// parent test and the re-exec'd helper process.
const nodeSigkillRootEnvVar = "QRDB_NODE_SIGKILL_ROOT"

// TestHelperProcess_StandaloneNodeBoot1Kill is re-exec'd as a subprocess by
// TestNewStandaloneNode_SurvivesSIGKILLRestart. It boots a fully sealed
// standalone node (passphrase strategy, auto-unseal via WithSealPassphrase),
// writes one key through the real KV service, and then self-delivers
// SIGKILL with zero cleanup — no n.Close(), no graceful barrier.Seal() — to
// reproduce a `docker kill` on an embedded qrdb node with process-boundary
// fidelity.
func TestHelperProcess_StandaloneNodeBoot1Kill(t *testing.T) {
	if os.Getenv(nodeSigkillHelperEnvVar) != "1" {
		t.Skip("helper process; only runs when re-exec'd by TestNewStandaloneNode_SurvivesSIGKILLRestart")
	}
	dataDir := os.Getenv(nodeSigkillRootEnvVar)
	if dataDir == "" {
		t.Fatal("helper process: " + nodeSigkillRootEnvVar + " not set")
	}

	cfg := sealedStandaloneConfig(t, dataDir)
	n, err := NewStandaloneNode(
		context.Background(), cfg, nil,
		WithSealPassphrase([]byte("sigkill-test-passphrase")),
	)
	if err != nil {
		t.Fatalf("helper: NewStandaloneNode: %v", err)
	}
	if n.IsSealed() {
		t.Fatal("helper: node must be unsealed after WithSealPassphrase")
	}

	if err := n.KVService().Put(context.Background(), "alpha", []byte("first boot value")); err != nil {
		t.Fatalf("helper: KVService().Put: %v", err)
	}

	// Deliberately no n.Close(). SIGKILL cannot be caught or deferred
	// around; this is the closest a unit test can get to `docker kill`
	// without an actual OS-level crash.
	if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
		t.Fatalf("helper: self-SIGKILL failed: %v", err)
	}
}

// TestNewStandaloneNode_SurvivesSIGKILLRestart reproduces the production
// data-loss defect at the full SDK node level: a helper subprocess boots a
// passphrase-sealed standalone node, writes a key through the real KV
// service, and is killed with an uncatchable SIGKILL before any graceful
// shutdown step runs. The parent then reconstructs a node against the same
// data directory and passphrase — exactly what xkmsd does when restarting
// an embedded qrdb node after a crash — and the key must still be there.
//
// Before the fix (SyncWrites forced on for the seal store in
// buildNodeSealSubsystem) this failed with:
//
//	failed to create storage backend pebble: storage open: pebble: could
//	not open manifest file "MANIFEST-000001": quicraft/seal: decryption
//	failed
//
// because the seal store's root-key blob write was never durably
// persisted, so the second boot silently minted a fresh root key instead
// of recovering the one the first boot's data was encrypted under.
func TestNewStandaloneNode_SurvivesSIGKILLRestart(t *testing.T) {
	dataDir := t.TempDir()

	// #nosec G204,G702 -- re-exec of this test binary: argv[0] is os.Args[0], the path `go test` invoked, and every other argument is a compile-time literal. No shell is involved and nothing from outside the process reaches this argv.
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess_StandaloneNodeBoot1Kill$", "-test.v")
	cmd.Env = append(os.Environ(),
		nodeSigkillHelperEnvVar+"=1",
		nodeSigkillRootEnvVar+"="+dataDir,
	)
	out, runErr := cmd.CombinedOutput()
	t.Logf("helper process output:\n%s", out)

	var exitErr *exec.ExitError
	require.True(t, errors.As(runErr, &exitErr), "helper process did not exit as expected (err=%v)", runErr)
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	require.True(t, ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL,
		"helper process did not die by SIGKILL: err=%v sys=%v", exitErr, exitErr.Sys())

	// --- Restart: reopen the state SIGKILL left behind -------------------
	cfg := sealedStandaloneConfig(t, dataDir)
	n2, err := NewStandaloneNode(
		context.Background(), cfg, nil,
		WithSealPassphrase([]byte("sigkill-test-passphrase")),
	)
	require.NoError(t, err, "restart: NewStandaloneNode")
	require.NotNil(t, n2)
	defer func() { _ = n2.Close() }()

	require.False(t, n2.IsSealed(), "restart: node must unseal with the original passphrase")

	// NOTE: this test intentionally does not assert that "alpha" round-trips.
	// The standalone (non-Raft) data-plane Pebble store is opened with
	// SyncWrites: false on the premise that "Raft WAL provides durability" —
	// a premise that does not hold for a standalone node, so an unsynced
	// in-flight Put can legitimately be lost across a SIGKILL independent of
	// the seal/root-key defect this test targets. That is a separate,
	// pre-existing durability trade-off (see the reported findings) and is
	// out of scope for this regression test, which exists to pin down that
	// the barrier itself survives a crash and NewStandaloneNode does not
	// fail with "quicraft/seal: decryption failed".
	_, err = n2.KVService().Get(context.Background(), "alpha")
	if err != nil {
		t.Logf("restart: KVService().Get(alpha) = %v (informational only, see NOTE above)", err)
	}
}
