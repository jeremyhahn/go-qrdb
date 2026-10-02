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

	"github.com/jeremyhahn/go-qrdb/pkg/api/holderwire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
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
	result, err := t.ceremonySvc.CeremonyInitialize(ctx, t.ceremonyCA, holderwire.CeremonyInitFrom(req))
	if err != nil {
		return nil, delayed("CeremonyInitialize", err)
	}
	return holderwire.CeremonyInitTo(result), nil
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
		return nil, delayed("CeremonyStatus", err)
	}
	return &transport.CeremonyStatusResponse{
		State:   info.State,
		SPKIPin: info.SPKIPin,
	}, nil
}

// contextCertThumbprint derives the caller's certificate thumbprint from the
// TLS connection state carried on ctx. It fails closed with
// service.ClientCertRequiredError when no verified peer certificate is
// present.
func contextCertThumbprint(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", &service.ClientCertRequiredError{}
	}
	state := auth.TLSStateFromContext(ctx)
	if state == nil || len(state.PeerCertificates) == 0 {
		return "", &service.ClientCertRequiredError{}
	}
	return mtls.Thumbprint(state.PeerCertificates[0].Raw), nil
}

// claimContext prepares a share claim made through this transport. A claim
// whose ctx carries a verified TLS peer certificate presents it, exactly as on
// a network transport. One with none is in process: there is no connection to
// carry a certificate, so ctx is marked with ceremony.WithInProcessClaim and
// the claim is bound to the holder's enrolled certificate, released only for
// a signature over the fresh claim nonce by that certificate's key.
func claimContext(ctx context.Context) (context.Context, string) {
	if thumb, err := contextCertThumbprint(ctx); err == nil {
		return ctx, thumb
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return ceremony.WithInProcessClaim(ctx), ""
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
		return nil, delayed("CeremonySignCSR", err)
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
