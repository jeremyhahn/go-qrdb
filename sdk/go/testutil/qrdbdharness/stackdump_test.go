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

//go:build unix

package qrdbdharness

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// helperReadyTimeout bounds how long a helper may take to write its first
// line. Under GODEBUG=fips140=only with -race, the first crypto/rand read in
// package init seeds the DRBG from the jitter entropy source, which takes
// seconds on a loaded runner.
const helperReadyTimeout = 30 * time.Second

// lockedBuffer is a bytes.Buffer safe for exec's copy goroutine to write
// while a test reads it.
type lockedBuffer struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	grew chan struct{} // signalled, without blocking, after every Write
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	select {
	case b.grewLocked() <- struct{}{}:
	default:
	}
	return n, err
}

// grewLocked returns the channel Write signals, creating it on first use.
// b.mu must be held. Its one-slot buffer keeps a Write that lands between a
// waiter's check and its receive from being lost.
func (b *lockedBuffer) grewLocked() chan struct{} {
	if b.grew == nil {
		b.grew = make(chan struct{}, 1)
	}
	return b.grew
}

// waitFor blocks until the buffer holds needle, and fails the test if it does
// not within limit.
func (b *lockedBuffer) waitFor(t *testing.T, needle string, limit time.Duration) {
	t.Helper()
	deadline := time.After(limit)
	for {
		b.mu.Lock()
		found := strings.Contains(b.buf.String(), needle)
		grew := b.grewLocked()
		b.mu.Unlock()
		if found {
			return
		}
		select {
		case <-grew:
		case <-deadline:
			t.Fatalf("helper did not write %q within %v; output:\n%s", needle, limit, b.String())
		}
	}
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startPortHelper launches this test binary in the given helper mode with an
// argument naming port, as a qrdbd command line names its listeners.
func startPortHelper(t *testing.T, mode string, port int, out *lockedBuffer) *Daemon {
	t.Helper()
	t.Setenv(helperEnv, mode)
	daemon, err := Start(os.Args[0], []string{"--admin-port=" + strconv.Itoa(port)}, WithOutput(out))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = daemon.Kill() })
	return daemon
}

func reservePort(t *testing.T) int {
	t.Helper()
	ports, err := ReserveTCPPorts(1)
	if err != nil {
		t.Fatalf("ReserveTCPPorts: %v", err)
	}
	return ports[0]
}

func healthURL(port int) string {
	return "https://127.0.0.1:" + strconv.Itoa(port) + "/health"
}

func TestWaitForHTTPOKDumpsStalledDaemon(t *testing.T) {
	port := reservePort(t)
	var out lockedBuffer
	// The booting helper writes its line from runHelper, so once the line is
	// in the output the helper is past package init and runHelper is on the
	// stack the dump must show. Without the wait, a SIGQUIT sent during a
	// slow init dumps only init's frames.
	daemon := startPortHelper(t, "booting", port, &out)
	out.waitFor(t, "booting\n", helperReadyTimeout)

	err := WaitForHTTPOK(healthURL(port), nil, nil, shortTimeout)
	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %v, want *TimeoutError", err)
	}
	pid := daemon.Cmd().Process.Pid
	if !slices.Equal(timeoutErr.StackDumped, []int{pid}) {
		t.Fatalf("StackDumped = %v, want [%d]", timeoutErr.StackDumped, pid)
	}
	if !strings.Contains(err.Error(), "SIGQUIT") {
		t.Errorf("error %q does not say the daemon was sent SIGQUIT", err)
	}
	// The dump is in the output by the time WaitForHTTPOK returns, not
	// merely on its way: the caller fails the test straight after.
	dump := out.String()
	for _, want := range []string{"SIGQUIT", "goroutine ", "runHelper"} {
		if !strings.Contains(dump, want) {
			t.Errorf("daemon output lacks %q:\n%s", want, dump)
		}
	}
	if err := daemon.Wait(); err == nil {
		t.Error("Wait returned nil for a daemon that exited on SIGQUIT")
	}
}

func TestWaitForHTTPOKLeavesOtherDaemonsAlone(t *testing.T) {
	waited, other := reservePort(t), reservePort(t)
	var out lockedBuffer
	daemon := startPortHelper(t, "sleep", other, &out)

	err := WaitForHTTPOK(healthURL(waited), nil, nil, shortTimeout)
	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %v, want *TimeoutError", err)
	}
	if len(timeoutErr.StackDumped) != 0 {
		t.Fatalf("StackDumped = %v, want none: no daemon names port %d", timeoutErr.StackDumped, waited)
	}
	if err := daemon.Cmd().Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("daemon on port %d is gone after a wait on port %d: %v", other, waited, err)
	}
}

func TestDumpStacksPrunesExitedDaemons(t *testing.T) {
	tests := []struct {
		name string
		wait func(*Daemon) error
	}{
		// Reaped through the harness, which drops it straight away.
		{name: "harness wait", wait: (*Daemon).Wait},
		// Reaped by a test that drives the process itself, as the
		// integration suites' startNode does; dumpStacks drops it when the
		// signal reports the process done.
		{name: "caller wait", wait: func(d *Daemon) error { return d.Cmd().Wait() }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			port := reservePort(t)
			var out lockedBuffer
			daemon := startPortHelper(t, "print", port, &out)
			if err := tc.wait(daemon); err != nil {
				t.Fatalf("wait: %v", err)
			}
			if pids := dumpStacks(healthURL(port)); len(pids) != 0 {
				t.Fatalf("dumpStacks signalled %v, want no exited daemon", pids)
			}
			if _, live := spawned.Load(daemon); live {
				t.Error("an exited daemon is still registered")
			}
		})
	}
}

func TestDumpStacksIgnoresURLWithoutPort(t *testing.T) {
	for _, raw := range []string{"https://127.0.0.1/health", "://bad"} {
		if pids := dumpStacks(raw); pids != nil {
			t.Errorf("dumpStacks(%q) = %v, want nil", raw, pids)
		}
	}
}

func TestNamesPort(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "flag value", args: []string{"--admin-port=24911"}, want: true},
		{name: "separate value", args: []string{"--admin-port", "24911"}, want: true},
		{name: "address", args: []string{"--rest-listen=127.0.0.1:24911"}, want: true},
		{name: "list element", args: []string{"--initial-members=1=127.0.0.1:24900,2=127.0.0.1:24911"}, want: true},
		{name: "prefix only", args: []string{"--admin-port=249110"}, want: false},
		{name: "suffix only", args: []string{"--admin-port=124911"}, want: false},
		{name: "absent", args: []string{"start", "--replica-id=3"}, want: false},
		{name: "no args", args: nil, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := namesPort(tc.args, "24911"); got != tc.want {
				t.Errorf("namesPort(%q) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestOutputWriter(t *testing.T) {
	var buf bytes.Buffer
	o := newOutputWriter(&buf)
	if _, err := o.Write([]byte("a")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	select {
	case <-o.drained:
		t.Fatal("drained before the stream ended")
	default:
	}
	n, err := o.ReadFrom(strings.NewReader("bc"))
	if err != nil || n != 2 {
		t.Fatalf("ReadFrom = %d, %v; want 2, nil", n, err)
	}
	select {
	case <-o.drained:
	default:
		t.Fatal("not drained after ReadFrom reached EOF")
	}
	if buf.String() != "abc" {
		t.Errorf("output = %q, want abc", buf.String())
	}
}
