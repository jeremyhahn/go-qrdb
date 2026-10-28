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

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// secretB64Suffix names the base64 sibling of a secret-bearing argument:
// "secret" travels as "secret" (text) or "secret_b64" (base64). It is the
// <name>_b64 convention the user auth-factor tools' credential_b64 set.
const secretB64Suffix = "_b64"

// secretArg reads a secret-bearing MCP tool argument as wipeable bytes.
//
// The argument has two forms. name carries a UTF-8 secret as text. name_b64
// carries the same secret as standard base64 (RFC 4648 §4, padded), the only
// form in which arbitrary bytes survive: a JSON string cannot carry invalid
// UTF-8, and an encoder replaces it with U+FFFD rather than refusing it. A
// caller sends one form or neither; sending both is refused with
// *SecretArgumentConflictError, never resolved in favour of either, and a
// name_b64 that is not valid base64 is refused with *ValueDecodeError.
//
// This is the ONE place in this package where a secret crosses from a Go
// string into bytes, and it is deliberately a single function so the residue
// is auditable rather than scattered.
//
// The residue: an MCP tool argument is a JSON string, and the JSON-RPC decode
// that produces it runs before any qrdb code does. The decoder hands back
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
// Returns nil for an absent or empty argument in either form, matching the
// "empty secret is the none strategy" convention the barrier tools rely on.
// The decoder string residue below applies to both forms alike (name_b64's
// string is the secret, encoded); the decoded bytes are the caller's to wipe.
//
// Either form present with a value that is not a JSON string is refused with
// *ToolArgumentError before anything is decoded (see stringArg).
func secretArg(req mcp.CallToolRequest, name string) ([]byte, error) {
	b64Name := name + secretB64Suffix
	s, hasText, err := stringArg(req, name)
	if err != nil {
		return nil, err
	}
	encoded, hasB64, err := stringArg(req, b64Name)
	if err != nil {
		return nil, err
	}
	if hasText && hasB64 {
		return nil, &SecretArgumentConflictError{Argument: name}
	}
	if hasB64 {
		b, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			secure.Wipe(b)
			return nil, &ValueDecodeError{Field: b64Name, Err: err}
		}
		if len(b) == 0 {
			return nil, nil
		}
		return b, nil
	}
	if s == "" {
		return nil, nil
	}
	return []byte(s), nil
}

// stringArg reads a string-typed tool argument, reporting whether it was
// present. A present value that is not a JSON string — an array, object,
// number, boolean or null — is refused with *ToolArgumentError.
//
// It exists because mcp.ParseString coerces instead of refusing: a number or
// boolean becomes its text, and an array or object becomes "". For a secret,
// a PIN or a credential that coercion is a security defect, not a
// convenience — "" means "no secret", so a malformed PIN would read as an
// absent one and an initialization would go ahead without it. Every
// secret-, PIN- or credential-bearing argument in this package is read
// through this function rather than mcp.ParseString.
func stringArg(req mcp.CallToolRequest, name string) (value string, present bool, err error) {
	raw, present := req.GetArguments()[name]
	if !present {
		return "", false, nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", true, &ToolArgumentError{Argument: name, Reason: "must be a string"}
	}
	return s, true, nil
}

// pinArg reads a PIN argument through secretArg, so a PIN travels in the
// same two forms a barrier secret does. The PIN backends take a string, so
// the decoded bytes are wiped once they have been copied into one.
func pinArg(req mcp.CallToolRequest, name string) (string, error) {
	b, err := secretArg(req, name)
	if err != nil {
		return "", err
	}
	defer secure.Wipe(b)
	return string(b), nil
}

// initializePINArgs reads the optional so_pin and user_pin arguments the
// initializing tools provision into the PIN backend.
func initializePINArgs(req mcp.CallToolRequest) (soPIN, userPIN string, err error) {
	if soPIN, err = pinArg(req, "so_pin"); err != nil {
		return "", "", err
	}
	if userPIN, err = pinArg(req, "user_pin"); err != nil {
		return "", "", err
	}
	return soPIN, userPIN, nil
}

// withSecretArg declares a secret-bearing argument in both of the forms
// secretArg reads: name, the text form, and name_b64, its standard base64
// sibling. Every secret argument is declared through it, so the two forms
// and their descriptions cannot drift apart from tool to tool.
func withSecretArg(name, description string) mcp.ToolOption {
	text := mcp.WithString(name,
		mcp.Description(description+" Text form, for a UTF-8 secret; mutually exclusive with "+name+secretB64Suffix+"."),
	)
	b64 := mcp.WithString(name+secretB64Suffix,
		mcp.Description("The same value as "+name+", standard base64 (RFC 4648 §4, padded), so arbitrary bytes travel losslessly. Mutually exclusive with "+name+"."),
	)
	return func(t *mcp.Tool) {
		text(t)
		b64(t)
	}
}
