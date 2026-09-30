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

//go:build !linux && !darwin

package secure

// inspectionHardeningSupported reports that this platform has no mechanism.
// Windows has no per-process equivalent of PR_SET_DUMPABLE or PT_DENY_ATTACH:
// any process holding PROCESS_VM_READ on this one can read its memory, and
// that right is governed by the process's security descriptor and the reader's
// privileges, not by anything this process can switch off for itself.
const inspectionHardeningSupported = false

// inspectionMechanism names the (absent) mechanism in logs.
const inspectionMechanism = "none"

// denyProcessInspection is never reached on this platform: HardenProcess
// checks inspectionHardeningSupported first and logs instead.
func denyProcessInspection() error { return nil }
