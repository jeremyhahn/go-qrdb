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

import "github.com/mark3labs/mcp-go/mcp"

// secretArg reads a secret-bearing MCP tool argument as wipeable bytes.
//
// This is the ONE place in this package where a secret crosses from a Go
// string into bytes, and it is deliberately a single function so the residue
// is auditable rather than scattered.
//
// The residue: an MCP tool argument is a JSON string, and the JSON-RPC decode
// that produces it runs before any qrdb code does. mcp.ParseString hands back
// a Go string, which is immutable and therefore CANNOT be zeroized; that copy
// lives on the heap until the collector happens to reuse the page. Nothing
// this package can do removes it, because the tool schema is the agent-facing
// contract and the decoder owns the string.
//
// What this function does buy: everything downstream of it — the service
// layer, pkg/seal, the sealing strategies — takes wipeable bytes, and every
// caller here wipes what it gets back. So the residue is bounded to the one
// decoder string on this one transport. Every other transport in the stack
// (REST, gRPC, QUIC, Unix, embedded) carries the barrier credential as bytes
// end to end and has no residue at all.
//
// Returns nil for an absent or empty argument, matching the "empty secret is
// the none strategy" convention the barrier tools rely on.
func secretArg(req mcp.CallToolRequest, name string) []byte {
	s := mcp.ParseString(req, name, "")
	if s == "" {
		return nil
	}
	return []byte(s)
}
