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

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// permissionClient is the embedded implementation of
// transport.PermissionClient. It is a thin projection over the global
// permission registry — no transport state is touched, so a single
// instance is shared across all callers.
type permissionClient struct {
	transport *Transport
}

// Compile-time interface assertion.
var _ transport.PermissionClient = (*permissionClient)(nil)

// List returns every registered permission, sorted by name.
func (c *permissionClient) List(ctx context.Context) ([]transport.PermissionInfo, error) {
	ctx, err := c.transport.admit(ctx, "PermissionClient.List")
	if err != nil {
		return nil, err
	}
	if err := c.transport.authorize(ctx, "PermissionClient.List"); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.transport.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	all := permission.All()
	out := make([]transport.PermissionInfo, len(all))
	for i, p := range all {
		info, err := transport.PermissionInfoFromDomain(p)
		if err != nil {
			return nil, err
		}
		out[i] = info
	}
	return out, nil
}

// Lookup returns the permission with the given name. Returns
// PermissionNotFoundError when the name is not registered.
func (c *permissionClient) Lookup(ctx context.Context, name string) (transport.PermissionInfo, error) {
	ctx, err := c.transport.admit(ctx, "PermissionClient.Lookup")
	if err != nil {
		return transport.PermissionInfo{}, err
	}
	if err := c.transport.authorize(ctx, "PermissionClient.Lookup"); err != nil {
		return transport.PermissionInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return transport.PermissionInfo{}, err
	}
	if c.transport.closed.Load() {
		return transport.PermissionInfo{}, &transport.ClosedError{}
	}
	p := permission.Permission(name)
	if !permission.IsRegistered(p) {
		return transport.PermissionInfo{}, &transport.PermissionNotFoundError{Name: name}
	}
	return transport.PermissionInfoFromDomain(p)
}

// LookupScope returns the scope of the permission with the given name.
//
// A name with no declared scope is an error, not PermissionScopeSelf. This
// method used to answer Self for any unregistered name "so callers can rely
// on the result for self-scope gating without a separate registration
// check" — which is precisely the fail-open that go-qrdb#504 removed: a
// caller gating on Self for a permission nobody had declared was gating on
// nothing. The error names the permission; a caller that wants a registration
// check has one.
func (c *permissionClient) LookupScope(ctx context.Context, name string) (transport.PermissionScope, error) {
	ctx, err := c.transport.admit(ctx, "PermissionClient.LookupScope")
	if err != nil {
		return "", err
	}
	if err := c.transport.authorize(ctx, "PermissionClient.LookupScope"); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c.transport.closed.Load() {
		return "", &transport.ClosedError{}
	}
	scope, err := permission.LookupScopeByName(name)
	if err != nil {
		return "", err
	}
	return transport.ScopeFromDomain(scope)
}

// PermissionClient returns the embedded permission registry projection.
// It implements transport.PermissionProvider so the SDK Client can
// surface the typed client via Client.Permissions().
func (t *Transport) PermissionClient() transport.PermissionClient {
	return &permissionClient{transport: t}
}
