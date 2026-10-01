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
	"bytes"
	"errors"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// inspectionHardeningSupported reports that Linux has a mechanism.
const inspectionHardeningSupported = true

// inspectionMechanism names the mechanism in logs and errors.
const inspectionMechanism = "prctl(PR_SET_DUMPABLE, 0)"

// denyProcessInspection marks the process non-dumpable. The kernel then
// requires CAP_SYS_PTRACE for ptrace attach and for opening /proc/<pid>/mem,
// hands /proc/<pid>/* to root, and writes no core file.
func denyProcessInspection() error {
	return unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
}

// procSelfStatus is the file tracerPID reads.
const procSelfStatus = "/proc/self/status"

// errNoTracerPID is a status file with no parseable TracerPid line.
var errNoTracerPID = errors.New("secure: " + procSelfStatus + " has no parseable TracerPid line")

// tracerPID returns the PID of the process tracing this one, or 0, from the
// TracerPid line of /proc/self/status. A process can always read its own
// status file, even once it is non-dumpable.
func tracerPID() (int, error) {
	status, err := os.ReadFile(procSelfStatus)
	if err != nil {
		return 0, err
	}
	return parseTracerPID(status)
}

// parseTracerPID extracts the TracerPid value from a /proc/<pid>/status body.
// A missing line or a value that is not a non-negative integer is an error,
// never 0: "not traced" must be read, not assumed.
func parseTracerPID(status []byte) (int, error) {
	const key = "TracerPid:"
	for _, line := range bytes.Split(status, []byte("\n")) {
		rest, ok := bytes.CutPrefix(line, []byte(key))
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(string(bytes.TrimSpace(rest)))
		if err != nil || pid < 0 {
			return 0, errNoTracerPID
		}
		return pid, nil
	}
	return 0, errNoTracerPID
}
