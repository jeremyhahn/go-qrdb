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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// sessionClient is the embedded implementation of
// transport.SessionClient. It is a thin façade over the configured
// SessionIssuer — Login owns issuance, Session owns validation and
// invalidation.
type sessionClient struct {
	transport *Transport
}

// Compile-time interface assertion.
var _ transport.SessionClient = (*sessionClient)(nil)

// ResolveIdentity returns the Identity bound to the given bearer.
func (c *sessionClient) ResolveIdentity(ctx context.Context, bearer string) (*transport.Identity, error) {
	if c.transport.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if c.transport.sessionIssuer == nil {
		return nil, &MissingSessionIssuerError{}
	}
	if bearer == "" {
		return nil, &transport.InvalidBearerError{Reason: "empty bearer"}
	}
	id, err := c.transport.sessionIssuer.Resolve(ctx, bearer)
	if err != nil {
		return nil, err
	}
	return identityToTransport(id), nil
}

// Revoke invalidates the bearer.
func (c *sessionClient) Revoke(ctx context.Context, bearer string) error {
	if c.transport.closed.Load() {
		return &transport.ClosedError{}
	}
	if c.transport.sessionIssuer == nil {
		return &MissingSessionIssuerError{}
	}
	if bearer == "" {
		return &transport.InvalidBearerError{Reason: "empty bearer"}
	}
	return c.transport.sessionIssuer.Revoke(ctx, bearer)
}

// SessionClient returns the embedded session client projection.
func (t *Transport) SessionClient() transport.SessionClient {
	return &sessionClient{transport: t}
}
