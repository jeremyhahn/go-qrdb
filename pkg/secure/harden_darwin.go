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

//go:build darwin

package secure

import "golang.org/x/sys/unix"

// inspectionHardeningSupported reports that macOS has a mechanism.
const inspectionHardeningSupported = true

// inspectionMechanism names the mechanism in logs and errors.
const inspectionMechanism = "ptrace(PT_DENY_ATTACH)"

// denyProcessInspection refuses every later debugger attach. The kernel
// terminates a process that is already being traced when this runs, which is
// the case AllowProcessInspectionEnv exists for.
func denyProcessInspection() error {
	return unix.PtraceDenyAttach()
}
