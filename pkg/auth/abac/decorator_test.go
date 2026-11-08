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

package abac

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// fakeAuthorizer is a controllable inner authorizer that records whether
// it was consulted.
type fakeAuthorizer struct {
	err   error
	calls int
}

func (f *fakeAuthorizer) Authorize(_ context.Context, _ *auth.Identity, _, _ string) error {
	f.calls++
	return f.err
}

// fakeDeniedError is the sentinel the fake inner authorizer denies with.
type fakeDeniedError struct{}

func (fakeDeniedError) Error() string { return "inner: denied" }

func testPolicy(name, tenantID string, rules ...Rule) *Policy {
	return &Policy{
		APIVersion: "abac.qrdb.io/v1",
		Kind:       PolicyKind,
		Metadata:   PolicyMetadata{Name: name, Tenant: tenantID},
		Spec: PolicySpec{
			Subjects: SubjectSelector{Any: true},
			Rules:    rules,
		},
	}
}

func testIdentity(subject, tenantID string, roles ...string) *auth.Identity {
	return &auth.Identity{Subject: subject, TenantID: tenantID, Roles: roles}
}

func TestDecorate_NilInner(t *testing.T) {
	_, err := Decorate(nil)
	if err == nil {
		t.Fatal("Decorate(nil) should fail")
	}
	var nilErr *NilAuthorizerError
	if !errors.As(err, &nilErr) {
		t.Errorf("error = %T, want *NilAuthorizerError", err)
	}
}

func TestDecorate_NoPolicies_PassThrough(t *testing.T) {
	inner := &fakeAuthorizer{}
	authz, err := Decorate(inner)
	if err != nil {
		t.Fatalf("Decorate() error = %v", err)
	}
	if authz != auth.Authorizer(inner) {
		t.Error("Decorate with zero policies should return the inner authorizer unchanged")
	}
}

func TestDecorate_InvalidPolicy(t *testing.T) {
	inner := &fakeAuthorizer{}
	bad := testPolicy("bad", "tenant-a") // no rules
	_, err := Decorate(inner, WithPolicies([]*Policy{bad}))
	if err == nil {
		t.Fatal("Decorate with an invalid policy should fail")
	}
	var compErr *CompilationFailedError
	if !errors.As(err, &compErr) {
		t.Errorf("error = %T, want *CompilationFailedError", err)
	}
}

func TestDecorator_DenyOverridesRBACAllow(t *testing.T) {
	inner := &fakeAuthorizer{} // inner would ALLOW
	policy := testPolicy("guardrail", "tenant-a", Rule{
		Name:      "no-delete",
		Effect:    EffectDeny,
		Actions:   []string{"delete"},
		Resources: []string{"/kv/**"},
	})
	authz, err := Decorate(inner, WithPolicies([]*Policy{policy}))
	if err != nil {
		t.Fatalf("Decorate() error = %v", err)
	}

	ctx := tenant.WithTenantID(context.Background(), "tenant-a")
	err = authz.Authorize(ctx, testIdentity("alice", "tenant-a", "admin"), "/kv/orders/1", "delete")
	if err == nil {
		t.Fatal("explicit deny should override the inner allow")
	}
	var denied *PolicyDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("error = %T, want *PolicyDeniedError", err)
	}
	if denied.Policy != "guardrail" || denied.Rule != "no-delete" {
		t.Errorf("denied by %q/%q, want guardrail/no-delete", denied.Policy, denied.Rule)
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Error("PolicyDeniedError should wrap auth.ErrUnauthorized")
	}
	if !strings.Contains(err.Error(), "guardrail") || !strings.Contains(err.Error(), "no-delete") {
		t.Errorf("error message should name the policy and rule: %v", err)
	}
	if inner.calls != 0 {
		t.Errorf("inner authorizer consulted %d times on explicit deny, want 0", inner.calls)
	}
}

func TestDecorator_AllowBeyondRBACDeny(t *testing.T) {
	inner := &fakeAuthorizer{err: fakeDeniedError{}} // inner would DENY
	policy := testPolicy("grant", "tenant-a", Rule{
		Name:      "read-public",
		Effect:    EffectAllow,
		Actions:   []string{"read"},
		Resources: []string{"/public/**"},
	})
	authz, err := Decorate(inner, WithPolicies([]*Policy{policy}))
	if err != nil {
		t.Fatalf("Decorate() error = %v", err)
	}

	ctx := tenant.WithTenantID(context.Background(), "tenant-a")
	if err := authz.Authorize(ctx, testIdentity("bob", "tenant-a"), "/public/docs/readme", "read"); err != nil {
		t.Errorf("explicit allow should grant beyond the inner deny, got %v", err)
	}
	if inner.calls != 0 {
		t.Errorf("inner authorizer consulted %d times on explicit allow, want 0", inner.calls)
	}
}

func TestDecorator_NoMatchDelegates(t *testing.T) {
	policy := testPolicy("narrow", "tenant-a", Rule{
		Name:      "read-public",
		Effect:    EffectAllow,
		Actions:   []string{"read"},
		Resources: []string{"/public/**"},
	})

	t.Run("inner allows", func(t *testing.T) {
		inner := &fakeAuthorizer{}
		authz, err := Decorate(inner, WithPolicies([]*Policy{policy}))
		if err != nil {
			t.Fatalf("Decorate() error = %v", err)
		}
		ctx := tenant.WithTenantID(context.Background(), "tenant-a")
		if err := authz.Authorize(ctx, testIdentity("bob", "tenant-a"), "/private/x", "write"); err != nil {
			t.Errorf("unmatched request should return the inner decision, got %v", err)
		}
		if inner.calls != 1 {
			t.Errorf("inner consulted %d times, want 1", inner.calls)
		}
	})

	t.Run("inner denies", func(t *testing.T) {
		inner := &fakeAuthorizer{err: fakeDeniedError{}}
		authz, err := Decorate(inner, WithPolicies([]*Policy{policy}))
		if err != nil {
			t.Fatalf("Decorate() error = %v", err)
		}
		ctx := tenant.WithTenantID(context.Background(), "tenant-a")
		err = authz.Authorize(ctx, testIdentity("bob", "tenant-a"), "/private/x", "write")
		if !errors.As(err, &fakeDeniedError{}) {
			t.Errorf("unmatched request should surface the inner error, got %v", err)
		}
		if inner.calls != 1 {
			t.Errorf("inner consulted %d times, want 1", inner.calls)
		}
	})
}

// grantInner is an inner auth.GrantAuthorizer whose Authorize refuses and
// whose AuthorizeGrant admits, as rbac's does for an SO granting keys:sign.
type grantInner struct {
	fakeAuthorizer
	grantCalls int
}

func (g *grantInner) AuthorizeGrant(context.Context, *auth.Identity, string, string) error {
	g.grantCalls++
	return nil
}

// TestDecorator_AuthorizeGrant proves the decorator's grant check evaluates
// the policies as Authorize does, and defers a request none of them decides
// to the inner authorizer's AuthorizeGrant.
func TestDecorator_AuthorizeGrant(t *testing.T) {
	policy := testPolicy("guardrail", "tenant-a", Rule{
		Name:      "no-delete",
		Effect:    EffectDeny,
		Actions:   []string{"delete"},
		Resources: []string{"/kv/**"},
	})
	inner := &grantInner{fakeAuthorizer: fakeAuthorizer{err: fakeDeniedError{}}}
	authz, err := Decorate(inner, WithPolicies([]*Policy{policy}))
	if err != nil {
		t.Fatalf("Decorate() error = %v", err)
	}
	ga, ok := authz.(auth.GrantAuthorizer)
	if !ok {
		t.Fatalf("the decorator %T is not an auth.GrantAuthorizer", authz)
	}
	ctx := tenant.WithTenantID(context.Background(), "tenant-a")
	if err := ga.AuthorizeGrant(ctx, testIdentity("bob", "tenant-a"), "keys", "sign"); err != nil {
		t.Fatalf("unmatched AuthorizeGrant = %v, want the inner grant decision", err)
	}
	if inner.grantCalls != 1 || inner.calls != 0 {
		t.Fatalf("inner AuthorizeGrant %d / Authorize %d calls, want 1 / 0", inner.grantCalls, inner.calls)
	}
	var denied *PolicyDeniedError
	if err := ga.AuthorizeGrant(ctx, testIdentity("bob", "tenant-a"), "/kv/orders/1", "delete"); !errors.As(err, &denied) {
		t.Fatalf("denied AuthorizeGrant = %v, want *PolicyDeniedError", err)
	}
}

func TestDecorator_TenantScoping(t *testing.T) {
	// A deny policy owned by tenant-a must never affect tenant-b.
	inner := &fakeAuthorizer{}
	policy := testPolicy("a-only", "tenant-a", Rule{
		Name:      "deny-all",
		Effect:    EffectDeny,
		Actions:   []string{"*"},
		Resources: []string{"/**"},
	})
	authz, err := Decorate(inner, WithPolicies([]*Policy{policy}))
	if err != nil {
		t.Fatalf("Decorate() error = %v", err)
	}

	ctxA := tenant.WithTenantID(context.Background(), "tenant-a")
	if err := authz.Authorize(ctxA, testIdentity("alice", "tenant-a"), "/kv/x", "read"); err == nil {
		t.Error("tenant-a policy should deny a tenant-a request")
	}

	ctxB := tenant.WithTenantID(context.Background(), "tenant-b")
	if err := authz.Authorize(ctxB, testIdentity("bob", "tenant-b"), "/kv/x", "read"); err != nil {
		t.Errorf("tenant-a policy must not affect tenant-b, got %v", err)
	}
	if inner.calls != 1 {
		t.Errorf("inner consulted %d times, want 1 (tenant-b delegation only)", inner.calls)
	}
}

func TestDecorator_SystemTenantGuardrail(t *testing.T) {
	// A system-tenant policy applies platform-wide, to every target tenant.
	inner := &fakeAuthorizer{}
	policy := testPolicy("platform-guardrail", tenantid.System, Rule{
		Name:      "no-raw-secrets",
		Effect:    EffectDeny,
		Actions:   []string{"read"},
		Resources: []string{"/secrets/**"},
	})
	authz, err := Decorate(inner, WithPolicies([]*Policy{policy}))
	if err != nil {
		t.Fatalf("Decorate() error = %v", err)
	}

	ctx := tenant.WithTenantID(context.Background(), "tenant-a")
	err = authz.Authorize(ctx, testIdentity("alice", "tenant-a"), "/secrets/db-password", "read")
	var denied *PolicyDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("system-tenant guardrail should deny in tenant-a, got %v", err)
	}
	if denied.Policy != "platform-guardrail" {
		t.Errorf("denied by %q, want platform-guardrail", denied.Policy)
	}
	if inner.calls != 0 {
		t.Errorf("inner consulted %d times, want 0", inner.calls)
	}
}

func TestDecorator_SystemTargetEvaluatedOnce(t *testing.T) {
	// Requests without a tenant binding resolve to the system tenant; the
	// system policy set must not be evaluated twice, and its policies apply.
	inner := &fakeAuthorizer{}
	policy := testPolicy("sys", tenantid.System, Rule{
		Name:      "deny-drop",
		Effect:    EffectDeny,
		Actions:   []string{"drop"},
		Resources: []string{"/db/**"},
	})
	authz, err := Decorate(inner, WithPolicies([]*Policy{policy}))
	if err != nil {
		t.Fatalf("Decorate() error = %v", err)
	}
	err = authz.Authorize(context.Background(), testIdentity("root", tenantid.System), "/db/main", "drop")
	var denied *PolicyDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("system policy should deny a system-target request, got %v", err)
	}
	if denied.TenantID != tenantid.System {
		t.Errorf("TenantID = %q, want %q", denied.TenantID, tenantid.System)
	}
}

func TestDecorator_NilIdentityDelegates(t *testing.T) {
	// A nil identity produces an empty subject that matches no policy, so
	// the decision falls through to the inner authorizer.
	inner := &fakeAuthorizer{err: fakeDeniedError{}}
	policy := testPolicy("any", "tenant-a", Rule{
		Name:      "allow-all",
		Effect:    EffectAllow,
		Actions:   []string{"*"},
		Resources: []string{"/**"},
	})
	authz, err := Decorate(inner, WithPolicies([]*Policy{policy}))
	if err != nil {
		t.Fatalf("Decorate() error = %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "tenant-a")
	err = authz.Authorize(ctx, nil, "/kv/x", "read")
	if !errors.As(err, &fakeDeniedError{}) {
		t.Errorf("nil identity should delegate to inner, got %v", err)
	}
	if inner.calls != 1 {
		t.Errorf("inner consulted %d times, want 1", inner.calls)
	}
}

func TestDecorator_SubjectAttributesFromMetadata(t *testing.T) {
	// Identity metadata surfaces as ABAC subject attributes.
	inner := &fakeAuthorizer{err: fakeDeniedError{}}
	policy := &Policy{
		APIVersion: "abac.qrdb.io/v1",
		Kind:       PolicyKind,
		Metadata:   PolicyMetadata{Name: "dept", Tenant: "tenant-a"},
		Spec: PolicySpec{
			Subjects: SubjectSelector{Match: map[string]interface{}{"department": "engineering"}},
			Rules: []Rule{{
				Name:      "eng-read",
				Effect:    EffectAllow,
				Actions:   []string{"read"},
				Resources: []string{"/eng/**"},
			}},
		},
	}
	authz, err := Decorate(inner, WithPolicies([]*Policy{policy}))
	if err != nil {
		t.Fatalf("Decorate() error = %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "tenant-a")

	eng := testIdentity("alice", "tenant-a")
	eng.Metadata = map[string]string{"department": "engineering"}
	if err := authz.Authorize(ctx, eng, "/eng/specs", "read"); err != nil {
		t.Errorf("engineering identity should be allowed, got %v", err)
	}

	sales := testIdentity("bob", "tenant-a")
	sales.Metadata = map[string]string{"department": "sales"}
	if err := authz.Authorize(ctx, sales, "/eng/specs", "read"); !errors.As(err, &fakeDeniedError{}) {
		t.Errorf("sales identity should delegate to inner (deny), got %v", err)
	}
}

// TestDecorator_SubjectAttributesFromIdentityAttributes asserts identity.
// Attributes (a user record's own ABAC attributes) reach a policy through the
// user_attrs namespace, and only through it.
func TestDecorator_SubjectAttributesFromIdentityAttributes(t *testing.T) {
	tests := []struct {
		name     string
		matchKey string
		wantOK   bool
	}{
		{name: "user_attrs namespace matches", matchKey: UserAttributePrefix + "department", wantOK: true},
		{name: "bare key never reads a user attribute", matchKey: "department", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &fakeAuthorizer{err: fakeDeniedError{}}
			policy := &Policy{
				APIVersion: "abac.qrdb.io/v1",
				Kind:       PolicyKind,
				Metadata:   PolicyMetadata{Name: "dept", Tenant: "tenant-a"},
				Spec: PolicySpec{
					Subjects: SubjectSelector{Match: map[string]interface{}{tt.matchKey: "engineering"}},
					Rules: []Rule{{
						Name:      "eng-read",
						Effect:    EffectAllow,
						Actions:   []string{"read"},
						Resources: []string{"/eng/**"},
					}},
				},
			}
			authz, err := Decorate(inner, WithPolicies([]*Policy{policy}))
			if err != nil {
				t.Fatalf("Decorate() error = %v", err)
			}
			ctx := tenant.WithTenantID(context.Background(), "tenant-a")

			eng := testIdentity("alice", "tenant-a")
			eng.Attributes = map[string]string{"department": "engineering"}
			err = authz.Authorize(ctx, eng, "/eng/specs", "read")
			if tt.wantOK && err != nil {
				t.Fatalf("Authorize() = %v, want allowed", err)
			}
			if !tt.wantOK && !errors.As(err, &fakeDeniedError{}) {
				t.Fatalf("Authorize() = %v, want the inner denial", err)
			}
		})
	}
}

// TestDecorator_AttributesCannotForgeAuthenticatedKeys is the forgery test: a
// user-record attribute named after an authentication-derived key must never
// satisfy a policy written against that key — neither by overriding the value
// the authenticator set, nor by supplying one the authenticator did not set.
func TestDecorator_AttributesCannotForgeAuthenticatedKeys(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]string
		matchKey string
		matchVal string
	}{
		{
			name:     "authenticator set the key",
			metadata: map[string]string{"organization": "untrusted-inc"},
			matchKey: "organization",
			matchVal: "trusted-corp",
		},
		{
			name:     "authenticator did not set the key",
			metadata: nil,
			matchKey: "organization",
			matchVal: "trusted-corp",
		},
		{
			name:     "certificate SAN",
			metadata: map[string]string{"dns_san": "client.example.com"},
			matchKey: "dns_san",
			matchVal: "admin.example.com",
		},
		{
			name:     "tenant source sentinel",
			metadata: nil,
			matchKey: "tenant_source",
			matchVal: "certificate",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &fakeAuthorizer{err: fakeDeniedError{}}
			policy := &Policy{
				APIVersion: "abac.qrdb.io/v1",
				Kind:       PolicyKind,
				Metadata:   PolicyMetadata{Name: "trusted", Tenant: "tenant-a"},
				Spec: PolicySpec{
					Subjects: SubjectSelector{Match: map[string]interface{}{tt.matchKey: tt.matchVal}},
					Rules: []Rule{{
						Name:      "trusted-admin",
						Effect:    EffectAllow,
						Actions:   []string{"admin"},
						Resources: []string{"/vault/**"},
					}},
				},
			}
			authz, err := Decorate(inner, WithPolicies([]*Policy{policy}))
			if err != nil {
				t.Fatalf("Decorate() error = %v", err)
			}
			ctx := tenant.WithTenantID(context.Background(), "tenant-a")

			attacker := testIdentity("mallory", "tenant-a")
			attacker.Metadata = tt.metadata
			attacker.Attributes = map[string]string{tt.matchKey: tt.matchVal}

			err = authz.Authorize(ctx, attacker, "/vault/secrets", "admin")
			if !errors.As(err, &fakeDeniedError{}) {
				t.Fatalf("a user attribute %q forged a trusted key: Authorize() = %v", tt.matchKey, err)
			}
		})
	}
}

// TestDecorator_UserAttrsConditionNamespace asserts condition expressions
// reach user-record attributes only as subject.user_attrs.<key>, and that
// subject.attrs.<key> does not see them.
func TestDecorator_UserAttrsConditionNamespace(t *testing.T) {
	tests := []struct {
		name   string
		expr   string
		wantOK bool
	}{
		{name: "user_attrs resolves", expr: "subject.user_attrs.region == \"us-east\"", wantOK: true},
		{name: "attrs does not see user attributes", expr: "subject.attrs.region == \"us-east\"", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &fakeAuthorizer{err: fakeDeniedError{}}
			policy := &Policy{
				APIVersion: "abac.qrdb.io/v1",
				Kind:       PolicyKind,
				Metadata:   PolicyMetadata{Name: "region", Tenant: "tenant-a"},
				Spec: PolicySpec{
					Subjects: SubjectSelector{Authenticated: true},
					Rules: []Rule{{
						Name:      "region-read",
						Effect:    EffectAllow,
						Actions:   []string{"read"},
						Resources: []string{"/geo/**"},
						When:      []Condition{{Expression: tt.expr}},
					}},
				},
			}
			authz, err := Decorate(inner, WithPolicies([]*Policy{policy}))
			if err != nil {
				t.Fatalf("Decorate() error = %v", err)
			}
			ctx := tenant.WithTenantID(context.Background(), "tenant-a")

			id := testIdentity("alice", "tenant-a")
			id.Attributes = map[string]string{"region": "us-east"}
			err = authz.Authorize(ctx, id, "/geo/map", "read")
			if tt.wantOK && err != nil {
				t.Fatalf("Authorize() = %v, want allowed", err)
			}
			if !tt.wantOK && !errors.As(err, &fakeDeniedError{}) {
				t.Fatalf("Authorize() = %v, want the inner denial", err)
			}
		})
	}
}

func TestDecorator_SubjectIDIsUserIDAndNameIsAccount(t *testing.T) {
	// A resolved identity's Subject is its immutable user id: subject.id is
	// that id and subject.name the account name, which a rename changes. An
	// unresolved identity has no account, so its name is its Subject.
	resolved := &auth.Identity{TenantID: "tenant-a"}
	resolved.BindUser(77, "alice")
	tests := []struct {
		name   string
		id     *auth.Identity
		expr   string
		wantOK bool
	}{
		{name: "id is the user id", id: resolved, expr: "subject.id == \"77\"", wantOK: true},
		{name: "id is not the name", id: resolved, expr: "subject.id == \"alice\"", wantOK: false},
		{name: "name is the account name", id: resolved, expr: "subject.name == \"alice\"", wantOK: true},
		{name: "unresolved name is the subject", id: testIdentity("svc", "tenant-a"), expr: "subject.name == \"svc\"", wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &fakeAuthorizer{err: fakeDeniedError{}}
			policy := &Policy{
				APIVersion: "abac.qrdb.io/v1",
				Kind:       PolicyKind,
				Metadata:   PolicyMetadata{Name: "owner", Tenant: "tenant-a"},
				Spec: PolicySpec{
					Subjects: SubjectSelector{Authenticated: true},
					Rules: []Rule{{
						Name:      "owner-read",
						Effect:    EffectAllow,
						Actions:   []string{"read"},
						Resources: []string{"/docs/**"},
						When:      []Condition{{Expression: tt.expr}},
					}},
				},
			}
			authz, err := Decorate(inner, WithPolicies([]*Policy{policy}))
			if err != nil {
				t.Fatalf("Decorate() error = %v", err)
			}
			ctx := tenant.WithTenantID(context.Background(), "tenant-a")
			err = authz.Authorize(ctx, tt.id, "/docs/plan", "read")
			if tt.wantOK && err != nil {
				t.Fatalf("Authorize() = %v, want allowed", err)
			}
			if !tt.wantOK && !errors.As(err, &fakeDeniedError{}) {
				t.Fatalf("Authorize() = %v, want the inner denial", err)
			}
		})
	}
}

func TestDecorate_PolicyDir(t *testing.T) {
	dir := t.TempDir()
	policyYAML := `apiVersion: abac.qrdb.io/v1
kind: Policy
metadata:
  name: dir-guardrail
  tenant: tenant-a
spec:
  subjects:
    any: true
  rules:
    - name: no-purge
      effect: deny
      actions: ["purge"]
      resources: ["/kv/**"]
`
	if err := os.WriteFile(filepath.Join(dir, "guardrail.yaml"), []byte(policyYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	inner := &fakeAuthorizer{}
	authz, err := Decorate(inner, WithPolicyDir(dir))
	if err != nil {
		t.Fatalf("Decorate() error = %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "tenant-a")
	err = authz.Authorize(ctx, testIdentity("alice", "tenant-a"), "/kv/orders", "purge")
	var denied *PolicyDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("dir-loaded policy should deny, got %v", err)
	}
	if denied.Policy != "dir-guardrail" {
		t.Errorf("denied by %q, want dir-guardrail", denied.Policy)
	}
}

func TestDecorate_PolicyDirInvalidFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte(":\tnot yaml {{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Decorate(&fakeAuthorizer{}, WithPolicyDir(dir))
	if err == nil {
		t.Fatal("Decorate with an unparsable policy file should fail")
	}
	var fileErr *PolicyFileError
	if !errors.As(err, &fileErr) {
		t.Errorf("error = %T, want *PolicyFileError", err)
	}
}

func TestDecorate_PolicyDirMissing(t *testing.T) {
	_, err := Decorate(&fakeAuthorizer{}, WithPolicyDir("/nonexistent/abac-policies"))
	if err == nil {
		t.Fatal("Decorate with a missing policy dir should fail")
	}
	var dirErr *PolicyDirError
	if !errors.As(err, &dirErr) {
		t.Errorf("error = %T, want *PolicyDirError", err)
	}
}
