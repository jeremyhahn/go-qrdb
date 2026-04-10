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

package dao

// FilterOp represents comparison operators exposed to callers via
// the fluent query API.
type FilterOp uint8

const (
	// FilterEq represents the equality operator (=).
	FilterEq FilterOp = iota + 1

	// FilterNeq represents the inequality operator (!=).
	FilterNeq

	// FilterGt represents the greater-than operator (>).
	FilterGt

	// FilterGte represents the greater-than-or-equal operator (>=).
	FilterGte

	// FilterLt represents the less-than operator (<).
	FilterLt

	// FilterLte represents the less-than-or-equal operator (<=).
	FilterLte

	// FilterIn represents the IN operator.
	FilterIn

	// FilterBetween represents the BETWEEN operator.
	FilterBetween

	// FilterPrefix represents the PREFIX operator.
	FilterPrefix
)

// filterOpNames maps FilterOp values to their string representations.
var filterOpNames = map[FilterOp]string{
	FilterEq:      "=",
	FilterNeq:     "!=",
	FilterGt:      ">",
	FilterGte:     ">=",
	FilterLt:      "<",
	FilterLte:     "<=",
	FilterIn:      "IN",
	FilterBetween: "BETWEEN",
	FilterPrefix:  "PREFIX",
}

// String returns the string representation of the filter operator.
func (f FilterOp) String() string {
	if name, ok := filterOpNames[f]; ok {
		return name
	}
	return "UNKNOWN"
}

// filter holds a parsed Where expression with validated field, operator,
// and parameterized values. Parsing occurs at Where() call time, not at
// Execute() time.
type filter struct {
	field  string
	op     FilterOp
	value  interface{}
	values []interface{} // for FilterIn
	end    interface{}   // for FilterBetween (upper bound)
}

// parseExpression parses a Where expression string into field name and
// operator using a zero-allocation single-pass byte scan. The expression
// format is: "fieldName op ?" where op is one of =, !=, >, >=, <, <=,
// IN, BETWEEN, PREFIX.
//
// Returns the parsed field name, operator, and required argument count.
// The field name is validated against validFields. The operator is
// validated against the supported operator set.
func parseExpression(expr string, validFields map[string]struct{}) (string, FilterOp, int, error) {
	n := len(expr)
	if n == 0 {
		return "", 0, 0, &ExpressionParseError{Expr: expr, Reason: "empty expression"}
	}

	// Phase 1: scan field name (everything before first space).
	i := 0
	for i < n && expr[i] != ' ' {
		i++
	}
	if i == 0 || i >= n {
		return "", 0, 0, &ExpressionParseError{Expr: expr, Reason: "missing operator"}
	}
	fieldName := expr[:i]

	// Validate field name against entity struct fields.
	if _, ok := validFields[fieldName]; !ok {
		return "", 0, 0, &FieldNotFoundError{Field: fieldName}
	}

	// Skip whitespace after field name.
	for i < n && expr[i] == ' ' {
		i++
	}
	if i >= n {
		return "", 0, 0, &ExpressionParseError{Expr: expr, Reason: "missing operator after field"}
	}

	// Phase 2: scan operator.
	opStart := i
	for i < n && expr[i] != ' ' && expr[i] != '?' {
		i++
	}
	opStr := expr[opStart:i]

	op, argCount, err := parseOperator(opStr)
	if err != nil {
		return "", 0, 0, err
	}

	return fieldName, op, argCount, nil
}

// parseOperator maps an operator string to a FilterOp and its required
// argument count. Uses a direct byte comparison for zero allocation.
func parseOperator(op string) (FilterOp, int, error) {
	switch op {
	case "=":
		return FilterEq, 1, nil
	case "!=":
		return FilterNeq, 1, nil
	case ">":
		return FilterGt, 1, nil
	case ">=":
		return FilterGte, 1, nil
	case "<":
		return FilterLt, 1, nil
	case "<=":
		return FilterLte, 1, nil
	case "IN":
		return FilterIn, 1, nil
	case "BETWEEN":
		return FilterBetween, 2, nil
	case "PREFIX":
		return FilterPrefix, 1, nil
	default:
		return 0, 0, &UnsupportedOperatorError{Operator: op}
	}
}

// buildFilter creates a filter from a parsed expression and arguments.
func buildFilter(field string, op FilterOp, argCount int, args []interface{}) (*filter, error) {
	if len(args) < argCount {
		return nil, &ExpressionParseError{
			Expr:   field + " " + op.String(),
			Reason: "insufficient arguments",
		}
	}

	f := &filter{
		field: field,
		op:    op,
	}

	switch op {
	case FilterIn:
		// For IN, the first arg should be a slice.
		f.values = flattenInArg(args[0])
	case FilterBetween:
		f.value = args[0]
		f.end = args[1]
	default:
		f.value = args[0]
	}

	return f, nil
}

// flattenInArg converts an IN argument to a slice of interface{}.
// Supports []string, []int64, []uint64, []float64, and []interface{}.
func flattenInArg(arg interface{}) []interface{} {
	switch v := arg.(type) {
	case []interface{}:
		return v
	case []string:
		result := make([]interface{}, len(v))
		for i, s := range v {
			result[i] = s
		}
		return result
	case []int64:
		result := make([]interface{}, len(v))
		for i, n := range v {
			result[i] = n
		}
		return result
	case []uint64:
		result := make([]interface{}, len(v))
		for i, n := range v {
			result[i] = n
		}
		return result
	case []float64:
		result := make([]interface{}, len(v))
		for i, n := range v {
			result[i] = n
		}
		return result
	default:
		return []interface{}{arg}
	}
}

// ExpressionParseError is returned when a Where expression cannot be parsed.
type ExpressionParseError struct {
	Expr   string
	Reason string
}

// Error returns a human-readable description.
func (e *ExpressionParseError) Error() string {
	return "dao: failed to parse expression " + e.Expr + ": " + e.Reason
}

// FieldNotFoundError is returned when a Where expression references a
// field that does not exist on the entity struct.
type FieldNotFoundError struct {
	Field string
}

// Error returns a human-readable description.
func (e *FieldNotFoundError) Error() string {
	return "dao: field not found: " + e.Field
}

// UnsupportedOperatorError is returned when a Where expression uses an
// operator that is not in the supported set.
type UnsupportedOperatorError struct {
	Operator string
}

// Error returns a human-readable description.
func (e *UnsupportedOperatorError) Error() string {
	return "dao: unsupported operator: " + e.Operator
}
