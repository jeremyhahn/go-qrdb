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

// go-qrdb#241 at the layer that actually ships.
//
// resolveTLS keeps its OWN cached-certificate branch, above the delegation to
// clusterenroll.Resolve, so that an X.509 parse failure keeps the qrdbd-typed
// *TLSLoadError. That branch returns before the package's cached path is ever
// reached -- so a fix that only lands in pkg/clusterenroll is fully tested,
// entirely green, and does nothing whatsoever for the daemon. This asserts
// against resolveTLS for that reason.
package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
)

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
func reserveNoisePort(t *testing.T, f *startFlags) {
	t.Helper()
	port, err := freePortOn(resolveNoiseHost(f), networkTCP)
	if err != nil {
		t.Fatalf("reserve a Noise port at %s: %v", resolveNoiseHost(f), err)
	}
	f.bootstrapNoisePort = port
}

// TestResolveTLS_FirstNodeResumesEnrollmentOnRestart: a first node that boots,
// serves, and restarts must come back with a live enrollment listener.
//
// Boot 1 goes through clusterenroll (paths 3-5). Boot 2 is the case: the
// certificates are cached, so resolveTLS takes its own path 2, and that is the
// branch that used to return a nil advertiser for every boot after the first.
func TestResolveTLS_FirstNodeResumesEnrollmentOnRestart(t *testing.T) {
	dir := t.TempDir()
	f := &startFlags{
		dataDir:         dir,
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001", "2=127.0.0.2:63001"},
		host:            testBindHost,
	}
	// Both boots bind, and boot 2 binds the same number boot 1 released, so
	// one reservation serves the pair. See reserveNoisePort for why a zero
	// here is the fixed default rather than an ephemeral port.
	reserveNoisePort(t, f)

	_, servers1, err := resolveTLS(f, testLogger())
	if err != nil {
		t.Fatalf("boot 1: %v", err)
	}
	if servers1 == nil {
		t.Fatal("boot 1 started no bootstrap servers, so this is not the case under test")
	}
	addr := servers1.Addr()
	servers1.Stop() // the daemon exits

	_, servers2, err := resolveTLS(f, testLogger())
	if err != nil {
		t.Fatalf("boot 2 (cached certificates): %v", err)
	}
	if servers2 == nil {
		t.Fatal("boot 2 returned no bootstrap servers: the daemon restarted with cached " +
			"certificates and came up with no enrollment listener, reporting healthy. " +
			"No peer can join it and nothing says so (go-qrdb#241)")
	}
	t.Cleanup(servers2.Stop)

	conn, err := net.DialTimeout("tcp", servers2.Addr(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial the resumed listener at %s: %v; non-nil but nothing is listening",
			servers2.Addr(), err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close probe conn: %v", err)
	}
	_ = addr
}

// TestRunStart_ReleasesTheEnrollmentListenerOnAnEarlyError.
//
// go-qrdb#241 gave runStart a live resource on a path that never owned one.
// Before it, the cached path -- the one every restart takes -- returned a nil
// advertiser, so the absence of the listener was also the absence of the leak.
// runStart stops bootServers only in its signal-shutdown branch, and there are
// 34 returns between resolveTLS and that branch. Each one leaked the listener
// goroutine, the socket, and the Noise static key's protected pages for the
// life of the process.
//
// Asserted by REBINDING the address rather than by observing that Stop was
// called: a leaked listener still owns the port, which is how this surfaces in
// practice -- the next start fails to bind -- rather than as a memory
// statistic.
func TestRunStart_ReleasesTheEnrollmentListenerOnAnEarlyError(t *testing.T) {
	dir := t.TempDir()

	// A regular file where the databases directory belongs fails database
	// setup, which runs after resolveTLS has already started the listener.
	if err := os.WriteFile(filepath.Join(dir, "databases"), []byte("x"), 0o600); err != nil {
		t.Fatalf("plant the failure: %v", err)
	}

	f := &startFlags{
		dataDir:         dir,
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001", "2=127.0.0.2:63001"},
		host:            testBindHost,
		smType:          "memory",
	}
	reserveNoisePort(t, f)
	port := f.bootstrapNoisePort

	if err := runStart(f); err == nil {
		t.Fatal("runStart must fail: the databases path is a regular file")
	}

	// The rebind probes the same address the listener took, for the same
	// reason the reservation did.
	ln, err := net.Listen("tcp", net.JoinHostPort(resolveNoiseHost(f), strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("rebind the enrollment port %d after runStart returned an error: %v. "+
			"The resumed listener, its goroutine and the Noise static key's protected "+
			"pages are still alive for the life of the process (go-qrdb#241)", port, err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestResolveTLS_RefusesADataDirectoryWithNoRecordedRole is go-qrdb#264 at the
// layer that ships, for the same reason the resume assertion above lives here:
// resolveTLS keeps its own cached-certificate branch above the delegation to
// clusterenroll.Resolve, so a refusal that only lands in pkg/clusterenroll is
// fully tested, entirely green, and never reached by the daemon.
//
// The error has to reach the caller of runStart, not merely be returned by
// resolveTLS, or "refuses to boot" is a claim about an internal function while
// the daemon carries on.
func TestResolveTLS_RefusesADataDirectoryWithNoRecordedRole(t *testing.T) {
	dir := t.TempDir()
	f := &startFlags{
		dataDir:         dir,
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001", "2=127.0.0.2:63001"},
		host:            testBindHost,
		smType:          "memory",
	}
	// Boot 1 binds; see reserveNoisePort.
	reserveNoisePort(t, f)

	// Boot 1 writes the material and the role, exactly as a current version
	// does. Removing the role file is what an upgraded pre-role data directory
	// looks like, and it is the only way to build one: no path in this version
	// leaves material without a role.
	_, servers1, err := resolveTLS(f, testLogger())
	if err != nil {
		t.Fatalf("boot 1: %v", err)
	}
	if servers1 == nil {
		t.Fatal("boot 1 started no bootstrap servers, so this is not the case under test")
	}
	servers1.Stop()
	if err := os.Remove(filepath.Join(dir, clusterenroll.RoleFile)); err != nil {
		t.Fatalf("age the data directory back to before the role file: %v", err)
	}

	_, servers2, err := resolveTLS(f, testLogger())
	if servers2 != nil {
		servers2.Stop()
		t.Error("the daemon started an enrollment listener for a data directory with no " +
			"recorded role; the role is not recoverable from the material")
	}
	var mre *clusterenroll.MissingRoleError
	if !errors.As(err, &mre) {
		t.Fatalf("resolveTLS error = %v; want *clusterenroll.MissingRoleError -- the daemon "+
			"must refuse to start rather than come up serving no enrollment (go-qrdb#264)", err)
	}

	if err := runStart(f); !errors.As(err, &mre) {
		t.Fatalf("runStart error = %v; want *clusterenroll.MissingRoleError. resolveTLS "+
			"refusing is not the daemon refusing if something between them swallows it", err)
	}
}
