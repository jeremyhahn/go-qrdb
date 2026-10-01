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
	"net"
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
