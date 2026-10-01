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
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestReadBaseline_SkipsCommentsAndBlanks(t *testing.T) {
	got, err := ReadBaseline("b.txt", strings.NewReader(`# header

example.com/m/a 91.50
  # indented comment
example.com/m/b 0.00
`))
	if err != nil {
		t.Fatalf("ReadBaseline: %v", err)
	}
	if len(got) != 2 || got["example.com/m/a"] != 91.50 || got["example.com/m/b"] != 0 {
		t.Errorf("got %v", got)
	}
}

// TestReadBaseline_RefusesMalformedLines: an unreadable baseline must fail,
// never read as empty -- an empty baseline would hold nothing to anything and
// turn every existing package into a "new" one checked only against the floor.
func TestReadBaseline_RefusesMalformedLines(t *testing.T) {
	for name, in := range map[string]string{
		"one field":    "example.com/m/a\n",
		"three fields": "example.com/m/a 91 x\n",
		"not a number": "example.com/m/a ninety\n",
		"above 100":    "example.com/m/a 100.5\n",
		"negative":     "example.com/m/a -1\n",
		"listed twice": "example.com/m/a 90\nexample.com/m/a 91\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ReadBaseline("b.txt", strings.NewReader(in))
			var be *BaselineError
			if !errors.As(err, &be) {
				t.Fatalf("err = %v, want *BaselineError", err)
			}
		})
	}
}

// TestWriteBaseline_RoundTripsAndSorts: what is written reads back, sorted by
// import path so a regeneration diffs line-for-line.
func TestWriteBaseline_RoundTripsAndSorts(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteBaseline(&buf, map[string]Stats{
		"example.com/m/z": {Covered: 1, Total: 2},
		"example.com/m/a": {Covered: 3, Total: 4},
	}); err != nil {
		t.Fatalf("WriteBaseline: %v", err)
	}
	var pkgLines []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(l, "example.com/") {
			pkgLines = append(pkgLines, l)
		}
	}
	if want := []string{"example.com/m/a 75.00", "example.com/m/z 50.00"}; strings.Join(pkgLines, "|") != strings.Join(want, "|") {
		t.Errorf("lines = %q, want %q", pkgLines, want)
	}
	back, err := ReadBaseline("b.txt", &buf)
	if err != nil {
		t.Fatalf("ReadBaseline of written file: %v", err)
	}
	if back["example.com/m/a"] != 75 || back["example.com/m/z"] != 50 {
		t.Errorf("round trip = %v", back)
	}
}

// TestWriteBaseline_FloorsNeverRoundsUp is the property that stops the gate
// failing on its own baseline. 2/3 is 66.666...%; rounding would write 66.67
// and hold the package ABOVE what it measured, so the very next identical run
// would regress by 0.003pp. Flooring writes 66.66.
func TestWriteBaseline_FloorsNeverRoundsUp(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteBaseline(&buf, map[string]Stats{"example.com/m/a": {Covered: 2, Total: 3}}); err != nil {
		t.Fatalf("WriteBaseline: %v", err)
	}
	if !strings.Contains(buf.String(), "example.com/m/a 66.66\n") {
		t.Errorf("wrote %q, want the floored 66.66", buf.String())
	}
}

// TestWriteBaseline_ExplainsItself: a reviewer meeting this file in a diff
// must learn from the file that a LOWERED number is the one to question.
func TestWriteBaseline_ExplainsItself(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteBaseline(&buf, map[string]Stats{}); err != nil {
		t.Fatalf("WriteBaseline: %v", err)
	}
	if !strings.Contains(buf.String(), "LOWERED number") {
		t.Error("the header no longer tells a reviewer what to question")
	}
}
