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

package driver

import (
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// substituteArgs replaces positional ? placeholders in the query with
// the string representation of the corresponding argument values. This
// follows the same approach as go-mysql-server which expects literal
// values rather than parameterized queries.
func substituteArgs(query string, args []driver.NamedValue) (string, error) {
	if len(args) == 0 {
		return query, nil
	}

	var b strings.Builder
	b.Grow(len(query))

	argIdx := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			if argIdx >= len(args) {
				return "", &ArgumentSubstitutionError{
					Index: argIdx,
					Err:   &NotEnoughArgumentsError{Expected: argIdx + 1, Got: len(args)},
				}
			}
			literal, err := formatArg(args[argIdx].Value)
			if err != nil {
				return "", &ArgumentSubstitutionError{Index: argIdx, Err: err}
			}
			b.WriteString(literal)
			argIdx++
		} else if query[i] == '\'' {
			// Skip over quoted strings to avoid replacing ? inside them.
			b.WriteByte(query[i])
			i++
			for i < len(query) {
				b.WriteByte(query[i])
				if query[i] == '\'' {
					break
				}
				i++
			}
		} else {
			b.WriteByte(query[i])
		}
	}

	return b.String(), nil
}

// formatArg converts a driver value to its SQL literal representation.
func formatArg(v interface{}) (string, error) {
	if v == nil {
		return "NULL", nil
	}
	switch val := v.(type) {
	case int64:
		return strconv.FormatInt(val, 10), nil
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64), nil
	case bool:
		if val {
			return "TRUE", nil
		}
		return "FALSE", nil
	case string:
		return "'" + escapeSQL(val) + "'", nil
	case []byte:
		return "'" + escapeSQL(string(val)) + "'", nil
	case time.Time:
		return "'" + val.Format("2006-01-02 15:04:05") + "'", nil
	default:
		return "'" + escapeSQL(fmt.Sprintf("%v", val)) + "'", nil
	}
}

// escapeSQL escapes single quotes for use in SQL string literals.
func escapeSQL(s string) string {
	// Escape backslashes BEFORE quotes: go-mysql-server honors backslash escapes
	// by default, so an embedded or trailing backslash could otherwise terminate
	// the surrounding single-quoted literal early and allow injection. Doubling
	// backslashes first keeps the subsequent quote-doubling unambiguous.
	s = strings.ReplaceAll(s, "\\", "\\\\")
	return strings.ReplaceAll(s, "'", "''")
}
