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

package middleware

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// AuthenticateQUIC performs authentication for a QUIC request. The
// context must already contain TLS state injected via auth.WithTLSState.
// Returns an enriched context with the authenticated identity on success.
func (m *AuthMiddleware) AuthenticateQUIC(ctx context.Context) (context.Context, error) {
	ctx, _, authnErr := m.authenticate(ctx)
	if authnErr != nil {
		return ctx, authnErr
	}
	return ctx, nil
}

// AuthorizeQUIC checks whether the authenticated caller in the context
// has permission for the given QUIC method. Methods without a permission
// mapping are allowed through (public endpoints).
func (m *AuthMiddleware) AuthorizeQUIC(ctx context.Context, method string) error {
	perm, ok := LookupQUIC(method)
	if !ok {
		return nil
	}

	identity := auth.IdentityFromContext(ctx)
	if identity == nil {
		return &auth.AuthError{Code: "unauthenticated", Message: "no identity in context"}
	}

	return m.authorize(ctx, identity, perm)
}

// LookupQUIC returns the permission required for a native QUIC method.
// Returns an empty Permission and false if the method has no mapping,
// indicating a public endpoint that does not require authorization.
func LookupQUIC(method string) (permission.Permission, bool) {
	p, ok := quicPermissions[method]
	return p, ok
}
