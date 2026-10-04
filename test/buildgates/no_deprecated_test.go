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
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// This gate is the backstop for the no-deprecated-surface ruling (go-qrdb,
// "no-compat-cleanup" lane): every API, the CLI, and the Go SDK stay clean of
// anything marked deprecated, kept-for-old-clients, or a redirect to a
// renamed command. A doc comment, a struct field or a proto option is cheap
// to add back under time pressure, and a reviewer reading one diff will not
// always recognize the shape of the thing this repository just finished
// deleting -- so the shape is checked here instead of trusted to review.
//
// Four distinct surfaces, four distinct checks:
//   - Go source: a comment whose first word is the literal "Deprecated:" --
//     the exact convention https://go.dev/wiki/Deprecated uses.
//   - .proto source: the `deprecated = true` field option.
//   - the CLI: a composite literal with a field key literally named
//     "Deprecated" (cobra.Command's own opt-out field), which a doc-comment
//     scan cannot see because it is a struct field, not a comment.
//   - the CLI: a redirect stub, recognized by the two concrete shapes this
//     repository used for one before the no-compat-cleanup lane deleted it --
//     a Short string starting "RENAMED:" or "REMOVED:", or a declared type
//     whose name contains "RedirectError".
//
// Excluded, deliberately, and why narrower here is not narrower as a rule:
//   - vendor/ and node_modules/ are not ours to fix.
//   - generated files (the "Code generated ... DO NOT EDIT." header every Go
//     generator including protoc-gen-go writes) are not hand-edited; a
//     generator's own self-deprecation (protoc-gen-go emits a "Deprecated:
//     Use X.Descriptor instead." comment on every enum and message type) is
//     a property of the generator, not of this repository's design.
//   - this package's own source is excluded from the Go-comment scan only,
//     because the paragraph above has to describe the pattern it looks for.

// generatedHeader is the canonical marker: https://go.dev/s/generatedcode.
var generatedHeader = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`)

// skipDirs are never descended into by any scan in this file.
var skipDirs = map[string]bool{
	".git":         true,
	"vendor":       true,
	"node_modules": true,
}

// deprecatedComment matches a comment line whose first word is
// "Deprecated:" -- the Go doc-comment convention for marking a symbol
// deprecated -- not merely a comment that mentions the word in passing;
// narrative text like "the deprecated foo command" does not match.
var deprecatedComment = regexp.MustCompile(`^Deprecated:`)

// protoDeprecatedOption matches the proto field/file option that marks a
// declaration deprecated.
var protoDeprecatedOption = regexp.MustCompile(`\bdeprecated\s*=\s*true\b`)

// Discovery floors. Every assertion below walks a tree, and an empty walk
// reports success on every one of them.
const (
	// minGoFilesScanned is a floor far below this repository's actual Go
	// file count (measured near 4800), wide enough to tolerate normal
	// churn while still catching a scan that walked nothing.
	minGoFilesScanned = 2000

	// minProtoFilesScanned is the exact current proto file count. A
	// deliberate deletion of a .proto file lowers this in the same commit.
	minProtoFilesScanned = 3

	// minCLIFilesScanned is a floor below this repository's actual cmd/
	// Go file count (measured near 325).
	minCLIFilesScanned = 100

	// minCobraCommandLiterals is a floor below the number of
	// cobra.Command{...} composite literals in cmd/ (measured near 242),
	// proving the CLI-specific scans are actually looking at command
	// literals, not merely walking files that happen to compile.
	minCobraCommandLiterals = 50
)

// parsedFile pairs a repo-relative path with its parsed AST.
type parsedFile struct {
	rel  string
	file *ast.File
}

// walkGoFiles parses every non-generated .go file under the given
// repo-relative directories, skipping skipDirs, and returns the total
// walked (for discovery floors) and the parsed files keyed by a shared
// *token.FileSet so callers can resolve positions.
func walkGoFiles(t *testing.T, root string, fset *token.FileSet, dirs ...string) (walked int, parsed []parsedFile) {
	t.Helper()
	for _, dir := range dirs {
		start := filepath.Join(root, dir)
		err := filepath.WalkDir(start, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				// A path this repository's own checkout cannot read (gone, or a
				// stray build/data artifact left world-unreadable by an unrelated
				// test) has no Go source for this gate to see either way. Skipping
				// it is not a vacuous pass: the discovery floor below still catches
				// a walk that found nothing, so this only widens what "nothing
				// wrong with it" tolerates, not what "found nothing" tolerates.
				if os.IsNotExist(err) || os.IsPermission(err) {
					t.Logf("walkGoFiles: skipping unreadable path %s: %v", path, err)
					if d != nil && d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				return err
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			// #nosec G304 -- path comes from walking this repository's own checkout.
			src, readErr := os.ReadFile(path)
			if readErr != nil {
				if os.IsPermission(readErr) {
					t.Logf("walkGoFiles: skipping unreadable file %s: %v", path, readErr)
					return nil
				}
				return readErr
			}
			if generatedHeader.Match(src) {
				return nil
			}
			walked++
			file, parseErr := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
			if parseErr != nil {
				// A file that fails to parse is a build break the compiler
				// will already catch; this gate is not the place to report it.
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			parsed = append(parsed, parsedFile{rel: filepath.ToSlash(rel), file: file})
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", start, err)
		}
	}
	return walked, parsed
}

// TestNoGoSourceDeclaresDeprecated fails if any non-generated, non-vendored
// Go source anywhere in the repository carries a "Deprecated:" doc comment.
func TestNoGoSourceDeclaresDeprecated(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	walked, parsed := walkGoFiles(t, root, fset, ".")

	if walked < minGoFilesScanned {
		t.Fatalf("scanned %d non-generated .go files, floor is %d; either the scan is "+
			"broken or the tree shrank -- if deliberate, lower minGoFilesScanned in the "+
			"same commit", walked, minGoFilesScanned)
	}

	// This gate's own package is exempt: the paragraph documenting the
	// convention above necessarily contains the string this test looks for.
	const selfPrefix = "test/buildgates/"

	type hit struct {
		rel, detail string
		line        int
	}
	var hits []hit
	for _, pf := range parsed {
		if strings.HasPrefix(pf.rel, selfPrefix) {
			continue
		}
		for _, group := range pf.file.Comments {
			for _, c := range group.List {
				text := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
				text = strings.TrimSpace(strings.TrimPrefix(text, "/*"))
				if deprecatedComment.MatchString(text) {
					hits = append(hits, hit{rel: pf.rel, line: fset.Position(c.Pos()).Line, detail: text})
				}
			}
		}
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].rel != hits[j].rel {
			return hits[i].rel < hits[j].rel
		}
		return hits[i].line < hits[j].line
	})
	for _, h := range hits {
		t.Errorf("%s:%d: %s -- a Deprecated: doc comment on our own source; per the "+
			"no-deprecated-surface ruling, delete the symbol (or the old alias) instead "+
			"of marking it deprecated", h.rel, h.line, h.detail)
	}
}

// TestNoProtoDeclaresDeprecated fails if any non-vendored .proto file sets
// the `deprecated = true` field or file option.
func TestNoProtoDeclaresDeprecated(t *testing.T) {
	root := repoRoot(t)
	walked := 0
	type hit struct {
		rel, detail string
		line        int
	}
	var hits []hit
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// See walkGoFiles: an unreadable leftover artifact has no .proto
			// content for this gate to see either way.
			if os.IsNotExist(err) || os.IsPermission(err) {
				t.Logf("TestNoProtoDeclaresDeprecated: skipping unreadable path %s: %v", path, err)
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".proto") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		// #nosec G304 -- path comes from walking this repository's own checkout.
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		walked++
		for i, line := range strings.Split(string(src), "\n") {
			if protoDeprecatedOption.MatchString(line) {
				hits = append(hits, hit{rel: rel, line: i + 1, detail: strings.TrimSpace(line)})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	if walked < minProtoFilesScanned {
		t.Fatalf("scanned %d .proto files, floor is %d; either the scan is broken or a "+
			"proto file was deleted -- if deliberate, lower minProtoFilesScanned in the "+
			"same commit", walked, minProtoFilesScanned)
	}

	for _, h := range hits {
		t.Errorf("%s:%d: %s -- declares `deprecated = true`; remove the field with "+
			"`reserved` instead and run `make proto`", h.rel, h.line, h.detail)
	}
}

// isCobraCommandLit reports whether cl's type looks like a cobra.Command
// (qualified) or Command (dot-imported / local alias) composite literal.
func isCobraCommandLit(cl *ast.CompositeLit) bool {
	switch t := cl.Type.(type) {
	case *ast.SelectorExpr:
		return t.Sel.Name == "Command"
	case *ast.Ident:
		return t.Name == "Command"
	default:
		return false
	}
}

// cliCobraLiterals walks every parsed cmd/ file and returns every
// cobra.Command composite literal found, for both CLI gates to share.
func cliCobraLiterals(parsed []parsedFile) []*ast.CompositeLit {
	var out []*ast.CompositeLit
	for _, pf := range parsed {
		ast.Inspect(pf.file, func(n ast.Node) bool {
			if cl, ok := n.(*ast.CompositeLit); ok && isCobraCommandLit(cl) {
				out = append(out, cl)
			}
			return true
		})
	}
	return out
}

// TestCLINoCobraDeprecatedField fails if any cmd/ cobra.Command composite
// literal sets a field literally named "Deprecated" -- cobra's own built-in
// deprecation marker, which renders as a warning but keeps the command
// runnable. The no-deprecated-surface ruling wants the command gone, not
// marked.
func TestCLINoCobraDeprecatedField(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	walked, parsed := walkGoFiles(t, root, fset, "cmd")

	if walked < minCLIFilesScanned {
		t.Fatalf("scanned %d cmd/ Go files, floor is %d; either the scan is broken or "+
			"cmd/ shrank -- if deliberate, lower minCLIFilesScanned in the same commit",
			walked, minCLIFilesScanned)
	}

	literals := cliCobraLiterals(parsed)
	if len(literals) < minCobraCommandLiterals {
		t.Fatalf("found %d cobra.Command composite literals under cmd/, floor is %d; "+
			"either the scan is broken or commands were removed -- if deliberate, lower "+
			"minCobraCommandLiterals in the same commit", len(literals), minCobraCommandLiterals)
	}

	for _, cl := range literals {
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != "Deprecated" {
				continue
			}
			pos := fset.Position(kv.Pos())
			t.Errorf("%s:%d: cobra.Command sets a Deprecated field; a deprecated CLI "+
				"command must be deleted outright, not marked Deprecated",
				pos.Filename, pos.Line)
		}
	}
}

// TestCLINoRedirectStub fails if cmd/ declares a redirect-stub command --
// recognized by the two concrete shapes this repository used before the
// no-compat-cleanup lane deleted them: a cobra.Command composite literal
// whose Short field is a string literal starting "RENAMED:" or "REMOVED:",
// or a declared type whose name contains "RedirectError".
func TestCLINoRedirectStub(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	walked, parsed := walkGoFiles(t, root, fset, "cmd")

	if walked < minCLIFilesScanned {
		t.Fatalf("scanned %d cmd/ Go files, floor is %d; either the scan is broken or "+
			"cmd/ shrank -- if deliberate, lower minCLIFilesScanned in the same commit",
			walked, minCLIFilesScanned)
	}

	literals := cliCobraLiterals(parsed)
	if len(literals) < minCobraCommandLiterals {
		t.Fatalf("found %d cobra.Command composite literals under cmd/, floor is %d; "+
			"either the scan is broken or commands were removed -- if deliberate, lower "+
			"minCobraCommandLiterals in the same commit", len(literals), minCobraCommandLiterals)
	}

	for _, cl := range literals {
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != "Short" {
				continue
			}
			lit, ok := kv.Value.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			val := strings.Trim(lit.Value, "`\"")
			if strings.HasPrefix(val, "RENAMED:") || strings.HasPrefix(val, "REMOVED:") {
				pos := fset.Position(kv.Pos())
				t.Errorf("%s:%d: Short field %q looks like a redirect-stub marker; the "+
					"no-deprecated-surface ruling deletes a renamed command outright "+
					"rather than keeping a stub that redirects to its replacement",
					pos.Filename, pos.Line, val)
			}
		}
	}

	for _, pf := range parsed {
		for _, decl := range pf.file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !strings.Contains(ts.Name.Name, "RedirectError") {
					continue
				}
				pos := fset.Position(ts.Pos())
				t.Errorf("%s:%d: %s %s", pos.Filename, pos.Line, fmt.Sprintf(
					"type %s looks like a redirect-stub error type;", ts.Name.Name),
					"the no-deprecated-surface ruling deletes the redirected command "+
						"and its error type together")
			}
		}
	}
}
