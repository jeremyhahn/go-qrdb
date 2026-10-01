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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// PolicyCompiler compiles policies into an optimized form for fast
// evaluation. Compilation results are cached by policy fingerprint.
type PolicyCompiler struct {
	mu    sync.RWMutex
	cache map[string]*CompiledPolicy
}

// NewPolicyCompiler creates a new policy compiler.
func NewPolicyCompiler() *PolicyCompiler {
	return &PolicyCompiler{
		cache: make(map[string]*CompiledPolicy),
	}
}

// Compile compiles a policy into its optimized form.
func (c *PolicyCompiler) Compile(policy *Policy) (*CompiledPolicy, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check cache
	fingerprint := computeFingerprint(policy)
	if cached, ok := c.cache[fingerprint]; ok {
		return cached, nil
	}

	// Validate policy first
	if err := policy.Validate(); err != nil {
		return nil, &CompilationFailedError{
			Policy: policy.Metadata.Name,
			Reason: "validation failed",
			Err:    err,
		}
	}

	// Compile subject matcher
	subjectMatcher, err := c.compileSubjectSelector(&policy.Spec.Subjects)
	if err != nil {
		return nil, &CompilationFailedError{
			Policy: policy.Metadata.Name,
			Reason: "failed to compile subject selector",
			Err:    err,
		}
	}

	// Compile rules
	rules := make([]*CompiledRule, 0, len(policy.Spec.Rules))
	for i, rule := range policy.Spec.Rules {
		compiledRule, err := c.compileRule(&rule, policy.Spec.Variables)
		if err != nil {
			return nil, &CompilationFailedError{
				Policy: policy.Metadata.Name,
				Reason: fmt.Sprintf("failed to compile rule %d (%s)", i, rule.Name),
				Err:    err,
			}
		}
		rules = append(rules, compiledRule)
	}

	// Sort rules: deny rules before allow rules, then by priority
	// (higher priority first) within the same effect.
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].Effect == EffectDeny && rules[j].Effect != EffectDeny {
			return true
		}
		if rules[i].Effect != EffectDeny && rules[j].Effect == EffectDeny {
			return false
		}
		return rules[i].Priority > rules[j].Priority
	})

	// Copy variables into the interface-valued map used by EvalContext.
	variables := make(map[string]interface{}, len(policy.Spec.Variables))
	for k, v := range policy.Spec.Variables {
		variables[k] = v
	}

	compiled := &CompiledPolicy{
		Name:           policy.Metadata.Name,
		Tenant:         policy.Metadata.Tenant,
		Priority:       policy.Metadata.Priority,
		SubjectMatcher: subjectMatcher,
		Rules:          rules,
		Variables:      variables,
	}

	// Cache the compiled policy
	c.cache[fingerprint] = compiled

	return compiled, nil
}

// compileSubjectSelector compiles a subject selector into an efficient matcher.
func (c *PolicyCompiler) compileSubjectSelector(selector *SubjectSelector) (*SubjectMatcher, error) {
	matcher := &SubjectMatcher{
		Users:         make(map[string]bool),
		Groups:        make(map[string]bool),
		Roles:         make(map[string]bool),
		Services:      make(map[string]bool),
		Matchers:      make([]AttributeMatcher, 0),
		Authenticated: selector.Authenticated,
		Any:           selector.Any,
	}

	// Build lookup maps for O(1) matching
	for _, user := range selector.Users {
		matcher.Users[user] = true
	}
	for _, group := range selector.Groups {
		matcher.Groups[group] = true
	}
	for _, role := range selector.Roles {
		matcher.Roles[role] = true
	}
	for _, service := range selector.Services {
		matcher.Services[service] = true
	}

	// Compile attribute matchers
	for key, value := range selector.Match {
		attrMatcher, err := c.compileAttributeMatcher(key, value)
		if err != nil {
			return nil, err
		}
		matcher.Matchers = append(matcher.Matchers, attrMatcher)
	}

	return matcher, nil
}

// compileAttributeMatcher creates a matcher for subject attributes.
func (c *PolicyCompiler) compileAttributeMatcher(key string, value interface{}) (AttributeMatcher, error) {
	switch v := value.(type) {
	case string:
		// Exact match
		return &exactAttributeMatcher{key: key, value: v}, nil
	case map[string]interface{}:
		// Complex matcher with operators
		return c.compileComplexAttributeMatcher(key, v)
	default:
		return &exactAttributeMatcher{key: key, value: fmt.Sprintf("%v", v)}, nil
	}
}

func (c *PolicyCompiler) compileComplexAttributeMatcher(key string, matchers map[string]interface{}) (AttributeMatcher, error) {
	// Extract operator and value
	for op, val := range matchers {
		switch op {
		case "gte", ">=":
			return &numericAttributeMatcher{key: key, value: val, op: ">="}, nil
		case "gt", ">":
			return &numericAttributeMatcher{key: key, value: val, op: ">"}, nil
		case "lte", "<=":
			return &numericAttributeMatcher{key: key, value: val, op: "<="}, nil
		case "lt", "<":
			return &numericAttributeMatcher{key: key, value: val, op: "<"}, nil
		case "in":
			return &inAttributeMatcher{key: key, values: toStringSlice(val)}, nil
		case "endswith":
			return &stringAttributeMatcher{key: key, value: fmt.Sprintf("%v", val), op: "endswith"}, nil
		case "startswith":
			return &stringAttributeMatcher{key: key, value: fmt.Sprintf("%v", val), op: "startswith"}, nil
		case "contains":
			return &stringAttributeMatcher{key: key, value: fmt.Sprintf("%v", val), op: "contains"}, nil
		}
	}
	return nil, &AttributeMatcherError{Key: key}
}

// compileRule compiles a policy rule into an optimized form.
func (c *PolicyCompiler) compileRule(rule *Rule, variables map[string]string) (*CompiledRule, error) {
	// Build resource trie for fast pattern matching
	resourceTrie := NewResourceTrie()
	for _, resource := range rule.Resources {
		// Substitute variables in resource patterns
		resource = substituteVariables(resource, variables)
		resourceTrie.Insert(resource, rule.Name)
	}

	// Compile when conditions
	parser := NewConditionParser(nil)
	conditions := make([]CompiledCondition, 0, len(rule.When))
	for _, condExpr := range rule.When {
		cond, err := parser.Parse(condExpr.Expression)
		if err != nil {
			return nil, err
		}
		conditions = append(conditions, cond)
	}

	// Compile unless conditions
	unless := make([]CompiledCondition, 0, len(rule.Unless))
	for _, condExpr := range rule.Unless {
		cond, err := parser.Parse(condExpr.Expression)
		if err != nil {
			return nil, err
		}
		unless = append(unless, cond)
	}

	return &CompiledRule{
		Name:         rule.Name,
		Effect:       rule.Effect,
		Priority:     rule.Priority,
		Actions:      rule.Actions,
		ResourceTrie: resourceTrie,
		Conditions:   conditions,
		Unless:       unless,
	}, nil
}

// CompileCondition compiles a single condition expression.
func (c *PolicyCompiler) CompileCondition(expr string) (CompiledCondition, error) {
	parser := NewConditionParser(nil)
	return parser.Parse(expr)
}

// ClearCache clears the compilation cache.
func (c *PolicyCompiler) ClearCache() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = make(map[string]*CompiledPolicy)
}

// Attribute matcher implementations

type exactAttributeMatcher struct {
	key   string
	value string
}

func (m *exactAttributeMatcher) Match(subject *Subject) bool {
	if val, ok := subject.attribute(m.key); ok {
		return fmt.Sprintf("%v", val) == m.value
	}
	return false
}

type numericAttributeMatcher struct {
	key   string
	value interface{}
	op    string
}

func (m *numericAttributeMatcher) Match(subject *Subject) bool {
	attrVal, ok := subject.attribute(m.key)
	if !ok {
		return false
	}

	left, lok := toFloat64(attrVal)
	right, rok := toFloat64(m.value)
	if !lok || !rok {
		return false
	}

	switch m.op {
	case ">":
		return left > right
	case ">=":
		return left >= right
	case "<":
		return left < right
	case "<=":
		return left <= right
	}
	return false
}

type stringAttributeMatcher struct {
	key   string
	value string
	op    string
}

func (m *stringAttributeMatcher) Match(subject *Subject) bool {
	attrVal, ok := subject.attribute(m.key)
	if !ok {
		return false
	}

	str := fmt.Sprintf("%v", attrVal)

	switch m.op {
	case "endswith":
		return evalEndsWith(str, m.value)
	case "startswith":
		return evalStartsWith(str, m.value)
	case "contains":
		return evalContains(str, m.value)
	}
	return false
}

type inAttributeMatcher struct {
	key    string
	values []string
}

func (m *inAttributeMatcher) Match(subject *Subject) bool {
	attrVal, ok := subject.attribute(m.key)
	if !ok {
		return false
	}

	str := fmt.Sprintf("%v", attrVal)
	for _, v := range m.values {
		if str == v {
			return true
		}
	}
	return false
}

// Helper functions

func substituteVariables(pattern string, variables map[string]string) string {
	result := pattern
	for k, v := range variables {
		result = strings.ReplaceAll(result, "${"+k+"}", v)
	}
	return result
}

func toStringSlice(v interface{}) []string {
	switch val := v.(type) {
	case []string:
		return val
	case []interface{}:
		result := make([]string, len(val))
		for i, item := range val {
			result[i] = fmt.Sprintf("%v", item)
		}
		return result
	case string:
		return []string{val}
	default:
		return []string{fmt.Sprintf("%v", v)}
	}
}

func computeFingerprint(policy *Policy) string {
	// Compute SHA256 hash of policy JSON
	data, err := json.Marshal(policy)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
