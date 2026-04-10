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

import (
	"errors"
	"testing"
)

func TestFilterOpString(t *testing.T) {
	tests := []struct {
		op   FilterOp
		want string
	}{
		{FilterEq, "="},
		{FilterNeq, "!="},
		{FilterGt, ">"},
		{FilterGte, ">="},
		{FilterLt, "<"},
		{FilterLte, "<="},
		{FilterIn, "IN"},
		{FilterBetween, "BETWEEN"},
		{FilterPrefix, "PREFIX"},
		{FilterOp(99), "UNKNOWN"},
	}
	for _, tt := range tests {
		if got := tt.op.String(); got != tt.want {
			t.Errorf("FilterOp(%d).String() = %q, want %q", tt.op, got, tt.want)
		}
	}
}

func TestParseExpressionValid(t *testing.T) {
	fields := map[string]struct{}{
		"Name": {}, "Email": {}, "Age": {}, "Price": {}, "Status": {},
	}

	tests := []struct {
		expr     string
		wantOp   FilterOp
		wantArgs int
		wantFld  string
	}{
		{"Name = ?", FilterEq, 1, "Name"},
		{"Name != ?", FilterNeq, 1, "Name"},
		{"Age > ?", FilterGt, 1, "Age"},
		{"Age >= ?", FilterGte, 1, "Age"},
		{"Age < ?", FilterLt, 1, "Age"},
		{"Age <= ?", FilterLte, 1, "Age"},
		{"Status IN ?", FilterIn, 1, "Status"},
		{"Price BETWEEN ? AND ?", FilterBetween, 2, "Price"},
		{"Name PREFIX ?", FilterPrefix, 1, "Name"},
	}
	for _, tt := range tests {
		field, op, argCount, err := parseExpression(tt.expr, fields)
		if err != nil {
			t.Errorf("parseExpression(%q) error: %v", tt.expr, err)
			continue
		}
		if field != tt.wantFld {
			t.Errorf("parseExpression(%q) field = %q, want %q", tt.expr, field, tt.wantFld)
		}
		if op != tt.wantOp {
			t.Errorf("parseExpression(%q) op = %v, want %v", tt.expr, op, tt.wantOp)
		}
		if argCount != tt.wantArgs {
			t.Errorf("parseExpression(%q) argCount = %d, want %d", tt.expr, argCount, tt.wantArgs)
		}
	}
}

func TestParseExpressionEmptyExpr(t *testing.T) {
	_, _, _, err := parseExpression("", map[string]struct{}{"Name": {}})
	if err == nil {
		t.Fatal("expected error for empty expression")
	}
	var parseErr *ExpressionParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ExpressionParseError, got %T", err)
	}
}

func TestParseExpressionMissingOperator(t *testing.T) {
	_, _, _, err := parseExpression("Name", map[string]struct{}{"Name": {}})
	if err == nil {
		t.Fatal("expected error for missing operator")
	}
}

func TestParseExpressionFieldNotFound(t *testing.T) {
	_, _, _, err := parseExpression("Unknown = ?", map[string]struct{}{"Name": {}})
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	var fieldErr *FieldNotFoundError
	if !errors.As(err, &fieldErr) {
		t.Fatalf("expected FieldNotFoundError, got %T", err)
	}
	if fieldErr.Field != "Unknown" {
		t.Errorf("FieldNotFoundError.Field = %q, want %q", fieldErr.Field, "Unknown")
	}
}

func TestParseExpressionUnsupportedOperator(t *testing.T) {
	_, _, _, err := parseExpression("Name LIKE ?", map[string]struct{}{"Name": {}})
	if err == nil {
		t.Fatal("expected error for unsupported operator")
	}
	var opErr *UnsupportedOperatorError
	if !errors.As(err, &opErr) {
		t.Fatalf("expected UnsupportedOperatorError, got %T", err)
	}
	if opErr.Operator != "LIKE" {
		t.Errorf("UnsupportedOperatorError.Operator = %q, want %q", opErr.Operator, "LIKE")
	}
}

func TestBuildFilterEq(t *testing.T) {
	f, err := buildFilter("Name", FilterEq, 1, []interface{}{"alice"})
	if err != nil {
		t.Fatalf("buildFilter() error: %v", err)
	}
	if f.field != "Name" {
		t.Errorf("field = %q, want %q", f.field, "Name")
	}
	if f.op != FilterEq {
		t.Errorf("op = %v, want %v", f.op, FilterEq)
	}
	if f.value != "alice" {
		t.Errorf("value = %v, want %q", f.value, "alice")
	}
}

func TestBuildFilterBetween(t *testing.T) {
	f, err := buildFilter("Price", FilterBetween, 2, []interface{}{10.0, 100.0})
	if err != nil {
		t.Fatalf("buildFilter() error: %v", err)
	}
	if f.value != 10.0 {
		t.Errorf("value = %v, want %v", f.value, 10.0)
	}
	if f.end != 100.0 {
		t.Errorf("end = %v, want %v", f.end, 100.0)
	}
}

func TestBuildFilterIn(t *testing.T) {
	f, err := buildFilter("Status", FilterIn, 1, []interface{}{[]string{"active", "pending"}})
	if err != nil {
		t.Fatalf("buildFilter() error: %v", err)
	}
	if len(f.values) != 2 {
		t.Fatalf("values len = %d, want 2", len(f.values))
	}
	if f.values[0] != "active" {
		t.Errorf("values[0] = %v, want %q", f.values[0], "active")
	}
}

func TestBuildFilterInsufficientArgs(t *testing.T) {
	_, err := buildFilter("Price", FilterBetween, 2, []interface{}{10.0})
	if err == nil {
		t.Fatal("expected error for insufficient args")
	}
}

func TestFlattenInArgStrings(t *testing.T) {
	result := flattenInArg([]string{"a", "b", "c"})
	if len(result) != 3 {
		t.Fatalf("flattenInArg([]string) len = %d, want 3", len(result))
	}
	if result[0] != "a" {
		t.Errorf("result[0] = %v, want %q", result[0], "a")
	}
}

func TestFlattenInArgInt64(t *testing.T) {
	result := flattenInArg([]int64{1, 2, 3})
	if len(result) != 3 {
		t.Fatalf("flattenInArg([]int64) len = %d, want 3", len(result))
	}
}

func TestFlattenInArgUint64(t *testing.T) {
	result := flattenInArg([]uint64{1, 2, 3})
	if len(result) != 3 {
		t.Fatalf("flattenInArg([]uint64) len = %d, want 3", len(result))
	}
}

func TestFlattenInArgFloat64(t *testing.T) {
	result := flattenInArg([]float64{1.0, 2.0})
	if len(result) != 2 {
		t.Fatalf("flattenInArg([]float64) len = %d, want 2", len(result))
	}
}

func TestFlattenInArgInterface(t *testing.T) {
	input := []interface{}{"a", 1, true}
	result := flattenInArg(input)
	if len(result) != 3 {
		t.Fatalf("flattenInArg([]interface{}) len = %d, want 3", len(result))
	}
}

func TestFlattenInArgScalar(t *testing.T) {
	result := flattenInArg("single")
	if len(result) != 1 {
		t.Fatalf("flattenInArg(scalar) len = %d, want 1", len(result))
	}
}

func TestExpressionParseErrorMessage(t *testing.T) {
	err := &ExpressionParseError{Expr: "test expr", Reason: "bad"}
	got := err.Error()
	if got != "dao: failed to parse expression test expr: bad" {
		t.Errorf("Error() = %q", got)
	}
}

func TestFieldNotFoundErrorMessage(t *testing.T) {
	err := &FieldNotFoundError{Field: "xyz"}
	got := err.Error()
	if got != "dao: field not found: xyz" {
		t.Errorf("Error() = %q", got)
	}
}

func TestUnsupportedOperatorErrorMessage(t *testing.T) {
	err := &UnsupportedOperatorError{Operator: "LIKE"}
	got := err.Error()
	if got != "dao: unsupported operator: LIKE" {
		t.Errorf("Error() = %q", got)
	}
}

// TestParseExpressionSQLInjectionAttempts verifies that parameterized
// expressions prevent injection attacks.
func TestParseExpressionSQLInjectionAttempts(t *testing.T) {
	fields := map[string]struct{}{"Name": {}}

	// Attempts to inject via field name.
	injections := []string{
		"Name; DROP TABLE users = ?",
		"Name' OR '1'='1 = ?",
		"Name\" OR 1=1-- = ?",
	}
	for _, expr := range injections {
		_, _, _, err := parseExpression(expr, fields)
		if err == nil {
			t.Errorf("expected error for injection attempt: %q", expr)
		}
	}
}

// TestParseOperatorAllValid verifies all valid operators parse correctly.
func TestParseOperatorAllValid(t *testing.T) {
	tests := []struct {
		op   string
		want FilterOp
		args int
	}{
		{"=", FilterEq, 1},
		{"!=", FilterNeq, 1},
		{">", FilterGt, 1},
		{">=", FilterGte, 1},
		{"<", FilterLt, 1},
		{"<=", FilterLte, 1},
		{"IN", FilterIn, 1},
		{"BETWEEN", FilterBetween, 2},
		{"PREFIX", FilterPrefix, 1},
	}
	for _, tt := range tests {
		op, args, err := parseOperator(tt.op)
		if err != nil {
			t.Errorf("parseOperator(%q) error: %v", tt.op, err)
			continue
		}
		if op != tt.want {
			t.Errorf("parseOperator(%q) = %v, want %v", tt.op, op, tt.want)
		}
		if args != tt.args {
			t.Errorf("parseOperator(%q) args = %d, want %d", tt.op, args, tt.args)
		}
	}
}
