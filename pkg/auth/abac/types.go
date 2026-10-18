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
	"strings"
	"time"
)

// PolicyKind is the required value of the Kind field on every policy
// document.
const PolicyKind = "Policy"

// Policy is a declarative ABAC policy document. The YAML schema mirrors
// the Kubernetes resource shape: apiVersion / kind / metadata / spec.
type Policy struct {
	// APIVersion is the policy schema version (e.g. "abac.qrdb.io/v1").
	APIVersion string `json:"apiVersion" yaml:"apiVersion"`

	// Kind is the resource type (always "Policy").
	Kind string `json:"kind" yaml:"kind"`

	// Metadata contains policy identification and scoping.
	Metadata PolicyMetadata `json:"metadata" yaml:"metadata"`

	// Spec contains the policy specification.
	Spec PolicySpec `json:"spec" yaml:"spec"`
}

// PolicyMetadata contains policy identification and description.
type PolicyMetadata struct {
	// Name is the unique policy name within the tenant.
	Name string `json:"name" yaml:"name"`

	// Tenant is the owning tenant ID. Policies owned by the reserved
	// system tenant apply platform-wide; every other tenant's policies
	// apply only to requests targeting that tenant.
	Tenant string `json:"tenant" yaml:"tenant"`

	// Description explains the policy's purpose.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Labels are arbitrary key-value labels.
	Labels map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`

	// Priority determines evaluation order (higher = first).
	Priority int `json:"priority,omitempty" yaml:"priority,omitempty"`

	// Created is the policy creation timestamp.
	Created time.Time `json:"created,omitempty" yaml:"created,omitempty"`

	// Updated is the last modification timestamp.
	Updated time.Time `json:"updated,omitempty" yaml:"updated,omitempty"`
}

// PolicySpec contains the policy specification.
type PolicySpec struct {
	// Enabled switches the policy on or off. It is presence-tracked: nil
	// means the document did not set it, which means enabled, so a document
	// written before the field existed keeps its meaning. A policy whose
	// Enabled is false is still validated and compiled, but the evaluator
	// skips it: its deny rules do not deny and its allow rules do not allow.
	// The field is never normalised, so the stored document round-trips
	// exactly as written.
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`

	// Subjects defines who this policy applies to.
	Subjects SubjectSelector `json:"subjects" yaml:"subjects"`

	// Variables defines reusable expressions substituted into resource
	// patterns via ${name} placeholders.
	Variables map[string]string `json:"variables,omitempty" yaml:"variables,omitempty"`

	// Rules defines the authorization rules.
	Rules []Rule `json:"rules" yaml:"rules"`
}

// EnabledSet reports whether the document set Enabled explicitly, as
// opposed to leaving it absent.
func (s *PolicySpec) EnabledSet() bool {
	return s.Enabled != nil
}

// IsEnabled reports the effective value of Enabled: true when the field is
// absent, otherwise the value the document set.
func (s *PolicySpec) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// SubjectSelector defines criteria for matching subjects.
type SubjectSelector struct {
	// Users is the list of specific user IDs.
	Users []string `json:"users,omitempty" yaml:"users,omitempty"`

	// Groups is the list of group IDs.
	Groups []string `json:"groups,omitempty" yaml:"groups,omitempty"`

	// Roles is the list of role IDs.
	Roles []string `json:"roles,omitempty" yaml:"roles,omitempty"`

	// Services is the list of service account IDs.
	Services []string `json:"services,omitempty" yaml:"services,omitempty"`

	// Match contains attribute-based matching criteria.
	Match map[string]interface{} `json:"match,omitempty" yaml:"match,omitempty"`

	// Authenticated matches all authenticated subjects.
	Authenticated bool `json:"authenticated,omitempty" yaml:"authenticated,omitempty"`

	// Any matches all subjects including anonymous.
	Any bool `json:"any,omitempty" yaml:"any,omitempty"`
}

// Rule represents a single authorization rule within a policy.
type Rule struct {
	// Name is the unique rule identifier within the policy.
	Name string `json:"name" yaml:"name"`

	// Effect is the authorization decision (allow or deny).
	Effect Effect `json:"effect" yaml:"effect"`

	// Priority determines rule evaluation order (higher = first).
	Priority int `json:"priority,omitempty" yaml:"priority,omitempty"`

	// Actions is the list of operations this rule applies to. "*" matches
	// every action; a trailing "*" matches by prefix (e.g. "write*").
	Actions []string `json:"actions" yaml:"actions"`

	// Resources is the list of resource glob patterns this rule applies
	// to (exact segments, "*", "**", ${var} captures, in-segment globs).
	Resources []string `json:"resources" yaml:"resources"`

	// When contains conditions that must all be true.
	When []Condition `json:"when,omitempty" yaml:"when,omitempty"`

	// Unless contains conditions that must all be false.
	Unless []Condition `json:"unless,omitempty" yaml:"unless,omitempty"`
}

// Effect represents the authorization decision of a rule.
type Effect string

const (
	// EffectAllow grants access.
	EffectAllow Effect = "allow"

	// EffectDeny denies access.
	EffectDeny Effect = "deny"
)

// Condition represents a policy evaluation condition.
type Condition struct {
	// Expression is the condition expression string.
	Expression string `json:"expression,omitempty" yaml:"expression,omitempty"`
}

// CheckRequest represents an authorization check request.
type CheckRequest struct {
	// Subject is the principal requesting access.
	Subject Subject `json:"subject" yaml:"subject"`

	// Action is the operation being performed.
	Action string `json:"action" yaml:"action"`

	// Resource is the target resource identifier.
	Resource string `json:"resource" yaml:"resource"`

	// Context contains additional request context, resolvable in
	// condition expressions via request.<key>.
	Context map[string]interface{} `json:"context,omitempty" yaml:"context,omitempty"`
}

// Subject represents a principal in an authorization check.
type Subject struct {
	// ID is the subject identifier.
	ID string `json:"id" yaml:"id"`

	// Name is the subject's display name.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Email is the subject's email address.
	Email string `json:"email,omitempty" yaml:"email,omitempty"`

	// TenantID is the subject's tenant.
	TenantID string `json:"tenant_id" yaml:"tenant_id"`

	// Roles is the list of role IDs.
	Roles []string `json:"roles,omitempty" yaml:"roles,omitempty"`

	// Groups is the list of group IDs.
	Groups []string `json:"groups,omitempty" yaml:"groups,omitempty"`

	// Attributes contains the AUTHENTICATION-DERIVED attributes (certificate
	// fields, token claims, middleware sentinels), resolvable in condition
	// expressions via subject.attrs.<key> and in a subject selector's match
	// map by their bare key.
	Attributes map[string]interface{} `json:"attributes,omitempty" yaml:"attributes,omitempty"`

	// UserAttributes contains the attributes stored on the subject's USER
	// RECORD, which an administrator holding roles:assign writes. They live
	// in their own namespace so no value written there can ever be read as
	// an authentication-derived attribute: a condition reaches them only as
	// subject.user_attrs.<key>, and a subject selector only through a match
	// key carrying UserAttributePrefix. A bare key never resolves here.
	UserAttributes map[string]interface{} `json:"user_attributes,omitempty" yaml:"user_attributes,omitempty"`

	// Authenticated indicates whether the subject is authenticated.
	Authenticated bool `json:"authenticated" yaml:"authenticated"`

	// MFAVerified indicates whether MFA was verified.
	MFAVerified bool `json:"mfa_verified,omitempty" yaml:"mfa_verified,omitempty"`

	// MFATimestamp is the last MFA verification time.
	MFATimestamp *time.Time `json:"mfa_timestamp,omitempty" yaml:"mfa_timestamp,omitempty"`

	// LastLogin is the last login timestamp.
	LastLogin *time.Time `json:"last_login,omitempty" yaml:"last_login,omitempty"`
}

// UserAttributePrefix is the subject-selector match-key prefix that selects
// Subject.UserAttributes rather than Subject.Attributes: the match key
// "user_attrs.department" reads the user record's "department" attribute,
// while the bare key "department" reads only an authentication-derived one.
const UserAttributePrefix = "user_attrs."

// attribute resolves a subject-selector match key against the namespace it
// names: a key carrying UserAttributePrefix reads UserAttributes, every other
// key reads Attributes. The two maps are never consulted for the same key, so
// a user-record attribute cannot satisfy a matcher written against an
// authentication-derived attribute of the same name.
func (s *Subject) attribute(key string) (interface{}, bool) {
	if name, ok := strings.CutPrefix(key, UserAttributePrefix); ok {
		v, found := s.UserAttributes[name]
		return v, found
	}
	v, found := s.Attributes[key]
	return v, found
}

// CheckResponse represents an authorization check response.
type CheckResponse struct {
	// Allowed indicates whether access is granted.
	Allowed bool `json:"allowed" yaml:"allowed"`

	// MatchedPolicy is the name of the policy that matched. Empty when no
	// rule matched (default deny / delegate).
	MatchedPolicy string `json:"matched_policy,omitempty" yaml:"matched_policy,omitempty"`

	// MatchedRule is the name of the rule that matched.
	MatchedRule string `json:"matched_rule,omitempty" yaml:"matched_rule,omitempty"`

	// EvaluationTimeNs is the evaluation duration in nanoseconds.
	EvaluationTimeNs int64 `json:"evaluation_time_ns" yaml:"evaluation_time_ns"`

	// Reason provides additional context for the decision.
	Reason string `json:"reason,omitempty" yaml:"reason,omitempty"`
}

// Validate validates a Policy for correctness.
func (p *Policy) Validate() error {
	if p.APIVersion == "" {
		return &InvalidPolicyError{Name: p.Metadata.Name, Reason: "policy apiVersion is required"}
	}
	if p.Kind != PolicyKind {
		return &InvalidPolicyError{Name: p.Metadata.Name, Reason: "policy kind must be 'Policy'"}
	}
	if p.Metadata.Name == "" {
		return &InvalidPolicyError{Reason: "policy name is required"}
	}
	if p.Metadata.Tenant == "" {
		return &InvalidPolicyError{Name: p.Metadata.Name, Reason: "policy tenant is required"}
	}
	if len(p.Spec.Rules) == 0 {
		return &InvalidPolicyError{Name: p.Metadata.Name, Reason: "policy must have at least one rule"}
	}
	for _, rule := range p.Spec.Rules {
		if err := rule.Validate(); err != nil {
			return &InvalidPolicyError{Name: p.Metadata.Name, Reason: "invalid rule", Err: err}
		}
	}
	return nil
}

// Validate validates a Rule for correctness.
func (r *Rule) Validate() error {
	if r.Name == "" {
		return &InvalidRuleError{Reason: "rule name is required"}
	}
	if r.Effect != EffectAllow && r.Effect != EffectDeny {
		return &InvalidRuleError{Rule: r.Name, Reason: "rule effect must be 'allow' or 'deny'"}
	}
	if len(r.Actions) == 0 {
		return &InvalidRuleError{Rule: r.Name, Reason: "rule must have at least one action"}
	}
	if len(r.Resources) == 0 {
		return &InvalidRuleError{Rule: r.Name, Reason: "rule must have at least one resource"}
	}
	return nil
}

// CompiledPolicy is the compiled form of a policy for fast evaluation.
type CompiledPolicy struct {
	Name           string
	Tenant         string
	Priority       int
	SubjectMatcher *SubjectMatcher
	Rules          []*CompiledRule
	Variables      map[string]interface{}
}

// CompiledRule represents a compiled authorization rule.
type CompiledRule struct {
	Name         string
	Effect       Effect
	Priority     int
	Actions      []string
	ResourceTrie *ResourceTrie
	Conditions   []CompiledCondition
	Unless       []CompiledCondition
}

// SubjectMatcher matches subjects against policy selectors using O(1)
// lookup maps built at compile time.
type SubjectMatcher struct {
	Users         map[string]bool
	Groups        map[string]bool
	Roles         map[string]bool
	Services      map[string]bool
	Matchers      []AttributeMatcher
	Authenticated bool
	Any           bool
}

// AttributeMatcher matches subject attributes.
type AttributeMatcher interface {
	Match(subject *Subject) bool
}

// CompiledCondition represents a compiled condition expression.
type CompiledCondition interface {
	Evaluate(ctx *EvalContext) bool
}

// EvalContext provides context for condition evaluation.
type EvalContext struct {
	Subject   *Subject
	Action    string
	Resource  string
	Request   map[string]interface{}
	Time      time.Time
	Variables map[string]interface{}
}
