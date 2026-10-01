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

// AuthorizeQUIC checks whether the authenticated caller in the context has
// permission for the given QUIC method. Per-method permissions come from the
// QUICMethodRegistry the QUIC server populates at handler-registration time —
// the single source of truth that replaced the old static quicPermissions
// table. DEFAULT-DENY: every dispatched method must carry a registered
// permission; an unregistered method (or a nil registry) is rejected rather
// than treated as public, so a newly-added handler cannot silently become
// unauthorized.
//
// payload is the frame's request payload; the method's registered
// QUICTargetFunc reads the tenant it acts on from it, and the permission is
// authorized against that tenant (see Target).
func (m *AuthMiddleware) AuthorizeQUIC(ctx context.Context, methods *QUICMethodRegistry, method string, payload []byte) error {
	identity := auth.IdentityFromContext(ctx)
	if identity == nil {
		return &auth.AuthError{Code: "unauthenticated", Message: "no identity in context"}
	}

	perm, ok := methods.Lookup(method)
	if !ok {
		return &auth.AuthError{
			Code:    "unauthorized",
			Message: "quic: method not authorized (no permission registration): " + method,
		}
	}

	target, err := methods.target(method, payload)
	if err != nil {
		return err
	}
	return m.authorizeTarget(ctx, identity, perm, target)
}
