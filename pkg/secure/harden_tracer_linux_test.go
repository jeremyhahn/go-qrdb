// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build linux

package secure

import (
	"errors"
	"testing"
)

// TestParseTracerPID covers every shape the status file can take. A missing or
// malformed line is an error, never "not traced".
func TestParseTracerPID(t *testing.T) {
	cases := []struct {
		name   string
		status string
		want   int
		isErr  bool
	}{
		{name: "not traced", status: "Name:\tqrdbd\nTracerPid:\t0\nUid:\t1000\n", want: 0},
		{name: "traced", status: "Name:\tqrdbd\nTracerPid:\t4242\n", want: 4242},
		{name: "spaces", status: "TracerPid:   17  \n", want: 17},
		{name: "missing line", status: "Name:\tqrdbd\nUid:\t1000\n", isErr: true},
		{name: "not a number", status: "TracerPid:\tabc\n", isErr: true},
		{name: "negative", status: "TracerPid:\t-1\n", isErr: true},
		{name: "empty", status: "", isErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTracerPID([]byte(tc.status))
			if tc.isErr {
				if !errors.Is(err, errNoTracerPID) {
					t.Fatalf("parseTracerPID = %d, %v; want errNoTracerPID", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("parseTracerPID = %d, %v; want %d, nil", got, err, tc.want)
			}
		})
	}
}

// TestTracerPID_ReadsThisProcess reads the real status file of the test
// process, which no test harness traces.
func TestTracerPID_ReadsThisProcess(t *testing.T) {
	pid, err := tracerPID()
	if err != nil {
		t.Fatalf("tracerPID: %v", err)
	}
	if pid != 0 {
		t.Fatalf("the test process reports TracerPid %d; it must be run untraced", pid)
	}
}
