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
	"github.com/dolthub/vitess/go/vt/sqlparser"
)

// countPlaceholders returns the number of positional ? placeholders a
// statement binds, or -1 when the count cannot be determined.
//
// The count comes from the parsed statement rather than from scanning the SQL
// text. Text scanning has to reimplement the lexer to be right: a question
// mark inside a comment, a backtick-quoted identifier, a double-quoted string,
// or an escaped single quote is not a placeholder, and each is a separate case
// to get wrong. The parser already knows. database/sql checks its caller's
// argument count against this number before the driver runs, so an over-count
// rejects a correct call outright.
//
// Returning -1 for an unparseable statement tells database/sql to skip that
// check and let the engine report the syntax error, which produces a better
// message than a placeholder-count mismatch would.
func countPlaceholders(query string) int {
	parsed, err := sqlparser.Parse(query)
	if err != nil {
		return -1
	}

	count := 0
	walkErr := sqlparser.Walk(func(node sqlparser.SQLNode) (bool, error) {
		if val, ok := node.(*sqlparser.SQLVal); ok && val.Type == sqlparser.ValArg {
			count++
		}
		return true, nil
	}, parsed)
	if walkErr != nil {
		return -1
	}
	return count
}
