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
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An embedder composes go-qrdb from a module of its own, importing sdk/go and
// nothing else (go-qrdb#626). A seam an in-tree test reaches through pkg/ can
// still be one an embedder cannot reach at all, and only a build outside this
// module says which. testdata/sdkcomposer is that embedder: it injects the CA
// keys, the per-tenant login CA keys, its own login CA and its volume keys,
// builds DAOs for a named database, builds a tenant SPIFFE SAN, reads a
// tenant-issued delegation, names its service principal and links a service
// annotated with the public permission option. This gate copies it into
// a module of its own -- requiring sdk/go, replaced to this tree, GOWORK=off --
// and runs its tests under the default build tags.

// composerSource is the embedder, relative to the repository root.
const composerSource = "test/buildgates/testdata/sdkcomposer"

// minComposerTests is a discovery floor: a module whose tests ran none would
// report ok.
const minComposerTests = 5

// composerImportViolations judges every Go file in dir, test files included,
// with nonSDKImports, and returns how many files it judged.
func composerImportViolations(t *testing.T, dir string) (violations []string, files int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		files++
		if bad := nonSDKImports(t, filepath.Join(dir, e.Name())); len(bad) > 0 {
			violations = append(violations, e.Name()+": imports "+strings.Join(bad, ", ")+" -- an embedder has only sdk/go/...")
		}
	}
	return violations, files
}

// modReplace is one replace directive as `go mod edit -json` prints it.
type modReplace struct {
	Old struct{ Path, Version string }
	New struct{ Path, Version string }
}

// fileReplaces returns the replace directives of the module in dir whose
// target is a directory rather than a module version.
func fileReplaces(t *testing.T, dir string) []modReplace {
	t.Helper()
	var mod struct{ Replace []modReplace }
	if err := json.Unmarshal([]byte(runGo(t, dir, "mod", "edit", "-json")), &mod); err != nil {
		t.Fatalf("parse go mod edit -json in %s: %v", dir, err)
	}
	var out []modReplace
	for _, r := range mod.Replace {
		if r.New.Version == "" {
			out = append(out, r)
		}
	}
	return out
}

// writeComposerModule turns dir into a module that requires sdk/go: it starts
// from sdk/go's own go.mod and go.sum, so every module sdk/go's packages need
// is already listed, and replaces sdk/go and go-qrdb with this tree. A
// directory replace sdk/go names relative to itself (the in-repo
// third_party/webtransport-go) is re-anchored at sdk/go, since the copy no
// longer sits there.
func writeComposerModule(t *testing.T, root, dir string) {
	t.Helper()
	sdkDir := filepath.Join(root, "sdk", "go")
	dst, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	for _, name := range []string{"go.mod", "go.sum"} {
		b, err := os.ReadFile(filepath.Join(sdkDir, name)) // #nosec G304 -- this repository's own module files
		if err != nil {
			t.Fatalf("read sdk/go/%s: %v", name, err)
		}
		if err := dst.WriteFile(name, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"mod", "edit",
		"-module=example.com/composer",
		"-require=" + sdkModulePrefix + "@v0.0.0",
	}
	for _, r := range fileReplaces(t, dir) {
		if filepath.IsAbs(r.New.Path) {
			continue
		}
		old := r.Old.Path
		if r.Old.Version != "" {
			old += "@" + r.Old.Version
		}
		args = append(args, "-replace="+old+"="+filepath.Join(sdkDir, filepath.FromSlash(r.New.Path)))
	}
	args = append(args,
		"-replace="+sdkModulePrefix+"="+sdkDir,
		"-replace="+localModule+"="+root,
	)
	runGo(t, dir, args...)
}

// runGo runs the go command in dir as a module of its own and returns its
// combined output.
func runGo(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("go", args...) // #nosec G204 -- fixed tool, arguments built by this file
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("go %s in the composer module: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	return out.String()
}

// TestSDK_ExternalComposerReachesEverySeam is the gate.
func TestSDK_ExternalComposerReachesEverySeam(t *testing.T) {
	root := repoRoot(t)
	src := filepath.Join(root, filepath.FromSlash(composerSource))
	violations, files := composerImportViolations(t, src)
	if files < 2 {
		t.Fatalf("judged %d composer files, want at least 2: the gate is not looking where the embedder is", files)
	}
	for _, v := range violations {
		t.Error(v)
	}

	dir := t.TempDir()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	dst, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(src, e.Name())) // #nosec G304 -- this repository's own testdata
		if err != nil {
			t.Fatal(err)
		}
		if err := dst.WriteFile(e.Name(), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeComposerModule(t, root, dir)

	out := runGo(t, dir, "test", "-count=1", "-v", "-tags", strings.Join(fullBuildTags(t), ","), "./...")
	if passed := strings.Count(out, "--- PASS: "); passed < minComposerTests {
		t.Fatalf("the composer module passed %d tests, want at least %d:\n%s", passed, minComposerTests, out)
	}
}

// TestSDK_ComposerModuleResolvesEveryDirectoryReplace holds the composer
// module to the replaces an embedder writes itself, sdk/go and go-qrdb at
// this tree, so the gate builds what an embedder builds and does not inject
// a replace sdk/go would carry (#1244).
func TestSDK_ComposerModuleResolvesEveryDirectoryReplace(t *testing.T) {
	root := repoRoot(t)
	dir := t.TempDir()
	writeComposerModule(t, root, dir)
	replaces := fileReplaces(t, dir)
	// An embedder has only its own replaces: those of sdk/go and go-qrdb,
	// pointing at this checkout. Anything else would build the composer
	// against code an embedder does not get.
	want := map[string]string{
		sdkModulePrefix: filepath.Join(root, "sdk", "go"),
		localModule:     root,
	}
	if len(replaces) != len(want) {
		t.Errorf("the composer module has %d directory replaces, want only sdk/go and go-qrdb: %+v", len(replaces), replaces)
	}
	for _, r := range replaces {
		if dst, ok := want[r.Old.Path]; !ok || r.New.Path != dst {
			t.Errorf("replace %s => %s, want only %s => %s and %s => %s", r.Old.Path, r.New.Path, sdkModulePrefix, want[sdkModulePrefix], localModule, root)
			continue
		}
		if _, err := os.Stat(filepath.Join(r.New.Path, "go.mod")); err != nil {
			t.Errorf("replace %s => %s: %v", r.Old.Path, r.New.Path, err)
		}
	}
}

// TestSDK_ExternalComposerReachesEverySeam_Detects is the control: the import
// judgement must reject an embedder file that reaches into pkg/ or internal/.
func TestSDK_ExternalComposerReachesEverySeam_Detects(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("good.go", "package composer\n\nimport _ \""+sdkModulePrefix+"/node\"\n")
	write("pkg.go", "package composer\n\nimport _ \""+localModule+"/pkg/cert\"\n")
	write("internal_test.go", "package composer\n\nimport _ \""+localModule+"/internal/thing\"\n")
	write("notes.txt", "not Go")

	got, files := composerImportViolations(t, dir)
	if files != 3 {
		t.Fatalf("judged %d files, want 3 (test files are judged, other files are not)", files)
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "internal_test.go:") || !strings.HasPrefix(got[1], "pkg.go:") {
		t.Fatalf("violations = %q, want internal_test.go and pkg.go", got)
	}
}
