// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package encfs_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/seal/encfs"
)

// helperEnvVar gates TestHelperProcess_Boot1Kill so it only runs the crash
// scenario when re-exec'd by TestPebble_SurvivesSIGKILLRestart; a normal
// `go test` run skips it immediately.
const helperEnvVar = "QRDB_ENCFS_SIGKILL_HELPER"

// helperRootEnvVar carries the shared temp root between the parent test and
// the re-exec'd helper process.
const helperRootEnvVar = "QRDB_ENCFS_SIGKILL_ROOT"

// TestHelperProcess_Boot1Kill is not a real test: it is re-exec'd as a
// subprocess by TestPebble_SurvivesSIGKILLRestart via `go test -run`. It
// boots a barrier + encfs-wrapped Pebble store exactly like boot 1 of
// TestPebble_SurvivesRestartWithPassphraseBarrier, writes and flushes one
// key, then immediately self-delivers SIGKILL with zero cleanup — no
// db.Close, no barrier.Seal, no sealStore.Close — to reproduce a `docker
// kill` on the embedded qrdb node with maximum fidelity (SIGKILL cannot be
// intercepted, so this is stronger than merely skipping defers).
func TestHelperProcess_Boot1Kill(t *testing.T) {
	if os.Getenv(helperEnvVar) != "1" {
		t.Skip("helper process; only runs when re-exec'd by TestPebble_SurvivesSIGKILLRestart")
	}
	root := os.Getenv(helperRootEnvVar)
	if root == "" {
		t.Fatal("helper process: " + helperRootEnvVar + " not set")
	}

	barrier1, _ := bootBarrier(t, filepath.Join(root, "seal"))

	fs1 := encfs.New(vfs.Default, barrier1)
	db, err := pebble.Open(filepath.Join(root, "pebble"), &pebble.Options{FS: fs1})
	if err != nil {
		t.Fatalf("helper: pebble.Open: %v", err)
	}
	if err := db.Set([]byte("alpha"), []byte("first boot value"), pebble.Sync); err != nil {
		t.Fatalf("helper: Set: %v", err)
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("helper: Flush: %v", err)
	}

	// Deliberately no db.Close(), no barrier1.Seal(), no sealStore.Close().
	// SIGKILL cannot be caught or deferred around; this is the closest a
	// unit test can get to `docker kill` without an actual OS-level crash.
	if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
		t.Fatalf("helper: self-SIGKILL failed: %v", err)
	}
}

// TestPebble_SurvivesSIGKILLRestart reproduces the production data-loss
// defect with process-boundary fidelity: a helper subprocess boots the
// barrier, writes and flushes one key through encfs, and is killed with an
// uncatchable SIGKILL before any graceful shutdown step runs. The parent
// then reopens the same on-disk state — exactly what xkmsd does when
// restarting an embedded qrdb node after a crash — and must be able to read
// the key back.
func TestPebble_SurvivesSIGKILLRestart(t *testing.T) {
	root := t.TempDir()

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess_Boot1Kill$", "-test.v")
	cmd.Env = append(os.Environ(),
		helperEnvVar+"=1",
		helperRootEnvVar+"="+root,
	)
	out, runErr := cmd.CombinedOutput()
	t.Logf("helper process output:\n%s", out)

	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		t.Fatalf("helper process did not exit as expected (err=%v)", runErr)
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("helper process did not die by SIGKILL: err=%v sys=%v", exitErr, exitErr.Sys())
	}

	// --- Boot 2: reopen the state SIGKILL left behind -------------------
	barrier2, sealStore2 := bootBarrier(t, filepath.Join(root, "seal"))
	defer func() { _ = sealStore2.Close() }()

	fs2 := encfs.New(vfs.Default, barrier2)
	db2, err := pebble.Open(filepath.Join(root, "pebble"), &pebble.Options{FS: fs2})
	if err != nil {
		t.Fatalf("boot 2: pebble.Open: %v", err)
	}
	defer func() { _ = db2.Close() }()

	got, closer, err := db2.Get([]byte("alpha"))
	if err != nil {
		t.Fatalf("boot 2: Get: %v", err)
	}
	defer func() { _ = closer.Close() }()
	if string(got) != "first boot value" {
		t.Errorf("boot 2: Get(alpha) = %q, want %q", got, "first boot value")
	}
}
