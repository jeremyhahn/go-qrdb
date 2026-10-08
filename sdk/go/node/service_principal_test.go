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

//go:build !integration

package node

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

func TestParseServicePrincipal_AcceptsValidNames(t *testing.T) {
	if got, err := parseServicePrincipal(nil); got != "" || err != nil {
		t.Fatalf("unconfigured = (%q, %v), want (\"\", nil)", got, err)
	}
	for _, name := range []string{"xkms", "quicbaas", "trusted-platform", "a", "0x.svc_1", strings.Repeat("a", maxServicePrincipalName)} {
		if got, err := parseServicePrincipal(&name); got != name || err != nil {
			t.Errorf("%q = (%q, %v), want accepted", name, got, err)
		}
	}
}

func TestParseServicePrincipal_RefusesInvalidNames(t *testing.T) {
	for _, name := range []string{
		"", strings.Repeat("a", maxServicePrincipalName+1),
		"so", "admin", "operator", "user", "auditor",
		"XKMS", "-xkms", ".xkms", "_xkms", "xk ms", "xkms:", "qrdb-service:xkms", "xkms/ca", "xkmś",
	} {
		_, err := parseServicePrincipal(&name)
		var cfgErr *ConfigError
		if !errors.As(err, &cfgErr) || cfgErr.Field != servicePrincipalField {
			t.Errorf("%q: err = %v, want *ConfigError on %s", name, err, servicePrincipalField)
		}
	}
}

func TestNewStandaloneNode_RefusesInvalidServicePrincipal(t *testing.T) {
	_, err := NewStandaloneNode(context.Background(), newMemoryNodeConfig(t), nil, WithServicePrincipal("admin"))
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) || cfgErr.Field != servicePrincipalField {
		t.Fatalf("err = %v, want *ConfigError on %s", err, servicePrincipalField)
	}
}

func TestServiceContext_BindsTheGrantedPrincipal(t *testing.T) {
	const name = "svc-context-shape-test"
	permission.RegisterRoleGrant(name, permission.AdminStatus, permission.AdminPeers)
	n := &Node{servicePrincipal: name}

	ctx, err := n.ServiceContext(context.Background())
	if err != nil {
		t.Fatalf("ServiceContext: %v", err)
	}
	id := auth.IdentityFromContext(ctx)
	if !auth.IsServicePrincipal(id) || id.Subject != auth.ServicePrincipalSubjectPrefix+name || id.TenantID != tenantid.System {
		t.Fatalf("identity = %+v, want service principal %q in the system tenant", id, name)
	}
	want := []string{string(permission.AdminPeers), string(permission.AdminStatus)}
	if !slices.Equal(id.ScopePermissions, want) || len(id.Roles) != 0 {
		t.Fatalf("ScopePermissions = %v, Roles = %v; want exactly %v and no roles", id.ScopePermissions, id.Roles, want)
	}
}

func TestServiceContext_WithoutPrincipal(t *testing.T) {
	ctx, err := (&Node{}).ServiceContext(context.Background())
	if !errors.Is(err, ErrNoServicePrincipal) || ctx != nil {
		t.Fatalf("ServiceContext = (%v, %v), want (nil, ErrNoServicePrincipal)", ctx, err)
	}
}
