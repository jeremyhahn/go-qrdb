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
	"sort"
	"sync"
	"time"
)

// PolicyEvaluator evaluates authorization requests against compiled
// policies. Policies are held in an internal tenant-keyed store populated
// via AddPolicies; there is no external storage or distribution layer.
//
// Decision semantics are deny-overrides across ALL applicable policies:
// every deny rule of every applicable policy is evaluated before any allow
// rule, so an allow in one policy can never shadow a deny in another.
type PolicyEvaluator struct {
	compiler *PolicyCompiler
	mu       sync.RWMutex
	policies map[string][]*CompiledPolicy
	count    int
}

// NewPolicyEvaluator creates a new policy evaluator with an empty policy
// store.
func NewPolicyEvaluator() *PolicyEvaluator {
	return &PolicyEvaluator{
		compiler: NewPolicyCompiler(),
		policies: make(map[string][]*CompiledPolicy),
	}
}

// AddPolicies compiles the given policies and adds them to the evaluator's
// store, grouped by their metadata.tenant. Policies within a tenant are
// kept sorted by policy priority (higher first). Returns the first
// compilation error encountered; no policies from the failing call are
// retained.
func (e *PolicyEvaluator) AddPolicies(policies ...*Policy) error {
	compiled := make([]*CompiledPolicy, 0, len(policies))
	for _, policy := range policies {
		cp, err := e.compiler.Compile(policy)
		if err != nil {
			return err
		}
		compiled = append(compiled, cp)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	for _, cp := range compiled {
		e.policies[cp.Tenant] = append(e.policies[cp.Tenant], cp)
		e.count++
	}
	for tenantID := range e.policies {
		list := e.policies[tenantID]
		sort.SliceStable(list, func(i, j int) bool {
			return list[i].Priority > list[j].Priority
		})
	}
	return nil
}

// Len returns the total number of policies in the store.
func (e *PolicyEvaluator) Len() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.count
}

// Reset removes all policies from the store.
func (e *PolicyEvaluator) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.policies = make(map[string][]*CompiledPolicy)
	e.count = 0
}

// Evaluate evaluates an authorization request against the policies of the
// subject's own tenant. Target: single-digit microsecond evaluation time.
func (e *PolicyEvaluator) Evaluate(ctx context.Context, req *CheckRequest) (*CheckResponse, error) {
	var tenants []string
	if req != nil {
		tenants = []string{req.Subject.TenantID}
	}
	return e.EvaluateTenants(ctx, req, tenants)
}

// EvaluateTenants evaluates an authorization request against the union of
// the given tenants' policies, in the order given. The decorator uses this
// to evaluate the TARGET tenant's policies plus the reserved system
// tenant's platform-wide policies.
func (e *PolicyEvaluator) EvaluateTenants(_ context.Context, req *CheckRequest, tenantIDs []string) (*CheckResponse, error) {
	startTime := time.Now()

	// Validate request
	if req == nil || req.Subject.ID == "" {
		return &CheckResponse{
			Allowed:          false,
			Reason:           "invalid request: subject required",
			EvaluationTimeNs: time.Since(startTime).Nanoseconds(),
		}, nil
	}

	policies := e.applicablePolicies(tenantIDs)

	baseCtx := EvalContext{
		Subject:  &req.Subject,
		Action:   req.Action,
		Resource: req.Resource,
		Request:  req.Context,
		Time:     time.Now(),
	}

	// Pass 1: deny rules across ALL applicable policies. An explicit deny
	// anywhere overrides any allow (deny-overrides combining).
	for _, policy := range policies {
		if !e.matchesSubject(policy.SubjectMatcher, &req.Subject) {
			continue
		}
		evalCtx := baseCtx
		evalCtx.Variables = policy.Variables
		for _, rule := range policy.Rules {
			if rule.Effect != EffectDeny {
				continue
			}
			if e.evaluateRule(rule, &evalCtx) {
				return &CheckResponse{
					Allowed:          false,
					Reason:           "denied by policy '" + policy.Name + "', rule '" + rule.Name + "'",
					MatchedPolicy:    policy.Name,
					MatchedRule:      rule.Name,
					EvaluationTimeNs: time.Since(startTime).Nanoseconds(),
				}, nil
			}
		}
	}

	// Pass 2: allow rules; first match wins.
	for _, policy := range policies {
		if !e.matchesSubject(policy.SubjectMatcher, &req.Subject) {
			continue
		}
		evalCtx := baseCtx
		evalCtx.Variables = policy.Variables
		for _, rule := range policy.Rules {
			if rule.Effect != EffectAllow {
				continue
			}
			if e.evaluateRule(rule, &evalCtx) {
				return &CheckResponse{
					Allowed:          true,
					Reason:           "allowed by policy '" + policy.Name + "', rule '" + rule.Name + "'",
					MatchedPolicy:    policy.Name,
					MatchedRule:      rule.Name,
					EvaluationTimeNs: time.Since(startTime).Nanoseconds(),
				}, nil
			}
		}
	}

	// Default deny if no matching allow rule found. MatchedPolicy is empty
	// here, which the Decorator uses to distinguish "no policy has an
	// opinion" (delegate to RBAC) from an explicit deny.
	return &CheckResponse{
		Allowed:          false,
		Reason:           "no matching allow policy",
		EvaluationTimeNs: time.Since(startTime).Nanoseconds(),
	}, nil
}

// applicablePolicies returns the union of the given tenants' compiled
// policies in tenant order, skipping duplicate tenant IDs.
func (e *PolicyEvaluator) applicablePolicies(tenantIDs []string) []*CompiledPolicy {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var result []*CompiledPolicy
	seen := make(map[string]bool, len(tenantIDs))
	for _, tenantID := range tenantIDs {
		if seen[tenantID] {
			continue
		}
		seen[tenantID] = true
		result = append(result, e.policies[tenantID]...)
	}
	return result
}

// evaluateRule checks if a rule matches the current request.
func (e *PolicyEvaluator) evaluateRule(rule *CompiledRule, ctx *EvalContext) bool {
	// Check action match
	if !e.matchesAction(rule.Actions, ctx.Action) {
		return false
	}

	// Check resource match using trie and capture variables
	_, capturedVars, matched := rule.ResourceTrie.Match(ctx.Resource)
	if !matched {
		return false
	}

	// Validate captured variables against subject attributes
	if !e.validateCapturedVars(capturedVars, ctx.Subject) {
		return false
	}

	// Evaluate unless conditions (any true means rule doesn't apply)
	for _, cond := range rule.Unless {
		if cond.Evaluate(ctx) {
			return false
		}
	}

	// Evaluate when conditions (all must be true)
	for _, cond := range rule.Conditions {
		if !cond.Evaluate(ctx) {
			return false
		}
	}

	return true
}

// validateCapturedVars checks if captured variables from resource matching
// match the corresponding subject attributes (e.g., ${subject.id} must match subject.ID).
func (e *PolicyEvaluator) validateCapturedVars(capturedVars map[string]string, subject *Subject) bool {
	for varName, capturedValue := range capturedVars {
		// Check for subject.* variables
		if len(varName) > 8 && varName[:8] == "subject." {
			attrName := varName[8:] // e.g., "id", "email", "tenant_id"
			expectedValue := ""
			switch attrName {
			case "id":
				expectedValue = subject.ID
			case "name":
				expectedValue = subject.Name
			case "email":
				expectedValue = subject.Email
			case "tenant", "tenant_id":
				expectedValue = subject.TenantID
			default:
				// Check in attributes map
				if subject.Attributes != nil {
					if v, ok := subject.Attributes[attrName]; ok {
						if s, ok := v.(string); ok {
							expectedValue = s
						}
					}
				}
			}
			if expectedValue != "" && capturedValue != expectedValue {
				return false
			}
		}
	}
	return true
}

// matchesSubject checks if a subject matches the policy's subject selector.
func (e *PolicyEvaluator) matchesSubject(matcher *SubjectMatcher, subject *Subject) bool {
	// If no matcher is defined, match all subjects (policy applies to everyone)
	if matcher == nil {
		return true
	}

	// Match all
	if matcher.Any {
		return true
	}

	// If no constraints defined (empty matcher), match everyone
	// This handles policies without explicit subject selectors
	if !matcher.Authenticated &&
		len(matcher.Users) == 0 &&
		len(matcher.Groups) == 0 &&
		len(matcher.Roles) == 0 &&
		len(matcher.Services) == 0 &&
		len(matcher.Matchers) == 0 {
		return true
	}

	// Match authenticated requirement
	if matcher.Authenticated && !subject.Authenticated {
		return false
	}

	// Track if we have any specific identity constraints
	hasIdentityConstraints := len(matcher.Users) > 0 || len(matcher.Groups) > 0 ||
		len(matcher.Roles) > 0 || len(matcher.Services) > 0

	// If no identity constraints, check if we just need authentication
	if !hasIdentityConstraints && len(matcher.Matchers) == 0 {
		// Only authenticated requirement - if we got here, authentication passed
		return matcher.Authenticated
	}

	// Match specific users
	if len(matcher.Users) > 0 {
		if _, ok := matcher.Users[subject.ID]; ok {
			return true
		}
	}

	// Match groups
	if len(matcher.Groups) > 0 {
		for _, group := range subject.Groups {
			if _, ok := matcher.Groups[group]; ok {
				return true
			}
		}
	}

	// Match roles
	if len(matcher.Roles) > 0 {
		for _, role := range subject.Roles {
			if _, ok := matcher.Roles[role]; ok {
				return true
			}
		}
	}

	// Match services
	if len(matcher.Services) > 0 {
		if _, ok := matcher.Services[subject.ID]; ok {
			return true
		}
	}

	// If we have identity constraints but none matched, return false
	if hasIdentityConstraints {
		return false
	}

	// Match attribute matchers (all must pass)
	for _, attrMatcher := range matcher.Matchers {
		if !attrMatcher.Match(subject) {
			return false
		}
	}

	// All attribute matchers passed
	return len(matcher.Matchers) > 0
}

// matchesAction checks if the requested action matches any in the rule's action list.
func (e *PolicyEvaluator) matchesAction(ruleActions []string, requestedAction string) bool {
	for _, action := range ruleActions {
		if action == "*" || action == requestedAction {
			return true
		}
		// Support prefix matching (e.g., "write*" matches "write:key")
		if len(action) > 0 && action[len(action)-1] == '*' {
			prefix := action[:len(action)-1]
			if len(requestedAction) >= len(prefix) && requestedAction[:len(prefix)] == prefix {
				return true
			}
		}
	}
	return false
}
