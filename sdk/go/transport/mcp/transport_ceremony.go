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
	"encoding/base64"
	"encoding/json"

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
	// The SO PIN travels in the request's own so_pin field too, which the
	// server reads; it comes from the PIN bound by transport.WithSOPIN when
	// the request carries none, exactly as on QUIC.
	soPIN, err := transport.ResolveSOPIN(ctx, "CeremonyInitialize", req.SOPin)
	if err != nil {
		return nil, err
	}

	args := map[string]any{
		"so_pin":    soPIN,
		"user_pin":  req.UserPin,
		"threshold": req.Threshold,
	}

	if req.CredentialSealStrategy != "" {
		args["credential_seal_strategy"] = req.CredentialSealStrategy
	}
	if len(req.Officers) > 0 {
		officersJSON, marshalErr := json.Marshal(req.Officers)
		if marshalErr != nil {
			return nil, &transport.RequestError{Method: "CeremonyInitialize", Err: &jsonDecodeError{Err: marshalErr}}
		}
		args["officers"] = string(officersJSON)
	}
	if req.Hostname != "" {
		args["hostname"] = req.Hostname
	}
	if len(req.SANs) > 0 {
		sansJSON, marshalErr := json.Marshal(req.SANs)
		if marshalErr != nil {
			return nil, &transport.RequestError{Method: "CeremonyInitialize", Err: &jsonDecodeError{Err: marshalErr}}
		}
		args["sans"] = string(sansJSON)
	}

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

// CeremonyClaimCertBegin starts a certificate claim challenge for the named
// officer by calling the qrdb_ceremony_claim_cert_begin MCP tool.
func (t *Transport) CeremonyClaimCertBegin(ctx context.Context, req *transport.ClaimCertBeginRequest) (*transport.ClaimCertBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_ceremony_claim_cert_begin", map[string]any{
		"username": req.Username,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonyClaimCertBegin", Err: err}
	}

	var resp transport.ClaimCertBeginResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CeremonyClaimCertBegin", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// CeremonyClaimCertComplete completes a certificate claim by submitting the
// signed nonce. Calls the qrdb_ceremony_claim_cert_complete MCP tool.
func (t *Transport) CeremonyClaimCertComplete(ctx context.Context, req *transport.ClaimCertCompleteRequest) (*transport.ClaimCertCompleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_ceremony_claim_cert_complete", map[string]any{
		"username":  req.Username,
		"nonce_hex": req.NonceHex,
		"signature": req.Signature,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonyClaimCertComplete", Err: err}
	}

	var resp transport.ClaimCertCompleteResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CeremonyClaimCertComplete", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// CeremonyBeginClaimShare starts the Shamir share claim handshake by calling
// the qrdb_ceremony_begin_claim_share MCP tool. The caller's certificate
// thumbprint is derived server-side from the session's mTLS peer chain.
func (t *Transport) CeremonyBeginClaimShare(ctx context.Context, req *transport.BeginClaimShareRequest) (*transport.BeginClaimShareResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_ceremony_begin_claim_share", map[string]any{
		"username": req.Username,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonyBeginClaimShare", Err: err}
	}

	var wire struct {
		NonceHex  string `json:"nonce_hex"`
		ExpiresAt string `json:"expires_at"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CeremonyBeginClaimShare", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.BeginClaimShareResponse{
		NonceHex:  wire.NonceHex,
		ExpiresAt: wire.ExpiresAt,
	}, nil
}

// CeremonyCompleteClaimShare completes the Shamir share claim handshake by
// calling the qrdb_ceremony_complete_claim_share MCP tool. The server returns
// a base64-encoded share which is decoded into the ShareJSON response field.
func (t *Transport) CeremonyCompleteClaimShare(ctx context.Context, req *transport.CompleteClaimShareRequest) (*transport.CompleteClaimShareResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_ceremony_complete_claim_share", map[string]any{
		"username":  req.Username,
		"nonce_hex": req.NonceHex,
		"signature": base64.StdEncoding.EncodeToString(req.Signature),
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonyCompleteClaimShare", Err: err}
	}

	var wire struct {
		ShareIndex int    `json:"share_index"`
		Share      string `json:"share"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CeremonyCompleteClaimShare", Err: &jsonDecodeError{Err: jsonErr}}
	}

	shareBytes, decErr := base64.StdEncoding.DecodeString(wire.Share)
	if decErr != nil {
		return nil, &transport.RequestError{Method: "CeremonyCompleteClaimShare", Err: decErr}
	}

	return &transport.CompleteClaimShareResponse{
		ShareIndex: wire.ShareIndex,
		ShareJSON:  string(shareBytes),
	}, nil
}

// CeremonySignCSR submits a CSR for signing during or after the ceremony by
// calling the qrdb_ceremony_sign_csr MCP tool.
func (t *Transport) CeremonySignCSR(ctx context.Context, req *transport.SignCSRInitRequest) (*transport.SignCSRInitResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	// The SO PIN travels in the request's own so_pin field too, which the
	// server reads; it comes from the PIN bound by transport.WithSOPIN when
	// the request carries none, exactly as on QUIC.
	soPIN, err := transport.ResolveSOPIN(ctx, "CeremonySignCSR", req.SOPin)
	if err != nil {
		return nil, err
	}

	text, err := t.callTool(ctx, "qrdb_ceremony_sign_csr", map[string]any{
		"username": req.Username,
		"so_pin":   soPIN,
		"csr_pem":  req.CSRPEM,
		"role":     req.Role,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonySignCSR", Err: err}
	}

	var resp transport.SignCSRInitResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CeremonySignCSR", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// CeremonyEnrollExternalOfficer enrols an officer whose key material lives
// outside go-qrdb (external CA cert or bare public key) by calling the
// qrdb_ceremony_enroll_external_officer MCP tool.
func (t *Transport) CeremonyEnrollExternalOfficer(ctx context.Context, req *transport.EnrollExternalOfficerRequest) (*transport.EnrollExternalOfficerResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_ceremony_enroll_external_officer", map[string]any{
		"username":    req.Username,
		"cert_pem":    req.CertPEM,
		"pubkey_pem":  req.PubKeyPEM,
		"share_index": req.ShareIndex,
		"auth_factor": req.AuthFactor,
		"share_bytes": req.ShareBytes,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonyEnrollExternalOfficer", Err: err}
	}

	var resp transport.EnrollExternalOfficerResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CeremonyEnrollExternalOfficer", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}
