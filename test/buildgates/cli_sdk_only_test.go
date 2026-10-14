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
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// sdkModulePrefix is the import path of the public SDK, the only part of this
// repository the CLI may import.
const sdkModulePrefix = localModule + "/sdk/go"

// minCLIFiles is a discovery floor: the assertions run inside a loop over the
// files found, and an empty walk would report success.
const minCLIFiles = 100

// cliInProcessComposer lists the non-test files that still import pkg/
// packages because they are the desktop GUI's hand-rolled in-process server
// (build tag qrdb_embedded). They are a ratchet, not an exemption: an entry
// that no longer imports a non-SDK package fails the gate and must be deleted
// from this list, and a file not listed may never gain such an import.
var cliInProcessComposer = map[string]bool{
	"cmd/qrdb/bindings_server.go":            true,
	"cmd/qrdb/database_instance_registry.go": true,
	"cmd/qrdb/embedded_barrier.go":           true,
	"cmd/qrdb/host_adapter.go":               true,
}

// nonSDKImports returns the go-qrdb imports of the Go source at file that are
// not part of the public SDK. The standard library and third-party modules
// are not judged.
func nonSDKImports(t *testing.T, file string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	var bad []string
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("%s: bad import literal %s", file, spec.Path.Value)
		}
		if p != localModule && !strings.HasPrefix(p, localModule+"/") {
			continue
		}
		if p == sdkModulePrefix || strings.HasPrefix(p, sdkModulePrefix+"/") {
			continue
		}
		bad = append(bad, p)
	}
	return bad
}

// cliSDKOnlyViolations walks the non-test Go files under root/cmd/qrdb,
// ignoring build tags, and reports every file importing a non-SDK go-qrdb
// package that is not in allow, plus every allow entry that no longer needs
// its place. It returns the number of files it judged.
func cliSDKOnlyViolations(t *testing.T, root string, allow map[string]bool) (violations []string, files int) {
	t.Helper()
	offending := map[string]bool{}
	err := filepath.WalkDir(filepath.Join(root, "cmd", "qrdb"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == "frontend" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		files++
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		bad := nonSDKImports(t, p)
		if len(bad) == 0 {
			return nil
		}
		offending[rel] = true
		if !allow[rel] {
			violations = append(violations, rel+": imports "+strings.Join(bad, ", ")+" -- the CLI may use only sdk/go/...")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk cmd/qrdb: %v", err)
	}
	for rel := range allow {
		if !offending[rel] {
			violations = append(violations, rel+": listed as an in-process composer but imports no non-SDK package; delete it from cliInProcessComposer")
		}
	}
	sort.Strings(violations)
	return violations, files
}

// TestCLI_ImportsOnlyTheSDK: cmd/qrdb talks to qrdb the way every public
// consumer does, through sdk/go. A pkg/ or internal import there is a capability
// the SDK does not offer its users, hidden by the one client that happens to be
// built in-tree.
func TestCLI_ImportsOnlyTheSDK(t *testing.T) {
	violations, files := cliSDKOnlyViolations(t, repoRoot(t), cliInProcessComposer)
	if files < minCLIFiles {
		t.Fatalf("judged %d CLI files, want at least %d: the walk is not looking where the code is", files, minCLIFiles)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

// TestCLI_ImportsOnlyTheSDK_Detects is the control: the gate must reject a
// pkg/ import, accept an SDK one, and flag a stale allowlist entry. A gate
// that cannot go red proves nothing.
func TestCLI_ImportsOnlyTheSDK_Detects(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "cmd", "qrdb")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	write := func(name, src string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("bad.go", "package main\n\nimport _ \""+localModule+"/pkg/service\"\n")
	write("internal.go", "package main\n\nimport _ \""+localModule+"/internal/thing\"\n")
	write("good.go", "package main\n\nimport (\n\t\"fmt\"\n\n\t\"github.com/spf13/cobra\"\n\n\t_ \""+sdkModulePrefix+"/secure\"\n)\n\nvar _ = fmt.Sprint\nvar _ = cobra.Command{}\n")
	write("sibling.go", "package main\n\nimport _ \""+localModule+"/sdk/gofake\"\n")
	write("bad_test.go", "package main\n\nimport _ \""+localModule+"/pkg/service\"\n")

	got, files := cliSDKOnlyViolations(t, root, map[string]bool{"cmd/qrdb/good.go": true})
	if files != 4 {
		t.Fatalf("judged %d files, want 4 (test files are not judged)", files)
	}
	want := map[string]bool{
		"cmd/qrdb/bad.go":      false,
		"cmd/qrdb/internal.go": false,
		"cmd/qrdb/sibling.go":  false,
		"cmd/qrdb/good.go":     false,
	}
	for _, v := range got {
		name := strings.SplitN(v, ":", 2)[0]
		if _, ok := want[name]; !ok {
			t.Fatalf("unexpected violation %q", v)
		}
		want[name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("expected a violation for %s, got %v", path.Base(name), got)
		}
	}
}
