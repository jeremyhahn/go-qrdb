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
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// Option configures the Decorator returned by Decorate.
type Option func(*decoratorOptions)

// decoratorOptions collects the policy sources supplied to Decorate.
type decoratorOptions struct {
	policyDirs []string
	policies   []*Policy
	liveReload bool
}

// WithPolicyDir loads every YAML policy file directly inside dir at
// construction time. The loaded policies form the initial active set; call
// Decorator.Reload (see WithLiveReload) to replace it later without
// restarting the embedding process.
func WithPolicyDir(dir string) Option {
	return func(o *decoratorOptions) { o.policyDirs = append(o.policyDirs, dir) }
}

// WithPolicies injects already-constructed policies. Intended for embedded
// hosts (xkms, quicbaas) and tests that build policies programmatically.
// The injected policies form the initial active set; call Decorator.Reload
// (see WithLiveReload) to replace it later without restarting the embedding
// process.
func WithPolicies(policies []*Policy) Option {
	return func(o *decoratorOptions) { o.policies = append(o.policies, policies...) }
}

// WithLiveReload keeps Decorate's returned *Decorator wrapper in force even
// when zero policies are configured at construction time. Without this
// option, Decorate returns the inner authorizer unchanged when no policies
// were loaded (zero overhead for unconfigured deployments) — but that bare
// inner authorizer has no Reload method, so an embedder that wants to add
// its first policy after boot (e.g. from a downstream policy CRUD store)
// must request WithLiveReload up front to guarantee a reloadable Decorator
// is always returned, regardless of the initial policy count.
func WithLiveReload() Option {
	return func(o *decoratorOptions) { o.liveReload = true }
}

// Decorate wraps the inner authorizer with ABAC policy evaluation.
//
// Semantics per request (see Decorator.Authorize):
//
//  1. Explicit DENY rule match  → deny immediately (overrides RBAC allow).
//  2. Explicit ALLOW rule match → allow immediately (grants beyond RBAC).
//  3. No rule matched           → delegate to inner (RBAC decides).
//
// When no policies are configured and WithLiveReload was not supplied,
// Decorate returns inner unchanged so an unconfigured deployment pays zero
// overhead. It returns an error when inner is nil or any configured policy
// source fails to load or compile (fail closed — a misconfigured policy set
// never boots an open enforcement path).
//
// The returned *Decorator's active policy set can be swapped at runtime via
// Reload without restarting the embedding process; see Reload and
// PolicyReloader.
func Decorate(inner auth.Authorizer, opts ...Option) (auth.Authorizer, error) {
	if inner == nil {
		return nil, &NilAuthorizerError{}
	}

	var o decoratorOptions
	for _, opt := range opts {
		opt(&o)
	}

	evaluator := NewPolicyEvaluator()
	for _, dir := range o.policyDirs {
		policies, err := LoadDir(dir)
		if err != nil {
			return nil, err
		}
		if err := evaluator.AddPolicies(policies...); err != nil {
			return nil, err
		}
	}
	if err := evaluator.AddPolicies(o.policies...); err != nil {
		return nil, err
	}

	// Zero policies loaded and no live-reload requested: pure pass-through,
	// no wrapper at all.
	if evaluator.Len() == 0 && !o.liveReload {
		return inner, nil
	}

	d := &Decorator{inner: inner}
	d.evaluator.Store(evaluator)
	return d, nil
}

// Decorator is an auth.Authorizer that evaluates ABAC policies before
// delegating undecided requests to an inner (RBAC) authorizer. Construct
// it with Decorate.
//
// The active policy set is held behind an atomic pointer so Authorize reads
// it lock-free and Reload swaps it in a single atomic store: concurrent
// Authorize calls either see the complete previous set or the complete new
// set, never a partially applied reload.
type Decorator struct {
	inner     auth.Authorizer
	evaluator atomic.Pointer[PolicyEvaluator]
}

// Compile-time interface checks.
var (
	_ auth.Authorizer = (*Decorator)(nil)
	_ PolicyReloader  = (*Decorator)(nil)
)

// PolicyReloader is implemented by Decorator to support replacing the
// active ABAC policy set at runtime, without restarting the process that
// embeds it. It is the seam a downstream policy store (e.g. a policy CRUD
// API) drives after persisting a change, so qrdb stays the single
// authorization decision point instead of the embedder caching its own
// stale copy of the decision logic.
type PolicyReloader interface {
	// Reload replaces the active policy set. See Decorator.Reload.
	Reload(policies []*Policy) error
}

// Reload compiles and validates policies into a new policy set and, only on
// success, atomically swaps it in as the set Authorize evaluates against.
// Compilation/validation runs entirely against the new set before the swap,
// so a failed Reload leaves the previously active policy set fully in
// force — Authorize never observes a partially applied reload. The error
// returned is whatever pkg/auth/abac's compiler produced (e.g.
// *CompilationFailedError, *InvalidPolicyError), already a typed error.
//
// Reload is safe for concurrent use with Authorize and with other Reload
// calls: the swap is a single atomic pointer store, and Authorize loads the
// pointer once per call, so readers never take a lock and never see a
// half-built policy set.
func (d *Decorator) Reload(policies []*Policy) error {
	next := NewPolicyEvaluator()
	if err := next.AddPolicies(policies...); err != nil {
		return err
	}
	d.evaluator.Store(next)
	return nil
}

// Authorize evaluates the request against the ABAC policies of the target
// tenant (from tenant.FromContext) plus the reserved system tenant's
// platform-wide policies, then applies the hybrid decision:
//
//   - explicit deny  → *PolicyDeniedError (wraps auth.ErrUnauthorized)
//   - explicit allow → nil
//   - no match       → delegate to the inner authorizer
//
// The check subject is built from the identity: Subject/TenantID/Roles map
// directly, identity Metadata becomes the ABAC attribute map, and
// Authenticated is true only for a non-nil identity. A nil identity
// produces an empty subject that matches no policy, so unauthenticated
// requests always fall through to the inner authorizer's own handling.
// The target tenant is exposed to condition expressions as
// request.tenant.
func (d *Decorator) Authorize(ctx context.Context, identity *auth.Identity, resource, action string) error {
	target := tenant.FromContext(ctx)

	subject := Subject{Authenticated: identity != nil}
	if identity != nil {
		subject.ID = identity.Subject
		subject.TenantID = identity.TenantID
		subject.Roles = identity.Roles
		if len(identity.Metadata) > 0 {
			attrs := make(map[string]interface{}, len(identity.Metadata))
			for k, v := range identity.Metadata {
				attrs[k] = v
			}
			subject.Attributes = attrs
		}
	}

	req := &CheckRequest{
		Subject:  subject,
		Action:   action,
		Resource: resource,
		Context:  map[string]interface{}{"tenant": target},
	}

	// Evaluate the target tenant's policies plus the system tenant's
	// platform-wide guardrails. When the target IS the system tenant the
	// two sets coincide and are evaluated once.
	tenants := []string{target}
	if !tenant.IsSystemTenant(target) {
		tenants = append(tenants, tenant.SystemTenantID)
	}

	resp, err := d.evaluator.Load().EvaluateTenants(ctx, req, tenants)
	if err != nil {
		// Fail closed: an evaluation failure must never grant access or
		// silently bypass the policy layer.
		return &EvaluationError{Err: err}
	}

	switch {
	case resp.Allowed:
		return nil
	case resp.MatchedPolicy != "":
		return &PolicyDeniedError{
			Policy:   resp.MatchedPolicy,
			Rule:     resp.MatchedRule,
			Subject:  subject.ID,
			Resource: resource,
			Action:   action,
			TenantID: target,
		}
	default:
		return d.inner.Authorize(ctx, identity, resource, action)
	}
}
