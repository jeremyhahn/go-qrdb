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
	"strconv"
	"strings"
	"time"
)

// errPrefix identifies this package in every error string it produces.
const errPrefix = "qrdbdharness: "

// CAError is returned when minting or persisting the harness certificate
// authority fails. Op names the failing step (for example "generate key").
type CAError struct {
	Op  string
	Err error
}

// Error returns a human-readable description.
func (e *CAError) Error() string {
	return errPrefix + "certificate authority: " + e.Op + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CAError) Unwrap() error { return e.Err }

// CertError is returned when issuing or persisting a leaf certificate fails.
// CommonName identifies the certificate being issued.
type CertError struct {
	CommonName string
	Op         string
	Err        error
}

// Error returns a human-readable description.
func (e *CertError) Error() string {
	return errPrefix + "issue certificate " + strconv.Quote(e.CommonName) + ": " + e.Op + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CertError) Unwrap() error { return e.Err }

// InvalidSubjectError is returned when a requested certificate subject cannot
// be used, either because it is empty or because it would not produce a usable
// on-disk file name.
type InvalidSubjectError struct {
	CommonName string
	Reason     string
}

// Error returns a human-readable description.
func (e *InvalidSubjectError) Error() string {
	return errPrefix + "invalid certificate subject " + strconv.Quote(e.CommonName) + ": " + e.Reason
}

// RepoRootError is returned when the go-qrdb repository root cannot be located
// by walking up from the working directory.
type RepoRootError struct {
	Start string
	Err   error
}

// Error returns a human-readable description.
func (e *RepoRootError) Error() string {
	msg := errPrefix + "no go-qrdb repository root (a directory containing cmd/qrdbd) at or above " + e.Start
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *RepoRootError) Unwrap() error { return e.Err }

// BuildError is returned when compiling a binary from the repository fails.
// Output carries the combined compiler output.
type BuildError struct {
	Package string
	Tags    string
	Output  string
	Err     error
}

// Error returns a human-readable description.
func (e *BuildError) Error() string {
	msg := errPrefix + "go build " + e.Package + " (tags " + strconv.Quote(e.Tags) + ")"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	if e.Output != "" {
		msg += "\n" + e.Output
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *BuildError) Unwrap() error { return e.Err }

// BuildCleanupError is returned when RemoveBuilds cannot remove the directory
// holding a compiled binary.
type BuildCleanupError struct {
	Dir string
	Err error
}

// Error returns a human-readable description.
func (e *BuildCleanupError) Error() string {
	return errPrefix + "remove build directory " + e.Dir + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *BuildCleanupError) Unwrap() error { return e.Err }

// DaemonStartError is returned when the qrdbd subprocess cannot be started.
type DaemonStartError struct {
	Binary string
	Err    error
}

// Error returns a human-readable description.
func (e *DaemonStartError) Error() string {
	return errPrefix + "start " + e.Binary + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *DaemonStartError) Unwrap() error { return e.Err }

// DaemonSignalError is returned when a running daemon cannot be signalled.
type DaemonSignalError struct {
	Signal string
	Err    error
}

// Error returns a human-readable description.
func (e *DaemonSignalError) Error() string {
	return errPrefix + "signal daemon with " + e.Signal + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *DaemonSignalError) Unwrap() error { return e.Err }

// DaemonWaitError is returned when a daemon has not exited by the time a
// bounded wait for it ends, and by a Daemon readiness wait when the daemon
// exits before it is ready; Err is then a *DaemonExitError. Tail is the last
// of the daemon's own output, which is usually what says why it is still
// running, or why it stopped.
type DaemonWaitError struct {
	PID  int
	Err  error
	Tail string
}

// Error returns a human-readable description that ends with the output tail.
func (e *DaemonWaitError) Error() string {
	state := " has not exited: "
	var exit *DaemonExitError
	if errors.As(e.Err, &exit) {
		state = " exited: "
	}
	return errPrefix + "daemon pid " + strconv.Itoa(e.PID) + state + e.Err.Error() +
		"\n--- daemon output tail ---\n" + e.Tail
}

// Unwrap returns the underlying error.
func (e *DaemonWaitError) Unwrap() error { return e.Err }

// DaemonExitError is why a Daemon readiness wait ended early: the daemon
// exited before Op on Target succeeded. Status is the process's exit status,
// nil for a clean exit.
type DaemonExitError struct {
	Op     string
	Target string
	Status error
}

// Error returns a human-readable description.
func (e *DaemonExitError) Error() string {
	status := "exit status 0"
	if e.Status != nil {
		status = e.Status.Error()
	}
	return "daemon exited before " + e.Op + " " + e.Target + " succeeded: " + status
}

// Unwrap returns the exit status.
func (e *DaemonExitError) Unwrap() error { return e.Status }

// PortCollisionError is returned by a Daemon readiness wait when the daemon
// exited because a listener port was already in use. Reserved reports that
// Port is one ReserveListenerPorts drew, so another process bound it between
// the reservation and the daemon's bind and StartReady may draw again; Port
// is empty when the failing port is not one of those -- a port the caller's
// args fixed.
type PortCollisionError struct {
	Port     string
	Reserved bool
	Err      *DaemonWaitError
}

// Error returns a human-readable description that ends with the output tail.
func (e *PortCollisionError) Error() string {
	port := "a port the caller fixed"
	if e.Reserved {
		port = "reserved port " + e.Port
	}
	return errPrefix + "listener bind failed on " + port + ": " + e.Err.Error()
}

// Unwrap returns the underlying *DaemonWaitError.
func (e *PortCollisionError) Unwrap() error { return e.Err }

// StartReadyError is returned by StartReady once it has retried a start and
// still failed. Attempts holds every attempt's error, in order.
type StartReadyError struct {
	Attempts []error
}

// Error returns a human-readable description listing every attempt.
func (e *StartReadyError) Error() string {
	var b strings.Builder
	b.WriteString(errPrefix + "daemon not ready after " + strconv.Itoa(len(e.Attempts)) + " starts")
	for i, err := range e.Attempts {
		b.WriteString("\n--- attempt " + strconv.Itoa(i+1) + " ---\n" + err.Error())
	}
	return b.String()
}

// Unwrap returns every attempt's error.
func (e *StartReadyError) Unwrap() []error { return e.Attempts }

// TimeoutError is returned by the readiness waiters when the daemon does not
// reach the expected state before the deadline. LastErr, when non-nil, carries
// the most recent probe failure and is the useful diagnostic.
type TimeoutError struct {
	Op      string
	Target  string
	Timeout time.Duration
	LastErr error
	// StackDumped lists the daemons sent SIGQUIT when the wait gave up;
	// each one's goroutine dump is in its output.
	StackDumped []int
}

// Error returns a human-readable description.
func (e *TimeoutError) Error() string {
	msg := errPrefix + e.Op + ": timed out after " + e.Timeout.String() + " waiting for " + e.Target
	if e.LastErr != nil {
		msg += " (last: " + e.LastErr.Error() + ")"
	}
	for _, pid := range e.StackDumped {
		msg += "; sent SIGQUIT to daemon pid " + strconv.Itoa(pid) + ", its goroutine dump is in its output"
	}
	return msg
}

// Unwrap returns the most recent probe failure.
func (e *TimeoutError) Unwrap() error { return e.LastErr }

// ProbeError describes a single failed readiness probe. It is only ever
// surfaced as the LastErr of a TimeoutError.
type ProbeError struct {
	Reason string
	Status int
	Body   string
}

// Error returns a human-readable description.
func (e *ProbeError) Error() string {
	msg := e.Reason
	if e.Status != 0 {
		msg += ": status " + strconv.Itoa(e.Status)
	}
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

// RelayError is returned when a UDPRelay cannot be set up, and by
// UDPRelay.Close when its front socket failed while the relay was serving.
// Op names the failing step and Addr the address it concerned.
type RelayError struct {
	Op   string
	Addr string
	Err  error
}

// Error returns a human-readable description.
func (e *RelayError) Error() string {
	return errPrefix + "udp relay: " + e.Op + " " + e.Addr + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *RelayError) Unwrap() error { return e.Err }
