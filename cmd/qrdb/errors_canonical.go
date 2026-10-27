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

// errors_canonical.go declares typed errors used by the canonical CLI
// surface (init / barrier / ceremony / ca / bootstrap / credential /
// auth) documented in each feature's docs/<feature>/cli.md.
package main

// PendingBootstrapBackendError is emitted by "bootstrap" subcommand
// handlers in qrdb. The TOFU implementation lives in xkms; qrdb
// exposes the command shape only so the canonical surface is
// identical across binaries.
type PendingBootstrapBackendError struct {
	Mode string
}

// Error returns a human-readable description.
func (e *PendingBootstrapBackendError) Error() string {
	return "bootstrap " + e.Mode + " is owned by xkms; invoke via xkmsctl bootstrap " + e.Mode + " or platformctl bootstrap " + e.Mode
}
