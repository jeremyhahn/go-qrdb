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

package analyzer

import (
	"go/ast"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// lineDirective is one directive comment, and whether it stands on a line of
// its own.
type lineDirective struct {
	text    string
	ownLine bool
}

// directiveComments maps each line of file carrying a comment that starts
// with prefix to that comment.
//
// ownLine is decided from the source text: only whitespace may precede the
// comment on its line. A file the driver cannot read yields ownLine false,
// which fails closed — the directive then covers only its own line.
func directiveComments(pass *analysis.Pass, file *ast.File, prefix string) map[int]lineDirective {
	out := make(map[int]lineDirective)
	var src []byte
	tf := pass.Fset.File(file.Pos())
	if pass.ReadFile != nil && tf != nil {
		src, _ = pass.ReadFile(tf.Name())
	}
	for _, group := range file.Comments {
		for _, c := range group.List {
			if !strings.HasPrefix(c.Text, prefix) {
				continue
			}
			line := pass.Fset.Position(c.Slash).Line
			out[line] = lineDirective{text: c.Text, ownLine: standsAlone(src, tf, line, c.Slash)}
		}
	}
	return out
}

// standsAlone reports whether only whitespace precedes pos on its line.
func standsAlone(src []byte, tf *token.File, line int, pos token.Pos) bool {
	if src == nil || tf == nil {
		return false
	}
	start, end := tf.Offset(tf.LineStart(line)), tf.Offset(pos)
	if start < 0 || end > len(src) || start > end {
		return false
	}
	return strings.TrimLeft(string(src[start:end]), " \t") == ""
}

// directiveAt returns the directive covering a reference on line: one on that
// same line, or one on the line directly above that stands on a line of its
// own. A comment trailing a different statement on the line above belongs to
// that statement and covers nothing below it.
func directiveAt(m map[int]lineDirective, line int) (lineDirective, bool) {
	if d, ok := m[line]; ok {
		return d, true
	}
	if d, ok := m[line-1]; ok && d.ownLine {
		return d, true
	}
	return lineDirective{}, false
}
