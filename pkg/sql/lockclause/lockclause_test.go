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

package lockclause

import (
	"context"
	"strconv"
	"testing"

	ast "github.com/dolthub/vitess/go/vt/sqlparser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalize_RewritesForShare(t *testing.T) {
	cases := []struct {
		in, want string
		share    []ShareClause
	}{
		{"SELECT * FROM t WHERE id = 1 FOR SHARE", "SELECT * FROM t WHERE id = 1 LOCK IN SHARE MODE", []ShareClause{{}}},
		{"select * from t for   share nowait", "select * from t LOCK IN SHARE MODE", []ShareClause{{NoWait: true}}},
		{"SELECT * FROM t FOR SHARE SKIP LOCKED", "SELECT * FROM t LOCK IN SHARE MODE", []ShareClause{{SkipLocked: true}}},
		{"SELECT * FROM a JOIN b FOR SHARE OF A, db.`B` NOWAIT", "SELECT * FROM a JOIN b LOCK IN SHARE MODE", []ShareClause{{Of: []string{"a", "b"}, NoWait: true}}},
		{"SELECT 'for share' FROM t", "SELECT 'for share' FROM t", nil},
		{`SELECT "for share" FROM t -- for share`, `SELECT "for share" FROM t -- for share`, nil},
		{"SELECT `for` FROM t /* for share */", "SELECT `for` FROM t /* for share */", nil},
		{"SELECT * FROM shares", "SELECT * FROM shares", nil},
		{"SELECT * FROM t LOCK IN SHARE MODE", "SELECT * FROM t LOCK IN SHARE MODE", nil},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			n := Normalize(c.in)
			assert.Equal(t, c.want, n.Query)
			assert.Equal(t, c.share, n.Share)
		})
	}
}

func TestNormalize_OriginalOffset(t *testing.T) {
	orig := "SELECT 1 FROM t FOR SHARE; SELECT 2 FROM u FOR SHARE NOWAIT; SELECT 3"
	n := Normalize(orig)
	require.Len(t, n.Share, 2)
	// Every boundary that follows a rewrite maps back to the same text.
	for _, marker := range []string{"; SELECT 2", "; SELECT 3"} {
		rw := indexOf(n.Query, marker)
		require.GreaterOrEqual(t, rw, 0)
		assert.Equal(t, marker, orig[n.OriginalOffset(rw):n.OriginalOffset(rw)+len(marker)])
	}
	assert.Equal(t, 3, n.OriginalOffset(3), "before any rewrite the offset is unchanged")
	inside := indexOf(n.Query, "IN SHARE")
	assert.Equal(t, indexOf(orig, "; SELECT 2"), n.OriginalOffset(inside), "inside a rewrite maps to its end")
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func analyzeText(t *testing.T, q string) *Intent {
	t.Helper()
	return Analyze(context.Background(), q, ast.ParserOptions{})
}

func TestAnalyze_LockClauses(t *testing.T) {
	cases := []struct {
		q      string
		table  string
		want   Strength
		nowait bool
		skip   bool
	}{
		{"SELECT * FROM accounts WHERE id = 1 FOR UPDATE", "accounts", Exclusive, false, false},
		{"SELECT * FROM Accounts a WHERE a.id = 1 FOR SHARE", "accounts", Shared, false, false},
		{"SELECT * FROM accounts LOCK IN SHARE MODE", "ACCOUNTS", Shared, false, false},
		{"SELECT * FROM accounts FOR UPDATE NOWAIT", "accounts", Exclusive, true, false},
		{"SELECT * FROM accounts FOR SHARE NOWAIT", "accounts", Shared, true, false},
		{"SELECT * FROM accounts FOR UPDATE SKIP LOCKED", "accounts", Exclusive, false, true},
		{"SELECT * FROM a JOIN b ON a.id = b.id FOR UPDATE", "b", Exclusive, false, false},
		{"SELECT * FROM a JOIN b ON a.id = b.id FOR UPDATE OF a", "b", None, false, false},
		{"SELECT * FROM a x JOIN b y ON x.id = y.id FOR UPDATE OF y", "b", Exclusive, false, false},
		{"SELECT * FROM a JOIN b ON a.id = b.id FOR SHARE OF a", "b", None, false, false},
		{"SELECT * FROM a WHERE id IN (SELECT a_id FROM b) FOR UPDATE", "b", None, false, false},
		{"SELECT * FROM a WHERE id IN (SELECT a_id FROM b FOR SHARE)", "b", Shared, false, false},
		{"SELECT * FROM (SELECT * FROM b) d FOR UPDATE", "b", None, false, false},
		{"SELECT * FROM a UNION SELECT * FROM b FOR UPDATE", "b", Exclusive, false, false},
		{"SELECT * FROM accounts", "accounts", None, false, false},
		{"SELECT 'for update' FROM accounts", "accounts", None, false, false},
	}
	for _, c := range cases {
		t.Run(c.q, func(t *testing.T) {
			in := analyzeText(t, c.q)
			assert.Equal(t, c.want, in.Locking(c.table))
			assert.Equal(t, c.nowait, in.NoWait)
			assert.Equal(t, c.skip, in.SkipLocked)
		})
	}
}

func TestAnalyze_DMLTargets(t *testing.T) {
	cases := []struct {
		q      string
		target string
		other  string
	}{
		{"UPDATE users SET n = n + 1 WHERE id = 1", "users", "accounts"},
		{"UPDATE users u JOIN accounts a ON u.id = a.uid SET u.n = 1", "accounts", "orders"},
		{"DELETE FROM users WHERE id IN (SELECT uid FROM orders)", "users", "orders"},
		{"DELETE u FROM users u JOIN orders o ON u.id = o.uid", "users", "orders"},
		{"DELETE a FROM accounts a WHERE a.balance >= 100", "accounts", "a"},
		{"DELETE users FROM users JOIN orders o ON users.id = o.uid", "users", "orders"},
		{"DELETE o FROM users u JOIN (orders o) ON u.id = o.uid", "orders", "users"},
	}
	for _, c := range cases {
		t.Run(c.q, func(t *testing.T) {
			in := analyzeText(t, c.q)
			assert.True(t, in.Targets(c.target), "target %s", c.target)
			assert.False(t, in.Targets(c.other), "not a target: %s", c.other)
			assert.False(t, in.Empty())
		})
	}
	assert.True(t, analyzeText(t, "INSERT INTO users (id) VALUES (1)").Empty())
	assert.True(t, analyzeText(t, "SELECT * FROM users").Empty())
}

func TestAnalyze_UnparseableTextHasNoIntent(t *testing.T) {
	assert.True(t, analyzeText(t, "UPDATE THIS IS NOT SQL").Empty())
}

func TestAnalyze_FirstStatementOnly(t *testing.T) {
	in := analyzeText(t, "SELECT * FROM a; SELECT * FROM b FOR UPDATE")
	assert.Equal(t, None, in.Locking("b"), "the current statement is the first one of the text")
}

func TestAnalyze_CacheIsBoundedAndConsistent(t *testing.T) {
	first := analyzeText(t, "SELECT * FROM cached FOR UPDATE")
	again := analyzeText(t, "SELECT * FROM cached FOR UPDATE")
	assert.Same(t, first, again)
	for i := 0; i <= cacheLimit; i++ {
		analyzeText(t, "UPDATE t"+strconv.Itoa(i)+" SET a = 1")
	}
	assert.LessOrEqual(t, cacheSize.Load(), int64(cacheLimit))
	assert.Equal(t, Exclusive, analyzeText(t, "SELECT * FROM cached FOR UPDATE").Locking("cached"))
}

func TestTokenize_SkipsLiteralsAndComments(t *testing.T) {
	toks := tokenize("SELECT 'a\\'b' , \"c\"\"d\" , `e``f` # x\n /* y */ -- z\n g")
	var words []string
	for _, tk := range toks {
		if tk.kind == tokWord {
			words = append(words, tk.ident())
		}
	}
	assert.Equal(t, []string{"SELECT", "e`f", "g"}, words)
	assert.False(t, token{kind: tokWord, text: "for", quoted: true}.is("for"))
	assert.Empty(t, tokenize("'unterminated"))
	assert.Len(t, tokenize("`unterminated"), 1)
	assert.Empty(t, tokenize("/* unterminated"))
}
