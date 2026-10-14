// Copyright (c) 2026 Jeremy Hahn
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

package auth

import (
	"context"
	"net/http"
)

// DPoPRequest is the request metadata an RFC 9449 (DPoP) verifier needs: the
// raw DPoP proof header and the HTTP method (htm) and target URI (htu) the
// proof must be bound to. qrdb carries the data only; verifying the proof is
// the embedder's Authenticator.
type DPoPRequest struct {
	// Proof is the raw value of the DPoP request header (a compact JWS).
	Proof string
	// Method is the HTTP request method ("htm" in the proof).
	Method string
	// URI is the request target URI without query and fragment ("htu").
	URI string
}

// dpopRequestKey carries the DPoPRequest through the request context.
type dpopRequestKey struct{}

// WithDPoPRequest stores the DPoP request metadata in the context so an
// embedder's proof-of-possession Authenticator can read it without a
// dependency on net/http. REST and MCP fill it from the HTTP request, gRPC
// from the "dpop" metadata; the native QUIC, embedded and MCP stdio
// transports have no HTTP request and never fill it. A request without a
// proof is not stored.
func WithDPoPRequest(ctx context.Context, req DPoPRequest) context.Context {
	if req.Proof == "" {
		return ctx
	}
	return context.WithValue(ctx, dpopRequestKey{}, req)
}

// DPoPRequestFromContext retrieves the metadata stored by WithDPoPRequest.
// The bool is false when the request carried no DPoP proof.
func DPoPRequestFromContext(ctx context.Context) (DPoPRequest, bool) {
	v, ok := ctx.Value(dpopRequestKey{}).(DPoPRequest)
	return v, ok
}

// WithDPoPFromHTTP stores the DPoP metadata of an HTTP request: the DPoP
// header, the request method, and the target URI built from the scheme
// (https when the connection is TLS, which covers HTTP/2 and HTTP/3), the
// Host and the escaped path. Query and fragment are excluded, as RFC 9449
// section 4.2 defines htu. A request with no DPoP header leaves ctx unchanged.
func WithDPoPFromHTTP(ctx context.Context, r *http.Request) context.Context {
	proof := r.Header.Get("DPoP")
	if proof == "" {
		return ctx
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return WithDPoPRequest(ctx, DPoPRequest{
		Proof:  proof,
		Method: r.Method,
		URI:    scheme + "://" + r.Host + r.URL.EscapedPath(),
	})
}
