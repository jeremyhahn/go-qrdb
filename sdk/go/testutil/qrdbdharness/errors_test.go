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
	"strings"
	"testing"
	"time"
)

// cause is the sentinel every wrapping error type is tested against.
var cause = errors.New("underlying failure")

func TestErrorMessages(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		contains []string
	}{
		{
			name:     "CAError",
			err:      &CAError{Op: "write cert", Err: cause},
			contains: []string{"certificate authority", "write cert", cause.Error()},
		},
		{
			name:     "CertError",
			err:      &CertError{CommonName: "admin", Op: "marshal key", Err: cause},
			contains: []string{`"admin"`, "marshal key", cause.Error()},
		},
		{
			name:     "InvalidSubjectError",
			err:      &InvalidSubjectError{CommonName: "a/b", Reason: "must not contain a path separator"},
			contains: []string{`"a/b"`, "must not contain a path separator"},
		},
		{
			name:     "EphemeralRangeError with cause",
			err:      &EphemeralRangeError{Path: "/proc/x", Reason: "read failed", Err: cause},
			contains: []string{"/proc/x", "read failed", cause.Error()},
		},
		{
			name:     "EphemeralRangeError without cause",
			err:      &EphemeralRangeError{Path: "/proc/x", Reason: "expected two fields, got 1"},
			contains: []string{"/proc/x", "expected two fields, got 1"},
		},
		{
			name:     "PortWindowError",
			err:      &PortWindowError{EphemeralLow: 2048, Window: portWindow},
			contains: []string{"2048", "8192"},
		},
		{
			name:     "PortExhaustedError",
			err:      &PortExhaustedError{Window: portWindow, Base: 24576, Low: 32768},
			contains: []string{"[24576,32768)"},
		},
		{
			name:     "PortReservationError with cause",
			err:      &PortReservationError{Network: "TCP", Requested: 3, Reserved: 1, LastErr: cause},
			contains: []string{"TCP", "3", "1", cause.Error()},
		},
		{
			name:     "PortReservationError without cause",
			err:      &PortReservationError{Network: "UDP", Requested: 2, Reserved: 0},
			contains: []string{"UDP", "2"},
		},
		{
			name:     "RepoRootError with cause",
			err:      &RepoRootError{Start: "/tmp/x", Err: cause},
			contains: []string{"/tmp/x", cause.Error()},
		},
		{
			name:     "RepoRootError without cause",
			err:      &RepoRootError{Start: "/tmp/x"},
			contains: []string{"cmd/qrdbd", "/tmp/x"},
		},
		{
			name:     "BuildError",
			err:      &BuildError{Package: "./cmd/qrdbd", Tags: DefaultBuildTags, Output: "syntax error", Err: cause},
			contains: []string{"./cmd/qrdbd", "raft", "syntax error", cause.Error()},
		},
		{
			name:     "BuildError without output",
			err:      &BuildError{Package: "./cmd/qrdbd", Tags: "raft"},
			contains: []string{"./cmd/qrdbd", `"raft"`},
		},
		{
			name:     "DaemonStartError",
			err:      &DaemonStartError{Binary: "/tmp/qrdbd", Err: cause},
			contains: []string{"/tmp/qrdbd", cause.Error()},
		},
		{
			name:     "DaemonSignalError",
			err:      &DaemonSignalError{Signal: "interrupt", Err: cause},
			contains: []string{"interrupt", cause.Error()},
		},
		{
			name:     "TimeoutError with cause",
			err:      &TimeoutError{Op: "WaitForLeader", Target: "https://host/status", Timeout: 90 * time.Second, LastErr: cause},
			contains: []string{"WaitForLeader", "https://host/status", "1m30s", cause.Error()},
		},
		{
			name:     "TimeoutError without cause",
			err:      &TimeoutError{Op: "WaitForFile", Target: "/data/host.id", Timeout: time.Second},
			contains: []string{"WaitForFile", "/data/host.id", "1s"},
		},
		{
			name:     "ProbeError with status and body",
			err:      &ProbeError{Reason: "unexpected status", Status: 403, Body: "denied"},
			contains: []string{"unexpected status", "403", "denied"},
		},
		{
			name:     "ProbeError bare",
			err:      &ProbeError{Reason: "no shard reports is_leader"},
			contains: []string{"no shard reports is_leader"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.err.Error()
			for _, want := range tc.contains {
				if !strings.Contains(msg, want) {
					t.Errorf("Error() = %q, want it to contain %q", msg, want)
				}
			}
			if !strings.HasPrefix(msg, errPrefix) && !strings.HasPrefix(tc.name, "ProbeError") {
				t.Errorf("Error() = %q, want it to identify the package with %q", msg, errPrefix)
			}
		})
	}
}

func TestErrorUnwrapping(t *testing.T) {
	wrapping := []error{
		&CAError{Op: "write cert", Err: cause},
		&CertError{CommonName: "admin", Op: "write key", Err: cause},
		&EphemeralRangeError{Path: "/proc/x", Reason: "read failed", Err: cause},
		&PortReservationError{Network: "TCP", Requested: 1, LastErr: cause},
		&RepoRootError{Start: "/tmp/x", Err: cause},
		&BuildError{Package: "./cmd/qrdbd", Err: cause},
		&DaemonStartError{Binary: "/tmp/qrdbd", Err: cause},
		&DaemonSignalError{Signal: "kill", Err: cause},
		&TimeoutError{Op: "WaitForHTTPOK", Target: "https://host/health", LastErr: cause},
	}
	for _, err := range wrapping {
		if !errors.Is(err, cause) {
			t.Errorf("%T does not unwrap to its cause", err)
		}
	}

	notWrapping := []error{
		&InvalidSubjectError{CommonName: "", Reason: "empty"},
		&PortWindowError{EphemeralLow: 2048, Window: portWindow},
		&PortExhaustedError{Window: portWindow, Base: 1024, Low: 9216},
		&ProbeError{Reason: "request failed"},
	}
	for _, err := range notWrapping {
		if errors.Is(err, cause) {
			t.Errorf("%T unexpectedly unwraps to the sentinel", err)
		}
	}
}
