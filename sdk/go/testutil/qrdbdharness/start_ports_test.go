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

package qrdbdharness

import (
	"bytes"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// argvHelperMode turns this test binary into a stand-in daemon that prints the
// argv it was launched with, one argument per line.
const argvHelperMode = "argv"

// productionPortDefaults are the fixed ports `qrdbd start` binds when a flag
// is left unset (cmd/qrdbd/start.go, clusterenroll.DefaultJoinPort and the
// mysql/pgsql flag files). A harnessed daemon must never be handed one.
var productionPortDefaults = map[string]int{
	"--admin-port":               63002,
	"--grpc-port":                63003,
	"--quic-port":                63004,
	"--mcp-port":                 63005,
	"--bootstrap-noise-port":     63006,
	"--cluster-join-listen-port": 63007,
	"--mysql-port":               3306,
	"--pgsql-port":               5432,
}

// flagValues returns every value args gives flag, in either form.
func flagValues(args []string, flag string) []string {
	var values []string
	for i, arg := range args {
		switch {
		case arg == flag && i+1 < len(args):
			values = append(values, args[i+1])
		case strings.HasPrefix(arg, flag+"="):
			values = append(values, strings.TrimPrefix(arg, flag+"="))
		}
	}
	return values
}

// assertReservedPort fails unless args sets flag exactly once, to a valid port
// other than its production default.
func assertReservedPort(t *testing.T, args []string, flag string) {
	t.Helper()
	values := flagValues(args, flag)
	if len(values) != 1 {
		t.Fatalf("%s set %d times in %q, want once", flag, len(values), args)
	}
	port, err := strconv.Atoi(values[0])
	if err != nil || port <= 0 || port > 65535 {
		t.Fatalf("%s = %q, want a port number", flag, values[0])
	}
	if port == productionPortDefaults[flag] {
		t.Fatalf("%s = %d, the production default", flag, port)
	}
}

// TestReserveListenerPortsCoversEveryProductionDefault fails when a listener
// with a fixed production default is missing from defaultPortListeners, so a
// harnessed daemon would bind that default.
func TestReserveListenerPortsCoversEveryProductionDefault(t *testing.T) {
	covered := make([]string, 0, len(defaultPortListeners))
	for _, l := range defaultPortListeners {
		covered = append(covered, l.port)
	}
	for flag := range productionPortDefaults {
		if !slices.Contains(covered, flag) {
			t.Errorf("%s has a fixed production default but ReserveListenerPorts never reserves it", flag)
		}
	}
}

// TestStartLaunchesDaemonWithReservedPorts is the guard over the spawned
// process itself: whatever argv a test hands Start, the daemon it launches
// carries an explicit, non-default port for every listener it enables, so a
// fixed default cannot slip back into a harnessed run.
func TestStartLaunchesDaemonWithReservedPorts(t *testing.T) {
	t.Setenv(helperEnv, argvHelperMode)
	args := []string{"start", "--data-dir", t.TempDir()}
	for _, l := range defaultPortListeners {
		if l.enabledBy != "" {
			args = append(args, l.enabledBy)
		}
	}
	var out bytes.Buffer
	daemon, err := Start(os.Args[0], args, WithOutput(&out))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = daemon.Kill() })
	if err := daemon.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	argv := strings.Split(strings.TrimSpace(out.String()), "\n")
	for _, l := range defaultPortListeners {
		assertReservedPort(t, argv, l.port)
	}
	if !slices.Equal(daemon.Cmd().Args[1:], argv) {
		t.Errorf("daemon printed %q, Cmd().Args = %q", argv, daemon.Cmd().Args[1:])
	}
}

// TestStartLeavesNonStartArgvAlone checks Start hands any other subcommand's
// argv to the daemon exactly as given.
func TestStartLeavesNonStartArgvAlone(t *testing.T) {
	t.Setenv(helperEnv, argvHelperMode)
	in := []string{"status", "--address", "127.0.0.1:1"}
	var out bytes.Buffer
	daemon, err := Start(os.Args[0], in, WithOutput(&out))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = daemon.Kill() })
	if err := daemon.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got := strings.Split(strings.TrimSpace(out.String()), "\n"); !slices.Equal(got, in) {
		t.Errorf("daemon launched with %q, want %q", got, in)
	}
}
