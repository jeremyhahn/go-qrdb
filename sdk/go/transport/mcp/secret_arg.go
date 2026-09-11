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

// secretArg renders a wipeable secret as the Go string an MCP tool argument
// must be.
//
// This is the ONE place in this transport where a secret crosses from bytes
// back into a string, and it is deliberately a single function so the residue
// is auditable rather than scattered.
//
// The residue: MCP tool arguments are a JSON object, and this transport builds
// it as map[string]any. Putting a []byte in that map does NOT produce the same
// wire value — encoding/json base64-encodes a []byte — and the server's tool
// schema declares these arguments as strings, so the conversion has to be
// explicit and has to happen here. The resulting Go string is immutable and
// therefore CANNOT be zeroized; it lives on the heap until the collector
// happens to reuse the page.
//
// MCP is the only transport in this SDK with this residue. REST, gRPC, QUIC,
// Unix and embedded all carry the barrier credential as bytes end to end,
// because their wire formats have a byte-string type and MCP's does not.
// Callers who cannot accept the residue should not select this transport for
// barrier operations.
//
// The caller's own []byte remains wipeable and every caller of this function
// still wipes it, so the residue is bounded to the string returned here.
func secretArg(b []byte) string {
	return string(b)
}
