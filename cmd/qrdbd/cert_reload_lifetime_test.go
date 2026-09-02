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
	"os"
	"path/filepath"
	"strings"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
)

// openInotifyInstances counts this process's inotify file descriptors.
//
// A process-wide count rather than a per-reloader one, because the resource
// being protected is process-wide — and, more to the point, inotify instances
// are limited PER USER by fs.inotify.max_user_instances, so one leaky binary
// exhausts every other process the same user is running. That is how a test
// leak in this package took out CI: both runner hosts sat at the default 128
// and unrelated jobs began failing with "too many open files" from
// inotify_init (go-qrdb#95).
func openInotifyInstances(t *testing.T) int {
	t.Helper()

	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("/proc unavailable, cannot count inotify instances: %v", err)
	}

	n := 0
	for _, e := range entries {
		// A missing target is normal: the descriptor list is a snapshot and
		// ReadDir's own fd closes underneath it.
		if target, lerr := os.Readlink(filepath.Join("/proc/self/fd", e.Name())); lerr == nil {
			if strings.Contains(target, "inotify") {
				n++
			}
		}
	}
	return n
}

// runStart releases its inotify watcher however it returns.
//
// The CertReloader is stopped on the clean-shutdown path, which is the only
// path a passing test takes. Every OTHER return — a config error, a failed
// subsystem, a signal during startup — left the watcher open for the lifetime
// of the process, because Stop was reachable only from the bottom of the
// function.
//
// This drives a runStart that fails EARLY, well before the shutdown block, and
// asserts the instance count is unchanged. An invalid database engine is the
// vehicle: it is refused after the reloader is wired and long before any
// shutdown, which is precisely the shape that leaked.
//
// Measured before the fix: the cmd/qrdbd TestRunStart family left 38 instances
// open in a single run, against a runner limit of 128.
func TestRunStart_ReleasesTheCertWatcherOnEveryReturn(t *testing.T) {
	dir := t.TempDir()

	// The reloader is only wired when node-cert.pem and node-key.pem both
	// exist, so the leak needs them present.
	writeTLSFixtures(t, dir)

	before := openInotifyInstances(t)

	f := &startFlags{
		dataDir:         dir,
		smType:          "memory",
		discoveryMethod: "static",
		listen:          "0.0.0.0:0",
		raftAddress:     "127.0.0.1:0",
		shardID:         1,
		replicaID:       1,
		deploymentID:    1,
		// Refused after the reloader is wired and before any shutdown.
		databases: []qrdbconfig.DatabaseConfig{
			{Name: "testdb", Engine: "invalid-engine", Backend: "pebble"},
		},
		host: testBindHost,
	}

	if runErr := runStart(f); runErr == nil {
		t.Fatal("runStart accepted an invalid database engine, so this test no " +
			"longer exercises an early return")
	}

	if after := openInotifyInstances(t); after > before {
		t.Errorf("runStart leaked %d inotify instance(s) on an early return "+
			"(%d before, %d after): the watcher outlives the call, and these "+
			"are limited per USER, so a test binary exhausts them for every "+
			"process the same user runs", after-before, before, after)
	}
}
