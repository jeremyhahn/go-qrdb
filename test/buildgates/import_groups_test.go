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

package buildgates

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// localModule is the import path prefix of this repository's packages.
const localModule = "github.com/jeremyhahn/go-qrdb"

// minImportFiles is a discovery floor: every assertion below runs inside a
// loop over the files found, and an empty walk would report success.
const minImportFiles = 1000

// TestImportGroups_LocalPackagesAreTheLastGroup: every Go file groups its
// imports as the formatter's -local setting has them -- standard library,
// then third party, then this repository's packages -- so the go-qrdb imports
// are one group, holding nothing else, after every other group. goimports
// keeps a group a human split by hand, so the formatter alone cannot hold
// this; a split here is how a third-party import ended up between two
// go-qrdb groups. A group introduced by a comment is a deliberate, explained
// exception (a side-effect or dot import, say) and is not judged.
func TestImportGroups_LocalPackagesAreTheLastGroup(t *testing.T) {
	root := repoRoot(t)
	var bad []string
	files := 0
	for _, dir := range []string{"pkg", "cmd", "sdk/go", "test"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if name := d.Name(); name == "testdata" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".pb.go") {
				return nil
			}
			files++
			if reason := importGroupViolation(t, path); reason != "" {
				rel, _ := filepath.Rel(root, path)
				bad = append(bad, rel+": "+reason)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	if files < minImportFiles {
		t.Fatalf("found %d Go files, want at least %d: the walk is not looking where the code is", files, minImportFiles)
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Error(b)
	}
}

// importGroupViolation returns why path's import groups break the rule, or
// "" when they keep it.
func importGroupViolation(t *testing.T, path string) string {
	t.Helper()
	src, err := os.ReadFile(path) //nolint:gosec // G304: a Go file of this repository found by the walk
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ImportsOnly|parser.ParseComments)
	if err != nil {
		return "" // not a parseable Go file (a template, say): not this gate's business
	}
	type group struct{ local, other, commented bool }
	var groups []group
	lastLine := -2
	for _, spec := range f.Imports {
		line := fset.Position(spec.Pos()).Line
		if spec.Doc != nil {
			line = fset.Position(spec.Doc.Pos()).Line
		}
		if line > lastLine+1 || len(groups) == 0 {
			groups = append(groups, group{commented: spec.Doc != nil})
		}
		g := &groups[len(groups)-1]
		if strings.HasPrefix(strings.Trim(spec.Path.Value, `"`), localModule) {
			g.local = true
		} else {
			g.other = true
		}
		lastLine = fset.Position(spec.End()).Line
	}
	seenLocal := false
	for _, g := range groups {
		if g.commented {
			continue
		}
		switch {
		case g.local && g.other:
			return "a group mixes go-qrdb imports with others"
		case g.local && seenLocal:
			return "go-qrdb imports are split across groups"
		case g.local:
			seenLocal = true
		case seenLocal:
			return "a non-go-qrdb group follows the go-qrdb group"
		}
	}
	return ""
}
