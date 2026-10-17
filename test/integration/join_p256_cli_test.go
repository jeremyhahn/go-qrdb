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

// join_p256_cli_test.go drives the real qrdbd binary through member-side CSR
// join over each bootstrap handshake suite: the trust-anchor fetch against the
// founder's enrollment listener and the CSR exchange against its join listener
// both run over the suite under test, then the joiner is admitted to the Raft
// group and serves replicated data.
//
// The rows are the suites the two listeners negotiate: Noise_NK (X25519), the
// FIPS-approved P-256 suite, and the P-256 suite in a cluster whose every
// process runs under GODEBUG=fips140=only -- where the Noise suite does not
// exist and P-256 is the only way in. The joiner is configured with the
// suite's keys alone, so a join that succeeds proves that suite carried both
// exchanges.
package integration_test

import (
	"bytes"
	"context"
	"crypto/fips140"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// logCapture is an io.Writer that keeps everything a daemon prints so a test
// can read back the public keys the daemon published in its log.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends p to the captured output.
func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

// await polls the captured output until the first capture group of re matches
// and returns it, failing the test when timeout elapses first.
func (c *logCapture) await(t *testing.T, re *regexp.Regexp, timeout time.Duration) string {
	t.Helper()
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	expired := time.After(timeout)
	for {
		c.mu.Lock()
		m := re.FindSubmatch(c.buf.Bytes())
		c.mu.Unlock()
		if m != nil {
			return string(m[1])
		}
		select {
		case <-poll.C:
		case <-expired:
			t.Fatalf("daemon log never matched %q within %v", re, timeout)
		}
	}
}

// publishedKeys are the responder public keys a founder logs for its two
// listeners, keyed by the log attribute that carries them.
var (
	anchorP256KeyRE = regexp.MustCompile(`(?:^|\s)p256_public_key=([0-9a-f]+)`)
	anchorNoiseKey  = regexp.MustCompile(`(?:^|\s)noise_public_key=([0-9a-f]+)`)
	joinP256KeyRE   = regexp.MustCompile(`join_p256_public_key=([0-9a-f]+)`)
	joinNoiseKeyRE  = regexp.MustCompile(`join_noise_public_key=([0-9a-f]+)`)
)

// setGODEBUG sets GODEBUG for the processes this test starts next and returns
// the function that restores the previous value. The test process itself is
// unaffected: GODEBUG is read when a Go program starts.
func setGODEBUG(t *testing.T, value string) (restore func()) {
	t.Helper()
	prev, had := os.LookupEnv("GODEBUG")
	if err := os.Setenv("GODEBUG", value); err != nil {
		t.Fatalf("set GODEBUG: %v", err)
	}
	restore = func() {
		if had {
			_ = os.Setenv("GODEBUG", prev)
			return
		}
		_ = os.Unsetenv("GODEBUG")
	}
	t.Cleanup(restore)
	return restore
}

// TestJoin_BootstrapSuites founds a two-member self-issued-CA group and joins
// a third node through the founder's enrollment and join listeners over one
// handshake suite per row.
func TestJoin_BootstrapSuites(t *testing.T) {
	tests := []struct {
		name string
		fips bool
		// noise marks a row that joins over Noise_NK alone. A test process
		// already under fips140=only cannot run it: its daemons inherit that
		// GODEBUG, so the row asserts the FIPS refusal instead.
		noise bool
		// flags builds the joiner's bootstrap flags from the founder's
		// published keys and listener addresses.
		flags func(noiseAddr, joinAddr string, keys *logCapture, t *testing.T) []string
	}{
		{
			name:  "noise_nk",
			noise: true,
			flags: func(noiseAddr, joinAddr string, keys *logCapture, t *testing.T) []string {
				return []string{
					"--bootstrap-noise-addr", noiseAddr,
					"--bootstrap-noise-key", keys.await(t, anchorNoiseKey, 30*time.Second),
					"--bootstrap-join-addr", joinAddr,
					"--bootstrap-join-key", keys.await(t, joinNoiseKeyRE, 30*time.Second),
				}
			},
		},
		{
			name: "p256",
			flags: func(noiseAddr, joinAddr string, keys *logCapture, t *testing.T) []string {
				return []string{
					"--bootstrap-noise-addr", noiseAddr,
					"--bootstrap-p256-key", keys.await(t, anchorP256KeyRE, 30*time.Second),
					"--bootstrap-join-addr", joinAddr,
					"--bootstrap-join-p256-key", keys.await(t, joinP256KeyRE, 30*time.Second),
				}
			},
		},
		{
			name: "p256_fips140_only",
			fips: true,
			flags: func(noiseAddr, joinAddr string, keys *logCapture, t *testing.T) []string {
				return []string{
					"--bootstrap-noise-addr", noiseAddr,
					"--bootstrap-p256-key", keys.await(t, anchorP256KeyRE, 30*time.Second),
					"--bootstrap-join-addr", joinAddr,
					"--bootstrap-join-p256-key", keys.await(t, joinP256KeyRE, 30*time.Second),
				}
			},
		},
	}

	// Build every binary before any row changes GODEBUG: `go build` and the
	// CLI are not what a row puts under FIPS.
	bin := buildBinary(t)
	buildCLIBinary(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.noise && fips140.Enforced() {
				assertNoiseOnlyBootstrapRefused(t, bin)
				return
			}
			runJoinSuiteRow(t, tt.fips, tt.flags)
		})
	}
}

// runJoinSuiteRow is one row of TestJoin_BootstrapSuites.
func runJoinSuiteRow(t *testing.T, fips bool, flags func(noiseAddr, joinAddr string, keys *logCapture, t *testing.T) []string) {
	raftPorts := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	noisePorts := allocateTCPPorts(t, 3)
	joinPorts := allocateTCPPorts(t, 3)

	node1Addr := fmt.Sprintf("127.0.0.1:%d", raftPorts[0])
	node2Addr := fmt.Sprintf("127.0.0.1:%d", raftPorts[1])
	node3Addr := fmt.Sprintf("127.0.0.1:%d", raftPorts[2])
	node2Key, node2KeyPEM, node2Identity := generateNodeKeypairAndIdentity(t)
	peers12 := fmt.Sprintf("1=%s,2=%s", node1Addr, node2Addr)
	peerAdminURLs := map[string]string{
		node1Addr: fmt.Sprintf("https://127.0.0.1:%d", adminPorts[0]),
		node2Addr: fmt.Sprintf("https://127.0.0.1:%d", adminPorts[1]),
		node3Addr: fmt.Sprintf("https://127.0.0.1:%d", adminPorts[2]),
	}

	// The CLI and the raw HTTPS helpers below are not under test for FIPS; only
	// the daemons are. A row that runs them under fips140=only restores the
	// ambient GODEBUG around those calls.
	var restore func()
	if fips {
		restore = setGODEBUG(t, "fips140=only")
	}
	daemonsUnderFIPS := func(on bool) {
		if !fips {
			return
		}
		if on {
			setGODEBUG(t, "fips140=only")
			return
		}
		restore()
	}

	founderLog := &logCapture{}
	node1 := startFoundingSelfIssuedCANode(t, raftPorts[0], adminPorts[0], grpcPorts[0], noisePorts[0], joinPorts[0],
		peers12, fmt.Sprintf("2=%s", node2Identity), peerAdminURLs, founderLog)

	node1CAKeyPath := filepath.Join(node1.dataDir, "ca-key.pem")
	node2LeafPEM := signCertWithCA(t, node1.caPath, node1CAKeyPath, nodeLeafTemplate("node2"), &node2Key.PublicKey)
	node2AdminKey, _, _ := generateNodeKeypairAndIdentity(t)
	node2AdminCertPEM := signCertWithCA(t, node1.caPath, node1CAKeyPath, adminCertTemplate(), &node2AdminKey.PublicKey)
	node2 := startSecondFounderExplicitTLS(t, raftPorts[1], adminPorts[1], grpcPorts[1],
		peers12, fmt.Sprintf("1=%s", node1.identity), node1.caPath, node2LeafPEM, node2KeyPEM, peerAdminURLs)

	daemonsUnderFIPS(false)
	cli := buildCLIBinary(t)
	credDir := t.TempDir()
	bootAdminIdentity(t, cli, node1.caPath, node1.adminPort, "SOPIN-node1-suite", "USERPIN-node1-suite",
		filepath.Join(credDir, "admin-cert.pem"), filepath.Join(credDir, "admin-key.pem"), "",
		"--barrier-strategy", "none")
	ceremonyBringUpBarrierWithCertHTTP(t, node2.client, node2.adminPort, node2AdminCertPEM)
	waitForHealthy(t, node1.dataDir, 90*time.Second)
	waitForHealthy(t, node2.dataDir, 90*time.Second)
	waitForReachable(t, node1.client, node1.adminPort, 30*time.Second)
	waitForReachable(t, node2.client, node2.adminPort, 30*time.Second)

	token3 := mintJoinTokenHTTP(t, node1.client, node1.adminPort, node3Addr)
	peers123 := fmt.Sprintf("%s,3=%s", peers12, node3Addr)
	memberIDs13 := fmt.Sprintf("1=%s,2=%s", node1.identity, node2.identity)

	daemonsUnderFIPS(true)
	transport := flags(
		fmt.Sprintf("127.0.0.1:%d", noisePorts[0]),
		fmt.Sprintf("127.0.0.1:%d", joinPorts[0]),
		founderLog, t)
	node3 := startJoiningSelfIssuedCANode(t, 3, raftPorts[2], adminPorts[2], grpcPorts[2], noisePorts[2], joinPorts[2],
		peers123, memberIDs13, node1, token3, peerAdminURLs, transport...)
	daemonsUnderFIPS(false)

	addMemberHTTP(t, node1.client, node1.adminPort, 1, 3, node3.raftAddr, node3.identity)
	finishJoiningNode(t, node3)
	waitForHealthy(t, node3.dataDir, 60*time.Second)

	kvPutHTTP(t, node1.client, node1.adminPort, "suite-"+strings.ReplaceAll(t.Name(), "/", "-"), []byte("replicated"))
	waitForKVValueHTTP(t, node3.client, node3.adminPort, "suite-"+strings.ReplaceAll(t.Name(), "/", "-"), "replicated", 30*time.Second)
}

// TestJoin_FIPSRefusesNoiseOnlyBootstrap starts the real daemon under
// GODEBUG=fips140=only configured to enroll over Noise_NK alone and shows it
// refuses, naming FIPS and the P-256 flag that replaces it -- the refusal the
// P-256 rows above are the way around.
func TestJoin_FIPSRefusesNoiseOnlyBootstrap(t *testing.T) {
	bin := buildBinary(t)
	setGODEBUG(t, "fips140=only")
	assertNoiseOnlyBootstrapRefused(t, bin)
}

// assertNoiseOnlyBootstrapRefused starts bin, under the ambient GODEBUG, as a
// joiner configured to enroll over Noise_NK alone, and fails t unless it
// refuses to start with an error that names FIPS and the P-256 flag.
func assertNoiseOnlyBootstrapRefused(t *testing.T, bin string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin,
		"start",
		"--data-dir", t.TempDir(),
		"--sm", "mvcc",
		"--discovery", "static",
		"--peers", "1=127.0.0.1:1,2=127.0.0.1:2",
		"--replica-id", "2",
		"--shard-id", "1",
		"--listen", "127.0.0.1:0",
		"--raft-address", "127.0.0.1:2",
		"--auth-enabled=false",
		"--bootstrap-noise-key", strings.Repeat("ab", 32),
		"--bootstrap-noise-addr", "127.0.0.1:1",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("a Noise-only bootstrap under FIPS must fail; output:\n%s", out)
	}
	for _, want := range []string{"FIPS", "--bootstrap-p256-key"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("refusal does not mention %q; output:\n%s", want, out)
		}
	}
}
