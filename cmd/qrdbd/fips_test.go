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

package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
)

// This file carries NO build constraint and must not acquire one. Its helpers
// are called from bootstrap_test.go, cmd_test.go and enrollment_resume_test.go
// (unconstrained) as well as from coverage_backfill_test.go,
// coverage_final_test.go and coverage_gaps_test.go (`!integration`), so its
// constraint has to be the union of both, which is none (AGENTS.md, "Build
// constraints on test files").

// noiseServed reports whether this runtime serves the Noise NK enrollment
// method. In FIPS 140-3 mode it does not, and the daemon's enrollment surface
// is the approved methods alone -- see pkg/clusterenroll's fips.go.
func noiseServed() bool { return clusterenroll.NoiseAvailable() }

// assertAdvertisesWithoutNoise asserts the enrollment contract a first node
// must satisfy when the Noise method is unavailable: it still gets an
// advertiser -- which is what makes cmd/qrdbd register /v1/ca/bootstrap for it
// -- that advertiser serves no Noise, and no Noise static key is written into
// the data directory.
//
// A caller reaching this has not skipped its own assertion; it has asserted the
// contract that REPLACES its own in this runtime. Where the caller's fixture is
// a fault in the Noise key file or its listen port, "the fixture is never
// reached" is the whole of what can truthfully be said, and asserting it catches
// a regression that started reading the key file in FIPS mode again.
func assertAdvertisesWithoutNoise(t *testing.T, bundle *TLSBundle, f *startFlags) {
	t.Helper()
	before := snapshotNoiseKey(t, f.dataDir)
	servers, err := startBootstrapServers(bundle, f, testLogger())
	if err != nil {
		t.Fatalf("startBootstrapServers must succeed when the Noise method is unavailable: %v", err)
	}
	if servers == nil {
		t.Fatal("a first node must still get an advertiser; nil is what stops /v1/ca/bootstrap being registered")
	}
	t.Cleanup(servers.Stop)
	if servers.NoiseEnabled() {
		t.Fatal("NoiseEnabled() = true, want false when the method is unavailable")
	}
	if got := servers.Addr(); got != "" {
		t.Errorf("Addr() = %q, want empty", got)
	}
	if got := servers.NoisePublicKey(); len(got) != 0 {
		t.Errorf("NoisePublicKey() = %x, want empty", got)
	}
	// The key file is compared against a snapshot taken BEFORE the call rather
	// than asserted absent: several callers PLANT a malformed noise-key.hex as
	// their fault fixture, and "absent" would fail on the fixture instead of on
	// the code. What has to hold is that the call neither created the file nor
	// touched one that was there.
	if after := snapshotNoiseKey(t, f.dataDir); after != before {
		t.Errorf("%s changed from %q to %q; the Noise key path must not be touched when the method is unavailable",
			noiseKeyFile, before, after)
	}
}

// snapshotNoiseKey describes the state of the data directory's Noise key path
// in a form two calls can be compared by.
//
// It reports rather than fails on every shape the callers plant there -- a
// directory, a regular file holding garbage, a path under a blocked parent,
// nothing at all -- because it is the fixtures themselves that make those
// shapes, and a helper that treated one of them as an error would fail on the
// fault the test deliberately injected instead of on the code.
func snapshotNoiseKey(t *testing.T, dataDir string) string {
	t.Helper()
	path := filepath.Join(dataDir, noiseKeyFile)
	info, err := os.Lstat(path)
	if err != nil {
		return "absent"
	}
	if !info.Mode().IsRegular() {
		return "non-regular:" + info.Mode().String()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "unreadable"
	}
	return "regular:" + string(data)
}

// TestStartBootstrapServers_TracksTheRuntimesNoiseRuling is the daemon-side
// statement of the ruling, asserted in whichever runtime the suite is executing:
// under fips140=only in the nightly FIPS job, under fips140=off in the ordinary
// one. Both branches assert, and between the two jobs both run on every commit.
func TestStartBootstrapServers_TracksTheRuntimesNoiseRuling(t *testing.T) {
	dir := t.TempDir()
	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS: %v", err)
	}
	f := &startFlags{dataDir: dir, host: testBindHost, adminPort: 9443}
	// Zero is the fixed default 63006, not an OS-assigned port (go-qrdb#313).
	reserveNoisePort(t, f)

	if !noiseServed() {
		assertAdvertisesWithoutNoise(t, bundle, f)
		return
	}

	servers, err := startBootstrapServers(bundle, f, testLogger())
	if err != nil {
		t.Fatalf("startBootstrapServers: %v", err)
	}
	t.Cleanup(servers.Stop)
	if !servers.NoiseEnabled() {
		t.Fatal("NoiseEnabled() = false, want true when the method is available")
	}
	if len(servers.NoisePublicKey()) == 0 {
		t.Fatal("expected a Noise public key")
	}
	if _, err := os.Stat(filepath.Join(dir, noiseKeyFile)); err != nil {
		t.Errorf("expected %s to be persisted: %v", noiseKeyFile, err)
	}
}

// TestBootstrapTLS_NoiseOnlyJoinIsRefusedByNameUnderFIPS is the join-side
// statement for the daemon. It runs the assertion that fits the runtime: a
// refusal naming FIPS under fips140=only, and an ordinary fetch failure against
// a dead address otherwise -- which is the pre-change behaviour, so this is also
// the daemon's regression guard.
func TestBootstrapTLS_NoiseOnlyJoinIsRefusedByNameUnderFIPS(t *testing.T) {
	f := &startFlags{
		dataDir:           t.TempDir(),
		bootstrapNoiseKey: "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
		// A loopback port nothing is listening on, so the non-FIPS branch
		// below fails on connection-refused immediately rather than sitting
		// out the 15s per-method timeout an unroutable address would cost.
		bootstrapNoiseAddr: net.JoinHostPort(testBindHost, strconv.Itoa(deadTCPPort(t))),
		raftAddress:        "127.0.0.1:63001",
		clusterTrustDomain: "test.cluster",
	}

	_, err := bootstrapTLS(f, testLogger())
	if err == nil {
		t.Fatal("a join against an unreachable bootstrap server must fail")
	}

	if !noiseServed() {
		var fipsErr *clusterenroll.NoiseFIPSUnavailableError
		if !errors.As(err, &fipsErr) {
			t.Fatalf("error = %v (%T), want *clusterenroll.NoiseFIPSUnavailableError", err, err)
		}
		msg := err.Error()
		if !strings.Contains(msg, "FIPS 140-3") {
			t.Errorf("the refusal does not name FIPS 140-3: %s", msg)
		}
		if strings.Contains(msg, "key size") {
			t.Errorf("the refusal still leads with the misleading key-size clause: %s", msg)
		}
		return
	}

	// Outside FIPS mode the method is attempted and fails on the network, which
	// is exactly what it did before this change.
	if strings.Contains(err.Error(), "FIPS") {
		t.Errorf("a non-FIPS runtime must not refuse a Noise join for FIPS reasons: %v", err)
	}
}

// deadTCPPort returns a loopback TCP port that nothing is listening on: it
// binds one, reads the number and closes it, so a dial to it is refused rather
// than dropped. A hard-coded number would be a port some other pipeline on the
// same host may hold (go-qrdb#313).
func deadTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", net.JoinHostPort(testBindHost, "0"))
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("release the port: %v", err)
	}
	return port
}

// assertNoiseJoinRefusedByName asserts what replaces a live-Noise-server
// fixture when the method is unavailable. The fixture itself cannot be built --
// there is no X25519 key for a server to have an identity with -- so what a
// test built around one can still assert is the daemon's answer to the join
// that fixture existed to serve: a refusal that names FIPS, raised before any
// dial, and NOT the "noise: invalid key size" the Noise library reports for a
// FIPS refusal.
//
// f is used as given except that its Noise coordinates are replaced with ones
// no server could be standing behind, since the point is that nothing is
// contacted.
func assertNoiseJoinRefusedByName(t *testing.T, f *startFlags) {
	t.Helper()
	f.bootstrapNoiseKey = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	f.bootstrapNoiseAddr = net.JoinHostPort(testBindHost, strconv.Itoa(deadTCPPort(t)))

	_, err := bootstrapTLS(f, testLogger())
	if err == nil {
		t.Fatal("a join configured with Noise alone must be refused when the method is unavailable")
	}
	var fipsErr *clusterenroll.NoiseFIPSUnavailableError
	if !errors.As(err, &fipsErr) {
		t.Fatalf("error = %v (%T), want *clusterenroll.NoiseFIPSUnavailableError", err, err)
	}
	if strings.Contains(err.Error(), "key size") {
		t.Errorf("the refusal still leads with the misleading key-size clause: %v", err)
	}
}
