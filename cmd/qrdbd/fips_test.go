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
