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

package quic

import "github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"

// awaitingAdminCredentialProbe is the abstraction the server needs to know
// whether the node is currently "awaiting admin credential" (go-qrdb#548
// review item 3). Defined as an interface, exactly like sealedWindowProbe,
// so tests can substitute a deterministic fake.
type awaitingAdminCredentialProbe interface {
	AwaitingAdminCredential() bool
}

// AwaitingAdminCredentialAllowlist is the set of native QUIC method names
// that remain reachable while the node is awaiting admin credential.
// Derived from the canonical transport-neutral verb table so it cannot
// drift from the other transports.
var AwaitingAdminCredentialAllowlist = sealedwindow.QUICAwaitingAdminCredential()

// awaitingAdminCredentialAllowed reports whether method remains reachable
// while the node is awaiting admin credential.
func awaitingAdminCredentialAllowed(method string) bool {
	_, ok := AwaitingAdminCredentialAllowlist[method]
	return ok
}
