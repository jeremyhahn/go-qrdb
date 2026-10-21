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
	"strconv"
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
	{name: "not", quoted: `"not x"`, want: "not x"},
	{name: "open parenthesis", quoted: `"(open"`, want: "(open"},
	{name: "close parenthesis", quoted: `"close)"`, want: "close)"},
	{name: "comma", quoted: `"a,b"`, want: "a,b"},
	{name: "brackets", quoted: `"[a, b]"`, want: "[a, b]"},
	{name: "single quotes", quoted: `"'a'"`, want: "'a'"},
	{name: "escaped quote", quoted: `"say \"hi\""`, want: `say "hi"`},
	{name: "escaped backslash", quoted: `"back\\slash"`, want: `back\slash`},
	{name: "escaped solidus", quoted: `"a\/b"`, want: "a/b"},
	{name: "escaped backspace", quoted: `"a\bb"`, want: "a\bb"},
	{name: "escaped form feed", quoted: `"a\fb"`, want: "a\fb"},
	{name: "escaped line feed", quoted: `"a\nb"`, want: "a\nb"},
	{name: "escaped carriage return", quoted: `"a\rb"`, want: "a\rb"},
	{name: "escaped tab", quoted: `"a\tb"`, want: "a\tb"},
	{name: "non-ASCII", quoted: "\"é\"", want: "é"},
	// \x5c is a backslash: "\x5cu00e9" holds the JSON escape, not the rune.
	{name: "unicode escape", quoted: "\"\x5cu00e9\"", want: "\xc3\xa9"},
	{name: "unicode escape upper-case hex", quoted: "\"\x5cu00C9\"", want: "\xc3\x89"},
	{name: "unicode escape NUL", quoted: `"a\u0000b"`, want: "a\x00b"},
	{name: "unicode escape control", quoted: `"\u001f"`, want: "\x1f"},
	{name: "unicode escape quote", quoted: "\"\x5cu0022\"", want: `"`},
	{name: "unicode escape noncharacter", quoted: "\"\x5cuffff\"", want: "\xef\xbf\xbf"},
	{name: "surrogate pair", quoted: "\"\x5cud83d\x5cude42\"", want: "\U0001F642"},
	{name: "surrogate pair upper-case hex", quoted: "\"\x5cuD834\x5cuDD1E\"", want: "\U0001D11E"},
	{name: "raw DEL", quoted: "\"\x7f\"", want: "\x7f"},
	{name: "word operators", quoted: `"a in b contains c exists"`, want: "a in b contains c exists"},
	{name: "empty", quoted: `""`, want: ""},
}

// conditionRejectCases are condition expressions whose quoting is rejected,
// with a substring of each rejection's reason.
var conditionRejectCases = []struct {
	name   string
	expr   string
	reason string
	offset int
}{
	{name: "unterminated quote", expr: `subject.id == "abc`, reason: "unterminated quoted value", offset: 14},
	{name: "unterminated after escaped quote", expr: `subject.id == "abc\"`, reason: "unterminated quoted value", offset: 14},
	{name: "unterminated in list", expr: `subject.id in ["a", "b]`, reason: "unterminated quoted value", offset: 20},
	{name: "unterminated left operand", expr: `"abc == subject.id`, reason: "unterminated quoted value", offset: 0},
	{name: "bad escape", expr: `subject.id == "a\x"`, reason: "invalid quoted value", offset: 16},
	{name: "bad escape bell", expr: `subject.id == "\a"`, reason: `unknown escape "\\a"`, offset: 15},
	{name: "bad escape single quote", expr: `subject.id == "\'"`, reason: `unknown escape "\\'"`, offset: 15},
	{name: "bad escape capital U", expr: `subject.id == "\U0001F642"`, reason: `unknown escape "\\U"`, offset: 15},
	{name: "bad escape non-ASCII", expr: `subject.id == "\é"`, reason: `unknown escape "\\\xc3"`, offset: 15},
	{name: "short unicode escape", expr: `subject.id == "\u12"`, reason: "four hex digits", offset: 15},
	{name: "non-hex unicode escape", expr: `subject.id == "\u12g4"`, reason: "four hex digits", offset: 15},
	{name: "signed unicode escape", expr: `subject.id == "\u+123"`, reason: "four hex digits", offset: 15},
	{name: "prefixed unicode escape", expr: `subject.id == "\u0x12"`, reason: "four hex digits", offset: 15},
	{name: "lone high surrogate", expr: `subject.id == "\ud800"`, reason: `unpaired UTF-16 surrogate \ud800`, offset: 15},
	{name: "lone low surrogate", expr: `subject.id == "\udc00x"`, reason: `unpaired UTF-16 surrogate \udc00`, offset: 15},
	{name: "high surrogate then letter", expr: "subject.id == \"\x5cuD800\x5cu0041\"", reason: "unpaired UTF-16 surrogate", offset: 15},
	{name: "surrogates reversed", expr: `subject.id == "\udc00\ud800"`, reason: "unpaired UTF-16 surrogate", offset: 15},
	{name: "two high surrogates", expr: `subject.id == "a\ud83d\ud83d"`, reason: "unpaired UTF-16 surrogate", offset: 16},
	{name: "high surrogate then short escape", expr: `subject.id == "\ud83d\ude4"`, reason: "unpaired UTF-16 surrogate", offset: 15},
	{name: "raw newline", expr: "subject.id == \"a\nb\"", reason: "invalid quoted value", offset: 16},
	{name: "raw tab", expr: "subject.id == \"a\tb\"", reason: "invalid quoted value", offset: 16},
	{name: "raw NUL", expr: "subject.id == \"a\x00b\"", reason: `unescaped control character '\x00'`, offset: 16},
	{name: "raw unit separator", expr: "subject.id == \"\x1f\"", reason: `unescaped control character '\x1f'`, offset: 15},
	{name: "invalid UTF-8", expr: "subject.id == \"\xff\"", reason: "not valid UTF-8", offset: 15},
	{name: "invalid UTF-8 after text", expr: "subject.id == \"ab\xc3\"", reason: "not valid UTF-8", offset: 17},
	{name: "single-quoted value", expr: `subject.id == 'abc'`, reason: `JSON double-quoted string, "abc"`, offset: 14},
	{name: "single-quoted list element", expr: `subject.id in ['a', 'b']`, reason: `JSON double-quoted string, "a"`, offset: 15},
	{name: "single-quoted second list element", expr: `subject.id in ["a",  'b']`, reason: `JSON double-quoted string, "b"`, offset: 21},
	{name: "single-quoted left operand", expr: `'abc' == subject.id`, reason: `JSON double-quoted string, "abc"`, offset: 0},
	{name: "unclosed single quote", expr: `subject.id == 'ab"c`, reason: `JSON double-quoted string, "ab\"c"`, offset: 14},
	{name: "text after quoted value", expr: `subject.id == "a"b`, reason: "must be a whole operand", offset: 14},
	{name: "text before quoted value", expr: `subject.id == a"b"`, reason: "must be a whole operand", offset: 14},
	{name: "bad escape in list element", expr: `subject.id in ["a", "b\q"]`, reason: "invalid quoted value", offset: 22},
	{name: "bad quote in one conjunct", expr: `subject.id == "a" and subject.id == "b\q"`, reason: "invalid quoted value", offset: 16},
	{name: "bad quote under not", expr: `not subject.id == "\x"`, reason: "invalid quoted value", offset: 15},
	{name: "bad quote in exists", expr: `"a\x" exists`, reason: "invalid quoted value", offset: 2},
	{name: "bad quote in field", expr: `"a\x"`, reason: "invalid quoted value", offset: 2},
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
			if parseErr.Offset != tc.offset {
				t.Errorf("Parse(%q) offset = %d in %q, want %d", tc.expr, parseErr.Offset, parseErr.Expression, tc.offset)
			}
			if want := " at byte " + strconv.Itoa(tc.offset) + ": "; !strings.Contains(err.Error(), want) {
				t.Errorf("Parse(%q) error = %q, want it to contain %q", tc.expr, err.Error(), want)
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

// TestParse_QuotedValueHoldsEveryOperator quotes every token the parser
// splits on, from its own operator tables, as a value on either side of a
// comparison and as a list element.
func TestParse_QuotedValueHoldsEveryOperator(t *testing.T) {
	tokens := []string{"not ", "(", ")", ",", "[", "]"}
	for _, logical := range logicalOperators {
		tokens = append(tokens, logical.token)
	}
	for _, comparison := range comparisonOperators {
		tokens = append(tokens, comparison.token)
	}
	for _, existence := range existenceOperators {
		tokens = append(tokens, existence.token)
	}
	all := "x" + strings.Join(tokens, "y") + "z"
	tokens = append(tokens, all)

	parser := NewConditionParser(nil)
	for _, token := range tokens {
		value := "a" + token + "b"
		quoted := quoteValue(value)
		forms := map[string]string{
			"right":        "subject.id == " + quoted,
			"left":         quoted + " == subject.id",
			"list element": "subject.id in [other, " + quoted + "]",
			"conjunct":     "subject.id != other and subject.id == " + quoted,
			"group":        "(subject.id == " + quoted + ") or subject.id == other",
		}
		for form, expr := range forms {
			t.Run(form+"/"+token, func(t *testing.T) {
				cond, err := parser.Parse(expr)
				if err != nil {
					t.Fatalf("Parse(%q) error = %v", expr, err)
				}
				if !cond.Evaluate(&EvalContext{Subject: &Subject{ID: value}}) {
					t.Errorf("Evaluate(%q) with subject.id %q = false, want true", expr, value)
				}
				if cond.Evaluate(&EvalContext{Subject: &Subject{ID: "a"}}) {
					t.Errorf("Evaluate(%q) with subject.id %q = true, want false", expr, "a")
				}
			})
		}
	}
}

// TestParse_QuotedValueMatchesAttribute proves a quoted value compares equal
// to the attribute value it decodes to, beside unquoted operands that keep
// their meaning.
func TestParse_QuotedValueMatchesAttribute(t *testing.T) {
	ctx := &EvalContext{
		Subject: &Subject{
			ID: "alice",
			Attributes: map[string]interface{}{
				"department": "R&D, \"core\" and ops",
				"badge":      "\U0001F642 (lead)",
				"floor":      "3",
			},
		},
		Variables: map[string]interface{}{"owner": "alice"},
	}
	parser := NewConditionParser(nil)
	tests := []struct {
		name string
		expr string
		want bool
	}{
		{name: "escaped quotes and and", expr: `subject.attributes.department == "R&D, \"core\" and ops"`, want: true},
		{name: "surrogate pair escape", expr: `subject.attributes.badge == "` + "\x5cud83d\x5cude42" + ` (lead)"`, want: true},
		{name: "quoted value differs", expr: `subject.attributes.department == "R&D"`, want: false},
		{name: "quoted number is a string", expr: `subject.attributes.floor == "3"`, want: true},
		{
			name: "quoted beside unquoted field",
			expr: `subject.attributes.department == "R&D, \"core\" and ops" and subject.id == alice`,
			want: true,
		},
		{
			name: "quoted beside variable",
			expr: `subject.id == ${owner} and subject.attributes.badge startswith "` + "\x5cud83d\x5cude42" + ` ("`,
			want: true,
		},
		{
			name: "mixed list",
			expr: `subject.attributes.department in [eng, "R&D, \"core\" and ops", sales]`,
			want: true,
		},
		{
			name: "mixed list without the value",
			expr: `subject.attributes.department in [eng, "R&D", "core and ops"]`,
			want: false,
		},
		{
			name: "quoted or unquoted disjunction",
			expr: `subject.attributes.badge == "x or y" or subject.id == alice`,
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond, err := parser.Parse(tt.expr)
			if err != nil {
				t.Fatalf("Parse(%q) error = %v", tt.expr, err)
			}
			if got := cond.Evaluate(ctx); got != tt.want {
				t.Errorf("Evaluate(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestValidateQuoted(t *testing.T) {
	tests := []struct {
		token string
		at    int
	}{
		{token: `""`, at: -1},
		{token: `"\"\\\/\b\f\n\r\t"`, at: -1},
		{token: "\"\x5cu0041\x5cud83d\x5cude42\"", at: -1},
		{token: `"a\u004"`, at: 2},
		{token: `"\ude42\ud83d"`, at: 1},
		{token: "\"a\x01\"", at: 2},
		{token: "\"\xe2\x82\"", at: 1},
	}
	for _, tt := range tests {
		if at, reason := validateQuoted(tt.token); at != tt.at {
			t.Errorf("validateQuoted(%q) = %d %q, want %d", tt.token, at, reason, tt.at)
		}
	}
}

func TestUnicodeEscape(t *testing.T) {
	tests := []struct {
		in   string
		at   int
		want rune
		ok   bool
	}{
		{in: "\x5cu00e9", want: 0xe9, ok: true},
		{in: "x\x5cuFFFF", at: 1, want: 0xffff, ok: true},
		{in: `\u00e`},
		{in: `\x00e9`},
		{in: `\u-0e9`},
		{in: "\x5cu00e9", at: 1},
	}
	for _, tt := range tests {
		got, ok := unicodeEscape(tt.in, tt.at)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("unicodeEscape(%q, %d) = %U %v, want %U %v", tt.in, tt.at, got, ok, tt.want, tt.ok)
		}
	}
}

func TestInvalidUTF8(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{in: "", want: -1},
		{in: "abc é \U0001F642", want: -1},
		{in: "\xef\xbf\xbd", want: -1},
		{in: "\xff", want: 0},
		{in: "ab\xc3", want: 2},
		{in: "é\xed\xa0\x80", want: 2},
	}
	for _, tt := range tests {
		if got := invalidUTF8(tt.in); got != tt.want {
			t.Errorf("invalidUTF8(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestSpan(t *testing.T) {
	tests := []struct {
		expr       string
		start, end int
		lo, hi     int
	}{
		{expr: "a == b", start: 0, end: 1, lo: 0, hi: 1},
		{expr: "a ==  b ", start: 4, end: 8, lo: 6, hi: 7},
		{expr: "a ==   ", start: 4, end: 7, lo: 7, hi: 7},
		{expr: "\xe3\x80\x80x\t", start: 0, end: 5, lo: 3, hi: 4},
	}
	for _, tt := range tests {
		if lo, hi := span(tt.expr, tt.start, tt.end); lo != tt.lo || hi != tt.hi {
			t.Errorf("span(%q, %d, %d) = %d, %d, want %d, %d", tt.expr, tt.start, tt.end, lo, hi, tt.lo, tt.hi)
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
		want int
	}{
		{in: `"a"`, want: -1},
		{in: `"a", "b"`, want: -1},
		{in: `"a`, want: 0},
		{in: `"a", "b`, want: 5},
		{in: `"a\"`, want: 0},
		{in: `"a\\"`, want: -1},
	}
	for _, tt := range tests {
		if got := unterminated(tt.in, 0); got != tt.want {
			t.Errorf("unterminated(%q) = %d, want %d", tt.in, got, tt.want)
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

func FuzzConditionParser(f *testing.F) {
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
	f.Add(`subject.attributes.department in [eng, "R&D, \"core\" and ops"]`)

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
			if parseErr.Offset < 0 || parseErr.Offset > len(parseErr.Expression) {
				t.Fatalf("Parse(%q) offset = %d, outside %q", expr, parseErr.Offset, parseErr.Expression)
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
