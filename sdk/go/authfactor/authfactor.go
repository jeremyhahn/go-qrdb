// Copyright 2026 Jeremy Hahn
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

// Package authfactor is the public Go SDK surface for QRDB's auth factor
// service. It is the Go counterpart to sdk/typescript/src/authfactor.ts and
// gives consumers (notably go-xkms after the PIV migration) a typed,
// transport-agnostic client without having to import the internal
// sdk/go/transport package directly.
//
// The package re-exports the wire types defined in sdk/go/transport and
// provides a thin Client wrapper that delegates to any
// transport.AuthFactorClient implementation (REST, gRPC, QUIC, MCP, Unix).
//
// Supported factor names: "pkcs11", "piv", "fido2".
//
// Example:
//
//	c, err := qrdbsdk.NewClient(ctx, qrdbsdk.RESTConfig(...))
//	if err != nil { /* handle */ }
//	defer c.Close(ctx)
//
//	af := authfactor.New(c.AuthFactors())
//	enrollment, err := af.EnrollFactor(ctx, &authfactor.EnrollFactorRequest{
//	    FactorName: authfactor.FactorFIDO2,
//	    Subject:    "custodian-1",
//	})
package authfactor

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Factor name constants
// ---------------------------------------------------------------------------

const (
	// FactorPKCS11 is the PKCS#11 / HSM-backed factor name.
	FactorPKCS11 = "pkcs11"

	// FactorPIV is the PIV smartcard factor name (cross-vendor, NIST SP 800-73-4).
	FactorPIV = "piv"

	// FactorFIDO2 is the FIDO2 / WebAuthn factor name.
	FactorFIDO2 = "fido2"
)

// ---------------------------------------------------------------------------
// Wire type re-exports (mirror sdk/typescript/src/authfactor.ts)
// ---------------------------------------------------------------------------

// EnrollFactorRequest is the public re-export of transport.EnrollFactorRequest.
type EnrollFactorRequest = transport.EnrollFactorRequest

// EnrollFactorResponse is the public re-export of transport.EnrollFactorResponse.
type EnrollFactorResponse = transport.EnrollFactorResponse

// ChallengeFactorRequest is the public re-export of transport.ChallengeFactorRequest.
type ChallengeFactorRequest = transport.ChallengeFactorRequest

// ChallengeFactorResponse is the public re-export of transport.ChallengeFactorResponse.
type ChallengeFactorResponse = transport.ChallengeFactorResponse

// VerifyFactorRequest is the public re-export of transport.VerifyFactorRequest.
type VerifyFactorRequest = transport.VerifyFactorRequest

// ListFactorsRequest is the public re-export of transport.ListFactorsRequest.
type ListFactorsRequest = transport.ListFactorsRequest

// ListFactorsResponse is the public re-export of transport.ListFactorsResponse.
type ListFactorsResponse = transport.ListFactorsResponse

// EnrollmentEntry is the public re-export of transport.EnrollmentEntry.
type EnrollmentEntry = transport.EnrollmentEntry

// RevokeFactorRequest is the public re-export of transport.RevokeFactorRequest.
type RevokeFactorRequest = transport.RevokeFactorRequest

// AuthFactorClient is the public re-export of transport.AuthFactorClient. It
// is exposed so consumers can write their own implementations (e.g. for tests
// or non-network in-process delegation) without importing the transport
// package.
type AuthFactorClient = transport.AuthFactorClient

// ---------------------------------------------------------------------------
// Client wrapper
// ---------------------------------------------------------------------------

// Client is a typed wrapper around any transport.AuthFactorClient. It mirrors
// the AuthFactorClient class in sdk/typescript/src/authfactor.ts and gives
// callers a stable public surface that does not depend on the internal
// transport package.
//
// The wrapper performs lightweight argument validation (returning a typed
// *NilRequestError) and otherwise delegates straight to the underlying
// transport client. Use New to construct one over an existing transport
// client returned by qrdbsdk.Client.AuthFactors().
type Client struct {
	inner AuthFactorClient
}

// New returns a Client that delegates to the given AuthFactorClient. New
// returns a non-nil *NilTransportError if inner is nil — propagating the
// failure as a typed error rather than panicking on a later method call.
//
// In the common case callers pass the result of qrdbsdk.Client.AuthFactors():
//
//	af, err := authfactor.New(c.AuthFactors())
func New(inner AuthFactorClient) (*Client, error) {
	if inner == nil {
		return nil, &NilTransportError{}
	}
	return &Client{inner: inner}, nil
}

// EnrollFactor enrolls a custodian in the given auth factor type. Maps to
// transport.AuthFactorClient.EnrollFactor.
func (c *Client) EnrollFactor(ctx context.Context, req *EnrollFactorRequest) (*EnrollFactorResponse, error) {
	if req == nil {
		return nil, &NilRequestError{Method: "EnrollFactor"}
	}
	return c.inner.EnrollFactor(ctx, req)
}

// ChallengeFactor issues a cryptographic challenge for a previously enrolled
// factor. Maps to transport.AuthFactorClient.ChallengeFactor.
func (c *Client) ChallengeFactor(ctx context.Context, req *ChallengeFactorRequest) (*ChallengeFactorResponse, error) {
	if req == nil {
		return nil, &NilRequestError{Method: "ChallengeFactor"}
	}
	return c.inner.ChallengeFactor(ctx, req)
}

// VerifyFactor verifies the custodian's response to an outstanding challenge.
// A successful call consumes the challenge (preventing replay). Maps to
// transport.AuthFactorClient.VerifyFactor.
func (c *Client) VerifyFactor(ctx context.Context, req *VerifyFactorRequest) error {
	if req == nil {
		return &NilRequestError{Method: "VerifyFactor"}
	}
	return c.inner.VerifyFactor(ctx, req)
}

// ListFactors lists enrolled auth factors, optionally filtered by subject.
// A nil request is treated as an empty filter, matching the TypeScript
// listFactors() default-arg behaviour.
func (c *Client) ListFactors(ctx context.Context, req *ListFactorsRequest) (*ListFactorsResponse, error) {
	if req == nil {
		req = &ListFactorsRequest{}
	}
	return c.inner.ListFactors(ctx, req)
}

// RevokeFactor permanently revokes an enrollment. Revoked enrollments cannot
// be used for subsequent challenges or verifications.
func (c *Client) RevokeFactor(ctx context.Context, req *RevokeFactorRequest) error {
	if req == nil {
		return &NilRequestError{Method: "RevokeFactor"}
	}
	return c.inner.RevokeFactor(ctx, req)
}
