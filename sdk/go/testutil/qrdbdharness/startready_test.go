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
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	// collideHelperMode exits with a bind failure on the port of the flag
	// collideFlagEnv names, for the first collideTimesEnv starts that share
	// collideStateEnv's file, then writes collideReadyEnv's file and stays up.
	collideHelperMode = "collide"
	collideFlagEnv    = "QRDBDHARNESS_COLLIDE_FLAG"
	collideTimesEnv   = "QRDBDHARNESS_COLLIDE_TIMES"
	collideStateEnv   = "QRDBDHARNESS_COLLIDE_STATE"
	collideReadyEnv   = "QRDBDHARNESS_COLLIDE_READY"
)

// runCollideHelper is the collide helper mode: the shape of a qrdbd whose
// listener port another process bound between the reservation and its bind.
func runCollideHelper() int {
	times, err := strconv.Atoi(os.Getenv(collideTimesEnv))
	if err != nil {
		return 2
	}
	state := os.Getenv(collideStateEnv)
	// #nosec G703 -- state is filepath.Join(t.TempDir(), "collisions"), set by
	// the parent test that started this helper; the helper is never run otherwise.
	seen, err := os.ReadFile(state)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 2
	}
	if len(seen) < times {
		// #nosec G703 -- the same parent-chosen t.TempDir() path read above.
		if err := os.WriteFile(state, append(seen, 'x'), 0o600); err != nil {
			return 2
		}
		port := flagValue(os.Args[1:], os.Getenv(collideFlagEnv))
		os.Stderr.WriteString("2026/10/30 12:34:56 quic udp socket bind on 0.0.0.0:" + port +
			" failed: listen udp 0.0.0.0:" + port + ": bind: address already in use\n")
		return 1
	}
	// #nosec G703 -- filepath.Join(t.TempDir(), "ready"), set by the parent test.
	if err := os.WriteFile(os.Getenv(collideReadyEnv), []byte("ready"), 0o600); err != nil {
		return 2
	}
	time.Sleep(5 * time.Minute)
	return 0
}

// flagValue returns the value args give flag, as "--flag v" or "--flag=v".
func flagValue(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(arg, flag+"="); ok {
			return v
		}
	}
	return ""
}

// collideSetup arms the collide helper to fail its first times starts on
// flag's port, and returns the ready func StartReady needs and a count of
// the collisions so far.
func collideSetup(t *testing.T, flag string, times int) (func(*Daemon) error, func() int) {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "collisions")
	ready := filepath.Join(dir, "ready")
	t.Setenv(helperEnv, collideHelperMode)
	t.Setenv(collideFlagEnv, flag)
	t.Setenv(collideTimesEnv, strconv.Itoa(times))
	t.Setenv(collideStateEnv, state)
	t.Setenv(collideReadyEnv, ready)
	count := func() int {
		seen, _ := os.ReadFile(state)
		return len(seen)
	}
	return func(d *Daemon) error { return d.WaitForFile(ready, time.Minute) }, count
}

func startReady(t *testing.T, args []string, ready func(*Daemon) error) (*Daemon, error) {
	t.Helper()
	d, err := StartReady(os.Args[0], args, ready)
	if d != nil {
		t.Cleanup(func() { _ = d.Kill() })
	}
	return d, err
}

func TestStartReadyReturnsAReadyDaemon(t *testing.T) {
	ready, collisions := collideSetup(t, "--admin-port", 0)
	d, err := startReady(t, []string{startCommand}, ready)
	if err != nil {
		t.Fatalf("StartReady: %v", err)
	}
	if d == nil || collisions() != 0 {
		t.Fatalf("daemon = %v after %d collisions, want a ready daemon on the first start", d, collisions())
	}
}

func TestStartReadyRetriesAReservedPortCollision(t *testing.T) {
	ready, collisions := collideSetup(t, "--admin-port", 1)
	d, err := startReady(t, []string{startCommand}, ready)
	if err != nil {
		t.Fatalf("StartReady: %v", err)
	}
	if got := collisions(); got != 1 {
		t.Errorf("collisions = %d, want the one collision retried", got)
	}
	if flagValue(d.args, "--admin-port") == "" {
		t.Errorf("args = %q, want a re-reserved --admin-port", d.args)
	}
}

func TestStartReadyRetriesAQUICPortCollision(t *testing.T) {
	ready, collisions := collideSetup(t, "--quic-port", 2)
	if _, err := startReady(t, []string{startCommand, "--enable-quic"}, ready); err != nil {
		t.Fatalf("StartReady: %v", err)
	}
	if got := collisions(); got != 2 {
		t.Errorf("collisions = %d, want both collisions retried", got)
	}
}

func TestStartReadyKeepsACallerFixedPortCollision(t *testing.T) {
	ready, collisions := collideSetup(t, "--admin-port", startAttempts)
	d, err := startReady(t, []string{startCommand, "--admin-port", "1"}, ready)
	var collision *PortCollisionError
	if !errors.As(err, &collision) || collision.Reserved {
		t.Fatalf("err = %v, want a *PortCollisionError on a caller-fixed port", err)
	}
	var retried *StartReadyError
	if errors.As(err, &retried) {
		t.Errorf("err = %v, want no retry of a caller-fixed port", err)
	}
	if got := collisions(); got != 1 {
		t.Errorf("collisions = %d, want a single start", got)
	}
	if d == nil {
		t.Error("daemon = nil, want the failed daemon returned to the caller")
	}
}

func TestStartReadyGivesUpAfterThreeCollisions(t *testing.T) {
	ready, collisions := collideSetup(t, "--grpc-port", startAttempts+1)
	d, err := startReady(t, []string{startCommand}, ready)
	if d != nil {
		t.Errorf("daemon = %v, want none after every start collided", d)
	}
	var retried *StartReadyError
	if !errors.As(err, &retried) || len(retried.Attempts) != startAttempts {
		t.Fatalf("err = %v, want a *StartReadyError with %d attempts", err, startAttempts)
	}
	for i, attempt := range retried.Attempts {
		var collision *PortCollisionError
		if !errors.As(attempt, &collision) || !collision.Reserved {
			t.Errorf("attempt %d = %v, want a reserved-port *PortCollisionError", i+1, attempt)
		}
		if !strings.Contains(err.Error(), "--- attempt "+strconv.Itoa(i+1)+" ---") {
			t.Errorf("err does not list attempt %d", i+1)
		}
	}
	if got := collisions(); got != startAttempts {
		t.Errorf("collisions = %d, want %d starts", got, startAttempts)
	}
}

func TestStartReadyReturnsANonCollisionFailure(t *testing.T) {
	t.Setenv(helperEnv, "print")
	missing := filepath.Join(t.TempDir(), "missing")
	d, err := startReady(t, []string{startCommand}, func(d *Daemon) error { return d.WaitForFile(missing, time.Minute) })
	var exit *DaemonExitError
	if !errors.As(err, &exit) {
		t.Fatalf("err = %v, want the ready func's *DaemonExitError", err)
	}
	var retried *StartReadyError
	if errors.As(err, &retried) {
		t.Errorf("err = %v, want no retry", err)
	}
	if d == nil {
		t.Error("daemon = nil, want the failed daemon returned to the caller")
	}
}

func TestStartReadyReportsAStartFailure(t *testing.T) {
	d, err := StartReady(filepath.Join(t.TempDir(), "missing"), nil, func(*Daemon) error { return nil })
	var startErr *DaemonStartError
	if d != nil || !errors.As(err, &startErr) {
		t.Fatalf("daemon = %v, err = %v, want a *DaemonStartError", d, err)
	}
}

func TestPortCollisionClassifiesTheTail(t *testing.T) {
	reserved := []string{"24981", "31000"}
	const bind = "12:34:56 bind on 0.0.0.0:24981 failed: address already in use"
	tests := map[string]struct {
		tail      string
		collision bool
		reserved  bool
		port      string
	}{
		"no bind failure":    {tail: "12:34:56 starting\nshutting down\n"},
		"reserved port":      {tail: "starting\n" + bind + "\n", collision: true, reserved: true, port: "24981"},
		"caller-fixed port":  {tail: "listen tcp 0.0.0.0:8443: bind: address already in use\n", collision: true},
		"unidentified port":  {tail: "bind: address already in use\n", collision: true},
		"ipv6 reserved port": {tail: "listen udp [::]:31000: bind: address already in use", collision: true, reserved: true, port: "31000"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			waitErr := &DaemonWaitError{PID: 1, Err: &DaemonExitError{Op: "op", Target: "target"}, Tail: tt.tail}
			err := portCollision(waitErr, reserved)
			var collision *PortCollisionError
			if got := errors.As(err, &collision); got != tt.collision {
				t.Fatalf("err = %v, want collision %v", err, tt.collision)
			}
			if !tt.collision {
				if err != waitErr {
					t.Errorf("err = %v, want the wait error unchanged", err)
				}
				return
			}
			if collision.Reserved != tt.reserved || collision.Port != tt.port {
				t.Errorf("collision = {Port: %q, Reserved: %v}, want {%q, %v}", collision.Port, collision.Reserved, tt.port, tt.reserved)
			}
			if !errors.Is(err, waitErr) || !strings.Contains(err.Error(), tt.tail) {
				t.Errorf("err = %v, want it to wrap the wait error and its tail", err)
			}
		})
	}
}

func TestDaemonExitErrorNamesACleanExit(t *testing.T) {
	err := &DaemonExitError{Op: "WaitForFile", Target: "/x"}
	if want := "daemon exited before WaitForFile /x succeeded: exit status 0"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != nil {
		t.Errorf("Unwrap() = %v, want nil for a clean exit", err.Unwrap())
	}
}
