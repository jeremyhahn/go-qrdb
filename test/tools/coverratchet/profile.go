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

package main

import (
	"bufio"
	"io"
	"path"
	"strconv"
	"strings"
)

// Stats is one package's statement coverage.
type Stats struct {
	// Covered is the number of statements executed at least once.
	Covered int
	// Total is the number of statements.
	Total int
}

// Pct returns the covered percentage. A package with no statements is 100%
// covered by definition -- there is nothing in it left unexecuted -- which
// keeps it from dragging the ratchet or tripping the floor.
func (s Stats) Pct() float64 {
	if s.Total == 0 {
		return 100
	}
	return 100 * float64(s.Covered) / float64(s.Total)
}

// block is one profile block, deduplicated by position.
type block struct {
	pkg     string
	stmts   int
	covered bool
}

// ParseProfile reads a `go test -coverprofile` file and returns coverage per
// Go package, keyed by import path.
//
// Blocks are deduplicated by their position (file plus line/column range).
// `go test` over many packages normally emits each block once, but a profile
// that names a block twice -- from -coverpkg, or from concatenated profiles --
// would otherwise count its statements twice. A duplicated block is covered if
// ANY occurrence executed it, which is the question coverage answers.
//
// A duplicated block whose statement counts DISAGREE is refused: the two
// occurrences describe different code at the same position, so the profile is
// from mismatched builds and no number derived from it means anything.
func ParseProfile(name string, r io.Reader) (map[string]Stats, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	blocks := make(map[string]*block)
	lineNo := 0
	sawMode := false
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if !sawMode {
			if !strings.HasPrefix(line, "mode:") {
				return nil, &ProfileError{Path: name, Line: lineNo,
					Reason: "the first line is not a `mode:` header, so this is not a coverage profile"}
			}
			sawMode = true
			continue
		}

		// "<file>:<sl>.<sc>,<el>.<ec> <numStmts> <count>". Splitting on
		// whitespace is safe because a Go import path cannot contain any; the
		// file is then everything before the LAST colon, since the range after
		// it never contains one.
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, &ProfileError{Path: name, Line: lineNo,
				Reason: "want 3 fields (position, statements, count), got " + strconv.Itoa(len(fields))}
		}
		pos := fields[0]
		stmts, err := strconv.Atoi(fields[1])
		if err != nil || stmts < 0 {
			return nil, &ProfileError{Path: name, Line: lineNo, Reason: "bad statement count " + strconv.Quote(fields[1]), Err: err}
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil || count < 0 {
			return nil, &ProfileError{Path: name, Line: lineNo, Reason: "bad execution count " + strconv.Quote(fields[2]), Err: err}
		}
		colon := strings.LastIndex(pos, ":")
		if colon <= 0 {
			return nil, &ProfileError{Path: name, Line: lineNo, Reason: "position " + strconv.Quote(pos) + " has no file:range separator"}
		}
		file := pos[:colon]

		if b, ok := blocks[pos]; ok {
			if b.stmts != stmts {
				return nil, &ProfileError{Path: name, Line: lineNo,
					Reason: "block " + strconv.Quote(pos) + " appears twice with different statement counts (" +
						strconv.Itoa(b.stmts) + " and " + strconv.Itoa(stmts) + "): the profile mixes builds"}
			}
			b.covered = b.covered || count > 0
			continue
		}
		blocks[pos] = &block{pkg: path.Dir(file), stmts: stmts, covered: count > 0}
	}
	if err := sc.Err(); err != nil {
		return nil, &ProfileError{Path: name, Reason: "read failed", Err: err}
	}
	if !sawMode {
		return nil, &ProfileError{Path: name, Reason: "the profile is empty"}
	}

	out := make(map[string]Stats)
	for _, b := range blocks {
		s := out[b.pkg]
		s.Total += b.stmts
		if b.covered {
			s.Covered += b.stmts
		}
		out[b.pkg] = s
	}
	return out, nil
}
