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

import "golang.org/x/sys/unix"

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
