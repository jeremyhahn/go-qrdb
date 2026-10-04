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

package middleware

// AwaitingAdminCredentialError is returned by every transport's
// awaiting-admin-credential gate (go-qrdb#548 review item 3) when a node is
// in that boot state -- auth.mtls_binding_policy is "required" and the
// bootstrap admin holds no live certificate binding -- and the request
// names a verb outside the two the node still serves there: ceremony
// status, and ceremony-initialize (which CeremonyServiceWithAdminRebind
// treats as the admin-credential retry once the ceremony is already
// initialized).
//
// The node is not locked up: re-run `qrdb init` against the Raft leader
// with --admin-csr, --admin-cert or --admin-generate to bind a certificate,
// after which the gate opens and the node serves normally -- no restart
// required. ClassifyAuthError maps this to AuthFailureUnavailable: neither
// the caller's credential nor a grant failed, so retry (once an admin
// certificate binds) is the honest instruction, the same shape as every
// other AuthFailureUnavailable member.
type AwaitingAdminCredentialError struct{}

// Error implements error.
func (e *AwaitingAdminCredentialError) Error() string {
	return `auth: node is awaiting admin credential (auth.mtls_binding_policy is "required" and the bootstrap admin holds no live certificate binding); ` +
		`re-run "qrdb init" against the Raft leader with --admin-csr, --admin-cert or --admin-generate to bind one`
}
