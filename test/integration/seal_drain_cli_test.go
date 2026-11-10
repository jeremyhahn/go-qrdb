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

// seal_drain_cli_test.go drives the seal-quiesce row end to end over every
// CLI-selectable transport: a `qrdb barrier seal` issued while writers are
// putting keys drains the writes in flight, refuses the writes that meet it
// (the sealed refusal, or a paused shard's while the seal runs), refuses every
// write after it with the sealed refusal, leaves the daemon serving (no Pebble
// panic), and loses no acknowledged write across the seal/unseal cycle.

package integration_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// sealDrainWriters is how many CLI writers put keys while the seal runs.
const sealDrainWriters = 4

// sealDrainResult is what one writer saw: the index of its last
// acknowledged put (-1 for none) and the refusal that ended it.
type sealDrainResult struct {
	lastAcked int
	refusal   string
	err       error
}

// isSealedRefusal reports whether a failed CLI call was refused by the
// barrier: the error names the seal and is not an RBAC refusal.
func isSealedRefusal(stderr string) bool {
	lower := strings.ToLower(stderr)
	return strings.Contains(lower, "sealed") &&
		!strings.Contains(lower, "permission denied") &&
		!strings.Contains(lower, "unauthorized")
}

// isSealingRefusal reports whether a write that met the seal while it ran
// was refused by it: the sealed refusal, or the retryable refusal of a shard
// the seal paused before wiping the keys (seal-pause-resume,
// quicraft.ErrShardPaused classified Unavailable on every transport). The
// gRPC and Unix SDKs' connect-time health probe reports that refusal as
// Unavailable "health check failed" without its cause.
func isSealingRefusal(stderr string) bool {
	lower := strings.ToLower(stderr)
	return isSealedRefusal(stderr) || strings.Contains(lower, "shard paused") ||
		strings.Contains(lower, "code = unavailable desc = health check failed")
}

func TestSealDrain_InFlightWrites_PerProtocol(t *testing.T) {
	t.Parallel()
	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			t.Parallel()
			node := startFreshSealedNode(t, "passphrase", 0, 0)
			waitForBarrierReachable(t, node.cli, node.noAuthFlagsFor(proto), 60*time.Second)
			const passphrase = "seal-drain-passphrase-1"
			node.initSystem(t, proto, "--barrier-strategy", "passphrase", "--passphrase", passphrase)
			node.waitForFullBoot(t)
			flags := node.flagsFor(proto)
			run := func(args ...string) (string, string, error) {
				return runCLISplit(t, node.cli, append(append([]string{}, flags...), args...)...)
			}
			key := func(w, i int) string { return fmt.Sprintf("seal-drain-%s-%d-%d", proto.name, w, i) }

			// Each writer puts keys until the barrier refuses one. It
			// reports its first acknowledged put before the seal is issued,
			// so the seal always meets writes that are still flowing.
			flowing := make(chan struct{}, sealDrainWriters)
			results := make([]sealDrainResult, sealDrainWriters)
			var wg sync.WaitGroup
			for w := 0; w < sealDrainWriters; w++ {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					results[w].lastAcked = -1
					for i := 0; ; i++ {
						_, stderr, err := run("kv", "put", key(w, i), key(w, i))
						if err != nil {
							results[w].refusal = stderr
							if !isSealingRefusal(stderr) {
								results[w].err = fmt.Errorf("put %d: %v: %s", i, err, stderr)
							}
							if i == 0 {
								flowing <- struct{}{}
							}
							return
						}
						results[w].lastAcked = i
						if i == 0 {
							flowing <- struct{}{}
						}
					}
				}(w)
			}
			for w := 0; w < sealDrainWriters; w++ {
				<-flowing
			}

			// The seal drains the writes in flight within seal.drain_timeout,
			// so it exits zero: a non-zero exit is the drain-timeout error.
			if _, stderr, err := run("barrier", "seal", "-y", "--so-pin", sealNodeSOPIN); err != nil {
				t.Fatalf("[%s] barrier seal with writes in flight: %v\nstderr: %s", proto.name, err, stderr)
			}
			wg.Wait()
			for w, r := range results {
				if r.err != nil {
					t.Fatalf("[%s] writer %d ended on an error that is not the seal's refusal: %v", proto.name, w, r.err)
				}
				if r.lastAcked < 0 {
					t.Fatalf("[%s] writer %d had no acknowledged put before the seal: %s", proto.name, w, r.refusal)
				}
			}

			// A write after the seal is refused with the sealed refusal.
			if _, stderr, err := run("kv", "put", key(sealDrainWriters, 0), "after-seal"); err == nil {
				t.Fatalf("[%s] kv put after the seal must be refused", proto.name)
			} else if !isSealedRefusal(stderr) {
				t.Fatalf("[%s] kv put after the seal must be refused by the barrier, got: %v: %s", proto.name, err, stderr)
			}

			// The daemon is still serving: no Pebble panic took it down.
			if status := barrierStatus(t, node.cli, flags); !status.Sealed {
				t.Fatalf("[%s] barrier status after the seal: want sealed, got %+v", proto.name, status)
			}

			if _, stderr, err := run("barrier", "unseal", "--passphrase", passphrase); err != nil {
				t.Fatalf("[%s] barrier unseal: %v\nstderr: %s", proto.name, err, stderr)
			}

			// No acknowledged write was lost to the seal.
			for w, r := range results {
				for _, i := range []int{0, r.lastAcked} {
					stdout, stderr, err := run("kv", "get", key(w, i))
					if err != nil || !strings.Contains(stdout, key(w, i)) {
						t.Fatalf("[%s] acknowledged put %s after unseal: %v: %s%s", proto.name, key(w, i), err, stdout, stderr)
					}
				}
			}
			if _, stderr, err := run("kv", "put", key(sealDrainWriters, 1), "after-unseal"); err != nil {
				t.Fatalf("[%s] kv put after unseal: %v\nstderr: %s", proto.name, err, stderr)
			}
		})
	}
}

// sealtestHoldTimeout bounds each wait on the hold seam, so a seam that never
// fires fails the test rather than hanging it.
const sealtestHoldTimeout = 2 * time.Minute

// sealtestHold is the test's end of the qrdb_sealtest hold seam
// (pkg/seal/quiesce/sealtest_hold.go): the quiesce-sealtest directory in the
// daemon's working directory, with its held, draining and release FIFOs.
type sealtestHold struct{ dir string }

func newSealtestHold(t *testing.T, workDir string) *sealtestHold {
	t.Helper()
	dir := filepath.Join(workDir, "quiesce-sealtest")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("hold seam dir: %v", err)
	}
	for _, name := range []string{"held", "draining", "release"} {
		if err := syscall.Mkfifo(filepath.Join(dir, name), 0o600); err != nil {
			t.Fatalf("hold seam fifo %s: %v", name, err)
		}
	}
	return &sealtestHold{dir: dir}
}

// arm names the lease whose next admitted operation the daemon holds. The
// rename makes the name appear whole.
func (h *sealtestHold) arm(t *testing.T, lease string) {
	t.Helper()
	tmp := filepath.Join(h.dir, "arm.tmp")
	if err := os.WriteFile(tmp, []byte(lease), 0o600); err != nil {
		t.Fatalf("arm hold seam: %v", err)
	}
	if err := os.Rename(tmp, filepath.Join(h.dir, "arm")); err != nil {
		t.Fatalf("arm hold seam: %v", err)
	}
}

// read returns a channel that receives everything the daemon writes to the
// named FIFO, once it closes its end.
func (h *sealtestHold) read(name string) <-chan string {
	c := make(chan string, 1)
	go func() {
		data, err := os.ReadFile(filepath.Join(h.dir, name))
		if err != nil {
			c <- "read " + name + ": " + err.Error()
			return
		}
		c <- string(data)
	}()
	return c
}

// release lets the held operation go on.
func (h *sealtestHold) release(t *testing.T) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(h.dir, "release"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("release hold seam: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("release hold seam: %v", err)
	}
}

// startSealtestStandaloneNode starts a fresh sealed standalone node from a
// qrdbd built with the qrdb_sealtest hold seam, in a working directory that
// holds the seam's FIFOs. Standalone, not a Raft node: a seal pauses Raft
// shards and waits for their applies before the barrier drains, so a write
// held inside a Raft apply would keep the seal pending without the drain.
// Standalone writes reach the barrier's Pebble store directly, and only the
// drain waits for them.
func startSealtestStandaloneNode(t *testing.T) (*permNode, *sealtestHold) {
	t.Helper()

	bin, err := qrdbdharness.BuildBinary(qrdbdharness.DaemonPackage,
		qrdbdharness.WithBuildTags(qrdbdharness.DefaultBuildTags+" qrdb_sealtest"),
		qrdbdharness.WithBinaryName("qrdbd-sealtest"))
	if err != nil {
		t.Fatalf("build qrdbd with the hold seam: %v", err)
	}
	cli := buildCLIBinary(t)
	dataDir := t.TempDir()
	workDir := t.TempDir()
	hold := newSealtestHold(t, workDir)

	// The drain timeout outlasts the hold, so the seal can only return
	// once the held write does.
	cfgPath := filepath.Join(t.TempDir(), "seal-config.yaml")
	cfg := "seal:\n  enabled: true\n  strategy: passphrase\n  drain_timeout: 10m\n" +
		"databases:\n  - name: \"default\"\n    backend: \"pebble\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("seal config: %v", err)
	}

	sockDir, err := os.MkdirTemp("", "qrdb-hold-")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	unixSocket := filepath.Join(sockDir, "qrdbd.sock")

	quicPorts := allocatePorts(t, 1)
	tcpPorts := allocateTCPPorts(t, 3) // admin (REST), grpc, mcp
	restPort, grpcPort, mcpPort, quicPort := tcpPorts[0], tcpPorts[1], tcpPorts[2], quicPorts[0]

	args := []string{
		"start",
		"--config", cfgPath,
		"--mode", "standalone",
		"--data-dir", dataDir,
		"--admin-port", fmt.Sprintf("%d", restPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--enable-rest",
		"--enable-grpc",
		"--enable-quic", "--quic-port", fmt.Sprintf("%d", quicPort),
		"--enable-mcp", "--mcp-port", fmt.Sprintf("%d", mcpPort),
		"--enable-unix", "--unix-socket-path", unixSocket,
		// No TLS flags: the daemon self-generates its CA with the key, as
		// startFreshSealedNode relies on.
	}
	var out io.Writer = &logWriter{t: t, prefix: "[seal-hold] "}
	daemon, err := qrdbdharness.Start(bin, args, qrdbdharness.WithOutput(out), qrdbdharness.WithWorkingDir(workDir))
	if err != nil {
		t.Fatalf("start qrdbd: %v", err)
	}
	spawnedDaemons.Store(daemon.Cmd(), daemon)
	t.Cleanup(func() {
		if err := daemon.Kill(); err != nil {
			t.Errorf("[seal-hold] kill: %v", err)
		}
	})

	waitForNonEmptyFile(t, filepath.Join(dataDir, "ca.pem"), 30*time.Second)
	ca := loadDaemonCA(t, dataDir)
	adminCert, adminKey := ca.boundAdminCert(t, t.TempDir())
	return &permNode{
		cli:        cli,
		ca:         ca,
		dataDir:    dataDir,
		unixSocket: unixSocket,
		restPort:   restPort,
		grpcPort:   grpcPort,
		mcpPort:    mcpPort,
		quicPort:   quicPort,
		adminCert:  adminCert,
		adminKey:   adminKey,
	}, hold
}

// TestSealDrain_HeldWrite_PerProtocol holds one admitted write inside the
// barrier's Pebble store's quiesce lease and seals over every transport: the
// seal must stay pending, its drain waiting on that one lease, until the
// write is let go; then the seal completes, the write is acknowledged, and it
// reads back after unseal. A seal that skipped the drain would return while
// the lease was held.
func TestSealDrain_HeldWrite_PerProtocol(t *testing.T) {
	t.Parallel()
	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			t.Parallel()
			node, hold := startSealtestStandaloneNode(t)
			waitForBarrierReachable(t, node.cli, node.noAuthFlagsFor(proto), 60*time.Second)
			const passphrase = "seal-hold-passphrase-1"
			node.initSystem(t, proto, "--barrier-strategy", "passphrase", "--passphrase", passphrase)
			waitForAdmin(t, node.ca.Pool, node.restPort, 90*time.Second)
			flags := node.flagsFor(proto)
			run := func(args ...string) (string, string, error) {
				return runCLISplit(t, node.cli, append(append([]string{}, flags...), args...)...)
			}
			key := "seal-hold-" + proto.name
			if _, stderr, err := run("kv", "put", key+"-before", "before"); err != nil {
				t.Fatalf("[%s] kv put before the hold: %v\nstderr: %s", proto.name, err, stderr)
			}

			// Hold the next operation the Pebble store admits: the put.
			pebbleDir := filepath.Join(node.dataDir, "pebble")
			hold.arm(t, pebbleDir)
			heldC := hold.read("held")
			type result struct {
				stderr string
				err    error
			}
			putC := make(chan result, 1)
			go func() {
				_, stderr, err := run("kv", "put", key, "held-value")
				putC <- result{stderr, err}
			}()
			select {
			case got := <-heldC:
				if got != pebbleDir {
					t.Fatalf("[%s] hold seam held %q; want the Pebble store %q", proto.name, got, pebbleDir)
				}
			case r := <-putC:
				t.Fatalf("[%s] kv put returned without reaching the hold: %v: %s", proto.name, r.err, r.stderr)
			case <-time.After(sealtestHoldTimeout):
				t.Fatalf("[%s] kv put never reached the hold", proto.name)
			}

			// Seal with the write held: the drain reports the one lease
			// and waits; the seal does not return.
			drainingC := hold.read("draining")
			sealC := make(chan result, 1)
			go func() {
				_, stderr, err := run("barrier", "seal", "-y", "--so-pin", sealNodeSOPIN)
				sealC <- result{stderr, err}
			}()
			select {
			case got := <-drainingC:
				if got != pebbleDir {
					t.Fatalf("[%s] drain waits on %q; want the one held lease %q", proto.name, got, pebbleDir)
				}
			case r := <-sealC:
				t.Fatalf("[%s] barrier seal returned while a write held its lease: %v: %s", proto.name, r.err, r.stderr)
			case <-time.After(sealtestHoldTimeout):
				t.Fatalf("[%s] barrier seal never drained the held lease", proto.name)
			}
			select {
			case r := <-sealC:
				t.Fatalf("[%s] barrier seal returned while its drain waited: %v: %s", proto.name, r.err, r.stderr)
			case r := <-putC:
				t.Fatalf("[%s] held kv put returned before its release: %v: %s", proto.name, r.err, r.stderr)
			default:
			}

			// Let the write go: it is acknowledged, and the seal completes.
			hold.release(t)
			if r := <-putC; r.err != nil {
				t.Fatalf("[%s] held kv put admitted before the seal: %v\nstderr: %s", proto.name, r.err, r.stderr)
			}
			if r := <-sealC; r.err != nil {
				t.Fatalf("[%s] barrier seal after the release: %v\nstderr: %s", proto.name, r.err, r.stderr)
			}

			if _, stderr, err := run("kv", "put", key+"-after", "after-seal"); err == nil {
				t.Fatalf("[%s] kv put after the seal must be refused", proto.name)
			} else if !isSealedRefusal(stderr) {
				t.Fatalf("[%s] kv put after the seal must be refused by the barrier, got: %v: %s", proto.name, err, stderr)
			}
			if _, stderr, err := run("barrier", "unseal", "--passphrase", passphrase); err != nil {
				t.Fatalf("[%s] barrier unseal: %v\nstderr: %s", proto.name, err, stderr)
			}
			stdout, stderr, err := run("kv", "get", key)
			if err != nil || !strings.Contains(stdout, "held-value") {
				t.Fatalf("[%s] held put after unseal: %v: %s%s", proto.name, err, stdout, stderr)
			}
		})
	}
}
