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
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
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

// unterminated returns the index of the first unterminated quoted value in s,
// the first quoted value of which opens at s[open], or -1 when every quoted
// value in s is terminated.
func unterminated(s string, open int) int {
	for {
		end := quotedEnd(s, open)
		if end < 0 {
			return open
		}
		next := strings.IndexByte(s[end:], '"')
		if next < 0 {
			return -1
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

// span trims the white space from both ends of expr[start:end] and returns
// the bounds of what remains.
func span(expr string, start, end int) (int, int) {
	text := expr[start:end]
	start += len(text) - len(strings.TrimLeftFunc(text, unicode.IsSpace))
	return start, start + len(strings.TrimSpace(text))
}

// parseOperand parses expr[start:end], one operand of the condition expr: a
// list literal, a quoted value, or an unquoted value resolved when the
// condition is evaluated.
func parseOperand(expr string, start, end int) (operand, error) {
	start, end = span(expr, start, end)
	mask := maskQuoted(expr[start:end])
	if len(mask) >= 2 && mask[0] == '[' && mask[len(mask)-1] == ']' {
		list, err := parseList(expr, start+1, end-1)
		if err != nil {
			return operand{}, err
		}
		return operand{literal: list, isLiteral: true}, nil
	}
	value, quoted, err := parseScalar(expr, start, end)
	if err != nil {
		return operand{}, err
	}
	if quoted {
		return operand{literal: value, isLiteral: true}, nil
	}
	return operand{expr: value}, nil
}

// parseList parses the comma-separated elements of a list literal, held in
// expr[start:end]. An element is a quoted value or unquoted text, which is a
// string as written.
func parseList(expr string, start, end int) ([]interface{}, error) {
	mask := maskQuoted(expr[start:end])
	items := make([]interface{}, 0, strings.Count(mask, ",")+1)
	from := start
	for i := start; i <= end; i++ {
		if i < end && mask[i-start] != ',' {
			continue
		}
		lo, hi := span(expr, from, i)
		item, _, err := parseScalar(expr, lo, hi)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
		from = i + 1
	}
	return items, nil
}

// parseScalar parses expr[start:end], one value that is not a list. A quoted
// value must be the whole of it; it decodes to its string and quoted is true.
// Text without a double quote is returned as written.
func parseScalar(expr string, start, end int) (value string, quoted bool, err error) {
	text := expr[start:end]
	if strings.HasPrefix(text, "'") {
		return "", false, singleQuotedError(expr, start, text)
	}
	open := strings.IndexByte(text, '"')
	if open < 0 {
		return text, false, nil
	}
	if at := unterminated(text, open); at >= 0 {
		return "", false, &ConditionParseError{Expression: expr, Offset: start + at, Reason: "unterminated quoted value"}
	}
	if open != 0 || quotedEnd(text, open) != len(text) {
		return "", false, &ConditionParseError{
			Expression: expr,
			Offset:     start,
			Reason:     "a quoted value must be a whole operand or list element: " + text,
		}
	}
	value, err = decodeQuoted(expr, start, text)
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// decodeQuoted decodes token, one RFC 8259 §7 string including its quotes,
// found at expr[start:]. validateQuoted rejects what RFC 8259 does not let a
// string carry; encoding/json then decodes it, implementing the §7 grammar
// exactly.
func decodeQuoted(expr string, start int, token string) (string, error) {
	if at, reason := validateQuoted(token); at >= 0 {
		return "", &ConditionParseError{Expression: expr, Offset: start + at, Reason: reason}
	}
	var value string
	if err := json.Unmarshal([]byte(token), &value); err != nil {
		return "", &ConditionParseError{Expression: expr, Offset: start, Reason: "invalid quoted value: " + err.Error()}
	}
	return value, nil
}

// simpleEscapes are the escapes RFC 8259 §7 defines besides \uXXXX.
var simpleEscapes = map[byte]bool{
	'"': true, '\\': true, '/': true, 'b': true, 'f': true, 'n': true, 'r': true, 't': true,
}

// validateQuoted returns the index in token, one terminated quoted value, of
// the first byte RFC 8259 does not let a string carry, and the reason, or -1:
// text that is not UTF-8 (§8.1), an unescaped control character or an escape
// §7 does not define, and a \uXXXX escape of a UTF-16 surrogate that is not
// half of a pair. §8.2 leaves an unpaired surrogate's meaning unpredictable,
// and encoding/json would decode it to U+FFFD, so a value that compares by
// equality is refused it.
func validateQuoted(token string) (int, string) {
	if at := invalidUTF8(token); at >= 0 {
		return at, "quoted value is not valid UTF-8"
	}
	last := len(token) - 1
	content := token[:last]
	for i := 1; i < last; i++ {
		c := token[i]
		if c < 0x20 {
			return i, "invalid quoted value: unescaped control character " + strconv.QuoteRune(rune(c))
		}
		if c != '\\' {
			continue
		}
		if simpleEscapes[token[i+1]] {
			i++
			continue
		}
		if token[i+1] != 'u' {
			return i, "invalid quoted value: unknown escape " + strconv.QuoteToASCII(token[i:i+2])
		}
		r, ok := unicodeEscape(content, i)
		if !ok {
			return i, `invalid quoted value: a \u escape takes four hex digits`
		}
		if !utf16.IsSurrogate(r) {
			i += 5
			continue
		}
		if low, ok := unicodeEscape(content, i+6); !ok || utf16.DecodeRune(r, low) == unicode.ReplacementChar {
			return i, "invalid quoted value: unpaired UTF-16 surrogate " + content[i:i+6]
		}
		i += 11
	}
	return -1, ""
}

// unicodeEscape returns the UTF-16 code unit of the \uXXXX escape at s[at:].
func unicodeEscape(s string, at int) (rune, bool) {
	if at+6 > len(s) || s[at:at+2] != `\u` {
		return 0, false
	}
	v, err := strconv.ParseUint(s[at+2:at+6], 16, 16)
	return rune(v), err == nil
}

// invalidUTF8 returns the index of the first byte of s that does not begin a
// UTF-8 encoded rune, or -1.
func invalidUTF8(s string) int {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return i
		}
		i += size
	}
	return -1
}

// singleQuotedError rejects text, a value at expr[start:] opening with a
// single quote, naming the JSON double-quoted form that writes it.
func singleQuotedError(expr string, start int, text string) error {
	inner := text[1:]
	if len(inner) > 0 && inner[len(inner)-1] == '\'' {
		inner = inner[:len(inner)-1]
	}
	return &ConditionParseError{
		Expression: expr,
		Offset:     start,
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
