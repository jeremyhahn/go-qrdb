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

package main

// secretBytes converts a caller-supplied secret that arrived as a Go string
// into wipeable bytes for a transport request.
//
// This is the ONE place in this command where a secret crosses from a string
// into bytes, and it is deliberately a single function so the residue is
// auditable rather than scattered across seven call sites.
//
// The residue: a cobra --passphrase flag and a Wails binding argument are
// both Go strings, produced by the flag parser and the JS bridge before any
// qrdb code runs. A Go string is immutable and therefore CANNOT be zeroized,
// so that copy lives on the heap until the collector happens to reuse the
// page. Nothing downstream of this function can remove it. (A --passphrase
// flag is also visible in the process table, which is the larger problem with
// that particular input and is why an operator should prefer a prompt.)
//
// What this function buys: from here down — the transport request types, the
// wire encoders, the daemon's handlers, the service layer, pkg/seal — the
// credential is bytes that CAN be zeroized, and every caller of this function
// wipes what it gets back. The residue is bounded to the one string the
// runtime handed us.
//
// Returns nil for an empty secret, so an omitted --passphrase produces an
// absent field rather than an empty non-nil slice.
func secretBytes(s string) []byte {
	if s == "" {
		return nil
	}
	return []byte(s)
}
