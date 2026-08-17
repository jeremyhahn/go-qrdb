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

import "context"

// httpAuthorizationKey carries the raw HTTP Authorization header value
// through the request context.
type httpAuthorizationKey struct{}

// WithHTTPAuthorization stores the raw HTTP Authorization header value
// (e.g. "Bearer eyJ...") in the context so credential-bearing
// Authenticators supplied by an embedder — an OAuth 2.1 bearer-token
// validator, for instance — can read it without a dependency on net/http.
// qrdb itself ships NO token-validating authenticator: this carrier plus
// the Authenticator interface is the extension seam; the implementation is
// the embedder's (see pkg/auth/wiring.AuthMiddlewareConfig.
// ExtraAuthenticators). An empty value is not stored.
func WithHTTPAuthorization(ctx context.Context, headerValue string) context.Context {
	if headerValue == "" {
		return ctx
	}
	return context.WithValue(ctx, httpAuthorizationKey{}, headerValue)
}

// HTTPAuthorizationFromContext retrieves the raw HTTP Authorization header
// value stored by WithHTTPAuthorization, or "" when none is present.
func HTTPAuthorizationFromContext(ctx context.Context) string {
	v, _ := ctx.Value(httpAuthorizationKey{}).(string) //nolint:errcheck // comma-ok on a value only WithHTTPAuthorization stores
	return v
}
