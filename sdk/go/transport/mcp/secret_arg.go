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

package mcp

import (
	"encoding/base64"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// secretB64Suffix names the base64 form of a secret-bearing tool argument:
// the server reads "secret" as text and "secret_b64" as standard base64
// (RFC 4648 §4, padded), and refuses a call carrying both.
const secretB64Suffix = "_b64"

// putSecret sets the secret-bearing argument name in args, always in its
// base64 form, name_b64, and removes any text form a generic encoding put
// there (the server refuses both together).
//
// This is the ONE place in this transport where a secret crosses from bytes
// back into a string, and it is deliberately a single function so the residue
// is auditable rather than scattered.
//
// Base64 is what makes the call lossless. MCP tool arguments are a JSON
// object, and a JSON string cannot carry invalid UTF-8: encoding/json
// replaces it with U+FFFD, silently, so a secret sent as text arrives as
// different bytes whenever it is not UTF-8. Its base64 encoding is ASCII and
// arrives exactly.
//
// The residue: the encoded Go string is immutable and therefore CANNOT be
// zeroized; it lives on the heap until the collector happens to reuse the
// page. MCP is the only transport in this SDK with this residue. REST, gRPC,
// QUIC, Unix and embedded all carry the barrier credential as bytes end to
// end, because their wire formats have a byte-string type and MCP's does not.
// Callers who cannot accept the residue should not select this transport for
// barrier operations.
//
// The caller's own []byte remains wipeable and every caller of this function
// still wipes it, so the residue is bounded to the string set here.
func putSecret(args map[string]any, name string, b []byte) {
	delete(args, name)
	args[name+secretB64Suffix] = base64.StdEncoding.EncodeToString(b)
}

// putStringSecret sets a string-typed secret argument -- a PIN, a database
// unseal secret -- through putSecret, so a secret that is not UTF-8 arrives
// as the bytes the caller holds rather than as U+FFFD.
func putStringSecret(args map[string]any, name, secret string) {
	b := []byte(secret)
	putSecret(args, name, b)
	secure.Wipe(b)
}
