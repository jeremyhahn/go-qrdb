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
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ConditionParser parses condition expressions from policy rules.
type ConditionParser struct {
	variables map[string]interface{}
}

// NewConditionParser creates a new condition parser.
func NewConditionParser(variables map[string]interface{}) *ConditionParser {
	return &ConditionParser{
		variables: variables,
	}
}

// logicalOperators are the binary logical operators in the order the parser
// splits on them.
var logicalOperators = []struct {
	token   string
	combine func([]CompiledCondition) CompiledCondition
}{
	{" and ", func(c []CompiledCondition) CompiledCondition { return &andCondition{conditions: c} }},
	{" or ", func(c []CompiledCondition) CompiledCondition { return &orCondition{conditions: c} }},
}

// comparisonOperators are the comparison operators in the order the parser
// searches for them: an operator is matched before any operator it contains.
var comparisonOperators = []struct {
	token string
	op    operatorType
}{
	// Equality operators
	{"==", opEqual},
	{"!=", opNotEqual},
	{" eq ", opEqual},
	{" neq ", opNotEqual},

	// Numeric comparison
	{">=", opGreaterThanOrEqual},
	{"<=", opLessThanOrEqual},
	{">", opGreaterThan},
	{"<", opLessThan},
	{" between ", opBetween},

	// String operators
	{" endswith ", opEndsWith},
	{" startswith ", opStartsWith},
	{" contains ", opContains},
	{" matches ", opMatches},
	{" like ", opLike},

	// Set operators
	{" in ", opIn},
	{" notin ", opNotIn},
	{" containsall ", opContainsAll},
	{" containsany ", opContainsAny},
	{" notcontains ", opNotContains},
	{" overlaps ", opOverlaps},
	{" disjoint ", opDisjoint},
}

// existenceOperators are the postfix existence operators.
var existenceOperators = []struct {
	token       string
	checkExists bool
}{
	{" exists", true},
	{" notexists", false},
}

// Parse parses a condition expression and returns a CompiledCondition.
// A value may be quoted as an RFC 8259 JSON string; quoted text never
// splits the expression (see quoting.go).
func (p *ConditionParser) Parse(expr string) (CompiledCondition, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, &ConditionParseError{Expression: expr, Reason: "empty expression"}
	}
	mask := maskQuoted(expr)

	// An expression enclosed in parentheses is a group.
	if enclosed(mask) {
		return p.Parse(expr[1 : len(expr)-1])
	}

	// Handle logical operators
	if cond, ok, err := p.parseLogical(expr, mask); ok {
		return cond, err
	}

	// Handle comparison operators
	return p.parseComparison(expr, mask)
}

// parseLogical handles logical operators: and, or, not. ok is false when
// expr is not a logical expression.
func (p *ConditionParser) parseLogical(expr, mask string) (cond CompiledCondition, ok bool, err error) {
	// Handle NOT
	if strings.HasPrefix(mask, "not ") {
		inner, err := p.Parse(expr[4:])
		if err != nil {
			return nil, true, err
		}
		return &notCondition{condition: inner}, true, nil
	}

	// Handle AND, then OR, outside parentheses
	for _, logical := range logicalOperators {
		if !strings.Contains(mask, logical.token) {
			continue
		}
		parts := splitByOperator(expr, logical.token)
		if len(parts) < 2 {
			continue
		}
		conditions := make([]CompiledCondition, 0, len(parts))
		for _, part := range parts {
			cond, err := p.Parse(part)
			if err != nil {
				return nil, true, err
			}
			conditions = append(conditions, cond)
		}
		return logical.combine(conditions), true, nil
	}

	return nil, false, nil
}

// parseComparison handles comparison and existence operators, and otherwise
// parses expr as a boolean field reference.
func (p *ConditionParser) parseComparison(expr, mask string) (CompiledCondition, error) {
	for _, comparison := range comparisonOperators {
		idx := strings.Index(mask, comparison.token)
		if idx == -1 {
			continue
		}
		left, err := parseOperand(expr, 0, idx)
		if err != nil {
			return nil, err
		}
		right, err := parseOperand(expr, idx+len(comparison.token), len(expr))
		if err != nil {
			return nil, err
		}
		return &comparisonCondition{left: left, right: right, operator: comparison.op}, nil
	}

	for _, existence := range existenceOperators {
		if !strings.HasSuffix(mask, existence.token) {
			continue
		}
		field, err := parseOperand(expr, 0, len(expr)-len(existence.token))
		if err != nil {
			return nil, err
		}
		return &existsCondition{field: field, checkExists: existence.checkExists}, nil
	}

	// Simple field reference (for boolean fields)
	field, err := parseOperand(expr, 0, len(expr))
	if err != nil {
		return nil, err
	}
	return &fieldCondition{field: field}, nil
}

// enclosed reports whether mask opens with a parenthesis whose match is its
// last byte.
func enclosed(mask string) bool {
	if len(mask) < 2 || mask[0] != '(' || mask[len(mask)-1] != ')' {
		return false
	}
	depth := 0
	for i := 0; i < len(mask); i++ {
		switch mask[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i == len(mask)-1
			}
		}
	}
	return false
}

// operatorType represents the type of comparison operator.
type operatorType int

const (
	opEqual operatorType = iota
	opNotEqual
	opGreaterThan
	opGreaterThanOrEqual
	opLessThan
	opLessThanOrEqual
	opBetween
	opEndsWith
	opStartsWith
	opContains
	opMatches
	opLike
	opIn
	opNotIn
	opContainsAll
	opContainsAny
	opNotContains
	opOverlaps
	opDisjoint
)

// Compiled condition implementations

type andCondition struct {
	conditions []CompiledCondition
}

func (c *andCondition) Evaluate(ctx *EvalContext) bool {
	for _, cond := range c.conditions {
		if !cond.Evaluate(ctx) {
			return false
		}
	}
	return true
}

type orCondition struct {
	conditions []CompiledCondition
}

func (c *orCondition) Evaluate(ctx *EvalContext) bool {
	for _, cond := range c.conditions {
		if cond.Evaluate(ctx) {
			return true
		}
	}
	return false
}

type notCondition struct {
	condition CompiledCondition
}

func (c *notCondition) Evaluate(ctx *EvalContext) bool {
	return !c.condition.Evaluate(ctx)
}

type comparisonCondition struct {
	left     operand
	right    operand
	operator operatorType
}

func (c *comparisonCondition) Evaluate(ctx *EvalContext) bool {
	leftVal := c.left.resolve(ctx)
	rightVal := c.right.resolve(ctx)

	switch c.operator {
	case opEqual:
		return compareEqual(leftVal, rightVal)
	case opNotEqual:
		return !compareEqual(leftVal, rightVal)
	case opGreaterThan:
		return compareGreater(leftVal, rightVal)
	case opGreaterThanOrEqual:
		return compareGreater(leftVal, rightVal) || compareEqual(leftVal, rightVal)
	case opLessThan:
		return compareLess(leftVal, rightVal)
	case opLessThanOrEqual:
		return compareLess(leftVal, rightVal) || compareEqual(leftVal, rightVal)
	case opBetween:
		return evalBetween(leftVal, rightVal)
	case opEndsWith:
		return evalEndsWith(leftVal, rightVal)
	case opStartsWith:
		return evalStartsWith(leftVal, rightVal)
	case opContains:
		return evalContains(leftVal, rightVal)
	case opMatches:
		return evalMatches(leftVal, rightVal)
	case opLike:
		return evalLike(leftVal, rightVal)
	case opIn:
		return evalIn(leftVal, rightVal)
	case opNotIn:
		return !evalIn(leftVal, rightVal)
	case opContainsAll:
		return evalContainsAll(leftVal, rightVal)
	case opContainsAny:
		return evalContainsAny(leftVal, rightVal)
	case opNotContains:
		return !evalContains(leftVal, rightVal)
	case opOverlaps:
		return evalOverlaps(leftVal, rightVal)
	case opDisjoint:
		return !evalOverlaps(leftVal, rightVal)
	}
	return false
}

type existsCondition struct {
	field       operand
	checkExists bool
}

func (c *existsCondition) Evaluate(ctx *EvalContext) bool {
	val := c.field.resolve(ctx)
	exists := val != nil
	return exists == c.checkExists
}

type fieldCondition struct {
	field operand
}

func (c *fieldCondition) Evaluate(ctx *EvalContext) bool {
	val := c.field.resolve(ctx)
	if b, ok := val.(bool); ok {
		return b
	}
	return val != nil
}

// Helper functions

func resolveValue(expr string, ctx *EvalContext) interface{} {
	expr = strings.TrimSpace(expr)

	// Handle variable substitution
	if strings.HasPrefix(expr, "${") && strings.HasSuffix(expr, "}") {
		varName := expr[2 : len(expr)-1]
		if val, ok := ctx.Variables[varName]; ok {
			return val
		}
		return nil
	}

	// Handle field references
	parts := strings.Split(expr, ".")
	if len(parts) == 0 {
		return nil
	}

	switch parts[0] {
	case "subject":
		return resolveSubjectField(parts[1:], ctx.Subject)
	case "resource":
		return resolveResourceField(parts[1:], ctx.Resource, ctx.Request)
	case "request":
		return resolveRequestField(parts[1:], ctx.Request)
	case "time":
		return resolveTimeField(parts[1:], ctx.Time)
	}

	// Try to parse as literal
	if val, err := strconv.ParseInt(expr, 10, 64); err == nil {
		return val
	}
	if val, err := strconv.ParseFloat(expr, 64); err == nil {
		return val
	}
	if val, err := strconv.ParseBool(expr); err == nil {
		return val
	}

	// Return as string
	return expr
}

func resolveSubjectField(parts []string, subject *Subject) interface{} {
	if len(parts) == 0 || subject == nil {
		return nil
	}

	switch parts[0] {
	case "id":
		return subject.ID
	case "name":
		return subject.Name
	case "email":
		return subject.Email
	case "tenant", "tenant_id":
		return subject.TenantID
	case "roles":
		return subject.Roles
	case "groups":
		return subject.Groups
	case "authenticated":
		return subject.Authenticated
	case "mfa_verified":
		return subject.MFAVerified
	case "mfa_timestamp":
		return subject.MFATimestamp
	case "last_login":
		return subject.LastLogin
	case "attrs", "attributes":
		if len(parts) > 1 && subject.Attributes != nil {
			return subject.Attributes[parts[1]]
		}
		return subject.Attributes
	case "user_attrs", "user_attributes":
		if len(parts) > 1 && subject.UserAttributes != nil {
			return subject.UserAttributes[parts[1]]
		}
		return subject.UserAttributes
	}

	return nil
}

func resolveResourceField(parts []string, resource string, request map[string]interface{}) interface{} {
	if len(parts) == 0 {
		return resource
	}

	// For resource fields, check request context
	if request != nil {
		if val, ok := request["resource_"+parts[0]]; ok {
			return val
		}
	}

	return nil
}

func resolveRequestField(parts []string, request map[string]interface{}) interface{} {
	if len(parts) == 0 || request == nil {
		return nil
	}

	return request[parts[0]]
}

func resolveTimeField(parts []string, t time.Time) interface{} {
	if len(parts) == 0 {
		return t
	}

	switch parts[0] {
	case "hour":
		return int64(t.Hour())
	case "minute":
		return int64(t.Minute())
	case "weekday":
		return strings.ToLower(t.Weekday().String()[:3])
	case "date":
		return t.Format("2006-01-02")
	case "since", "until":
		// Not implemented: would require function-call parsing.
		return nil
	}

	return t
}

// Comparison functions

func compareEqual(left, right interface{}) bool {
	return fmt.Sprintf("%v", left) == fmt.Sprintf("%v", right)
}

func compareGreater(left, right interface{}) bool {
	l, lok := toFloat64(left)
	r, rok := toFloat64(right)
	return lok && rok && l > r
}

func compareLess(left, right interface{}) bool {
	l, lok := toFloat64(left)
	r, rok := toFloat64(right)
	return lok && rok && l < r
}

func toFloat64(v interface{}) (float64, bool) {
	switch val := v.(type) {
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case float64:
		return val, true
	case string:
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

func evalBetween(left, right interface{}) bool {
	// Parse range from right: "min..max"
	rangeStr, ok := right.(string)
	if !ok {
		return false
	}

	parts := strings.Split(rangeStr, "..")
	if len(parts) != 2 {
		return false
	}

	val, vok := toFloat64(left)
	lo, lok := toFloat64(strings.TrimSpace(parts[0]))
	hi, hok := toFloat64(strings.TrimSpace(parts[1]))

	return vok && lok && hok && val >= lo && val <= hi
}

func evalEndsWith(left, right interface{}) bool {
	ls, lok := left.(string)
	rs, rok := right.(string)
	return lok && rok && strings.HasSuffix(ls, rs)
}

func evalStartsWith(left, right interface{}) bool {
	ls, lok := left.(string)
	rs, rok := right.(string)
	return lok && rok && strings.HasPrefix(ls, rs)
}

func evalContains(left, right interface{}) bool {
	// String contains
	if ls, lok := left.(string); lok {
		if rs, rok := right.(string); rok {
			return strings.Contains(ls, rs)
		}
	}

	// Slice contains
	if slice, ok := toSlice(left); ok {
		for _, item := range slice {
			if compareEqual(item, right) {
				return true
			}
		}
	}

	return false
}

func evalMatches(left, right interface{}) bool {
	ls, lok := left.(string)
	rs, rok := right.(string)
	if !lok || !rok {
		return false
	}

	re, err := regexp.Compile(rs)
	if err != nil {
		return false
	}

	return re.MatchString(ls)
}

func evalLike(left, right interface{}) bool {
	ls, lok := left.(string)
	pattern, pok := right.(string)
	if !lok || !pok {
		return false
	}

	return matchGlob(pattern, ls)
}

func evalIn(left, right interface{}) bool {
	// Handle CIDR matching for IP addresses
	if ls, lok := left.(string); lok {
		if ip := net.ParseIP(ls); ip != nil {
			// Try to parse right as CIDR
			if rs, rok := right.(string); rok {
				if inCIDR(ip, rs) {
					return true
				}
			}
		}
	}

	// Handle array membership
	rightSlice, ok := toSlice(right)
	if !ok {
		return false
	}

	for _, item := range rightSlice {
		if compareEqual(left, item) {
			return true
		}
	}

	return false
}

func evalContainsAll(left, right interface{}) bool {
	leftSlice, lok := toSlice(left)
	rightSlice, rok := toSlice(right)
	if !lok || !rok {
		return false
	}

	for _, ritem := range rightSlice {
		found := false
		for _, litem := range leftSlice {
			if compareEqual(litem, ritem) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}

func evalContainsAny(left, right interface{}) bool {
	leftSlice, lok := toSlice(left)
	rightSlice, rok := toSlice(right)
	if !lok || !rok {
		return false
	}

	for _, ritem := range rightSlice {
		for _, litem := range leftSlice {
			if compareEqual(litem, ritem) {
				return true
			}
		}
	}

	return false
}

func evalOverlaps(left, right interface{}) bool {
	return evalContainsAny(left, right)
}

func toSlice(v interface{}) ([]interface{}, bool) {
	switch val := v.(type) {
	case []interface{}:
		return val, true
	case []string:
		result := make([]interface{}, len(val))
		for i, s := range val {
			result[i] = s
		}
		return result, true
	case string:
		// Try to parse as array literal: [a, b, c]
		val = strings.TrimSpace(val)
		if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
			val = val[1 : len(val)-1]
			parts := strings.Split(val, ",")
			result := make([]interface{}, len(parts))
			for i, part := range parts {
				part = strings.TrimSpace(part)
				// Strip surrounding quotes if present
				if len(part) >= 2 {
					if (part[0] == '"' && part[len(part)-1] == '"') ||
						(part[0] == '\'' && part[len(part)-1] == '\'') {
						part = part[1 : len(part)-1]
					}
				}
				result[i] = part
			}
			return result, true
		}
	}
	return nil, false
}

func inCIDR(ip net.IP, cidr string) bool {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	return ipNet.Contains(ip)
}

// splitByOperator splits an expression by an operator, respecting parentheses
// and quoted values.
func splitByOperator(expr, op string) []string {
	mask := maskQuoted(expr)
	var parts []string
	depth, start := 0, 0

	for i := 0; i < len(mask); {
		switch {
		case mask[i] == '(':
			depth++
		case mask[i] == ')':
			depth--
		case depth == 0 && strings.HasPrefix(mask[i:], op):
			parts = append(parts, expr[start:i])
			i += len(op)
			start = i
			continue
		}
		i++
	}

	if start < len(expr) {
		parts = append(parts, expr[start:])
	}

	return parts
}
