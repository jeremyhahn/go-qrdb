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
	"strconv"
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

// EphemeralRangeError is returned when the kernel's ephemeral port range
// cannot be determined, which makes it impossible to reserve listen ports
// safely outside that range.
type EphemeralRangeError struct {
	Path   string
	Reason string
	Err    error
}

// Error returns a human-readable description.
func (e *EphemeralRangeError) Error() string {
	msg := errPrefix + "cannot determine kernel ephemeral port range from " + e.Path + ": " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *EphemeralRangeError) Unwrap() error { return e.Err }

// PortWindowError is returned when the kernel's ephemeral floor sits so low
// that the reserved allocation window would reach into the well-known port
// range below 1024.
type PortWindowError struct {
	EphemeralLow int
	Window       int
}

// Error returns a human-readable description.
func (e *PortWindowError) Error() string {
	return errPrefix + "ephemeral floor " + strconv.Itoa(e.EphemeralLow) +
		" leaves no room for a " + strconv.Itoa(e.Window) + "-port test window"
}

// PortExhaustedError is returned when every candidate in the reserved
// allocation window has already been handed out.
type PortExhaustedError struct {
	Window int
	Base   int
	Low    int
}

// Error returns a human-readable description.
func (e *PortExhaustedError) Error() string {
	return errPrefix + "exhausted " + strconv.Itoa(e.Window) + "-port window [" +
		strconv.Itoa(e.Base) + "," + strconv.Itoa(e.Low) + ")"
}

// PortReservationError is returned when fewer ports than requested could be
// bind-verified within the attempt budget.
type PortReservationError struct {
	Network   string
	Requested int
	Reserved  int
	LastErr   error
}

// Error returns a human-readable description.
func (e *PortReservationError) Error() string {
	msg := errPrefix + "could not reserve " + strconv.Itoa(e.Requested) + " unique " +
		e.Network + " ports (reserved " + strconv.Itoa(e.Reserved) + ")"
	if e.LastErr != nil {
		msg += ": last bind error: " + e.LastErr.Error()
	}
	return msg
}

// Unwrap returns the last bind failure, when one was recorded.
func (e *PortReservationError) Unwrap() error { return e.LastErr }

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

// TimeoutError is returned by the readiness waiters when the daemon does not
// reach the expected state before the deadline. LastErr, when non-nil, carries
// the most recent probe failure and is the useful diagnostic.
type TimeoutError struct {
	Op      string
	Target  string
	Timeout time.Duration
	LastErr error
}

// Error returns a human-readable description.
func (e *TimeoutError) Error() string {
	msg := errPrefix + e.Op + ": timed out after " + e.Timeout.String() + " waiting for " + e.Target
	if e.LastErr != nil {
		msg += " (last: " + e.LastErr.Error() + ")"
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
