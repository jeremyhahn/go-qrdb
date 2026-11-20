//go:build qrdb_sql

// Copyright 2026 Jeremy Hahn
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

package engine

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScanRegions_Empty(t *testing.T) {
	assert.Empty(t, scanRegions(""))
}

func TestScanRegions_NoRegions(t *testing.T) {
	assert.Empty(t, scanRegions("SELECT * FROM users WHERE id = 1"))
}

func TestScanRegions_PlainStringLiteral(t *testing.T) {
	regions := scanRegions("SELECT * FROM t WHERE name = 'alice'")
	require.Len(t, regions, 1)
	assert.Equal(t, regionStringLiteral, regions[0].kind)
	assert.Equal(t, "'alice'", "SELECT * FROM t WHERE name = 'alice'"[regions[0].start:regions[0].end])
}

func TestScanRegions_EscapeStringLiteral_AfterWordBoundary(t *testing.T) {
	regions := scanRegions("SELECT * FROM t WHERE name = E'alice'")
	require.Len(t, regions, 1)
	assert.Equal(t, regionEscapeStringLiteral, regions[0].kind)
}

func TestScanRegions_EscapeStringLiteral_AtStartOfQuery(t *testing.T) {
	// The E is the very first byte, exercising the i == 1 boundary in the
	// word-boundary check.
	query := "E'alice'"
	regions := scanRegions(query)
	require.Len(t, regions, 1)
	assert.Equal(t, regionEscapeStringLiteral, regions[0].kind)
	assert.Equal(t, 0, regions[0].start)
}

func TestScanRegions_EscapeStringLiteral_LowercaseE(t *testing.T) {
	regions := scanRegions("SELECT * FROM t WHERE name = e'alice'")
	require.Len(t, regions, 1)
	assert.Equal(t, regionEscapeStringLiteral, regions[0].kind)
}

func TestScanRegions_NotEscapeStringLiteral_WhenEIsPartOfLongerWord(t *testing.T) {
	// "TYPE'foo'" -- the E directly before the quote is the last letter of
	// TYPE, not a standalone E prefix, so this must be a plain literal.
	regions := scanRegions("SELECT * FROM t WHERE name = TYPE'foo'")
	require.Len(t, regions, 1)
	assert.Equal(t, regionStringLiteral, regions[0].kind)
}

func TestScanRegions_EscapedQuotesInsideLiteral(t *testing.T) {
	query := "SELECT * FROM t WHERE name = 'it''s here'"
	regions := scanRegions(query)
	require.Len(t, regions, 1)
	assert.Equal(t, "'it''s here'", query[regions[0].start:regions[0].end])
}

func TestScanRegions_UnterminatedStringLiteral(t *testing.T) {
	query := "SELECT * FROM t WHERE name = 'unterminated"
	regions := scanRegions(query)
	require.Len(t, regions, 1)
	assert.Equal(t, len(query), regions[0].end)
}

func TestScanRegions_LineComment_EndsAtNewline(t *testing.T) {
	query := "SELECT 1 -- a comment\nFROM t"
	regions := scanRegions(query)
	require.Len(t, regions, 1)
	assert.Equal(t, regionLineComment, regions[0].kind)
	assert.Equal(t, "-- a comment", query[regions[0].start:regions[0].end])
}

func TestScanRegions_LineComment_EndsAtEOF(t *testing.T) {
	query := "SELECT 1 -- a trailing comment"
	regions := scanRegions(query)
	require.Len(t, regions, 1)
	assert.Equal(t, regionLineComment, regions[0].kind)
	assert.Equal(t, len(query), regions[0].end)
}

func TestScanRegions_BlockComment_Simple(t *testing.T) {
	query := "SELECT /* comment */ 1"
	regions := scanRegions(query)
	require.Len(t, regions, 1)
	assert.Equal(t, regionBlockComment, regions[0].kind)
	assert.Equal(t, "/* comment */", query[regions[0].start:regions[0].end])
}

func TestScanRegions_BlockComment_Nested(t *testing.T) {
	query := "SELECT /* outer /* inner */ still outer */ 1"
	regions := scanRegions(query)
	require.Len(t, regions, 1)
	assert.Equal(t, regionBlockComment, regions[0].kind)
	assert.Equal(t, "/* outer /* inner */ still outer */", query[regions[0].start:regions[0].end])
}

func TestScanRegions_BlockComment_Unterminated(t *testing.T) {
	query := "SELECT /* never closes"
	regions := scanRegions(query)
	require.Len(t, regions, 1)
	assert.Equal(t, len(query), regions[0].end)
}

func TestScanRegions_DollarQuoted_NamedTag(t *testing.T) {
	query := "SELECT $tag$hello$tag$ FROM t"
	regions := scanRegions(query)
	require.Len(t, regions, 1)
	assert.Equal(t, regionDollarQuoted, regions[0].kind)
	assert.Equal(t, "$tag$hello$tag$", query[regions[0].start:regions[0].end])
}

func TestScanRegions_DollarQuoted_EmptyTag(t *testing.T) {
	query := "SELECT $$hello$$ FROM t"
	regions := scanRegions(query)
	require.Len(t, regions, 1)
	assert.Equal(t, regionDollarQuoted, regions[0].kind)
	assert.Equal(t, "$$hello$$", query[regions[0].start:regions[0].end])
}

func TestScanRegions_DollarQuoted_Unterminated(t *testing.T) {
	query := "SELECT $tag$never closes"
	regions := scanRegions(query)
	require.Len(t, regions, 1)
	assert.Equal(t, len(query), regions[0].end)
}

func TestScanRegions_DollarSign_NotATag_Digit(t *testing.T) {
	// $1 is a positional parameter, not a dollar-quote tag (tags cannot
	// start with a digit).
	regions := scanRegions("SELECT * FROM t WHERE id = $1")
	assert.Empty(t, regions)
}

func TestScanRegions_DollarSign_NotATag_TrailingDollarWithNoClose(t *testing.T) {
	// "$" immediately followed by an identifier that never reaches a
	// closing "$" is not a dollar-quote tag either.
	regions := scanRegions("SELECT * FROM t WHERE id = $tag")
	assert.Empty(t, regions)
}

func TestScanRegions_DollarSign_AtEndOfQuery(t *testing.T) {
	regions := scanRegions("SELECT * FROM t WHERE id = $")
	assert.Empty(t, regions)
}

func TestScanRegions_MultipleRegionKinds(t *testing.T) {
	query := "SELECT 'a', E'b', $$c$$ FROM t -- trailing\n/* block */ WHERE x = 1"
	regions := scanRegions(query)
	require.Len(t, regions, 5)
	assert.Equal(t, regionStringLiteral, regions[0].kind)
	assert.Equal(t, regionEscapeStringLiteral, regions[1].kind)
	assert.Equal(t, regionDollarQuoted, regions[2].kind)
	assert.Equal(t, regionLineComment, regions[3].kind)
	assert.Equal(t, regionBlockComment, regions[4].kind)
}

func TestRegionContainsPos_Inside(t *testing.T) {
	regions := []region{{kind: regionStringLiteral, start: 5, end: 10}}
	assert.True(t, regionContainsPos(regions, 7))
}

func TestRegionContainsPos_BeforeAllRegions(t *testing.T) {
	regions := []region{{kind: regionStringLiteral, start: 5, end: 10}}
	assert.False(t, regionContainsPos(regions, 2))
}

func TestRegionContainsPos_AfterAllRegions(t *testing.T) {
	regions := []region{{kind: regionStringLiteral, start: 5, end: 10}}
	assert.False(t, regionContainsPos(regions, 20))
}

func TestRegionContainsPos_BetweenTwoRegions(t *testing.T) {
	regions := []region{
		{kind: regionStringLiteral, start: 0, end: 5},
		{kind: regionStringLiteral, start: 10, end: 15},
	}
	assert.False(t, regionContainsPos(regions, 7))
}

func TestRegionContainsPos_NoRegions(t *testing.T) {
	assert.False(t, regionContainsPos(nil, 0))
}

func TestApplyOutsideRegions_NoMatches(t *testing.T) {
	query := "SELECT * FROM users"
	result := applyOutsideRegions(query, regexp.MustCompile(`\$\d+`), "?")
	assert.Equal(t, query, result)
}

func TestApplyOutsideRegions_MatchInsideRegion(t *testing.T) {
	query := "SELECT * FROM t WHERE body = 'has $1 inside'"
	result := applyOutsideRegions(query, dollarParamRe, "?")
	assert.Equal(t, query, result)
}

func TestFindOutsideRegions_NoMatches(t *testing.T) {
	loc := findOutsideRegions("SELECT * FROM users", onConflictRe)
	assert.Nil(t, loc)
}

func TestFindOutsideRegions_AllMatchesInsideRegions(t *testing.T) {
	loc := findOutsideRegions("SELECT * FROM t WHERE body = 'ON CONFLICT is mentioned here'", onConflictRe)
	assert.Nil(t, loc)
}

func TestApplyEscapeStrings_NoEscapeStrings(t *testing.T) {
	query := "SELECT * FROM t WHERE name = 'plain'"
	assert.Equal(t, query, applyEscapeStrings(query))
}

func TestMaskForCast_PreservesStringLiteralBoundaries(t *testing.T) {
	query := "SELECT '2024-01-01'::date"
	regions := scanRegions(query)
	masked := maskForCast(query, regions)

	// The opening and closing quotes survive; the interior does not.
	assert.Equal(t, byte('\''), masked[7])
	assert.Equal(t, byte('\''), masked[18])
	assert.Equal(t, byte(castMaskByte), masked[8])
}

func TestMaskForCast_MasksCommentEntirely(t *testing.T) {
	query := "SELECT /* x */ 1"
	regions := scanRegions(query)
	masked := maskForCast(query, regions)

	for i := 7; i < 14; i++ {
		assert.Equal(t, byte(castMaskByte), masked[i], "index %d", i)
	}
}

func TestApplyCast_NoMatches(t *testing.T) {
	query := "SELECT * FROM users"
	assert.Equal(t, query, applyCast(query))
}

func TestApplyCast_LiteralAdjacentToCastNotConsumedByInterior(t *testing.T) {
	// The literal's interior contains "::text" as plain data; only the
	// real cast at the literal's own boundary (immediately after the
	// closing quote) may fire, and only when one is actually present.
	query := "SELECT 'has ::text inside' FROM dual"
	assert.Equal(t, query, applyCast(query))
}

func TestMatchesOutsideLiterals(t *testing.T) {
	param := regexp.MustCompile(`\$(\d+)|\?`)
	cases := []struct {
		q    string
		want bool
	}{
		{"SELECT * FROM t WHERE id = $1", true},
		{"SELECT 'costs $1'", false},
		{"SELECT 1 -- $1 in a comment", false},
		{"SELECT $$ $1 $$", false},
		{"SELECT 'what?' , $2", true},
		{"SELECT 1", false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, MatchesOutsideLiterals(c.q, param), c.q)
	}
}
