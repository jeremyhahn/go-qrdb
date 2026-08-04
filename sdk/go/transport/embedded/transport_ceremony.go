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

package embedded

import (
	"context"
	"encoding/base64"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// CeremonyClient implementation
// ---------------------------------------------------------------------------

// CeremonyInitialize starts the initialization ceremony by delegating to the
// injected CeremonyServicer. The embedded transport passes the injected
// CeremonyCA directly and forwards the per-request SO and user PINs from the
// request so that callers can supply credentials on the call itself rather
// than mutating shared service configuration. Per-request PINs take
// precedence over any construction-time configuration; when both are absent
// the ceremony fails closed at the service layer.
func (t *Transport) CeremonyInitialize(ctx context.Context, req *transport.CeremonyInitializeRequest) (*transport.CeremonyInitializeResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.ceremonySvc == nil {
		return nil, &NilServiceError{Service: "CeremonyServicer"}
	}
	if t.ceremonyCA == nil {
		return nil, &NilServiceError{Service: "CeremonyCA"}
	}
	if req == nil {
		return nil, &InvalidRequestError{Method: "CeremonyInitialize"}
	}
	opts := &service.CeremonyInitializeOptions{
		SOPin:                  req.SOPin,
		UserPin:                req.UserPin,
		CredentialSealStrategy: req.CredentialSealStrategy,
		Threshold:              req.Threshold,
		Officers:               ceremonyOfficerConfigsFromRequest(req.Officers),
		Hostname:               req.Hostname,
		SANs:                   req.SANs,
	}
	result, err := t.ceremonySvc.CeremonyInitialize(ctx, t.ceremonyCA, opts)
	if err != nil {
		return nil, err
	}
	return &transport.CeremonyInitializeResponse{
		SPKIPin:    result.SPKIPin,
		CACertPEM:  string(result.CACertPEM),
		TLSCertPEM: string(result.TLSCertPEM),
		SOCertPEM:  string(result.SOCertPEM),
		SOKeyPEM:   string(result.SOKeyPEM),
		State:      result.State,
		ShareCount: result.ShareCount,
	}, nil
}

// ceremonyOfficerConfigsFromRequest converts the SDK-level officer request
// shape to the service-layer type. Returns nil when officers is empty so
// the service layer's "non-empty overrides the configured value"
// precedence rule works unchanged.
func ceremonyOfficerConfigsFromRequest(officers []transport.CeremonyOfficerRequest) []service.CeremonyOfficerConfig {
	if len(officers) == 0 {
		return nil
	}
	out := make([]service.CeremonyOfficerConfig, len(officers))
	for i, o := range officers {
		out[i] = service.CeremonyOfficerConfig{Username: o.Username, CSRPEM: o.CSRPEM}
	}
	return out
}

// CeremonyStatus returns the current ceremony state by delegating to the
// injected CeremonyServicer.
func (t *Transport) CeremonyStatus(ctx context.Context) (*transport.CeremonyStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.ceremonySvc == nil {
		return nil, &NilServiceError{Service: "CeremonyServicer"}
	}
	info, err := t.ceremonySvc.CeremonyStatus(ctx)
	if err != nil {
		return nil, err
	}
	return &transport.CeremonyStatusResponse{
		State:   info.State,
		SPKIPin: info.SPKIPin,
	}, nil
}

// CeremonyClaimCertBegin starts a certificate claim challenge for the named
// officer by delegating to the injected CeremonyServicer.
func (t *Transport) CeremonyClaimCertBegin(ctx context.Context, req *transport.ClaimCertBeginRequest) (*transport.ClaimCertBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.ceremonySvc == nil {
		return nil, &NilServiceError{Service: "CeremonyServicer"}
	}
	challenge, err := t.ceremonySvc.CeremonyBeginClaimCert(ctx, req.Username)
	if err != nil {
		return nil, err
	}
	return &transport.ClaimCertBeginResponse{
		NonceHex:  challenge.NonceHex,
		ExpiresAt: challenge.ExpiresAt.Format(time.RFC3339),
	}, nil
}

// CeremonyClaimCertComplete completes a certificate claim by submitting
// the signed nonce. The base64-encoded signature from the request is
// decoded before passing to the service layer.
func (t *Transport) CeremonyClaimCertComplete(ctx context.Context, req *transport.ClaimCertCompleteRequest) (*transport.ClaimCertCompleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.ceremonySvc == nil {
		return nil, &NilServiceError{Service: "CeremonyServicer"}
	}
	sigBytes, err := base64.StdEncoding.DecodeString(req.Signature)
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonyClaimCertComplete", Err: err}
	}
	result, err := t.ceremonySvc.CeremonyCompleteClaimCert(ctx, req.Username, req.NonceHex, sigBytes)
	if err != nil {
		return nil, err
	}
	return &transport.ClaimCertCompleteResponse{
		CertPEM:   string(result.CertPEM),
		CACertPEM: string(result.CACertPEM),
	}, nil
}

// CeremonyClaimShare retrieves the Shamir share for an officer who has
// already claimed their certificate by delegating to the injected
// CeremonyServicer.
func (t *Transport) CeremonyClaimShare(ctx context.Context, req *transport.ClaimShareRequest) (*transport.ClaimShareResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.ceremonySvc == nil {
		return nil, &NilServiceError{Service: "CeremonyServicer"}
	}
	shareBytes, err := t.ceremonySvc.CeremonyClaimShare(ctx, req.Username)
	if err != nil {
		return nil, err
	}
	return &transport.ClaimShareResponse{
		ShareJSON: string(shareBytes),
	}, nil
}

// CeremonySignCSR submits a CSR for signing during or after the ceremony
// by delegating to the injected CeremonyServicer.
func (t *Transport) CeremonySignCSR(ctx context.Context, req *transport.SignCSRInitRequest) (*transport.SignCSRInitResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.ceremonySvc == nil {
		return nil, &NilServiceError{Service: "CeremonyServicer"}
	}
	result, err := t.ceremonySvc.CeremonySignCSR(ctx, req.Username, req.SOPin, req.CSRPEM, req.Role)
	if err != nil {
		return nil, err
	}
	return &transport.SignCSRInitResponse{
		CertPEM: string(result.CertPEM),
	}, nil
}

// ---------------------------------------------------------------------------
// Request dispatch handlers
// ---------------------------------------------------------------------------

func handleCeremonyInitialize(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.CeremonyInitializeRequest)
	if !ok {
		return &InvalidRequestError{Method: "CeremonyInitialize"}
	}
	result, err := t.CeremonyInitialize(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.CeremonyInitializeResponse); ok {
		*out = result
	}
	return nil
}

func handleCeremonyStatus(ctx context.Context, t *Transport, _, resp any) error {
	result, err := t.CeremonyStatus(ctx)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.CeremonyStatusResponse); ok {
		*out = result
	}
	return nil
}

func handleCeremonyClaimCertBegin(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.ClaimCertBeginRequest)
	if !ok {
		return &InvalidRequestError{Method: "CeremonyClaimCertBegin"}
	}
	result, err := t.CeremonyClaimCertBegin(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.ClaimCertBeginResponse); ok {
		*out = result
	}
	return nil
}

func handleCeremonyClaimCertComplete(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.ClaimCertCompleteRequest)
	if !ok {
		return &InvalidRequestError{Method: "CeremonyClaimCertComplete"}
	}
	result, err := t.CeremonyClaimCertComplete(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.ClaimCertCompleteResponse); ok {
		*out = result
	}
	return nil
}

func handleCeremonyClaimShare(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.ClaimShareRequest)
	if !ok {
		return &InvalidRequestError{Method: "CeremonyClaimShare"}
	}
	result, err := t.CeremonyClaimShare(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.ClaimShareResponse); ok {
		*out = result
	}
	return nil
}

func handleCeremonySignCSR(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.SignCSRInitRequest)
	if !ok {
		return &InvalidRequestError{Method: "CeremonySignCSR"}
	}
	result, err := t.CeremonySignCSR(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.SignCSRInitResponse); ok {
		*out = result
	}
	return nil
}

// ---------------------------------------------------------------------------
// Functional options
// ---------------------------------------------------------------------------

// WithCeremonyService injects a CeremonyServicer into the transport.
func WithCeremonyService(svc service.CeremonyServicer) Option {
	return func(t *Transport) {
		t.ceremonySvc = svc
	}
}

// WithCeremonyCA injects a CeremonyCA into the transport for use during
// initialization.
func WithCeremonyCA(ca ceremony.CeremonyCA) Option {
	return func(t *Transport) {
		t.ceremonyCA = ca
	}
}

// CeremonyEnrollExternalOfficer enrols an officer whose key material lives
// outside go-qrdb (external CA cert or bare public key) by delegating to
// the injected CeremonyServicer.
func (t *Transport) CeremonyEnrollExternalOfficer(ctx context.Context, req *transport.EnrollExternalOfficerRequest) (*transport.EnrollExternalOfficerResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.ceremonySvc == nil {
		return nil, &NilServiceError{Service: "CeremonyServicer"}
	}
	result, err := t.ceremonySvc.CeremonyEnrollExternalOfficer(ctx, &service.CeremonyEnrollExternalOfficerRequest{
		Username:   req.Username,
		CertPEM:    req.CertPEM,
		PubKeyPEM:  req.PubKeyPEM,
		ShareIndex: req.ShareIndex,
		AuthFactor: req.AuthFactor,
		ShareBytes: req.ShareBytes,
	})
	if err != nil {
		return nil, err
	}
	return &transport.EnrollExternalOfficerResponse{
		SealedShare: result.SealedShare,
	}, nil
}
