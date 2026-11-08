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
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.CeremonyClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// CeremonyClient implementation
// ---------------------------------------------------------------------------

// CeremonyInitialize starts the initialization ceremony by calling the
// qrdb_ceremony_initialize MCP tool. Request parameters are passed as tool
// arguments for forward compatibility.
func (t *Transport) CeremonyInitialize(ctx context.Context, req *transport.CeremonyInitializeRequest) (*transport.CeremonyInitializeResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	ctx, err := t.guardSecret(ctx, req)
	if err != nil {
		return nil, err
	}
	// The SO PIN travels in the request's own so_pin field too, which the
	// server reads; it comes from the PIN bound by transport.WithSOPIN when
	// the request carries none, exactly as on QUIC.
	soPIN, err := transport.ResolveSOPIN(ctx, "CeremonyInitialize", req.SOPin)
	if err != nil {
		return nil, err
	}

	args, err := wire.EncodeArgs(req, wire.CeremonyJSONFields...)
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonyInitialize", Err: err}
	}
	// The secret-bearing fields are rewritten here, not left to EncodeArgs's
	// generic struct encoding, which renders the PINs as text and req.Secret
	// ([]byte) as an untagged base64 string the server reads as text. Each
	// travels as its <name>_b64 argument instead, so arbitrary bytes arrive
	// exactly (go-qrdb#1130).
	delete(args, "so_pin")
	delete(args, "user_pin")
	if soPIN != "" {
		putStringSecret(args, "so_pin", soPIN)
	}
	if req.UserPin != "" {
		putStringSecret(args, "user_pin", req.UserPin)
	}
	putSecret(args, "secret", req.Secret)

	text, err := t.callTool(ctx, "qrdb_ceremony_initialize", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonyInitialize", Err: err}
	}

	var resp transport.CeremonyInitializeResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CeremonyInitialize", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// CeremonyStatus returns the current ceremony state by calling the
// qrdb_ceremony_status MCP tool.
func (t *Transport) CeremonyStatus(ctx context.Context) (*transport.CeremonyStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_ceremony_status", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonyStatus", Err: err}
	}

	var resp transport.CeremonyStatusResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CeremonyStatus", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// CeremonySignCSR submits a CSR for signing during or after the ceremony by
// calling the qrdb_ceremony_sign_csr MCP tool.
func (t *Transport) CeremonySignCSR(ctx context.Context, req *transport.SignCSRInitRequest) (*transport.SignCSRInitResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	ctx, err := t.guardSecret(ctx, req)
	if err != nil {
		return nil, err
	}
	// The SO PIN travels in the request's own so_pin field too, which the
	// server reads; it comes from the PIN bound by transport.WithSOPIN when
	// the request carries none, exactly as on QUIC.
	soPIN, err := transport.ResolveSOPIN(ctx, "CeremonySignCSR", req.SOPin)
	if err != nil {
		return nil, err
	}

	args := map[string]any{
		"username": req.Username,
		"csr_pem":  req.CSRPEM,
		"role":     req.Role,
	}
	putStringSecret(args, "so_pin", soPIN)
	text, err := t.callTool(ctx, "qrdb_ceremony_sign_csr", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonySignCSR", Err: err}
	}

	var resp transport.SignCSRInitResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CeremonySignCSR", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}
