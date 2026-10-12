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
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
)

// This file carries no build constraint on purpose: reserveNoisePort is
// called from files under every tag set this package builds with, and
// TestMain arms holdDefaultNoisePort under all of them. A helper's
// constraint must be a superset of every caller's (AGENTS.md).

// reserveNoisePort gives the caller's flags a Noise port nothing else on this
// host holds, probed AT THE HOST THE LISTENER WILL BIND.
//
// clusterenroll.Options.NoisePort documents zero as "defaults to
// DefaultNoisePort" -- 63006, a fixed number -- and NOT as the ":0" the Go
// convention would read it as. So `bootstrapNoisePort: 0` is not an ephemeral
// port, it is THE most contended port in the package, and every concurrent
// copy of these tests wants exactly it. Gitea runs pull_request CI on
// refs/pull/N/head across five runners sharing one host, so concurrent copies
// are the normal case rather than the exception: this reddened main's CI on
// 0676e9af with two of these tests failing 40 ms apart on
//
//	listen tcp 127.0.0.1:63006: bind: address already in use
//
// and it was the THIRD sighting -- see the comment above
// TestNewStartCmd_ValidConfigFileApplied in coverage_gaps_test.go for the
// first, which was misread as a config-loading bug and filed as go-qrdb#84.
//
// The host is read back off the flags through resolveNoiseHost rather than
// assumed to be testBindHost, because a port proven free at one address says
// nothing about the same number at another -- that is go-qrdb#253, and probing
// where you actually bind is the whole lesson of go-qrdb#313.
//
// Every test that can reach the Noise listener -- resolveTLS on a first node,
// or runStart on flags that get that far -- calls this. holdDefaultNoisePort
// is what makes forgetting it fail every time instead of one run in three.
func reserveNoisePort(t *testing.T, f *startFlags) {
	t.Helper()
	port, err := freePortOn(resolveNoiseHost(f), networkTCP)
	if err != nil {
		t.Fatalf("reserve a Noise port at %s: %v", resolveNoiseHost(f), err)
	}
	f.bootstrapNoisePort = port
}

// holdDefaultNoisePort occupies clusterenroll.DefaultNoisePort on the
// wildcard address for the life of this package's test binary, and returns
// the function that lets it go.
//
// It is the guard behind reserveNoisePort. A test that reaches the Noise
// listener without reserving a port binds the fixed default, and that only
// fails when some concurrent process happens to hold the port: the defect
// then surfaces as a flake in somebody else's run (go-qrdb#84 and its two
// repeats). Holding the port here makes the same omission fail on every run,
// in the test that has it, with "63006: bind: address already in use".
//
// It never depends on the port being free. If the listen fails, something
// else already holds the port, and an occupied port is all the guard needs;
// TestDefaultNoisePortIsHeld checks that, rather than this function's own
// success.
func holdDefaultNoisePort() func() {
	ln, err := net.Listen(networkTCP, net.JoinHostPort("", strconv.Itoa(clusterenroll.DefaultNoisePort)))
	if err != nil {
		return func() {}
	}
	return func() { _ = ln.Close() }
}

// TestDefaultNoisePortIsHeld proves the guard is armed: while this package's
// tests run, nothing in them can bind the default Noise port at the address
// these tests bind on. Removing holdDefaultNoisePort from TestMain turns this
// red on any host where the port is otherwise free.
func TestDefaultNoisePortIsHeld(t *testing.T) {
	assertDefaultNoisePortHeld(t)
}

// TestHoldDefaultNoisePort_SecondHoldIsANoOp: a hold while TestMain's is live
// cannot listen, and must hand back a release that leaves TestMain's hold in
// place -- the shape it takes when a concurrent copy of this package already
// holds the port.
func TestHoldDefaultNoisePort_SecondHoldIsANoOp(t *testing.T) {
	holdDefaultNoisePort()()
	assertDefaultNoisePortHeld(t)
}

// assertDefaultNoisePortHeld fails t if the default Noise port can be bound
// at either address this package's tests bind on.
func assertDefaultNoisePortHeld(t *testing.T) {
	t.Helper()
	for _, host := range []string{testBindHost, raftListenHost} {
		addr := net.JoinHostPort(host, strconv.Itoa(clusterenroll.DefaultNoisePort))
		ln, err := net.Listen(networkTCP, addr)
		if err == nil {
			_ = ln.Close()
			t.Fatalf("bound %s during the package's tests: holdDefaultNoisePort is not armed in TestMain, so a test that forgets reserveNoisePort would pass here and flake elsewhere", addr)
		}
	}
}
