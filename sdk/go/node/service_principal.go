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

package node

import (
	"context"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// servicePrincipalField names WithServicePrincipal in a *ConfigError.
const servicePrincipalField = "WithServicePrincipal"

// ErrNoServicePrincipal is returned by ServiceContext on a node built without
// WithServicePrincipal.
var ErrNoServicePrincipal = errors.New("node: no service principal configured (WithServicePrincipal)")

// WithServicePrincipal names the node-sanctioned service principal the
// embedding host's OWN in-process calls run as (docs/auth): loading its CA
// state, its stores, its signing keys -- work no end user is behind.
// ServiceContext binds it to a context for the node's embedded client.
//
// Its authority is exactly what the host declares under the same name through
// the permission role-grant seam, and nothing else:
//
//	sdk.RegisterPermissionRoleGrant("xkms", sdk.PermissionCredentialsStore, sdk.PermissionCredentialsRetrieve)
//	n, err := node.NewStandaloneNode(ctx, cfg, nil, node.WithServicePrincipal("xkms"))
//
// Every call is authorized by the node's authorizer against those grants,
// like any caller's -- there is no implicit superuser and no bypass -- and the
// audit trail attributes it to the subject "qrdb-service:<name>". The
// principal lives in the system tenant.
//
// No network transport accepts it on its own authority: a presented identity
// claiming the principal is refused as unauthenticated
// (*ServicePrincipalPresentedError), and a call the embedded client forwards
// to another replica travels as the WithEmbeddedCaller identity, never as the
// principal. The one way the principal reaches a request from the network is
// a delegation certificate a tenant's own CA issued to it (docs/identity-rbac):
// it then acts inside that tenant, never the system tenant, holding the
// delegation's scope intersected with these grants. A node built without
// WithServicePrincipal accepts no delegation.
//
// name is lowercase letters, digits, '.', '_' and '-', starts with a letter
// or digit, is at most 64 bytes and is not a built-in role name; anything
// else refuses to start the node (*ConfigError).
func WithServicePrincipal(name string) NodeOption {
	return func(o *nodeOptions) {
		n := name
		o.servicePrincipal = &n
	}
}

// parseServicePrincipal validates the configured principal's name, returning
// "" when none was configured.
func parseServicePrincipal(name *string) (string, error) {
	if name == nil {
		return "", nil
	}
	// Its grants are declared under its own name, and a built-in role's
	// grants are every account's that holds it.
	if permission.IsBuiltinRole(*name) {
		return "", &ConfigError{Field: servicePrincipalField, Message: "name " + *name + " is a built-in role"}
	}
	// The syntax is the delegation's: a tenant names this principal in the
	// delegations it issues, so the two must accept exactly the same names.
	var invalid *delegation.GrantError
	if err := delegation.ValidatePrincipal(*name); errors.As(err, &invalid) {
		return "", &ConfigError{Field: servicePrincipalField, Message: "name " + invalid.Reason}
	}
	return *name, nil
}

// ServiceContext returns ctx carrying the node's service principal
// (WithServicePrincipal), for calls through the node's embedded client
// (Client). It holds the permissions granted to the principal's name at the
// moment of the call, so a grant registered later is honoured by the next
// ServiceContext. It returns ErrNoServicePrincipal on a node configured
// without one.
func (n *Node) ServiceContext(ctx context.Context) (context.Context, error) {
	if n.servicePrincipal == "" {
		return nil, ErrNoServicePrincipal
	}
	grants := permission.RolePermissions(n.servicePrincipal)
	perms := make([]string, len(grants))
	for i, p := range grants {
		perms[i] = string(p)
	}
	return auth.WithIdentity(ctx, auth.NewServicePrincipal(n.servicePrincipal, tenantid.System, perms)), nil
}
