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
	"strings"
	"sync"
	"testing"
	"time"
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
