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
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// abacGuardrailPolicy returns a system-tenant policy denying keys:destroy
// and allowing keys:export for any subject.
func abacGuardrailPolicy() *abac.Policy {
	return &abac.Policy{
		APIVersion: "abac.qrdb.io/v1",
		Kind:       abac.PolicyKind,
		Metadata:   abac.PolicyMetadata{Name: "node-guardrail", Tenant: tenantid.System},
		Spec: abac.PolicySpec{
			Subjects: abac.SubjectSelector{Any: true},
			Rules: []abac.Rule{
				{
					Name:      "deny-destroy",
					Effect:    abac.EffectDeny,
					Actions:   []string{"destroy"},
					Resources: []string{"keys"},
				},
				{
					Name:      "allow-export",
					Effect:    abac.EffectAllow,
					Actions:   []string{"export"},
					Resources: []string{"keys"},
				},
			},
		},
	}
}

// TestEmbeddedNode_ABACDecoratesRBAC proves the embedded wiring end to end:
// a node built with WithABACPolicies serves the COMPOSED provider through
// node.Client().RBAC().Authorize — ABAC deny overrides, ABAC allow grants
// beyond RBAC, and unmatched requests still resolve through RBAC.
func TestEmbeddedNode_ABACDecoratesRBAC(t *testing.T) {
	ctx := context.Background()
	n := newMemoryNodeWithUser(t, WithABACPolicies([]*abac.Policy{abacGuardrailPolicy()}))
	svcNode := n.Services()
	require.NotNil(t, svcNode.Role)

	adminRoleID := seedAdminRole(t, svcNode.Role, tenantid.System)
	rbacClient := n.Client().RBAC()
	require.NotNil(t, rbacClient)

	id := &transport.Identity{
		Subject:  "admin",
		TenantID: tenantid.System,
		Roles:    []string{adminRoleID},
	}
	selfCtx := tenant.WithTenantID(ctx, tenantid.System)

	// ABAC deny: keys:destroy matched the deny rule, regardless of RBAC.
	err := rbacClient.Authorize(selfCtx, id, "keys", "destroy")
	require.Error(t, err, "ABAC deny rule must reject keys:destroy")
	var denied *abac.PolicyDeniedError
	require.True(t, errors.As(err, &denied), "expected *abac.PolicyDeniedError, got %v", err)
	assert.Equal(t, "node-guardrail", denied.Policy)
	assert.Equal(t, "deny-destroy", denied.Rule)
	assert.ErrorIs(t, err, auth.ErrUnauthorized)

	// ABAC allow beyond RBAC: keys:export is NOT in the seeded Admin
	// permission set, but the allow rule grants it.
	require.NoError(t, rbacClient.Authorize(selfCtx, id, "keys", "export"),
		"ABAC allow rule must grant keys:export beyond RBAC")

	// Unmatched: tenants:read has no ABAC rule and resolves through RBAC
	// (permitted by the seeded Admin role).
	require.NoError(t, rbacClient.Authorize(selfCtx, id, "tenants", "read"),
		"unmatched request must delegate to RBAC")
}

// TestNodeDecorateABAC_Unconfigured verifies the pass-through path: a node
// without ABAC options hands back the inner authorizer unchanged.
func TestNodeDecorateABAC_Unconfigured(t *testing.T) {
	n := &Node{}
	inner := &auth.NoopAuthorizer{}
	got, err := n.decorateABAC(inner)
	require.NoError(t, err)
	assert.Same(t, auth.Authorizer(inner), got)
}

// TestNodeDecorateABAC_BadPolicyDir verifies the fail-closed path: an
// unreadable policy dir surfaces as *ABACWiringError.
func TestNodeDecorateABAC_BadPolicyDir(t *testing.T) {
	n := &Node{abacPolicyDir: filepath.Join(t.TempDir(), "missing")}
	_, err := n.decorateABAC(&auth.NoopAuthorizer{})
	require.Error(t, err)
	var wireErr *ABACWiringError
	require.True(t, errors.As(err, &wireErr))
	var dirErr *abac.PolicyDirError
	assert.True(t, errors.As(err, &dirErr))
}

// TestNodeDecorateABAC_InvalidPolicy verifies that an invalid injected
// policy aborts wiring with *ABACWiringError.
func TestNodeDecorateABAC_InvalidPolicy(t *testing.T) {
	bad := &abac.Policy{APIVersion: "abac.qrdb.io/v1", Kind: abac.PolicyKind,
		Metadata: abac.PolicyMetadata{Name: "bad", Tenant: "t1"}} // no rules
	n := &Node{abacPolicies: []*abac.Policy{bad}}
	_, err := n.decorateABAC(&auth.NoopAuthorizer{})
	require.Error(t, err)
	var wireErr *ABACWiringError
	require.True(t, errors.As(err, &wireErr))
}

// TestNode_ABACPolicyReloader_Unconfigured verifies that a node built
// without WithABACPolicies, WithABACPolicyDir, or WithABACLiveReload has no
// reload seam: ABACPolicyReloader returns nil.
func TestNode_ABACPolicyReloader_Unconfigured(t *testing.T) {
	n := newMemoryNodeWithUser(t)
	assert.Nil(t, n.ABACPolicyReloader())
}

// TestNode_ABACPolicyReloader_ReachesEmbeddedAuthorizer proves the exported
// reload seam reaches the live decorator wired into the embedded transport:
// after Node.ABACPolicyReloader().Reload swaps in a new policy set, the
// SAME node.Client().RBAC() authorizer immediately enforces the new
// decisions, with no restart.
func TestNode_ABACPolicyReloader_ReachesEmbeddedAuthorizer(t *testing.T) {
	ctx := context.Background()
	setA := []*abac.Policy{abacGuardrailPolicy()} // denies keys:destroy, allows keys:export
	n := newMemoryNodeWithUser(t, WithABACPolicies(setA))
	svcNode := n.Services()

	adminRoleID := seedAdminRole(t, svcNode.Role, tenantid.System)
	rbacClient := n.Client().RBAC()
	require.NotNil(t, rbacClient)

	id := &transport.Identity{
		Subject:  "admin",
		TenantID: tenantid.System,
		Roles:    []string{adminRoleID},
	}
	selfCtx := tenant.WithTenantID(ctx, tenantid.System)

	// Set A is in force: destroy denied, export allowed.
	err := rbacClient.Authorize(selfCtx, id, "keys", "destroy")
	var denied *abac.PolicyDeniedError
	require.True(t, errors.As(err, &denied), "expected set A's deny rule to fire, got %v", err)
	require.NoError(t, rbacClient.Authorize(selfCtx, id, "keys", "export"))

	reloader := n.ABACPolicyReloader()
	require.NotNil(t, reloader, "a node built with WithABACPolicies must expose a reload seam")

	// Reload to a policy set with the OPPOSITE decisions: destroy allowed,
	// export denied.
	setB := []*abac.Policy{{
		APIVersion: "abac.qrdb.io/v1",
		Kind:       abac.PolicyKind,
		Metadata:   abac.PolicyMetadata{Name: "node-guardrail-v2", Tenant: tenantid.System},
		Spec: abac.PolicySpec{
			Subjects: abac.SubjectSelector{Any: true},
			Rules: []abac.Rule{
				{Name: "allow-destroy", Effect: abac.EffectAllow, Actions: []string{"destroy"}, Resources: []string{"keys"}},
				{Name: "deny-export", Effect: abac.EffectDeny, Actions: []string{"export"}, Resources: []string{"keys"}},
			},
		},
	}}
	require.NoError(t, reloader.Reload(setB))

	// The SAME rbacClient, with no restart, now enforces set B.
	require.NoError(t, rbacClient.Authorize(selfCtx, id, "keys", "destroy"),
		"after reload, set B's allow rule must grant keys:destroy")
	err = rbacClient.Authorize(selfCtx, id, "keys", "export")
	require.True(t, errors.As(err, &denied), "after reload, set B's deny rule must reject keys:export, got %v", err)
	assert.Equal(t, "node-guardrail-v2", denied.Policy)
}

// TestNode_ABACPolicyReloader_LiveReloadFromZero proves WithABACLiveReload
// exposes a working reload seam even when the node starts with zero ABAC
// policies, so an embedder can add its first policy after boot instead of
// restarting the process.
func TestNode_ABACPolicyReloader_LiveReloadFromZero(t *testing.T) {
	ctx := context.Background()
	n := newMemoryNodeWithUser(t, WithABACLiveReload())
	svcNode := n.Services()

	adminRoleID := seedAdminRole(t, svcNode.Role, tenantid.System)
	rbacClient := n.Client().RBAC()
	require.NotNil(t, rbacClient)

	id := &transport.Identity{
		Subject:  "admin",
		TenantID: tenantid.System,
		Roles:    []string{adminRoleID},
	}
	selfCtx := tenant.WithTenantID(ctx, tenantid.System)

	// No ABAC policy exists yet: RBAC decides, and the seeded Admin role
	// grants self-scope tenants:read.
	require.NoError(t, rbacClient.Authorize(selfCtx, id, "tenants", "read"))

	reloader := n.ABACPolicyReloader()
	require.NotNil(t, reloader, "WithABACLiveReload must expose a reload seam even with zero initial policies")
	denyRead := testPolicyForTenant("first-policy", tenantid.System, abac.Rule{
		Name:      "deny-tenants-read",
		Effect:    abac.EffectDeny,
		Actions:   []string{"read"},
		Resources: []string{"tenants"},
	})
	require.NoError(t, reloader.Reload([]*abac.Policy{denyRead}))

	// The first policy is now active: tenants:read is denied even though
	// RBAC still grants it.
	err := rbacClient.Authorize(selfCtx, id, "tenants", "read")
	var denied *abac.PolicyDeniedError
	require.True(t, errors.As(err, &denied), "expected the newly-reloaded deny rule to fire, got %v", err)
	assert.Equal(t, "first-policy", denied.Policy)
}

// testPolicyForTenant builds a minimal, valid ABAC policy applying to every
// subject in tenantID.
func testPolicyForTenant(name, tenantID string, rules ...abac.Rule) *abac.Policy {
	return &abac.Policy{
		APIVersion: "abac.qrdb.io/v1",
		Kind:       abac.PolicyKind,
		Metadata:   abac.PolicyMetadata{Name: name, Tenant: tenantID},
		Spec: abac.PolicySpec{
			Subjects: abac.SubjectSelector{Any: true},
			Rules:    rules,
		},
	}
}
