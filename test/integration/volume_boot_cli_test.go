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

// volume_boot_cli_test.go drives `qrdbd start` (the real daemon binary,
// cmd/qrdbd) against a volume pool whose configured mount does not exist,
// proving the owner's 2026-10-06 ruling end to end: the DEFAULT is a loud,
// non-zero-exit startup failure naming the inaccessible pool and the
// override flag; --allow-degraded-volume-boot (or its config-file
// equivalent, storage.allow_degraded_volume_boot) is the explicit opt-in
// that lets the node boot anyway. See docs/volume-pools/{architecture,
// engineering,cli,user}.md.
package integration_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// writeVolumeBootConfig writes a minimal qrdbd config naming one volume
// pool ("broken") at poolPath, optionally turning on
// storage.allow_degraded_volume_boot.
func writeVolumeBootConfig(t *testing.T, poolPath string, allowDegraded bool) string {
	t.Helper()
	degraded := ""
	if allowDegraded {
		degraded = "  allow_degraded_volume_boot: true\n"
	}
	// seal.enabled: false avoids the sealed-window listener entirely: this
	// test is about the volume-pool boot gate, not the unseal ceremony, and
	// WaitForNodeHost polls for host construction, which (when seal IS
	// enabled) only happens after an operator unseals. auth.enabled: false
	// goes with it -- config validation refuses seal disabled while auth
	// stays on ("auth.enabled requires seal.enabled: true").
	yaml := fmt.Sprintf("seal:\n  enabled: false\nauth:\n  enabled: false\nstorage:\n  volume_pools:\n    - name: broken\n      path: %s\n%s", poolPath, degraded)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "qrdbd.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0600); err != nil {
		t.Fatalf("writeVolumeBootConfig: write: %v", err)
	}
	return cfgPath
}

// waitForOutput waits until the daemon's output contains want. The log line
// is written before the event the caller already waited on, but it reaches
// the buffer through the pipe copier, so it can arrive after that event is
// observable; reading the buffer once would race the copier.
func waitForOutput(out *nodeLog, want string, timeout time.Duration) bool {
	return pollUntil(timeout, 50*time.Millisecond, func() bool {
		return strings.Contains(out.String(), want)
	})
}

// singleNodeVolumeBootArgs returns the minimal `qrdbd start` argument list
// for a single-node, single-shard boot against dataDir and cfgPath, plus
// any extra args (e.g. "--allow-degraded-volume-boot"). qrdbdharness.Start
// reserves a port for every listener it would otherwise bind at a fixed
// default.
func singleNodeVolumeBootArgs(dataDir, cfgPath string, extra ...string) []string {
	args := []string{
		"start",
		"--config", cfgPath,
		"--data-dir", dataDir,
		"--sm", "memory",
		"--discovery", "static",
		"--listen", "127.0.0.1:0",
		"--raft-address", "127.0.0.1:0",
		"--shard-id", "1",
		"--replica-id", "1",
	}
	return append(args, extra...)
}

// TestQrdbdStart_UnopenableVolumePool_DefaultFailsLoud proves the default:
// a configured volume pool whose mount does not exist fails `qrdbd start`
// outright. The process exits non-zero, names the failed pool and the
// override flag on stderr/stdout, and never reaches host construction (the
// data directory's host-id marker is never written).
func TestQrdbdStart_UnopenableVolumePool_DefaultFailsLoud(t *testing.T) {
	binary := buildBinary(t)
	dataDir := t.TempDir()
	missingPool := filepath.Join(t.TempDir(), "does-not-exist")
	cfgPath := writeVolumeBootConfig(t, missingPool, false)

	// nodeLog, not a bare bytes.Buffer: the exec copy goroutine is still
	// writing while the test reads.
	out := &nodeLog{}
	daemon, err := qrdbdharness.Start(binary, singleNodeVolumeBootArgs(dataDir, cfgPath), qrdbdharness.WithOutput(out))
	if err != nil {
		t.Fatalf("qrdbdharness.Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	waitErr := daemon.WaitContext(ctx)
	var hung *qrdbdharness.DaemonWaitError
	if errors.As(waitErr, &hung) {
		_ = daemon.Kill()
		t.Fatalf("qrdbd start did not exit within 30s; it must fail immediately: %v", hung)
	}
	if waitErr == nil {
		t.Fatalf("qrdbd start exited 0, want non-zero; output:\n%s", out.String())
	}

	combined := out.String()
	if !strings.Contains(combined, "broken") {
		t.Fatalf("output does not name the failed pool %q:\n%s", "broken", combined)
	}
	if !strings.Contains(combined, "allow-degraded-volume-boot") {
		t.Fatalf("output does not mention the override flag:\n%s", combined)
	}
	if err := qrdbdharness.WaitForNodeHost(dataDir, 200*time.Millisecond); err == nil {
		t.Fatal("the raft host must never be constructed when a volume pool failed to open and the default was not overridden")
	}
}

// TestQrdbdStart_UnopenableVolumePool_DegradedBootOverrideStarts proves the
// override: the SAME unopenable pool, but with
// --allow-degraded-volume-boot, lets `qrdbd start` continue -- the node
// reaches host construction (the data directory's host-id marker appears)
// despite the pool that failed to open.
func TestQrdbdStart_UnopenableVolumePool_DegradedBootOverrideStarts(t *testing.T) {
	binary := buildBinary(t)
	dataDir := t.TempDir()
	missingPool := filepath.Join(t.TempDir(), "does-not-exist")
	cfgPath := writeVolumeBootConfig(t, missingPool, false)

	out := &nodeLog{}
	daemon, err := qrdbdharness.Start(binary,
		singleNodeVolumeBootArgs(dataDir, cfgPath, "--allow-degraded-volume-boot"),
		qrdbdharness.WithOutput(out))
	if err != nil {
		t.Fatalf("qrdbdharness.Start: %v", err)
	}
	t.Cleanup(func() {
		if err := daemon.Kill(); err != nil {
			t.Errorf("kill qrdbd: %v", err)
		}
	})

	if err := qrdbdharness.WaitForNodeHost(dataDir, 30*time.Second); err != nil {
		t.Fatalf("node never reached host construction under --allow-degraded-volume-boot: %v\noutput:\n%s", err, out.String())
	}

	if !waitForOutput(out, "broken", 30*time.Second) {
		t.Fatalf("degraded boot must still name the skipped pool in its log output:\n%s", out.String())
	}
}

// TestQrdbdStart_UnopenableVolumePool_ConfigFileOverrideStarts proves the
// config-file equivalent, storage.allow_degraded_volume_boot, works the
// same as the CLI flag.
func TestQrdbdStart_UnopenableVolumePool_ConfigFileOverrideStarts(t *testing.T) {
	binary := buildBinary(t)
	dataDir := t.TempDir()
	missingPool := filepath.Join(t.TempDir(), "does-not-exist")
	cfgPath := writeVolumeBootConfig(t, missingPool, true)

	out := &nodeLog{}
	daemon, err := qrdbdharness.Start(binary, singleNodeVolumeBootArgs(dataDir, cfgPath), qrdbdharness.WithOutput(out))
	if err != nil {
		t.Fatalf("qrdbdharness.Start: %v", err)
	}
	t.Cleanup(func() {
		if err := daemon.Kill(); err != nil {
			t.Errorf("kill qrdbd: %v", err)
		}
	})

	if err := qrdbdharness.WaitForNodeHost(dataDir, 30*time.Second); err != nil {
		t.Fatalf("node never reached host construction under storage.allow_degraded_volume_boot: %v\noutput:\n%s", err, out.String())
	}
}
