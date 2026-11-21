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

package transport

import (
	"context"
	"errors"
	"sort"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// PermissionCatalog projects every registered permission, with its declared
// scope, sorted by name. Servers answer ListPermissionCatalog with it on
// every protocol, so a remote PermissionClient sees exactly the registry the
// embedded one reads.
func PermissionCatalog() ([]PermissionInfo, error) {
	all := permission.All()
	out := make([]PermissionInfo, 0, len(all))
	for _, p := range all {
		info, err := PermissionInfoFromDomain(p)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// PermissionEndpoints is the one wire call a remote PermissionClient needs:
// the server's permission catalog (ListPermissionCatalog, permissions:list).
// Each network transport implements it over its own protocol.
type PermissionEndpoints interface {
	// ListPermissionCatalog returns every registered permission with its
	// declared scope, sorted by name.
	ListPermissionCatalog(ctx context.Context) ([]PermissionInfo, error)
}

// errEmptyPermissionScope reports a catalog entry the server sent with no
// scope. It is an error, never PermissionScopeSelf: a caller gating on Self
// for a permission with no declared scope would be gating on nothing.
var errEmptyPermissionScope = errors.New("transport: permission has no declared scope")

// remotePermissionClient answers PermissionClient from the server's
// catalog. List is the server's answer; Lookup and LookupScope filter it.
type remotePermissionClient struct {
	endpoints PermissionEndpoints
}

// NewRemotePermissionClient returns a PermissionClient that answers from
// the server's permission catalog through e.
func NewRemotePermissionClient(e PermissionEndpoints) PermissionClient {
	return &remotePermissionClient{endpoints: e}
}

// List returns the server's permission catalog, sorted by name.
func (c *remotePermissionClient) List(ctx context.Context) ([]PermissionInfo, error) {
	return c.endpoints.ListPermissionCatalog(ctx)
}

// Lookup returns the catalog entry named name, or *PermissionNotFoundError
// when the server does not register it.
func (c *remotePermissionClient) Lookup(ctx context.Context, name string) (PermissionInfo, error) {
	all, err := c.endpoints.ListPermissionCatalog(ctx)
	if err != nil {
		return PermissionInfo{}, err
	}
	for _, p := range all {
		if p.Name == name {
			return p, nil
		}
	}
	return PermissionInfo{}, &PermissionNotFoundError{Name: name}
}

// LookupScope returns the declared scope of the permission named name:
// *PermissionNotFoundError for an unknown name, and an error, never
// PermissionScopeSelf, for an entry with no scope.
func (c *remotePermissionClient) LookupScope(ctx context.Context, name string) (PermissionScope, error) {
	p, err := c.Lookup(ctx, name)
	if err != nil {
		return "", err
	}
	if p.Scope == "" {
		return "", errEmptyPermissionScope
	}
	return p.Scope, nil
}
