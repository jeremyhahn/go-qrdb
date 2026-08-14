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
// auth) per docs/08-bootstrap-auth-architecture.md §6.
package main

// CanonicalRedirectError is emitted when an operator invokes one of the
// renamed legacy command paths. The error names the deprecated path
// and the canonical replacement so operators can migrate their
// runbooks without guesswork.
type CanonicalRedirectError struct {
	Old string
	New string
}

// Error returns a human-readable description.
func (e *CanonicalRedirectError) Error() string {
	return "subcommand '" + e.Old +
		"' has been replaced by '" + e.New +
		"' (per docs/08-bootstrap-auth-architecture.md §6). Run: " + e.New
}

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

// PendingCredentialBackendError is emitted by "credential" subcommand
// handlers before the credential service surface ships in the SDK.
type PendingCredentialBackendError struct {
	Op string
}

// Error returns a human-readable description.
func (e *PendingCredentialBackendError) Error() string {
	return e.Op + " is not yet wired to the SDK CredentialService"
}
