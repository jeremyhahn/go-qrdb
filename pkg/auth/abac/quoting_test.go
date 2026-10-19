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
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// quotedValueCases are quoted condition values, as written, and the string
// each decodes to. Every one holds text that would split an unquoted value.
var quotedValueCases = []struct {
	name   string
	quoted string
	want   string
}{
	{name: "equality operator", quoted: `"a == b"`, want: "a == b"},
	{name: "inequality operator", quoted: `"a != b"`, want: "a != b"},
	{name: "and", quoted: `"x and y"`, want: "x and y"},
	{name: "or", quoted: `"x or y"`, want: "x or y"},
	{name: "open parenthesis", quoted: `"(open"`, want: "(open"},
	{name: "close parenthesis", quoted: `"close)"`, want: "close)"},
	{name: "comma", quoted: `"a,b"`, want: "a,b"},
	{name: "escaped quote", quoted: `"say \"hi\""`, want: `say "hi"`},
	{name: "escaped backslash", quoted: `"back\\slash"`, want: `back\slash`},
	{name: "non-ASCII", quoted: `"é"`, want: "é"},
	{name: "unicode escape", quoted: `"é"`, want: "é"},
	{name: "word operators", quoted: `"a in b contains c exists"`, want: "a in b contains c exists"},
	{name: "empty", quoted: `""`, want: ""},
}

// conditionRejectCases are condition expressions whose quoting is rejected,
// with a substring of each rejection's reason.
var conditionRejectCases = []struct {
	name   string
	expr   string
	reason string
}{
	{name: "unterminated quote", expr: `subject.id == "abc`, reason: "unterminated quoted value"},
	{name: "unterminated after escaped quote", expr: `subject.id == "abc\"`, reason: "unterminated quoted value"},
	{name: "unterminated in list", expr: `subject.id in ["a", "b]`, reason: "unterminated quoted value"},
	{name: "bad escape", expr: `subject.id == "a\x"`, reason: "invalid quoted value"},
	{name: "raw newline", expr: "subject.id == \"a\nb\"", reason: "invalid quoted value"},
	{name: "raw tab", expr: "subject.id == \"a\tb\"", reason: "invalid quoted value"},
	{name: "invalid UTF-8", expr: "subject.id == \"\xff\"", reason: "not valid UTF-8"},
	{name: "single-quoted value", expr: `subject.id == 'abc'`, reason: `JSON double-quoted string, "abc"`},
	{name: "single-quoted list element", expr: `subject.id in ['a', 'b']`, reason: `JSON double-quoted string, "a"`},
	{name: "single-quoted left operand", expr: `'abc' == subject.id`, reason: `JSON double-quoted string, "abc"`},
	{name: "unclosed single quote", expr: `subject.id == 'ab"c`, reason: `JSON double-quoted string, "ab\"c"`},
	{name: "text after quoted value", expr: `subject.id == "a"b`, reason: "must be a whole operand"},
	{name: "text before quoted value", expr: `subject.id == a"b"`, reason: "must be a whole operand"},
	{name: "bad quote in one conjunct", expr: `subject.id == "a" and subject.id == "b\q"`, reason: "invalid quoted value"},
	{name: "bad quote under not", expr: `not subject.id == "\x"`, reason: "invalid quoted value"},
	{name: "bad quote in exists", expr: `"a\x" exists`, reason: "invalid quoted value"},
	{name: "bad quote in field", expr: `"a\x"`, reason: "invalid quoted value"},
}

func mustParseComparison(t *testing.T, expr string) *comparisonCondition {
	t.Helper()
	cond, err := NewConditionParser(nil).Parse(expr)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", expr, err)
	}
	cmp, ok := cond.(*comparisonCondition)
	if !ok {
		t.Fatalf("Parse(%q) = %T, want *comparisonCondition", expr, cond)
	}
	return cmp
}

func TestParse_QuotedValueIsOneToken(t *testing.T) {
	for _, tc := range quotedValueCases {
		t.Run(tc.name, func(t *testing.T) {
			expr := "subject.id == " + tc.quoted
			cmp := mustParseComparison(t, expr)
			if cmp.operator != opEqual || cmp.left.isLiteral || cmp.left.expr != "subject.id" {
				t.Fatalf("Parse(%q) = %+v, want subject.id == literal", expr, cmp)
			}
			if !cmp.right.isLiteral || cmp.right.literal != tc.want {
				t.Fatalf("Parse(%q) right = %+v, want literal %q", expr, cmp.right, tc.want)
			}
			ctx := &EvalContext{Subject: &Subject{ID: tc.want}}
			if !cmp.Evaluate(ctx) {
				t.Errorf("Evaluate(%q) with subject.id %q = false, want true", expr, tc.want)
			}
		})
	}
}

func TestParse_QuotedValuePositions(t *testing.T) {
	parser := NewConditionParser(nil)
	tests := []struct {
		name string
		expr string
		ctx  *EvalContext
		want bool
	}{
		{
			name: "quoted value with and inside a conjunction",
			expr: `subject.id == "a and b" and subject.id != "c or d"`,
			ctx:  &EvalContext{Subject: &Subject{ID: "a and b"}},
			want: true,
		},
		{
			name: "quoted value with or inside a disjunction",
			expr: `subject.id == "x or y" or subject.id == "z"`,
			ctx:  &EvalContext{Subject: &Subject{ID: "x or y"}},
			want: true,
		},
		{
			name: "quoted left operand",
			expr: `"p == q" == subject.id`,
			ctx:  &EvalContext{Subject: &Subject{ID: "p == q"}},
			want: true,
		},
		{
			name: "quoted list elements",
			expr: `subject.id in ["a,b", "c]"]`,
			ctx:  &EvalContext{Subject: &Subject{ID: "a,b"}},
			want: true,
		},
		{
			name: "quoted list element is not split",
			expr: `subject.id in ["a,b", "c]"]`,
			ctx:  &EvalContext{Subject: &Subject{ID: "a"}},
			want: false,
		},
		{
			name: "not over a quoted value",
			expr: `not subject.id == "x and y"`,
			ctx:  &EvalContext{Subject: &Subject{ID: "x"}},
			want: true,
		},
		{
			name: "parenthesis inside a quoted value in a group",
			expr: `(subject.id == "x)") and (subject.id != "(")`,
			ctx:  &EvalContext{Subject: &Subject{ID: "x)"}},
			want: true,
		},
		{
			name: "group within a disjunction",
			expr: `(subject.id == "a" and subject.id != "b") or subject.id == "c"`,
			ctx:  &EvalContext{Subject: &Subject{ID: "c"}},
			want: true,
		},
		{
			name: "quoted startswith pattern",
			expr: `subject.id startswith "a b, c"`,
			ctx:  &EvalContext{Subject: &Subject{ID: "a b, c d"}},
			want: true,
		},
		{
			name: "escaped regex",
			expr: `subject.id matches "^a\\.b$"`,
			ctx:  &EvalContext{Subject: &Subject{ID: "a.b"}},
			want: true,
		},
		{
			name: "escaped regex does not match any byte",
			expr: `subject.id matches "^a\\.b$"`,
			ctx:  &EvalContext{Subject: &Subject{ID: "axb"}},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond, err := parser.Parse(tt.expr)
			if err != nil {
				t.Fatalf("Parse(%q) error = %v", tt.expr, err)
			}
			if got := cond.Evaluate(tt.ctx); got != tt.want {
				t.Errorf("Evaluate(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestParse_UnquotedValueUnchanged(t *testing.T) {
	tests := []struct {
		name      string
		expr      string
		wantRight operand
	}{
		{name: "bare word", expr: "subject.id == alice", wantRight: operand{expr: "alice"}},
		{name: "apostrophe inside", expr: "subject.id == O'Brien", wantRight: operand{expr: "O'Brien"}},
		{name: "field reference", expr: "subject.id == resource.owner", wantRight: operand{expr: "resource.owner"}},
		{name: "variable", expr: "subject.id == ${owner}", wantRight: operand{expr: "${owner}"}},
		{
			name:      "list",
			expr:      "subject.id in [a, b]",
			wantRight: operand{literal: []interface{}{"a", "b"}, isLiteral: true},
		},
		{
			name:      "empty list",
			expr:      "subject.id in []",
			wantRight: operand{literal: []interface{}{""}, isLiteral: true},
		},
		{name: "unclosed list", expr: "subject.id in [a, b", wantRight: operand{expr: "[a, b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmp := mustParseComparison(t, tt.expr)
			if !reflect.DeepEqual(cmp.right, tt.wantRight) {
				t.Errorf("Parse(%q) right = %#v, want %#v", tt.expr, cmp.right, tt.wantRight)
			}
		})
	}
}

func TestParse_QuotingRejected(t *testing.T) {
	parser := NewConditionParser(nil)
	for _, tc := range conditionRejectCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parser.Parse(tc.expr)
			var parseErr *ConditionParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("Parse(%q) error = %v, want *ConditionParseError", tc.expr, err)
			}
			if !strings.Contains(parseErr.Reason, tc.reason) {
				t.Errorf("Parse(%q) reason = %q, want it to contain %q", tc.expr, parseErr.Reason, tc.reason)
			}
		})
	}
}

func TestParse_QuotedValueRoundTrip(t *testing.T) {
	values := []string{
		"", "plain", `a "quoted" word`, `back\slash`, "tab\there", "new\nline",
		"\x00\x1f", "é ü 中文 🙂", "<tag> & </tag>", "a == b and c or d", "[x, y]",
		"'single'", "  ",
	}
	encoders := map[string]func(string) string{
		"quoteValue": quoteValue,
		"json.Marshal": func(v string) string {
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("json.Marshal(%q) error = %v", v, err)
			}
			return string(b)
		},
	}
	for name, encode := range encoders {
		for _, v := range values {
			expr := "subject.id == " + encode(v)
			cmp := mustParseComparison(t, expr)
			if cmp.right.literal != v {
				t.Errorf("%s: Parse(%q) right = %q, want %q", name, expr, cmp.right.literal, v)
			}
		}
	}
}

func TestMaskQuoted(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "a == b", want: "a == b"},
		{in: `a == "b c"`, want: `a == "___"`},
		{in: `"x\"y" or "z"`, want: `"____" or "_"`},
		{in: `a == "open`, want: `a == "____`},
		{in: `a == "\`, want: `a == "_`},
		{in: `""`, want: `""`},
	}
	for _, tt := range tests {
		if got := maskQuoted(tt.in); got != tt.want {
			t.Errorf("maskQuoted(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestUnterminated(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{in: `"a"`, want: false},
		{in: `"a", "b"`, want: false},
		{in: `"a`, want: true},
		{in: `"a", "b`, want: true},
		{in: `"a\"`, want: true},
		{in: `"a\\"`, want: false},
	}
	for _, tt := range tests {
		if got := unterminated(tt.in, 0); got != tt.want {
			t.Errorf("unterminated(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestEnclosed(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{in: "(a)", want: true},
		{in: "((a) and (b))", want: true},
		{in: "(a) and (b)", want: false},
		{in: "(a", want: false},
		{in: "a)", want: false},
		{in: "()", want: true},
		{in: "(", want: false},
		{in: "((a)", want: false},
	}
	for _, tt := range tests {
		if got := enclosed(tt.in); got != tt.want {
			t.Errorf("enclosed(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestQuoteValue(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "abc", want: `"abc"`},
		{in: `a"b\c`, want: `"a\"b\\c"`},
		{in: "<&>", want: `"<&>"`},
		{in: "\n", want: `"\n"`},
	}
	for _, tt := range tests {
		if got := quoteValue(tt.in); got != tt.want {
			t.Errorf("quoteValue(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

func FuzzParseCondition(f *testing.F) {
	for _, tc := range quotedValueCases {
		f.Add("subject.id == " + tc.quoted)
		f.Add(tc.want)
	}
	for _, tc := range conditionRejectCases {
		f.Add(tc.expr)
	}
	f.Add(`(subject.id == "x)") and (subject.id != "(")`)
	f.Add("(a and b) or c")
	f.Add("subject.id in [a, b]")

	parser := NewConditionParser(nil)
	f.Fuzz(func(t *testing.T, expr string) {
		if mask := maskQuoted(expr); len(mask) != len(expr) {
			t.Fatalf("maskQuoted(%q) has length %d, want %d", expr, len(mask), len(expr))
		}
		if _, err := parser.Parse(expr); err != nil {
			var parseErr *ConditionParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("Parse(%q) error = %T %v, want *ConditionParseError", expr, err, err)
			}
		}

		// Any UTF-8 text, quoted, parses back to itself as one value.
		if !utf8.ValidString(expr) {
			return
		}
		quoted := "subject.id == " + quoteValue(expr)
		cond, err := parser.Parse(quoted)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", quoted, err)
		}
		cmp, ok := cond.(*comparisonCondition)
		if !ok || cmp.right.literal != expr {
			t.Fatalf("Parse(%q) = %#v, want right literal %q", quoted, cond, expr)
		}
	})
}
