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
	"strings"
	"unicode/utf8"
)

// A condition value may be written as an RFC 8259 §7 JSON string: double
// quotes, backslash escapes and \uXXXX. The parser tokenizes before it
// splits: every operator search runs over a mask of the expression in which
// the bytes inside a quoted value are blanked, so a quoted value is one token
// whatever it contains, including an operator, " and ", " or ", a
// parenthesis or a comma. JSON double quotes are the only quoting form; a
// value opening with a single quote is rejected.

// maskByte replaces every byte inside a quoted value in an expression's mask.
// It is no operator, space, parenthesis, bracket or comma, so an operator
// search over the mask never matches text inside a quoted value.
const maskByte = '_'

// operand is one side of a comparison, or the field of an existence or
// boolean condition. A quoted value or a list literal is decoded when the
// condition is parsed and held in literal; anything else is held in expr and
// resolved against the evaluation context.
type operand struct {
	expr      string
	literal   interface{}
	isLiteral bool
}

// resolve returns the operand's value in ctx.
func (o operand) resolve(ctx *EvalContext) interface{} {
	if o.isLiteral {
		return o.literal
	}
	return resolveValue(o.expr, ctx)
}

// quotedEnd returns the index one past the closing quote of the quoted value
// that opens at s[start], or -1 when the value is unterminated.
func quotedEnd(s string, start int) int {
	for i := start + 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return -1
}

// unterminated reports whether any quoted value in s, the first of which
// opens at s[open], is unterminated.
func unterminated(s string, open int) bool {
	for {
		end := quotedEnd(s, open)
		if end < 0 {
			return true
		}
		next := strings.IndexByte(s[end:], '"')
		if next < 0 {
			return false
		}
		open = end + next
	}
}

// maskQuoted returns s with every byte inside a quoted value, between but not
// including its quotes, replaced by maskByte. The result has the length of s,
// so an index into it is an index into s. An unterminated quoted value is
// masked to the end of s.
func maskQuoted(s string) string {
	if strings.IndexByte(s, '"') < 0 {
		return s
	}
	b := []byte(s)
	for i := 0; i < len(b); i++ {
		if b[i] != '"' {
			continue
		}
		end := quotedEnd(s, i)
		last := end - 1
		if end < 0 {
			last = len(b)
		}
		for j := i + 1; j < last; j++ {
			b[j] = maskByte
		}
		i = last
	}
	return string(b)
}

// parseOperand parses one operand of the condition expr: a list literal, a
// quoted value, or an unquoted value resolved when the condition is
// evaluated.
func parseOperand(expr, text string) (operand, error) {
	text = strings.TrimSpace(text)
	mask := maskQuoted(text)
	if len(mask) >= 2 && mask[0] == '[' && mask[len(mask)-1] == ']' {
		list, err := parseList(expr, text[1:len(text)-1])
		if err != nil {
			return operand{}, err
		}
		return operand{literal: list, isLiteral: true}, nil
	}
	value, quoted, err := parseScalar(expr, text)
	if err != nil {
		return operand{}, err
	}
	if quoted {
		return operand{literal: value, isLiteral: true}, nil
	}
	return operand{expr: value}, nil
}

// parseList parses the comma-separated elements of a list literal. An element
// is a quoted value or unquoted text, which is a string as written.
func parseList(expr, inner string) ([]interface{}, error) {
	mask := maskQuoted(inner)
	items := make([]interface{}, 0, strings.Count(mask, ",")+1)
	start := 0
	for i := 0; i <= len(mask); i++ {
		if i < len(mask) && mask[i] != ',' {
			continue
		}
		item, _, err := parseScalar(expr, strings.TrimSpace(inner[start:i]))
		if err != nil {
			return nil, err
		}
		items = append(items, item)
		start = i + 1
	}
	return items, nil
}

// parseScalar parses one value that is not a list. A quoted value must be the
// whole of text; it decodes to its string and quoted is true. Text without a
// double quote is returned as written.
func parseScalar(expr, text string) (value string, quoted bool, err error) {
	if strings.HasPrefix(text, "'") {
		return "", false, singleQuotedError(expr, text)
	}
	open := strings.IndexByte(text, '"')
	if open < 0 {
		return text, false, nil
	}
	if unterminated(text, open) {
		return "", false, &ConditionParseError{Expression: expr, Reason: "unterminated quoted value"}
	}
	end := quotedEnd(text, open)
	if open != 0 || end != len(text) {
		return "", false, &ConditionParseError{
			Expression: expr,
			Reason:     "a quoted value must be a whole operand or list element: " + text,
		}
	}
	value, err = decodeQuoted(expr, text)
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// decodeQuoted decodes token, one RFC 8259 §7 string including its quotes.
// An unknown escape, a raw control character or text that is not UTF-8
// (RFC 8259 §8.1) is rejected.
func decodeQuoted(expr, token string) (string, error) {
	if !utf8.ValidString(token) {
		return "", &ConditionParseError{Expression: expr, Reason: "quoted value is not valid UTF-8"}
	}
	var value string
	if err := json.Unmarshal([]byte(token), &value); err != nil {
		return "", &ConditionParseError{Expression: expr, Reason: "invalid quoted value: " + err.Error()}
	}
	return value, nil
}

// singleQuotedError rejects a value opening with a single quote, naming the
// JSON double-quoted form that writes it.
func singleQuotedError(expr, text string) error {
	inner := text[1:]
	if len(inner) > 0 && inner[len(inner)-1] == '\'' {
		inner = inner[:len(inner)-1]
	}
	return &ConditionParseError{
		Expression: expr,
		Reason: "single-quoted value " + text +
			": quote a condition value as an RFC 8259 JSON double-quoted string, " + quoteValue(inner),
	}
}

// quoteValue returns value as an RFC 8259 JSON string, the form in which a
// condition quotes it.
func quoteValue(value string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	// Encoding a string cannot fail: invalid UTF-8 is written as U+FFFD.
	_ = enc.Encode(value)
	return strings.TrimSuffix(b.String(), "\n")
}
